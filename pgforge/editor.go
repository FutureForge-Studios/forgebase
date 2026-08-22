package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
)

// Per-project connection pool. pgforged connects as the postgres superuser to
// each project database (superuser bypasses the REVOKE CONNECT, so the admin
// dashboard can read/write any project). Connections are cached per slug.
var (
	projConns   = map[string]*sql.DB{}
	projConnsMu sync.Mutex
)

func (a *app) dbFor(slug string) (*sql.DB, error) {
	projConnsMu.Lock()
	defer projConnsMu.Unlock()
	if db, ok := projConns[slug]; ok {
		return db, nil
	}
	dsn := ""
	if a.projectMode(slug) == "instance" {
		// through the cold-start proxy: any panel/editor touch wakes a
		// sleeping instance automatically
		_, pw := a.projectCred(slug)
		dsn = a.instanceDSN(slug, pw)
	} else {
		u := *a.baseURL
		u.Path = "/" + slug
		dsn = u.String()
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(3)
	db.SetConnMaxIdleTime(5 * time.Minute)
	projConns[slug] = db
	return db, nil
}

func closeConn(slug string) {
	projConnsMu.Lock()
	defer projConnsMu.Unlock()
	if db, ok := projConns[slug]; ok {
		db.Close()
		delete(projConns, slug)
	}
}

// execQuerier is satisfied by both *sql.DB and *sql.Tx, so the SQL editor can
// run a statement directly (as the owner) or inside a role-impersonation
// transaction (to test RLS as anon/authenticated/service_role).
type execQuerier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// scanRows reads a dynamic result set into string cells (nil -> Null=true).
// Display safety: these cells become HTML, so raw binary turns into a size
// marker, oversized text is cut (the trailing marker is what the edit JS keys
// on to refuse inline edits), and at most maxScanRows rows are kept - a
// select * over a blob-heavy or huge table must not take the panel down.
type cell struct {
	Val  string
	Null bool
}

const (
	cellTextCap = 4096 // display bytes per cell; the grid's SQL-side cap is smaller
	maxScanRows = 1000
)

func displayValue(b []byte) string {
	if !utf8.Valid(b) {
		return fmt.Sprintf("[binary · %s]", humanBytes(int64(len(b))))
	}
	s := string(b)
	if len(s) > cellTextCap {
		n := cellTextCap
		for n > 0 && !utf8.ValidString(s[:n]) {
			n--
		}
		return s[:n] + fmt.Sprintf(" ... [truncated, %s total - edit via SQL]", humanBytes(int64(len(s))))
	}
	return s
}

func scanRows(rows *sql.Rows) ([]string, [][]cell, error) {
	return scanRowsN(rows, maxScanRows)
}

// scanRowsN caps at n rows (the SQL editor's row-limit selector).
func scanRowsN(rows *sql.Rows, n int) ([]string, [][]cell, error) {
	if n <= 0 || n > 10000 {
		n = maxScanRows
	}
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]cell
	for rows.Next() && len(out) < n {
		raw := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range raw {
			ptr[i] = &raw[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			return nil, nil, err
		}
		rec := make([]cell, len(cols))
		for i, v := range raw {
			switch x := v.(type) {
			case nil:
				rec[i] = cell{Null: true}
			case []byte:
				rec[i] = cell{Val: displayValue(x)}
			case time.Time:
				rec[i] = cell{Val: x.Format("2006-01-02 15:04:05")}
			default:
				rec[i] = cell{Val: displayValue([]byte(fmt.Sprintf("%v", x)))}
			}
		}
		out = append(out, rec)
	}
	return cols, out, rows.Err()
}

// ----------------------------------------------------------------- table editor

func (a *app) listTables(db *sql.DB) []string {
	rows, err := db.Query(`SELECT table_name FROM information_schema.tables
		WHERE table_schema='public' AND table_type='BASE TABLE' ORDER BY 1`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		out = append(out, t)
	}
	return out
}

// qrel qualifies a relation name with its schema, both safely quoted.
func qrel(schema, name string) string {
	return pq.QuoteIdentifier(schema) + "." + pq.QuoteIdentifier(name)
}

// buildTableDef reconstructs a readable CREATE TABLE statement (plus its
// secondary indexes) from catalog metadata - the "show me the SQL" view.
func buildTableDef(sc, table string, cols []tableCol, pk []string, idxs []dbIndex) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", qrel(sc, table))
	var lines []string
	for _, c := range cols {
		l := "  " + pq.QuoteIdentifier(c.Name) + " " + c.Type
		if c.Default != "" {
			l += " DEFAULT " + c.Default
		}
		if c.Nullable == "NO" {
			l += " NOT NULL"
		}
		lines = append(lines, l)
	}
	if len(pk) > 0 {
		qp := make([]string, len(pk))
		for i, k := range pk {
			qp[i] = pq.QuoteIdentifier(k)
		}
		lines = append(lines, "  PRIMARY KEY ("+strings.Join(qp, ", ")+")")
	}
	for _, c := range cols {
		if c.FKTable != "" {
			lines = append(lines, fmt.Sprintf("  FOREIGN KEY (%s) REFERENCES %s (%s)",
				pq.QuoteIdentifier(c.Name), qrel(c.FKSchema, c.FKTable), pq.QuoteIdentifier(c.FKCol)))
		}
	}
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\n);")
	for _, ix := range idxs {
		if ix.Table == table && !ix.Primary {
			b.WriteString("\n" + ix.Def + ";")
		}
	}
	return b.String()
}

// roleFor returns the project's database role name (defaults to the slug).
func (a *app) roleFor(slug string) string {
	role := ""
	a.db.QueryRow(`SELECT coalesce(role_name,'') FROM projects WHERE slug=$1`, slug).Scan(&role)
	if role == "" {
		role = slug
	}
	return role
}

// chownRel hands a panel-created object to the project role. The panel
// connects as the cluster superuser, so without this a table created here
// would be owned by postgres - outside the reach of the owner's own
// migrations, GRANTs and tooling. Best-effort: a failure only means the
// object stays superuser-owned, which is what happened before this existed.
func (a *app) chownRel(db *sql.DB, slug, kind, schema, name string) {
	db.Exec(fmt.Sprintf(`ALTER %s %s OWNER TO %s`, kind, qrel(schema, name),
		pq.QuoteIdentifier(a.roleFor(slug))))
}

// schemas the panel must never create over or drop
var reservedSchemas = map[string]bool{
	"public": true, "auth": true, "storage": true, "graphql": true,
	"information_schema": true, "pgforge": true,
}

func (a *app) schemaCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := sanitizeIdent(r.FormValue("name"))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", err.Error())
		return
	}
	if name == "" || strings.HasPrefix(name, "pg_") || reservedSchemas[name] {
		redirectErr(w, r, "/p/"+slug+"/tables", "Pick a different schema name.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE SCHEMA %s AUTHORIZATION %s`,
		pq.QuoteIdentifier(name), pq.QuoteIdentifier(a.roleFor(slug)))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Create schema failed: "+err.Error())
		return
	}
	a.audit(r, "schema-create", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/tables?sc="+name, "Schema "+name+" created.")
}

func (a *app) schemaDrop(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.FormValue("name")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", err.Error())
		return
	}
	if name == "public" || reservedSchemas[name] || a.edSchema(db, name) != name {
		redirectErr(w, r, "/p/"+slug+"/tables", "That schema cannot be dropped here.")
		return
	}
	// RESTRICT: only an EMPTY schema drops - anything inside makes this fail
	// loudly instead of cascading data away.
	if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA %s RESTRICT`, pq.QuoteIdentifier(name))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+name, "Drop failed (schema must be empty): "+err.Error())
		return
	}
	a.audit(r, "schema-drop", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/tables", "Schema "+name+" dropped.")
}

// listSchemas returns the database's user schemas, public first.
func (a *app) listSchemas(db *sql.DB) []string {
	rows, err := db.Query(`SELECT nspname FROM pg_namespace
		WHERE nspname NOT LIKE 'pg\_%' AND nspname NOT IN ('information_schema')
		ORDER BY (nspname <> 'public'), nspname`)
	if err != nil {
		return []string{"public"}
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	if len(out) == 0 {
		out = []string{"public"}
	}
	return out
}

// edSchema validates a requested schema name against the live database,
// falling back to public - every editor handler resolves schemas through this
// so a crafted value can never reach SQL.
func (a *app) edSchema(db *sql.DB, req string) string {
	if req == "" || req == "public" {
		return "public"
	}
	for _, s := range a.listSchemas(db) {
		if s == req {
			return req
		}
	}
	return "public"
}

// relation is anything browsable in the table editor.
type relation struct {
	Name, Kind string // kind: table | view | matview | foreign
}

var relKinds = map[string]string{"r": "table", "p": "table", "v": "view", "m": "matview", "f": "foreign"}

// listRelations lists tables, views, materialized views and foreign tables in
// one schema (tables first, then by name).
func (a *app) listRelations(db *sql.DB, schema string) []relation {
	rows, err := db.Query(`SELECT c.relname, c.relkind::text FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f')
		ORDER BY (c.relkind NOT IN ('r','p')), c.relname`, schema)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []relation
	for rows.Next() {
		var name, rk string
		rows.Scan(&name, &rk)
		out = append(out, relation{Name: name, Kind: relKinds[rk]})
	}
	return out
}

// relKind reports what a name is in a schema ("" when absent).
func relKind(db *sql.DB, schema, rel string) string {
	var rk string
	db.QueryRow(`SELECT c.relkind::text FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','v','m','f')`,
		schema, rel).Scan(&rk)
	return relKinds[rk]
}

