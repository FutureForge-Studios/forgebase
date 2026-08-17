package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
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
	u := *a.baseURL
	u.Path = "/" + slug
	db, err := sql.Open("postgres", u.String())
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
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]cell
	for rows.Next() && len(out) < maxScanRows {
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

func (a *app) tablePK(db *sql.DB, table string) []string {
	rows, err := db.Query(`SELECT a.attname FROM pg_index i
		JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=ANY(i.indkey)
		WHERE i.indrelid=$1::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey,a.attnum)`, "public."+pq.QuoteIdentifier(table))
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
}

func (a *app) tableCols(db *sql.DB, table string) []tableCol {
	rows, err := db.Query(`SELECT column_name, data_type, is_nullable, coalesce(column_default,'')
		FROM information_schema.columns WHERE table_schema='public' AND table_name=$1
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []tableCol
	for rows.Next() {
		var c tableCol
		rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default)
		out = append(out, c)
	}
	return out
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
	tables := a.listTables(db)
	sel := r.URL.Query().Get("t")
	if sel == "" && len(tables) > 0 {
		sel = tables[0]
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

	data := map[string]any{"Slug": slug, "Tables": tables, "Sel": sel, "DBs": dbs}
	if sel != "" && contains(tables, sel) {
		cols := a.tableCols(db, sel)
		pk := a.tablePK(db, sel)
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
		// Pagination: order by the primary key (indexed, so it's cheap and
		// stable across pages) when there is one; otherwise no ORDER BY - ordering
		// by ctid used to force a full scan and made edited rows "vanish".
		const pageSize = 100
		page := 1
		if p, e := strconv.Atoi(r.URL.Query().Get("p")); e == nil && p > 1 {
			page = p
		}
		orderBy := ""
		if len(pk) > 0 {
			qpk := make([]string, len(pk))
			for i, k := range pk {
				qpk[i] = pq.QuoteIdentifier(k)
			}
			orderBy = " ORDER BY " + strings.Join(qpk, ",")
		}
		gctx, gcancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer gcancel()
		q := fmt.Sprintf(`SELECT %s FROM public.%s%s LIMIT %d OFFSET %d`,
			selExpr, pq.QuoteIdentifier(sel), orderBy, pageSize, (page-1)*pageSize)
		rows, qerr := db.QueryContext(gctx, q)
		if qerr != nil {
			data["Error"] = qerr.Error()
		} else {
			cnames, recs, _ := scanRows(rows)
			rows.Close()
			data["Cols"] = cnames
			data["Rows"] = recs
			data["Meta"] = cols
			data["PK"] = pk
			data["HasPK"] = len(pk) > 0
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
				"public."+pq.QuoteIdentifier(sel)).Scan(&est)
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
	r.ParseForm()
	var cols []string
	var vals []any
	var ph []string
	i := 1
	for _, c := range a.tableCols(db, table) {
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
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Nothing to insert.")
		return
	}
	q := fmt.Sprintf(`INSERT INTO public.%s (%s) VALUES (%s)`,
		pq.QuoteIdentifier(table), strings.Join(cols, ","), strings.Join(ph, ","))
	if _, err := db.Exec(q, vals...); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Insert failed: "+err.Error())
		return
	}
	a.audit(r, "row-insert", slug+"/"+table)
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+table, "Row inserted.")
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
	pk := a.tablePK(db, table)
	if len(pk) == 0 {
		http.Error(w, "table has no primary key", 400)
		return
	}
	var where []string
	var args []any
	args = append(args, val)
	i := 2
	for _, k := range pk {
		where = append(where, fmt.Sprintf("%s=$%d", pq.QuoteIdentifier(k), i))
		args = append(args, r.FormValue("pk_"+k))
		i++
	}
	q := fmt.Sprintf(`UPDATE public.%s SET %s=$1 WHERE %s`,
		pq.QuoteIdentifier(table), pq.QuoteIdentifier(col), strings.Join(where, " AND "))
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
	pk := a.tablePK(db, table)
	if len(pk) == 0 {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Table has no primary key; delete via SQL editor.")
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
	q := fmt.Sprintf(`DELETE FROM public.%s WHERE %s`, pq.QuoteIdentifier(table), strings.Join(where, " AND "))
	res, err := db.Exec(q, args...)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Delete failed: "+err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "No row matched; nothing deleted.")
		return
	}
	a.audit(r, "row-delete", slug+"/"+table)
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+table, "Row deleted.")
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
		out = append(out, schemaTable{Name: t, Cols: a.tableCols(db, t)})
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
		"Schema": a.schemaTree(slug), "Saved": a.savedQueries(slug)})
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
	if qs := strings.TrimSpace(query); qs != "" {
		if len(qs) > 60 {
			qs = qs[:60] + "…"
		}
		a.audit(r, "sql-run", slug+": "+qs)
	}
	db, err := a.dbFor(slug)
	data := map[string]any{"Slug": slug, "Query": query, "Schema": a.schemaTree(slug), "Saved": a.savedQueries(slug), "RunAs": ""}
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
				cols, recs, _ := scanRows(rows)
				rows.Close()
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
	}
	content := renderContent(sqlBody, data)
	a.renderShell(w, r, shellData{Title: slug + " · SQL", Nav: "sql", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "SQL Editor"}}}, content)
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
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS public.%s (%s)`,
		pq.QuoteIdentifier(table), strings.Join(defs, ", "))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Create table failed: "+err.Error())
		return
	}
	ins := fmt.Sprintf(`INSERT INTO public.%s (%s) VALUES (%s)`,
		pq.QuoteIdentifier(table), strings.Join(qcols, ","), strings.Join(ph, ","))
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
	a.reloadPostgRESTSchema(slug) // the new table is queryable over REST immediately
	a.audit(r, "import-csv", fmt.Sprintf("%s (%d rows)", table, len(records)))
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+table, fmt.Sprintf("Imported %d rows into %s.", len(records), table))
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
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE public.%s (id bigserial PRIMARY KEY)`, pq.QuoteIdentifier(name))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Create failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "table-create", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+name, "Table "+name+" created. Add columns below.")
}

func (a *app) dropTable(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, "/p/"+slug+"/tables", "Unknown table.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP TABLE public.%s`, pq.QuoteIdentifier(table))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables", "Drop failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "table-drop", slug+"/"+table)
	redirectMsg(w, r, "/p/"+slug+"/tables", "Table "+table+" dropped.")
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
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, "/p/"+slug+"/tables", "Unknown table.")
		return
	}
	if name == "" {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Enter a column name.")
		return
	}
	if !colTypes[typ] {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Unsupported column type.")
		return
	}
	stmt := fmt.Sprintf(`ALTER TABLE public.%s ADD COLUMN %s %s`,
		pq.QuoteIdentifier(table), pq.QuoteIdentifier(name), typ)
	if d := strings.TrimSpace(r.FormValue("default")); d != "" {
		stmt += " DEFAULT " + pq.QuoteLiteral(d) // Postgres casts the literal to the column type
	}
	if r.FormValue("notnull") == "on" {
		stmt += " NOT NULL"
	}
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Add column failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "column-add", slug+"/"+table+"."+name)
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+table, "Column "+name+" added.")
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
	if !a.publicTableExists(db, table) || !columnExists(db, table, col) {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Unknown table or column.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE public.%s DROP COLUMN %s`,
		pq.QuoteIdentifier(table), pq.QuoteIdentifier(col))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/tables?t="+table, "Drop column failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "column-drop", slug+"/"+table+"."+col)
	redirectMsg(w, r, "/p/"+slug+"/tables?t="+table, "Column "+col+" dropped.")
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
	if !a.publicTableExists(db, table) {
		http.Error(w, "unknown table", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM public.%s LIMIT 500000`, pq.QuoteIdentifier(table)))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
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