// tableIn reports whether name is a real (writable) table in the schema.
func tableIn(db *sql.DB, schema, rel string) bool {
	return relKind(db, schema, rel) == "table"
}

func (a *app) tablePK(db *sql.DB, schema, table string) []string {
	rows, err := db.Query(`SELECT a.attname FROM pg_index i
		JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=ANY(i.indkey)
		WHERE i.indrelid=$1::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey,a.attnum)`, qrel(schema, table))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		out = append(out, c)
	}
	return out
}

type tableCol struct {
	Name, Type, Nullable, Default string
	UDT, UDTSchema, Comment       string
	FKSchema, FKTable, FKCol      string
	EnumVals                      []string
}

func (a *app) tableCols(db *sql.DB, schema, table string) []tableCol {
	reg := qrel(schema, table)
	// comments come via pg_attribute.attnum, NOT ordinal_position - the two
	// drift apart once a column has ever been dropped from the table
	rows, err := db.Query(`SELECT c.column_name, c.data_type, c.is_nullable, coalesce(c.column_default,''),
			c.udt_name, coalesce(c.udt_schema,''), coalesce((SELECT col_description($3::regclass, a.attnum)
				FROM pg_attribute a WHERE a.attrelid=$3::regclass AND a.attname=c.column_name),'')
		FROM information_schema.columns c WHERE c.table_schema=$2 AND c.table_name=$1
		ORDER BY c.ordinal_position`, table, schema, reg)
	if err != nil {
		return nil
	}
	var out []tableCol
	for rows.Next() {
		var c tableCol
		rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.UDT, &c.UDTSchema, &c.Comment)
		out = append(out, c)
	}
	rows.Close()
	// information_schema.columns skips materialized views and some foreign
	// tables - fall back to the catalog so their grids still get metadata
	if len(out) == 0 {
		if ars, err := db.Query(`SELECT a.attname, format_type(a.atttypid, a.atttypmod),
				CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END,
				coalesce(col_description($1::regclass, a.attnum),'')
			FROM pg_attribute a WHERE a.attrelid=$1::regclass AND a.attnum>0 AND NOT a.attisdropped
			ORDER BY a.attnum`, reg); err == nil {
			for ars.Next() {
				var c tableCol
				ars.Scan(&c.Name, &c.Type, &c.Nullable, &c.Comment)
				out = append(out, c)
			}
			ars.Close()
		}
	}
	// foreign keys: map each referencing column to its target schema.table.column
	if fks, err := db.Query(`SELECT a.attname, tn.nspname, cl.relname, af.attname
			FROM pg_constraint co
			JOIN pg_class cl ON cl.oid = co.confrelid
			JOIN pg_namespace tn ON tn.oid = cl.relnamespace
			JOIN unnest(co.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
			JOIN unnest(co.confkey) WITH ORDINALITY AS f(attnum, ord) ON f.ord = k.ord
			JOIN pg_attribute a ON a.attrelid = co.conrelid AND a.attnum = k.attnum
			JOIN pg_attribute af ON af.attrelid = co.confrelid AND af.attnum = f.attnum
			WHERE co.conrelid = $1::regclass AND co.contype = 'f'`, reg); err == nil {
		ref := map[string][3]string{}
		for fks.Next() {
			var col, rs, rt, rc string
			fks.Scan(&col, &rs, &rt, &rc)
			ref[col] = [3]string{rs, rt, rc}
		}
		fks.Close()
		for i := range out {
			if r, ok := ref[out[i].Name]; ok {
				out[i].FKSchema, out[i].FKTable, out[i].FKCol = r[0], r[1], r[2]
			}
		}
	}
	// enum labels for user-defined enum types (one lookup per distinct type)
	enums := map[string][]string{}
	for i := range out {
		if out[i].Type != "USER-DEFINED" {
			continue
		}
		ekey := out[i].UDTSchema + "." + out[i].UDT
		vals, seen := enums[ekey]
		if !seen {
			if ers, err := db.Query(`SELECT e.enumlabel FROM pg_type t
					JOIN pg_enum e ON e.enumtypid=t.oid
					JOIN pg_namespace n ON n.oid=t.typnamespace AND n.nspname=$2
					WHERE t.typname=$1 ORDER BY e.enumsortorder`, out[i].UDT, out[i].UDTSchema); err == nil {
				for ers.Next() {
					var l string
					ers.Scan(&l)
					vals = append(vals, l)
				}
				ers.Close()
			}
			enums[ekey] = vals
		}
		out[i].EnumVals = vals
		if len(vals) > 0 {
			out[i].Type = out[i].UDT // show the enum's own name instead of USER-DEFINED
		}
	}
	return out
}

// filterView echoes an active filter back into the UI.
type filterView struct{ Col, Op, Val string }

// sortView echoes an active sort rule back into the UI.
type sortView struct{ Col, Dir string }

var filterOps = map[string]string{
	"eq": "=", "neq": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=",
	"like": "LIKE", "ilike": "ILIKE",
}

// buildFilters turns repeated f=<col>.<op>.<value> params into a WHERE clause
// with bind args. Unknown columns/operators are dropped silently.
func buildFilters(params []string, colSet map[string]bool) (string, []any, []filterView) {
	var conds []string
	var args []any
	var views []filterView
	for _, p := range params {
		parts := strings.SplitN(p, ".", 3)
		if len(parts) < 2 || !colSet[parts[0]] {
			continue
		}
		col, op := parts[0], parts[1]
		val := ""
		if len(parts) == 3 {
			val = parts[2]
		}
		qi := pq.QuoteIdentifier(col)
		switch {
		case op == "is" && val == "null":
			conds = append(conds, qi+" IS NULL")
		case op == "is" && val == "notnull":
			conds = append(conds, qi+" IS NOT NULL")
		case op == "in":
			items := strings.Split(val, ",")
			ph := make([]string, len(items))
			for i, it := range items {
				args = append(args, strings.TrimSpace(it))
				ph[i] = fmt.Sprintf("$%d", len(args))
			}
			conds = append(conds, qi+"::text IN ("+strings.Join(ph, ",")+")")
		default:
			sqlOp, ok := filterOps[op]
			if !ok {
				continue
			}
			args = append(args, val)
			// compare as text so any column type works with any typed input
			conds = append(conds, fmt.Sprintf("%s::text %s $%d", qi, sqlOp, len(args)))
		}
		views = append(views, filterView{col, op, val})
	}
	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, views
}

// buildSort turns repeated s=<col>.asc|desc params into an ORDER BY.
func buildSort(params []string, colSet map[string]bool) (string, []sortView) {
	var parts []string
	var views []sortView
	for _, p := range params {
		bits := strings.SplitN(p, ".", 2)
		if !colSet[bits[0]] {
			continue
		}
		dir := "ASC"
		vd := "asc"
		if len(bits) == 2 && bits[1] == "desc" {
			dir, vd = "DESC", "desc"
		}
		parts = append(parts, pq.QuoteIdentifier(bits[0])+" "+dir)
		views = append(views, sortView{bits[0], vd})
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " ORDER BY " + strings.Join(parts, ", "), views
}

// bulkDeleteRows deletes many rows in one transaction. The rows arrive as a
// JSON array of {pkcol: value} objects; every column is validated against the
// table's real primary key.
func (a *app) bulkDeleteRows(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Database unavailable.")
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/tables?t=" + table + "&sc=" + sc
	if !tableIn(db, sc, table) {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Unknown table.")
		return
	}
	pk := a.tablePK(db, sc, table)
	if len(pk) == 0 {
		redirectErr(w, r, back, "Bulk delete needs a primary key.")
		return
	}
	var keys []map[string]string
	if json.Unmarshal([]byte(r.FormValue("keys")), &keys) != nil || len(keys) == 0 || len(keys) > 1000 {
		redirectErr(w, r, back, "Nothing selected.")
		return
	}
	tx, err := db.Begin()
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	n := 0
	for _, k := range keys {
		var conds []string
		var args []any
		for _, col := range pk {
			v, ok := k[col]
			if !ok {
				tx.Rollback()
				redirectErr(w, r, back, "Selection missing key column "+col+".")
				return
			}
			args = append(args, v)
			conds = append(conds, fmt.Sprintf("%s::text = $%d", pq.QuoteIdentifier(col), len(args)))
		}
		res, derr := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE %s`,
			qrel(sc, table), strings.Join(conds, " AND ")), args...)
		if derr != nil {
			tx.Rollback()
			redirectErr(w, r, back, "Delete failed: "+derr.Error())
			return
		}
		if aff, _ := res.RowsAffected(); aff > 0 {
			n++
		}
	}
	tx.Commit()
	a.audit(r, "rows-bulk-delete", fmt.Sprintf("%s/%s.%s x%d", slug, sc, table, n))
	redirectMsg(w, r, back, fmt.Sprintf("Deleted %d row(s).", n))
}

// duplicateTable copies a table's structure (LIKE ... INCLUDING ALL), and
// optionally its data, into a new table.
func (a *app) duplicateTable(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	src := r.FormValue("table")
	dst := strings.TrimSpace(strings.ToLower(r.FormValue("name")))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Database unavailable.")
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	if !tableIn(db, sc, src) {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Unknown table.")
		return
	}
	dst = sanitizeIdent(dst)
	if dst == "" {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+src+"&sc="+sc, "Enter a valid new table name.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE %s (LIKE %s INCLUDING ALL)`,
		qrel(sc, dst), qrel(sc, src))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+src+"&sc="+sc, "Duplicate failed: "+err.Error())
		return
	}
	a.chownRel(db, slug, "TABLE", sc, dst)
	if r.FormValue("with_data") == "on" {
		if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s`,
			qrel(sc, dst), qrel(sc, src))); err != nil {
			redirectErr(w, r, "/p/"+slug+"/tables?t="+dst+"&sc="+sc, "Structure copied, data copy failed: "+err.Error())
			return
		}
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "table-duplicate", slug+"/"+sc+"."+src+" -> "+dst)
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+dst+"&sc="+sc, "Table "+dst+" created from "+src+".")
}

func (a *app) tablesPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sc := a.edSchema(db, r.URL.Query().Get("sc"))
	rels := a.listRelations(db, sc)
	sel := r.URL.Query().Get("t")
	kind := ""
	for _, rel := range rels {
		if rel.Name == sel {
			kind = rel.Kind
		}
	}
	if kind == "" {
		sel = ""
	}
	if sel == "" && len(rels) > 0 {
		sel, kind = rels[0].Name, rels[0].Kind
	}
	// sibling databases (this project + its branches) for the DB selector
	var dbs []string
	if rows, err := a.db.Query(`SELECT slug FROM projects WHERE slug=$1 OR parent=$1 ORDER BY slug`, slug); err == nil {
		for rows.Next() {
			var s string
			rows.Scan(&s)
			dbs = append(dbs, s)
		}
		rows.Close()
	}

	data := map[string]any{"Slug": slug, "Rels": rels, "Sel": sel, "DBs": dbs,
		"Schema": sc, "Schemas": a.listSchemas(db), "Kind": kind, "Editable": kind == "table"}
	if sel != "" {
		cols := a.tableCols(db, sc, sel)
		pk := a.tablePK(db, sc, sel)
		// Defensive projection: bytea never leaves the database (a size marker
		// is computed in SQL) and non-key text is cut at 2 KB, so a table full
		// of images or huge documents still renders instantly. PK columns are
		// never truncated - the edit/delete forms need their exact values.
		isPK := map[string]bool{}
		for _, k := range pk {
			isPK[k] = true
		}
		var proj []string
		types := map[string]string{}
		for _, c := range cols {
			qi := pq.QuoteIdentifier(c.Name)
			types[c.Name] = c.Type
			switch {
			case c.Type == "bytea":
				proj = append(proj, fmt.Sprintf(
					`CASE WHEN %s IS NULL THEN NULL ELSE '[bytea · '||pg_size_pretty(octet_length(%s)::bigint)||']' END AS %s`,
					qi, qi, qi))
			case isPK[c.Name]:
				proj = append(proj, qi)
			default:
				proj = append(proj, fmt.Sprintf(
					`CASE WHEN %s IS NULL THEN NULL WHEN length(%s::text) > 2048 THEN left(%s::text,2048)||' ... [truncated, '||pg_size_pretty(length(%s::text)::bigint)||' total - edit via SQL]' ELSE %s::text END AS %s`,
					qi, qi, qi, qi, qi, qi))
			}
		}
		selExpr := "*"
		if len(proj) > 0 {
			selExpr = strings.Join(proj, ", ")
		}
		data["Types"] = types
		// table comment + per-column metadata for the client-side editors
		var tComment string
		db.QueryRow(`SELECT coalesce(obj_description($1::regclass),'')`,
			qrel(sc, sel)).Scan(&tComment)
		data["TableComment"] = tComment
		if kind == "view" || kind == "matview" {
			var def string
			db.QueryRow(`SELECT pg_get_viewdef($1::regclass, true)`, qrel(sc, sel)).Scan(&def)
			data["ViewDef"] = def
		}
		if kind == "table" && len(cols) > 0 {
			data["TableDef"] = buildTableDef(sc, sel, cols, pk, a.listIndexes(db, sc))
		}
		type colMetaJS struct {
			T    string   `json:"t"`
			Null bool     `json:"null"`
			FKS  string   `json:"fks,omitempty"`
			FKT  string   `json:"fkt,omitempty"`
			FKC  string   `json:"fkc,omitempty"`
			Enum []string `json:"enum,omitempty"`
		}
		cmeta := map[string]colMetaJS{}
		fkm := map[string]string{}
		for _, c := range cols {
			cmeta[c.Name] = colMetaJS{T: c.Type, Null: c.Nullable == "YES", FKS: c.FKSchema, FKT: c.FKTable, FKC: c.FKCol, Enum: c.EnumVals}
			if c.FKTable != "" {
				fkm[c.Name] = c.FKTable + "." + c.FKCol
			}
		}
		if b, err := json.Marshal(cmeta); err == nil {
			data["ColMetaJS"] = template.JS(b)
		}
		data["FKs"] = fkm
		// user enum types offered by the column pickers (current schema +
		// public, shown schema-qualified when not local)
		var enumTypes []string
		for _, e := range a.listEnums(db, sc) {
			enumTypes = append(enumTypes, e.Name)
		}
		if sc != "public" {
			for _, e := range a.listEnums(db, "public") {
				enumTypes = append(enumTypes, "public."+e.Name)
			}
		}
		data["EnumTypes"] = enumTypes
		colSet := map[string]bool{}
		for _, c := range cols {
			colSet[c.Name] = true
		}
		// Stacked filters: repeated f=<col>.<op>.<value> params. Columns are
		// validated against the real schema and values always travel as bind
		// parameters - the operator set is a fixed whitelist.
		where, args, filters := buildFilters(r.URL.Query()["f"], colSet)
		data["Filters"] = filters
		// Multi-sort: repeated s=<col>.asc|desc params (falls back to PK order).
		orderBy, sorts := buildSort(r.URL.Query()["s"], colSet)
		data["Sorts"] = sorts
		if orderBy == "" && len(pk) > 0 {
			qpk := make([]string, len(pk))
			for i, k := range pk {
				qpk[i] = pq.QuoteIdentifier(k)
			}
			orderBy = " ORDER BY " + strings.Join(qpk, ",")
		}
		// View-as-role: render the grid under anon/authenticated/service_role
		// inside a rolled-back transaction, so RLS policies apply to what you
		// see - the exact experience an API caller gets.
		va := r.URL.Query().Get("va")
		if va != "anon" && va != "authenticated" && va != "service_role" {
			va = ""
		}
		data["ViewAs"] = va
		if kind == "table" {
			var rlsOn bool
			var polN int
			db.QueryRow(`SELECT c.relrowsecurity,
					(SELECT count(*) FROM pg_policies p WHERE p.schemaname=$1 AND p.tablename=$2)
				FROM pg_class c WHERE c.oid = $3::regclass`, sc, sel, qrel(sc, sel)).Scan(&rlsOn, &polN)
			data["RLSOn"] = rlsOn
			data["RLSPol"] = polN
		}
		// Page size selector (25/100/500)
		pageSize := 100
		if ps, e := strconv.Atoi(r.URL.Query().Get("ps")); e == nil && (ps == 25 || ps == 100 || ps == 500) {
			pageSize = ps
		}
		data["PageSize"] = pageSize
		page := 1
		if p, e := strconv.Atoi(r.URL.Query().Get("p")); e == nil && p > 1 {
			page = p
		}
		gctx, gcancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer gcancel()
		q := fmt.Sprintf(`SELECT %s FROM %s%s%s LIMIT %d OFFSET %d`,
			selExpr, qrel(sc, sel), where, orderBy, pageSize, (page-1)*pageSize)
		var rows *sql.Rows
		var qerr error
		var vtx *sql.Tx
		if va == "" {
			rows, qerr = db.QueryContext(gctx, q, args...)
		} else if vtx, qerr = db.BeginTx(gctx, nil); qerr == nil {
			if _, qerr = vtx.ExecContext(gctx, "SET LOCAL ROLE "+pq.QuoteIdentifier(va)); qerr == nil {
				rows, qerr = vtx.QueryContext(gctx, q, args...)
			}
			if qerr != nil {
				vtx.Rollback()
				vtx = nil
			}
		}
		if qerr != nil {
			data["Error"] = qerr.Error()
		} else {
			cnames, recs, _ := scanRows(rows)
			rows.Close()
			if vtx != nil {
				vtx.Rollback()
			}
			if va != "" {
				// impersonated views are read-only in the grid
				data["Editable"] = false
				data["Cols"] = cnames
				data["Rows"] = recs
				data["Meta"] = cols
			}
			data["PK"] = pk
			data["HasPK"] = len(pk) > 0 && va == ""
			data["Page"] = page
			data["PrevPage"] = page - 1
			data["NextPage"] = page + 1
			data["HasPrev"] = page > 1
			data["HasNext"] = len(recs) == pageSize
			data["FirstRow"] = (page-1)*pageSize + 1
			data["LastRow"] = (page-1)*pageSize + len(recs)
			// Estimate only - never a blind count(*), which on a huge unanalyzed
			// table would itself be a full scan. -1 (never analyzed) shows as "?".
			var est int64 = -1
			db.QueryRowContext(gctx, `SELECT reltuples::bigint FROM pg_class WHERE oid=$1::regclass`,
				qrel(sc, sel)).Scan(&est)
			data["Est"] = est
			data["EstUnknown"] = est < 0
		}
	}
	content := renderContent(tablesBody, data)
	a.renderShell(w, r, shellData{Title: slug + " · Tables", Nav: "tables", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Table Editor"}}}, content)
}

func (a *app) rowInsert(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("__table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/tables?t=" + table + "&sc=" + sc
	if !tableIn(db, sc, table) {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Unknown table.")
		return
	}
	r.ParseForm()
	var cols []string
	var vals []any
	var ph []string
	i := 1
	for _, c := range a.tableCols(db, sc, table) {
		v := r.FormValue("c_" + c.Name)
		if v == "" {
			continue // let defaults/NULL apply
		}
		cols = append(cols, pq.QuoteIdentifier(c.Name))
		vals = append(vals, v)
		ph = append(ph, fmt.Sprintf("$%d", i))
		i++
	}
	if len(cols) == 0 {
		redirectErr(w, r, back, "Nothing to insert.")
		return
	}
	q := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		qrel(sc, table), strings.Join(cols, ","), strings.Join(ph, ","))
	if _, err := db.Exec(q, vals...); err != nil {
		redirectErr(w, r, back, "Insert failed: "+err.Error())
		return
	}
	a.audit(r, "row-insert", slug+"/"+sc+"."+table)
	redirectMsg(w, r, back, "Row inserted.")
}

func (a *app) rowUpdate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("__table")
	col := r.FormValue("__col")
	val := r.FormValue("__val")
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	if !tableIn(db, sc, table) {
		http.Error(w, "unknown table", 400)
		return
	}
	pk := a.tablePK(db, sc, table)
	if len(pk) == 0 {
		http.Error(w, "table has no primary key", 400)
		return
	}
	var where []string
	var args []any
	// __null=1 writes SQL NULL (nil travels as NULL through the driver)
	var sv any = val
	if r.FormValue("__null") == "1" {
		sv = nil
	}
	args = append(args, sv)
	i := 2
	for _, k := range pk {
		where = append(where, fmt.Sprintf("%s=$%d", pq.QuoteIdentifier(k), i))
		args = append(args, r.FormValue("pk_"+k))
		i++
	}
	q := fmt.Sprintf(`UPDATE %s SET %s=$1 WHERE %s`,
		qrel(sc, table), pq.QuoteIdentifier(col), strings.Join(where, " AND "))
	res, err := db.Exec(q, args...)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// No row matched - report it instead of a false 204, so the grid shows
		// the edit failed rather than silently discarding it.
		http.Error(w, "no row matched (the key may be unselectable in the grid)", http.StatusConflict)
		return
	}
	a.audit(r, "row-update", slug+"/"+table)
	w.WriteHeader(204)
}

func (a *app) rowDelete(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("__table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/tables?t=" + table + "&sc=" + sc
	if !tableIn(db, sc, table) {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Unknown table.")
		return
	}
	pk := a.tablePK(db, sc, table)
	if len(pk) == 0 {
		redirectErr(w, r, back, "Table has no primary key; delete via SQL editor.")
		return
	}
	var where []string
	var args []any
	i := 1
	for _, k := range pk {
		where = append(where, fmt.Sprintf("%s=$%d", pq.QuoteIdentifier(k), i))
		args = append(args, r.FormValue("pk_"+k))
		i++
	}
	q := fmt.Sprintf(`DELETE FROM %s WHERE %s`, qrel(sc, table), strings.Join(where, " AND "))
	res, err := db.Exec(q, args...)
	if err != nil {
		redirectErr(w, r, back, "Delete failed: "+err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		redirectErr(w, r, back, "No row matched; nothing deleted.")
		return
	}
	a.audit(r, "row-delete", slug+"/"+sc+"."+table)
	redirectMsg(w, r, back, "Row deleted.")
}

// rowUpdateFull updates several columns of one row in a single UPDATE. The
// side panel posts every field plus a __dirty list; only dirty columns are
// written, so untouched fields can never clobber concurrent changes.
func (a *app) rowUpdateFull(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	r.ParseForm()
	table := r.FormValue("__table")
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	if !tableIn(db, sc, table) {
		http.Error(w, "unknown table", 400)
		return
	}
	pk := a.tablePK(db, sc, table)
	if len(pk) == 0 {
		http.Error(w, "table has no primary key", 400)
		return
	}
	colSet := map[string]bool{}
	for _, c := range a.tableCols(db, sc, table) {
		colSet[c.Name] = true
	}
	var sets []string
	var args []any
	for _, c := range r.Form["__dirty"] {
		if !colSet[c] {
			continue
		}
		if r.FormValue("n_"+c) == "1" {
			sets = append(sets, pq.QuoteIdentifier(c)+"=NULL")
			continue
		}
		args = append(args, r.FormValue("c_"+c))
		sets = append(sets, fmt.Sprintf("%s=$%d", pq.QuoteIdentifier(c), len(args)))
	}
	if len(sets) == 0 {
		http.Error(w, "nothing changed", 400)
		return
	}
	var where []string
	for _, k := range pk {
		args = append(args, r.FormValue("pk_"+k))
		where = append(where, fmt.Sprintf("%s=$%d", pq.QuoteIdentifier(k), len(args)))
	}
	q := fmt.Sprintf(`UPDATE %s SET %s WHERE %s`,
		qrel(sc, table), strings.Join(sets, ", "), strings.Join(where, " AND "))
	res, err := db.Exec(q, args...)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "no row matched", http.StatusConflict)
		return
	}
	a.audit(r, "row-update", slug+"/"+table)
	w.WriteHeader(204)
}

// rowJSON returns one full row as JSON for the side panel - the grid truncates
// long values, so the panel re-reads them at full length (capped at 256 KB per
// value; bytea stays a size marker).
func (a *app) rowJSON(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.URL.Query().Get("t")
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sc := a.edSchema(db, r.URL.Query().Get("sc"))
	if relKind(db, sc, table) == "" {
		http.Error(w, "unknown table", 400)
		return
	}
	pk := a.tablePK(db, sc, table)
	if len(pk) == 0 {
		http.Error(w, "table has no primary key", 400)
		return
	}
	cols := a.tableCols(db, sc, table)
	var proj []string
	for _, c := range cols {
		qi := pq.QuoteIdentifier(c.Name)
		if c.Type == "bytea" {
			proj = append(proj, fmt.Sprintf(
				`CASE WHEN %s IS NULL THEN NULL ELSE '[bytea · '||pg_size_pretty(octet_length(%s)::bigint)||']' END`, qi, qi))
		} else {
			proj = append(proj, fmt.Sprintf(`left(%s::text, 262144)`, qi))
		}
	}
	var where []string
	var args []any
	for _, k := range pk {
		args = append(args, r.URL.Query().Get("pk_"+k))
		where = append(where, fmt.Sprintf("%s=$%d", pq.QuoteIdentifier(k), len(args)))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	row := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s LIMIT 1`,
		strings.Join(proj, ", "), qrel(sc, table), strings.Join(where, " AND ")), args...)
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := row.Scan(ptrs...); err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	type field struct {
		C    string  `json:"c"`
		V    *string `json:"v"`
		Null bool    `json:"null"`
	}
	out := make([]field, 0, len(cols))
	for i, c := range cols {
		f := field{C: c.Name}
		switch v := vals[i].(type) {
		case nil:
			f.Null = true
		case []byte:
			s := string(v)
			f.V = &s
		case string:
			f.V = &v
		default:
			s := fmt.Sprint(v)
			f.V = &s
		}
		out = append(out, f)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// fkOptions returns up to 50 candidate values for a foreign-key column: the
// referenced key plus a human label (the referenced table's first text-ish
// column). Backs the pickers in the row panel.
func (a *app) fkOptions(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	refT := r.URL.Query().Get("t")
	refC := r.URL.Query().Get("c")
	search := r.URL.Query().Get("q")
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sc := a.edSchema(db, r.URL.Query().Get("s"))
	if relKind(db, sc, refT) == "" {
		http.Error(w, "unknown table", 400)
		return
	}
	label, okCol := "", false
	for _, c := range a.tableCols(db, sc, refT) {
		if c.Name == refC {
			okCol = true
		}
		if label == "" && c.Name != refC && (c.Type == "text" || c.Type == "character varying") {
			label = c.Name
		}
	}
	if !okCol {
		http.Error(w, "unknown column", 400)
		return
	}
	lsel := "''"
	if label != "" {
		lsel = "left(" + pq.QuoteIdentifier(label) + "::text, 60)"
	}
	q := fmt.Sprintf(`SELECT %s::text, coalesce(%s,'') FROM %s`,
		pq.QuoteIdentifier(refC), lsel, qrel(sc, refT))
	var args []any
	if search != "" {
		args = append(args, "%"+search+"%")
		q += fmt.Sprintf(` WHERE %s::text ILIKE $1`, pq.QuoteIdentifier(refC))
		if label != "" {
			q += fmt.Sprintf(` OR %s::text ILIKE $1`, pq.QuoteIdentifier(label))
		}
	}
	q += ` ORDER BY 1 LIMIT 50`
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	defer rows.Close()
	type opt struct {
		V string `json:"v"`
		L string `json:"l"`
	}
	out := make([]opt, 0, 50)
	for rows.Next() {
		var o opt
		rows.Scan(&o.V, &o.L)
		out = append(out, o)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// target types the column editor may cast to (base name + optional precision)
var alterTypeRe = regexp.MustCompile(`^(text|integer|bigint|smallint|boolean|real|double precision|numeric(\(\d+,\d+\))?|timestamptz|timestamp|date|time|uuid|jsonb|json|inet|bytea|varchar\(\d+\)|character varying(\(\d+\))?)$`)

// default expressions allowed through raw; anything else is quoted as a literal
var rawDefaultRe = regexp.MustCompile(`^(now\(\)|current_timestamp|current_date|gen_random_uuid\(\)|true|false|-?\d+(\.\d+)?)$`)

// columnAlter applies the column-edit form as a diff: only aspects that changed
// produce ALTERs, all inside one transaction, with any rename last so earlier
// statements still address the old name.
func (a *app) columnAlter(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	col := r.FormValue("column")
	back := "/p/" + slug + "/tables?t=" + table
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back = "/p/" + slug + "/tables?t=" + table + "&sc=" + sc
	okc := false
	for _, c := range a.tableCols(db, sc, table) {
		if c.Name == col {
			okc = true
		}
	}
	if !tableIn(db, sc, table) || !okc {
		redirectErr(w, r, back, "Unknown table or column.")
		return
	}
	qt, qc := qrel(sc, table), pq.QuoteIdentifier(col)
	var stmts, applied []string
	if typ := strings.ToLower(strings.TrimSpace(r.FormValue("type"))); typ != "" {
		if alterTypeRe.MatchString(typ) {
			stmts = append(stmts, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s`, qt, qc, typ, qc, typ))
		} else {
			es, en := sc, typ
			if i := strings.IndexByte(typ, '.'); i > 0 {
				es, en = typ[:i], typ[i+1:]
			}
			if !a.enumExists(db, es, en) {
				redirectErr(w, r, back, "Unsupported target type.")
				return
			}
			// enums cast reliably via text
			et := qrel(es, en)
			stmts = append(stmts, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::text::%s`, qt, qc, et, qc, et))
		}
		applied = append(applied, "type")
	}
	if d, od := strings.TrimSpace(r.FormValue("default")), strings.TrimSpace(r.FormValue("__old_default")); d != od {
		switch {
		case d == "":
			stmts = append(stmts, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT`, qt, qc))
		case rawDefaultRe.MatchString(strings.ToLower(d)):
			stmts = append(stmts, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s`, qt, qc, d))
		default:
			stmts = append(stmts, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s`, qt, qc, pq.QuoteLiteral(d)))
		}
		applied = append(applied, "default")
	}
	if nn, was := r.FormValue("notnull") == "on", r.FormValue("__old_notnull") == "1"; nn != was {
		if nn {
			stmts = append(stmts, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s SET NOT NULL`, qt, qc))
		} else {
			stmts = append(stmts, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL`, qt, qc))
		}
		applied = append(applied, "nullability")
	}
	if cm, ocm := strings.TrimSpace(r.FormValue("comment")), strings.TrimSpace(r.FormValue("__old_comment")); cm != ocm {
		lit := "NULL"
		if cm != "" {
			lit = pq.QuoteLiteral(cm)
		}
		stmts = append(stmts, fmt.Sprintf(`COMMENT ON COLUMN %s.%s IS %s`, qt, qc, lit))
		applied = append(applied, "comment")
	}
	if newName := sanitizeIdent(r.FormValue("name")); newName != "" && newName != col {
		stmts = append(stmts, fmt.Sprintf(`ALTER TABLE %s RENAME COLUMN %s TO %s`, qt, qc, pq.QuoteIdentifier(newName)))
		applied = append(applied, "name")
	}
	if len(stmts) == 0 {
		redirectMsg(w, r, back, "No changes.")
		return
	}
	tx, err := db.Begin()
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			tx.Rollback()
			redirectErr(w, r, back, "Change failed (nothing applied): "+err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "column-alter", slug+"/"+sc+"."+table+"."+col+" ("+strings.Join(applied, ",")+")")
	redirectMsg(w, r, back, "Column "+col+" updated ("+strings.Join(applied, ", ")+").")
}

func (a *app) tableRename(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	newName := sanitizeIdent(r.FormValue("name"))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	if !tableIn(db, sc, table) {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Unknown table.")
		return
	}
	if newName == "" || newName == table {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table+"&sc="+sc, "Enter a new table name.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`,
		qrel(sc, table), pq.QuoteIdentifier(newName))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table+"&sc="+sc, "Rename failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "table-rename", slug+"/"+sc+"."+table+" -> "+newName)
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+newName+"&sc="+sc, "Table renamed to "+newName+".")
}

// matviewRefresh re-runs a materialized view's query (plain refresh - the
// concurrent variant needs a unique index and is a later refinement).
func (a *app) matviewRefresh(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/tables?t=" + table + "&sc=" + sc
	if relKind(db, sc, table) != "matview" {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Not a materialized view.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`REFRESH MATERIALIZED VIEW %s`, qrel(sc, table))); err != nil {
		redirectErr(w, r, back, "Refresh failed: "+err.Error())
		return
	}
	a.audit(r, "matview-refresh", slug+"/"+sc+"."+table)
	redirectMsg(w, r, back, "Materialized view refreshed.")
}

func (a *app) tableComment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	cm := strings.TrimSpace(r.FormValue("comment"))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/tables?t=" + table + "&sc=" + sc
	kw := map[string]string{"table": "TABLE", "view": "VIEW", "matview": "MATERIALIZED VIEW", "foreign": "FOREIGN TABLE"}[relKind(db, sc, table)]
	if kw == "" {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Unknown table.")
		return
	}
	lit := "NULL"
	if cm != "" {
		lit = pq.QuoteLiteral(cm)
	}
	if _, err := db.Exec(fmt.Sprintf(`COMMENT ON %s %s IS %s`, kw, qrel(sc, table), lit)); err != nil {
		redirectErr(w, r, back, "Comment failed: "+err.Error())
		return
	}
	a.audit(r, "table-comment", slug+"/"+sc+"."+table)
	redirectMsg(w, r, back, "Comment saved.")
}

// ----------------------------------------------------------------- sql editor

// schemaTree returns tables with their columns+types for the SQL editor browser.
type schemaTable struct {
	Name string
	Cols []tableCol
}

func (a *app) schemaTree(slug string) []schemaTable {
	db, err := a.dbFor(slug)
	if err != nil {
		return nil
	}
	var out []schemaTable
	for _, t := range a.listTables(db) {
		out = append(out, schemaTable{Name: t, Cols: a.tableCols(db, "public", t)})
	}
	return out
}

type savedQuery struct{ ID, Name, SQL string }

func (a *app) savedQueries(slug string) []savedQuery {
	rows, err := a.db.Query(`SELECT id, name, sql FROM saved_queries WHERE slug=$1 ORDER BY name`, slug)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []savedQuery
	for rows.Next() {
		var q savedQuery
		rows.Scan(&q.ID, &q.Name, &q.SQL)
		out = append(out, q)
	}
	return out
}

func (a *app) sqlPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	q := ""
	if id := r.URL.Query().Get("load"); id != "" {
		a.db.QueryRow(`SELECT sql FROM saved_queries WHERE id=$1 AND slug=$2`, id, slug).Scan(&q)
	}
	content := renderContent(sqlBody, map[string]any{"Slug": slug, "Query": q,
		"Schema": a.schemaTree(slug), "Saved": a.savedQueries(slug),
		"History": a.sqlHistory(slug), "Limit": 0})
	a.renderShell(w, r, shellData{Title: slug + " · SQL", Nav: "sql", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "SQL Editor"}}}, content)
}

func (a *app) saveQuery(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := strings.TrimSpace(r.FormValue("name"))
	sql := r.FormValue("query")
	if name == "" || strings.TrimSpace(sql) == "" {
		redirectErr(w, r, "/p/"+slug+"/sql", "Enter a name and a query to save.")
		return
	}
	a.db.Exec(`INSERT INTO saved_queries(slug,name,sql) VALUES ($1,$2,$3)`, slug, name, sql)
	a.audit(r, "sql-save", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/sql", "Saved query \""+name+"\".")
}

func (a *app) deleteSavedQuery(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a.db.Exec(`DELETE FROM saved_queries WHERE id=$1 AND slug=$2`, r.FormValue("id"), slug)
	a.audit(r, "sql-delete", slug)
	redirectMsg(w, r, "/p/"+slug+"/sql", "Saved query deleted.")
}

func (a *app) sqlRun(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	query := r.FormValue("query")
	// "Explain" button: wrap the statement in a JSON-format plan request and
	// render it as a tree instead of running it for real.
	explainMode := r.FormValue("explain") == "1"
	if explainMode {
		q := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
		low := strings.ToLower(q)
		if !strings.HasPrefix(low, "explain") {
			analyze := "false"
			if r.FormValue("analyze") == "1" && (strings.HasPrefix(low, "select") || strings.HasPrefix(low, "with")) {
				analyze = "true" // ANALYZE only for reads - never execute writes to plan them
			}
			query = "EXPLAIN (ANALYZE " + analyze + ", COSTS true, BUFFERS false, FORMAT JSON) " + q
		}
	}
	if qs := strings.TrimSpace(query); qs != "" {
		if len(qs) > 60 {
			qs = qs[:60] + "…"
		}
		a.audit(r, "sql-run", slug+": "+qs)
	}
	rowLimit := 0
	fmt.Sscanf(r.FormValue("limit"), "%d", &rowLimit)
	db, err := a.dbFor(slug)
	echo := r.FormValue("buffer") // full editor buffer (run-selection posts only the selection as query)
	if strings.TrimSpace(echo) == "" {
		echo = r.FormValue("query")
	}
	data := map[string]any{"Slug": slug, "Query": echo, "Schema": a.schemaTree(slug), "Saved": a.savedQueries(slug), "RunAs": "", "Limit": rowLimit, "History": a.sqlHistory(slug)}
	if err != nil {
		data["Error"] = err.Error()
	} else {
		// Interactive queries get a hard 30s deadline so a stray pg_sleep or a
		// runaway CTE can't wedge a pool connection (lib/pq cancels on ctx done).
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		start := time.Now()
		trimmed := strings.ToLower(stripLeadingComments(query))

		// Run-as-role: run inside a transaction under anon/authenticated/
		// service_role so you can TEST rls policies right here (owner bypasses
		// RLS). Empty = owner (the default).
		var runner execQuerier = db
		var tx *sql.Tx
		if role := r.FormValue("role"); role == "anon" || role == "authenticated" || role == "service_role" {
			if t, e := db.BeginTx(ctx, nil); e == nil {
				tx = t
				t.ExecContext(ctx, fmt.Sprintf("SET LOCAL ROLE %s", pq.QuoteIdentifier(role)))
				runner = t
				data["RunAs"] = role
			}
		}
		hadErr := false
		if strings.HasPrefix(trimmed, "select") || strings.HasPrefix(trimmed, "with") ||
			strings.HasPrefix(trimmed, "show") || strings.HasPrefix(trimmed, "explain") ||
			strings.HasPrefix(trimmed, "table") {
			rows, qerr := runner.QueryContext(ctx, query)
			if qerr != nil {
				data["Error"] = qerr.Error()
				hadErr = true
			} else {
				cols, recs, _ := scanRowsN(rows, rowLimit)
				rows.Close()
				if explainMode && len(recs) > 0 && len(recs[0]) > 0 {
					data["Plan"] = renderPlanTree(recs[0][0].Val)
				}
				data["Cols"] = cols
				data["Rows"] = recs
				data["Took"] = time.Since(start).Round(time.Millisecond).String()
				data["Count"] = len(recs)
				if len(recs) >= maxScanRows {
					data["Capped"] = true
				}
			}
		} else {
			res, eerr := runner.ExecContext(ctx, query)
			if eerr != nil {
				data["Error"] = eerr.Error()
				hadErr = true
			} else {
				n, _ := res.RowsAffected()
				data["Took"] = time.Since(start).Round(time.Millisecond).String()
				data["Affected"] = n
				data["Ok"] = true
				if isDDL(trimmed) {
					a.reloadPostgRESTSchema(slug) // new/changed tables show on REST now
				}
			}
		}
		if tx != nil {
			if hadErr {
				tx.Rollback()
			} else {
				tx.Commit()
			}
		}
		// persistent, team-visible run history (newest 200 per project)
		if qs := strings.TrimSpace(r.FormValue("query")); qs != "" && !explainMode {
			a.db.Exec(`INSERT INTO sql_history(slug, sql, ok, took_ms) VALUES ($1,$2,$3,$4)`,
				slug, qs, !hadErr, int(time.Since(start).Milliseconds()))
			a.db.Exec(`DELETE FROM sql_history h WHERE h.slug=$1 AND h.id NOT IN (
				SELECT id FROM sql_history WHERE slug=$1 ORDER BY at DESC LIMIT 200)`, slug)
			data["History"] = a.sqlHistory(slug)
		}
	}
	content := renderContent(sqlBody, data)
	a.renderShell(w, r, shellData{Title: slug + " · SQL", Nav: "sql", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "SQL Editor"}}}, content)
}

type sqlHistRow struct {
	ID              int64
	SQL, Took, When string
	OK              bool
}

// sqlHistory returns the newest runs for the history panel.
func (a *app) sqlHistory(slug string) []sqlHistRow {
	rows, err := a.db.Query(`SELECT id, sql, ok, took_ms, to_char(at,'Mon DD HH24:MI')
		FROM sql_history WHERE slug=$1 ORDER BY at DESC LIMIT 25`, slug)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []sqlHistRow
	for rows.Next() {
		var h sqlHistRow
		var ms int
		rows.Scan(&h.ID, &h.SQL, &h.OK, &ms, &h.When)
		h.Took = fmt.Sprintf("%dms", ms)
		out = append(out, h)
	}
	return out
}

type planNode struct {
	Label, Detail string
	Depth         int
}

// renderPlanTree flattens EXPLAIN (FORMAT JSON) output into an indented tree
// the template can render - a visual query plan without any client-side JS.
func renderPlanTree(planJSON string) []planNode {
	var top []map[string]any
	if json.Unmarshal([]byte(planJSON), &top) != nil || len(top) == 0 {
		return nil
	}
	root, _ := top[0]["Plan"].(map[string]any)
	if root == nil {
		return nil
	}
	var out []planNode
	var walk func(n map[string]any, depth int)
	walk = func(n map[string]any, depth int) {
		nt, _ := n["Node Type"].(string)
		label := nt
		if rel, ok := n["Relation Name"].(string); ok && rel != "" {
			label += " on " + rel
		}
		if idx, ok := n["Index Name"].(string); ok && idx != "" {
			label += " using " + idx
		}
		det := ""
		if c, ok := n["Total Cost"].(float64); ok {
			det = fmt.Sprintf("cost=%.1f", c)
		}
		if rws, ok := n["Plan Rows"].(float64); ok {
			det += fmt.Sprintf(" rows=%.0f", rws)
		}
		if at, ok := n["Actual Total Time"].(float64); ok {
			det += fmt.Sprintf(" time=%.2fms", at)
		}
		if ar, ok := n["Actual Rows"].(float64); ok {
			det += fmt.Sprintf(" actual=%.0f", ar)
		}
		if f, ok := n["Filter"].(string); ok && f != "" {
			det += " filter: " + f
		}
		if ic, ok := n["Index Cond"].(string); ok && ic != "" {
			det += " cond: " + ic
		}
		out = append(out, planNode{Label: label, Detail: strings.TrimSpace(det), Depth: depth})
		if subs, ok := n["Plans"].([]any); ok {
			for _, sp := range subs {
				if m, ok := sp.(map[string]any); ok {
					walk(m, depth+1)
				}
			}
		}
	}
	walk(root, 0)
	return out
}

// detectDelimiter sniffs the header line for the most likely CSV delimiter, so a
// semicolon- or tab-separated export (Excel in much of Europe) doesn't collapse
// into one giant text column.
func detectDelimiter(br *bufio.Reader) rune {
	peek, _ := br.Peek(8192)
	line := string(peek)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	counts := map[rune]int{',': 0, ';': 0, '\t': 0, '|': 0}
	inQuote := false
	for _, c := range line {
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if _, ok := counts[c]; ok {
			counts[c]++
		}
	}
	best, bestN := ',', -1
	for d, n := range counts {
		if n > bestN {
			bestN, best = n, d
		}
	}
	return best
}

// exportSQLInserts streams a result set as INSERT statements - a portable dump
// of the visible table that restores anywhere with plain psql.
func exportSQLInserts(w http.ResponseWriter, rows *sql.Rows, table, qtable string) {
	cols, _ := rows.Columns()
	qcols := make([]string, len(cols))
	for i, c := range cols {
		qcols[i] = pq.QuoteIdentifier(c)
	}
	w.Header().Set("Content-Type", "application/sql")
	w.Header().Set("Content-Disposition", "attachment; filename="+table+".sql")
	fmt.Fprintf(w, "-- %s: exported by ForgeBase\n", table)
	raw := make([]any, len(cols))
	ptr := make([]any, len(cols))
	for i := range raw {
		ptr[i] = &raw[i]
	}
	for rows.Next() {
		if rows.Scan(ptr...) != nil {
			return
		}
		vals := make([]string, len(cols))
		for i, v := range raw {
			switch x := v.(type) {
			case nil:
				vals[i] = "NULL"
			case bool:
				vals[i] = fmt.Sprintf("%v", x)
			case int64:
				vals[i] = fmt.Sprintf("%d", x)
			case float64:
				vals[i] = fmt.Sprintf("%g", x)
			case time.Time:
				vals[i] = pq.QuoteLiteral(x.Format(time.RFC3339Nano))
			case []byte:
				vals[i] = pq.QuoteLiteral(string(x))
			default:
				vals[i] = pq.QuoteLiteral(fmt.Sprintf("%v", x))
			}
		}
		fmt.Fprintf(w, "INSERT INTO %s (%s) VALUES (%s);\n",
			qtable, strings.Join(qcols, ", "), strings.Join(vals, ", "))
	}
}

// importCSV creates a table from a CSV and loads every row inside one
// transaction: either all rows land or none do, and any parse or insert error
// is reported (with the row number) instead of silently truncating the import.
func (a *app) importCSV(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Upload too large or malformed.")
		return
	}
	table := sanitizeIdent(r.FormValue("table"))
	if table == "" {
		redirectErr(w, r, "/p/"+slug+"/tables", "Enter a table name.")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "No CSV provided.")
		return
	}
	defer file.Close()

	br := bufio.NewReader(file)
	cr := csv.NewReader(br)
	cr.Comma = detectDelimiter(br)
	cr.LazyQuotes = false
	head, err := cr.Read()
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Empty or unreadable CSV.")
		return
	}
	// Strict field count from here on, so a ragged row is a loud error rather
	// than silently truncated/NULL-padded.
	cr.FieldsPerRecord = len(head)

	// Column names: sanitize, fill blanks, and de-duplicate correctly even for
	// three or more colliding headers.
	cols := make([]string, len(head))
	used := map[string]bool{}
	for i, h := range head {
		base := sanitizeIdent(h)
		if base == "" {
			base = fmt.Sprintf("col%d", i+1)
		}
		c := base
		for k := 1; used[c]; k++ {
			c = fmt.Sprintf("%s_%d", base, k)
		}
		used[c] = true
		cols[i] = c
	}

	// Read all rows; distinguish a real parse error from EOF.
	var records [][]string
	for {
		rec, rerr := cr.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			msg := rerr.Error()
			if errors.Is(rerr, csv.ErrFieldCount) {
				msg = fmt.Sprintf("row %d has a different number of columns than the header", len(records)+2)
			}
			redirectErr(w, r, "/p/"+slug+"/tables", "CSV parse error: "+msg+" (nothing imported).")
			return
		}
		records = append(records, rec)
	}

	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	var defs []string
	for i, c := range cols {
		defs = append(defs, pq.QuoteIdentifier(c)+" "+inferColType(records, i))
	}
	qcols := make([]string, len(cols))
	ph := make([]string, len(cols))
	for i, c := range cols {
		qcols[i] = pq.QuoteIdentifier(c)
		ph[i] = fmt.Sprintf("$%d", i+1)
	}

	// One transaction: create the table and load every row, or roll the whole
	// thing back on the first failure. A timeout guards against a pathological
	// file wedging a connection.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Import failed: "+err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (%s)`,
		qrel(sc, table), strings.Join(defs, ", "))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Create table failed: "+err.Error())
		return
	}
	ins := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		qrel(sc, table), strings.Join(qcols, ","), strings.Join(ph, ","))
	stmt, err := tx.PrepareContext(ctx, ins)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Import failed: "+err.Error())
		return
	}
	for ri, rec := range records {
		vals := make([]any, len(cols))
		for i := range cols {
			if i < len(rec) && rec[i] != "" {
				vals[i] = rec[i] // Postgres casts the text to the typed column
			}
		}
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			redirectErr(w, r, "/p/"+slug+"/tables",
				fmt.Sprintf("Row %d failed: %s (nothing imported).", ri+2, err.Error()))
			return
		}
	}
	if err := tx.Commit(); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Import failed on commit: "+err.Error())
		return
	}
	a.chownRel(db, slug, "TABLE", sc, table)
	a.reloadPostgRESTSchema(slug) // the new table is queryable over REST immediately
	a.audit(r, "import-csv", fmt.Sprintf("%s.%s (%d rows)", sc, table, len(records)))
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+table+"&sc="+sc, fmt.Sprintf("Imported %d rows into %s.", len(records), table))
}

var (
	intRe     = regexp.MustCompile(`^-?\d{1,18}$`)
	numRe     = regexp.MustCompile(`^-?\d*\.\d+$`)
	tsLayouts = []string{"2006-01-02", "2006-01-02 15:04:05", "2006-01-02T15:04:05", time.RFC3339}
)

// hasLeadingZero reports a numeric string like "01234" or "-0007" whose leading
// zero is significant (ZIP/phone/SKU/account numbers). Promoting these to bigint
// silently destroys the zero, so such columns must stay text.
func hasLeadingZero(v string) bool {
	v = strings.TrimPrefix(v, "-")
	return len(v) > 1 && v[0] == '0'
}

// inferColType picks the tightest Postgres type that fits every non-empty value
// in a column, falling back to text. It is deliberately conservative: it will
// not treat leading-zero numbers as integers, and only full true/false (not the
// single letters t/f, which collide with real data like grade codes) as boolean.
func inferColType(records [][]string, ci int) string {
	sawInt, sawNum, sawBool, sawTS, any := true, true, true, true, false
	for _, rec := range records {
		if ci >= len(rec) {
			continue
		}
		v := strings.TrimSpace(rec[ci])
		if v == "" {
			continue
		}
		any = true
		if !intRe.MatchString(v) || hasLeadingZero(v) {
			sawInt = false
		}
		if !numRe.MatchString(v) {
			sawNum = false
		}
		lv := strings.ToLower(v)
		if lv != "true" && lv != "false" {
			sawBool = false
		}
		ts := false
		for _, l := range tsLayouts {
			if _, err := time.Parse(l, v); err == nil {
				ts = true
				break
			}
		}
		if !ts {
			sawTS = false
		}
	}
	switch {
	case !any:
		return "text"
	case sawInt:
		return "bigint"
	case sawNum:
		return "numeric"
	case sawBool:
		return "boolean"
	case sawTS:
		return "timestamptz"
	default:
		return "text"
	}
}

var identBad = regexp.MustCompile(`[^a-z0-9_]+`)

func sanitizeIdent(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = identBad.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return ""
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "t_" + s
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// stripLeadingComments removes leading -- and /* */ comments + whitespace so the
// read-vs-write branch detection sees the real first keyword (a query that
// starts with a comment used to be misrouted to Exec and its rows discarded).
func stripLeadingComments(s string) string {
	s = strings.TrimSpace(s)
	for {
		switch {
		case strings.HasPrefix(s, "--"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return ""
			}
			s = strings.TrimSpace(s[i+1:])
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return ""
			}
			s = strings.TrimSpace(s[i+2:])
		default:
			return s
		}
	}
}

// colTypes is the allowlist of column types the visual schema editor may use
// (the type name is interpolated into DDL, so it must not come from free text).
var colTypes = map[string]bool{
	"text": true, "varchar": true, "integer": true, "bigint": true, "smallint": true,
	"boolean": true, "numeric": true, "real": true, "double precision": true,
	"timestamptz": true, "timestamp": true, "date": true, "time": true,
	"uuid": true, "json": true, "jsonb": true, "bytea": true,
}

// createTable makes a new table with a bigserial primary key; columns are added
// from the table editor afterward.
func (a *app) createTable(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := sanitizeIdent(r.FormValue("name"))
	if name == "" {
		redirectErr(w, r, "/p/"+slug+"/tables", "Enter a table name.")
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE %s (id bigserial PRIMARY KEY)`, qrel(sc, name))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Create failed: "+err.Error())
		return
	}
	a.chownRel(db, slug, "TABLE", sc, name)
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "table-create", slug+"/"+sc+"."+name)
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+name+"&sc="+sc, "Table "+name+" created. Add columns below.")
}

func (a *app) dropTable(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	kw := map[string]string{"table": "TABLE", "view": "VIEW", "matview": "MATERIALIZED VIEW", "foreign": "FOREIGN TABLE"}[relKind(db, sc, table)]
	if kw == "" {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Unknown table.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP %s %s`, kw, qrel(sc, table))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Drop failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "table-drop", slug+"/"+sc+"."+table)
	redirectMsg(w, r, "/p/"+slug+"/tables?sc="+sc, "Table "+table+" dropped.")
}

func (a *app) addColumn(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	name := sanitizeIdent(r.FormValue("name"))
	typ := strings.ToLower(strings.TrimSpace(r.FormValue("type")))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/tables?t=" + table + "&sc=" + sc
	if !tableIn(db, sc, table) {
		redirectErr(w, r, "/p/"+slug+"/tables?sc="+sc, "Unknown table.")
		return
	}
	if name == "" {
		redirectErr(w, r, back, "Enter a column name.")
		return
	}
	typSQL := typ
	if !colTypes[typ] {
		es, en := sc, typ
		if i := strings.IndexByte(typ, '.'); i > 0 {
			es, en = typ[:i], typ[i+1:]
		}
		if !a.enumExists(db, es, en) {
			redirectErr(w, r, back, "Unsupported column type.")
			return
		}
		typSQL = qrel(es, en)
	}
	stmt := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`,
		qrel(sc, table), pq.QuoteIdentifier(name), typSQL)
	if d := strings.TrimSpace(r.FormValue("default")); d != "" {
		stmt += " DEFAULT " + pq.QuoteLiteral(d) // Postgres casts the literal to the column type
	}
	if r.FormValue("notnull") == "on" {
		stmt += " NOT NULL"
	}
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, back, "Add column failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "column-add", slug+"/"+sc+"."+table+"."+name)
	redirectMsg(w, r, back, "Column "+name+" added.")
}

func (a *app) dropColumn(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	col := r.FormValue("column")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/tables?t=" + table + "&sc=" + sc
	okc := false
	for _, c := range a.tableCols(db, sc, table) {
		if c.Name == col {
			okc = true
		}
	}
	if !tableIn(db, sc, table) || !okc {
		redirectErr(w, r, back, "Unknown table or column.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s DROP COLUMN %s`,
		qrel(sc, table), pq.QuoteIdentifier(col))); err != nil {
		redirectErr(w, r, back, "Drop column failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "column-drop", slug+"/"+sc+"."+table+"."+col)
	redirectMsg(w, r, back, "Column "+col+" dropped.")
}

// exportCSV streams a whole table as a CSV download (bounded + timed so a huge
// table can't wedge the process). bytea is emitted as a size marker.
func (a *app) exportCSV(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.URL.Query().Get("t")
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sc := a.edSchema(db, r.URL.Query().Get("sc"))
	if relKind(db, sc, table) == "" {
		http.Error(w, "unknown table", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %s LIMIT 500000`, qrel(sc, table)))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	if r.URL.Query().Get("fmt") == "sql" {
		exportSQLInserts(w, rows, table, qrel(sc, table))
		return
	}
	cols, _ := rows.Columns()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, table))
	cw := csv.NewWriter(w)
	cw.Write(cols)
	raw := make([]any, len(cols))
	ptr := make([]any, len(cols))
	for i := range raw {
		ptr[i] = &raw[i]
	}
	for rows.Next() {
		if rows.Scan(ptr...) != nil {
			break
		}
		rec := make([]string, len(cols))
		for i, v := range raw {
			switch x := v.(type) {
			case nil:
				rec[i] = ""
			case []byte:
				if utf8.Valid(x) {
					rec[i] = string(x)
				} else {
					rec[i] = fmt.Sprintf("[binary %d bytes]", len(x))
				}
			case time.Time:
				rec[i] = x.Format(time.RFC3339)
			default:
				rec[i] = fmt.Sprintf("%v", x)
			}
		}
		cw.Write(rec)
	}
	cw.Flush()
	a.audit(r, "export-csv", slug+"/"+table)
}

// isDDL reports whether a (comment-stripped, lowercased) statement changes the
// schema, so we know to nudge PostgREST to reload its cache afterward.
func isDDL(q string) bool {
	for _, p := range []string{"create", "alter", "drop", "comment", "grant", "revoke"} {
		if strings.HasPrefix(q, p) {
			return true
		}
	}
	return false
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
