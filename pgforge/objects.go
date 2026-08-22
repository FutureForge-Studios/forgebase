package main

// Database object UIs: functions, triggers, enum types and indexes, managed
// visually per schema. Everything executes as the project's database owner -
// the same power the SQL editor already grants - but every identifier that
// reaches SQL is either validated against the live catalog or safely quoted.

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/lib/pq"
)

type dbFunc struct {
	Name, Args, Ret, Lang, Volatility, Def string
	SecDef                                 bool
}

type dbTrigger struct {
	Name, Table, Def string
	Enabled          bool
}

type dbEnum struct {
	Name   string
	Labels []string
}

type dbIndex struct {
	Name, Table, Def, Size string
	Unique, Primary        bool
	Scans                  int64
}

var volatilityNames = map[string]string{"i": "immutable", "s": "stable", "v": "volatile"}

func (a *app) listFunctions(db *sql.DB, schema string) []dbFunc {
	rows, err := db.Query(`SELECT p.proname, pg_get_function_identity_arguments(p.oid),
			pg_get_function_result(p.oid), l.lanname, p.provolatile::text, p.prosecdef,
			pg_get_functiondef(p.oid)
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		JOIN pg_language l ON l.oid = p.prolang
		WHERE n.nspname = $1 AND p.prokind = 'f'
			AND NOT EXISTS (SELECT 1 FROM pg_depend d
				WHERE d.objid = p.oid AND d.deptype = 'e')
		ORDER BY p.proname`, schema)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []dbFunc
	for rows.Next() {
		var f dbFunc
		var vol string
		rows.Scan(&f.Name, &f.Args, &f.Ret, &f.Lang, &vol, &f.SecDef, &f.Def)
		f.Volatility = volatilityNames[vol]
		out = append(out, f)
	}
	return out
}

func (a *app) listTriggers(db *sql.DB, schema string) []dbTrigger {
	rows, err := db.Query(`SELECT t.tgname, c.relname, pg_get_triggerdef(t.oid, true),
			t.tgenabled <> 'D'
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND NOT t.tgisinternal
		ORDER BY c.relname, t.tgname`, schema)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []dbTrigger
	for rows.Next() {
		var t dbTrigger
		rows.Scan(&t.Name, &t.Table, &t.Def, &t.Enabled)
		out = append(out, t)
	}
	return out
}

func (a *app) listEnums(db *sql.DB, schema string) []dbEnum {
	rows, err := db.Query(`SELECT t.typname, e.enumlabel
		FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		JOIN pg_enum e ON e.enumtypid = t.oid
		WHERE n.nspname = $1
			AND NOT EXISTS (SELECT 1 FROM pg_depend d
				WHERE d.objid = t.oid AND d.deptype = 'e')
		ORDER BY t.typname, e.enumsortorder`, schema)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []dbEnum
	for rows.Next() {
		var name, label string
		rows.Scan(&name, &label)
		if len(out) == 0 || out[len(out)-1].Name != name {
			out = append(out, dbEnum{Name: name})
		}
		out[len(out)-1].Labels = append(out[len(out)-1].Labels, label)
	}
	return out
}

func (a *app) listIndexes(db *sql.DB, schema string) []dbIndex {
	rows, err := db.Query(`SELECT ic.relname, tc.relname, pg_get_indexdef(i.indexrelid),
			i.indisunique, i.indisprimary,
			pg_size_pretty(pg_relation_size(i.indexrelid)),
			coalesce(s.idx_scan, 0)
		FROM pg_index i
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_class tc ON tc.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = ic.relnamespace
		LEFT JOIN pg_stat_user_indexes s ON s.indexrelid = i.indexrelid
		WHERE n.nspname = $1
			AND NOT EXISTS (SELECT 1 FROM pg_depend d
				WHERE d.objid = tc.oid AND d.deptype = 'e')
		ORDER BY tc.relname, ic.relname`, schema)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []dbIndex
	for rows.Next() {
		var ix dbIndex
		rows.Scan(&ix.Name, &ix.Table, &ix.Def, &ix.Unique, &ix.Primary, &ix.Size, &ix.Scans)
		out = append(out, ix)
	}
	return out
}

// trigger functions available to the trigger builder, across user schemas
type trigFunc struct{ Schema, Name string }

func (a *app) listTriggerFuncs(db *sql.DB) []trigFunc {
	rows, err := db.Query(`SELECT n.nspname, p.proname
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		JOIN pg_type t ON t.oid = p.prorettype
		WHERE t.typname = 'trigger' AND n.nspname NOT LIKE 'pg\_%'
			AND n.nspname <> 'information_schema'
		ORDER BY n.nspname, p.proname`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []trigFunc
	for rows.Next() {
		var f trigFunc
		rows.Scan(&f.Schema, &f.Name)
		out = append(out, f)
	}
	return out
}

func (a *app) objectsPage(w http.ResponseWriter, r *http.Request) {
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
	tab := r.URL.Query().Get("tab")
	if tab != "triggers" && tab != "enums" && tab != "indexes" {
		tab = "functions"
	}
	data := map[string]any{
		"Slug": slug, "Schema": sc, "Schemas": a.listSchemas(db), "Tab": tab,
	}
	switch tab {
	case "functions":
		data["Funcs"] = a.listFunctions(db, sc)
	case "triggers":
		data["Triggers"] = a.listTriggers(db, sc)
		data["TrigFuncs"] = a.listTriggerFuncs(db)
		var tbls []string
		for _, rel := range a.listRelations(db, sc) {
			if rel.Kind == "table" {
				tbls = append(tbls, rel.Name)
			}
		}
		data["Tables"] = tbls
	case "enums":
		data["Enums"] = a.listEnums(db, sc)
	case "indexes":
		data["Indexes"] = a.listIndexes(db, sc)
		var tbls []string
		for _, rel := range a.listRelations(db, sc) {
			if rel.Kind == "table" {
				tbls = append(tbls, rel.Name)
			}
		}
		data["Tables"] = tbls
	}
	content := renderContent(objectsBody, data)
	a.renderShell(w, r, shellData{Title: slug + " · Objects", Nav: "objects", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Objects"}}}, content)
}

var createFuncRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(OR\s+REPLACE\s+)?FUNCTION`)

// functionCreate runs a CREATE [OR REPLACE] FUNCTION statement verbatim - the
// form is a guarded doorway to DDL the SQL editor could already run.
func (a *app) functionCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=functions&sc=" + sc
	sqlText := strings.TrimSpace(r.FormValue("sql"))
	if !createFuncRe.MatchString(sqlText) {
		redirectErr(w, r, back, "The statement must start with CREATE [OR REPLACE] FUNCTION.")
		return
	}
	if _, err := db.Exec(sqlText); err != nil {
		redirectErr(w, r, back, "Create failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "function-create", slug+"/"+sc)
	redirectMsg(w, r, back, "Function saved.")
}

func (a *app) functionDrop(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.FormValue("name")
	args := r.FormValue("args")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=functions&sc=" + sc
	// the (name, identity-args) pair must exist in the catalog - the DROP is
	// then built from catalog-derived text, not raw form input
	found := false
	for _, f := range a.listFunctions(db, sc) {
		if f.Name == name && f.Args == args {
			found = true
		}
	}
	if !found {
		redirectErr(w, r, back, "Unknown function.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP FUNCTION %s(%s)`, qrel(sc, name), args)); err != nil {
		redirectErr(w, r, back, "Drop failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "function-drop", slug+"/"+sc+"."+name)
	redirectMsg(w, r, back, "Function "+name+" dropped.")
}

var trigTimings = map[string]bool{"BEFORE": true, "AFTER": true, "INSTEAD OF": true}

func (a *app) triggerCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=triggers&sc=" + sc
	name := sanitizeIdent(r.FormValue("name"))
	table := r.FormValue("table")
	timing := strings.ToUpper(r.FormValue("timing"))
	level := "ROW"
	if r.FormValue("level") == "STATEMENT" {
		level = "STATEMENT"
	}
	fn := r.FormValue("function") // "schema.name" from the picker
	if name == "" || !tableIn(db, sc, table) || !trigTimings[timing] {
		redirectErr(w, r, back, "Fill in a name, table and timing.")
		return
	}
	r.ParseForm()
	var events []string
	for _, ev := range r.Form["events"] {
		switch ev {
		case "INSERT", "UPDATE", "DELETE", "TRUNCATE":
			events = append(events, ev)
		}
	}
	if len(events) == 0 {
		redirectErr(w, r, back, "Pick at least one event.")
		return
	}
	var fs, fname string
	for _, tf := range a.listTriggerFuncs(db) {
		if tf.Schema+"."+tf.Name == fn {
			fs, fname = tf.Schema, tf.Name
		}
	}
	if fname == "" {
		redirectErr(w, r, back, "Pick a trigger function (a function returning type trigger).")
		return
	}
	stmt := fmt.Sprintf(`CREATE TRIGGER %s %s %s ON %s FOR EACH %s EXECUTE FUNCTION %s()`,
		pq.QuoteIdentifier(name), timing, strings.Join(events, " OR "),
		qrel(sc, table), level, qrel(fs, fname))
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, back, "Create failed: "+err.Error())
		return
	}
	a.audit(r, "trigger-create", slug+"/"+sc+"."+table+"."+name)
	redirectMsg(w, r, back, "Trigger "+name+" created.")
}

// triggerExists validates a (table, trigger) pair against the catalog.
func (a *app) triggerExists(db *sql.DB, schema, table, name string) bool {
	for _, t := range a.listTriggers(db, schema) {
		if t.Table == table && t.Name == name {
			return true
		}
	}
	return false
}

func (a *app) triggerToggle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table, name := r.FormValue("table"), r.FormValue("name")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=triggers&sc=" + sc
	if !a.triggerExists(db, sc, table, name) {
		redirectErr(w, r, back, "Unknown trigger.")
		return
	}
	verb := "ENABLE"
	if r.FormValue("action") == "disable" {
		verb = "DISABLE"
	}
	if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s %s TRIGGER %s`,
		qrel(sc, table), verb, pq.QuoteIdentifier(name))); err != nil {
		redirectErr(w, r, back, "Change failed: "+err.Error())
		return
	}
	a.audit(r, "trigger-"+strings.ToLower(verb), slug+"/"+sc+"."+table+"."+name)
	redirectMsg(w, r, back, "Trigger "+name+" "+strings.ToLower(verb)+"d.")
}

func (a *app) triggerDrop(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table, name := r.FormValue("table"), r.FormValue("name")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=triggers&sc=" + sc
	if !a.triggerExists(db, sc, table, name) {
		redirectErr(w, r, back, "Unknown trigger.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP TRIGGER %s ON %s`,
		pq.QuoteIdentifier(name), qrel(sc, table))); err != nil {
		redirectErr(w, r, back, "Drop failed: "+err.Error())
		return
	}
	a.audit(r, "trigger-drop", slug+"/"+sc+"."+table+"."+name)
	redirectMsg(w, r, back, "Trigger "+name+" dropped.")
}

// splitEnumLabels accepts one label per line or a comma-separated list.
func splitEnumLabels(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ',' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (a *app) enumHasLabel(db *sql.DB, schema, name, label string) bool {
	for _, e := range a.listEnums(db, schema) {
		if e.Name == name {
			for _, l := range e.Labels {
				if l == label {
					return true
				}
			}
		}
	}
	return false
}

func (a *app) enumExists(db *sql.DB, schema, name string) bool {
	for _, e := range a.listEnums(db, schema) {
		if e.Name == name {
			return true
		}
	}
	return false
}

func (a *app) enumCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=enums&sc=" + sc
	name := sanitizeIdent(r.FormValue("name"))
	labels := splitEnumLabels(r.FormValue("labels"))
	if name == "" || len(labels) == 0 {
		redirectErr(w, r, back, "Enter a type name and at least one value.")
		return
	}
	quoted := make([]string, len(labels))
	for i, l := range labels {
		quoted[i] = pq.QuoteLiteral(l)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TYPE %s AS ENUM (%s)`,
		qrel(sc, name), strings.Join(quoted, ", "))); err != nil {
		redirectErr(w, r, back, "Create failed: "+err.Error())
		return
	}
	a.chownRel(db, slug, "TYPE", sc, name)
	a.audit(r, "enum-create", slug+"/"+sc+"."+name)
	redirectMsg(w, r, back, "Enum "+name+" created.")
}

func (a *app) enumAddValue(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.FormValue("name")
	label := strings.TrimSpace(r.FormValue("label"))
	pos, ref := r.FormValue("pos"), r.FormValue("ref")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=enums&sc=" + sc
	if !a.enumExists(db, sc, name) || label == "" {
		redirectErr(w, r, back, "Unknown enum or empty value.")
		return
	}
	stmt := fmt.Sprintf(`ALTER TYPE %s ADD VALUE IF NOT EXISTS %s`, qrel(sc, name), pq.QuoteLiteral(label))
	if (pos == "before" || pos == "after") && a.enumHasLabel(db, sc, name, ref) {
		stmt += " " + strings.ToUpper(pos) + " " + pq.QuoteLiteral(ref)
	}
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, back, "Add failed: "+err.Error())
		return
	}
	a.audit(r, "enum-add-value", slug+"/"+sc+"."+name+"+"+label)
	redirectMsg(w, r, back, "Value added to "+name+".")
}

func (a *app) enumRenameValue(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.FormValue("name")
	from, to := r.FormValue("from"), strings.TrimSpace(r.FormValue("to"))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=enums&sc=" + sc
	if !a.enumHasLabel(db, sc, name, from) || to == "" {
		redirectErr(w, r, back, "Unknown enum value or empty new name.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`ALTER TYPE %s RENAME VALUE %s TO %s`,
		qrel(sc, name), pq.QuoteLiteral(from), pq.QuoteLiteral(to))); err != nil {
		redirectErr(w, r, back, "Rename failed: "+err.Error())
		return
	}
	a.audit(r, "enum-rename-value", slug+"/"+sc+"."+name+": "+from+" -> "+to)
	redirectMsg(w, r, back, "Value renamed.")
}

func (a *app) enumDrop(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.FormValue("name")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=enums&sc=" + sc
	if !a.enumExists(db, sc, name) {
		redirectErr(w, r, back, "Unknown enum.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP TYPE %s`, qrel(sc, name))); err != nil {
		redirectErr(w, r, back, "Drop failed (still used by a column?): "+err.Error())
		return
	}
	a.audit(r, "enum-drop", slug+"/"+sc+"."+name)
	redirectMsg(w, r, back, "Enum "+name+" dropped.")
}

var indexMethods = map[string]bool{"btree": true, "hash": true, "gin": true, "gist": true, "brin": true}

func (a *app) indexCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=indexes&sc=" + sc
	table := r.FormValue("table")
	if !tableIn(db, sc, table) {
		redirectErr(w, r, back, "Unknown table.")
		return
	}
	colSet := map[string]bool{}
	for _, c := range a.tableCols(db, sc, table) {
		colSet[c.Name] = true
	}
	var cols []string
	for _, c := range strings.Split(r.FormValue("columns"), ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !colSet[c] {
			redirectErr(w, r, back, "Unknown column "+c+".")
			return
		}
		cols = append(cols, pq.QuoteIdentifier(c))
	}
	if len(cols) == 0 {
		redirectErr(w, r, back, "List at least one column.")
		return
	}
	method := strings.ToLower(r.FormValue("method"))
	if !indexMethods[method] {
		method = "btree"
	}
	name := sanitizeIdent(r.FormValue("name"))
	if name == "" {
		base := table + "_" + strings.ToLower(strings.Trim(strings.Join(strings.Fields(r.FormValue("columns")), "_"), ","))
		name = sanitizeIdent(strings.ReplaceAll(base, ",", "_")) + "_idx"
	}
	unique := ""
	if r.FormValue("unique") == "on" {
		unique = "UNIQUE "
	}
	stmt := fmt.Sprintf(`CREATE %sINDEX %s ON %s USING %s (%s)`,
		unique, pq.QuoteIdentifier(name), qrel(sc, table), method, strings.Join(cols, ", "))
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, back, "Create failed: "+err.Error())
		return
	}
	a.audit(r, "index-create", slug+"/"+sc+"."+table+"."+name)
	redirectMsg(w, r, back, "Index "+name+" created.")
}

func (a *app) indexDrop(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.FormValue("name")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/objects", err.Error())
		return
	}
	sc := a.edSchema(db, r.FormValue("__schema"))
	back := "/p/" + slug + "/objects?tab=indexes&sc=" + sc
	found := false
	for _, ix := range a.listIndexes(db, sc) {
		if ix.Name == name {
			if ix.Primary {
				redirectErr(w, r, back, "Primary key indexes cannot be dropped here.")
				return
			}
			found = true
		}
	}
	if !found {
		redirectErr(w, r, back, "Unknown index.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP INDEX %s`, qrel(sc, name))); err != nil {
		redirectErr(w, r, back, "Drop failed: "+err.Error())
		return
	}
	a.audit(r, "index-drop", slug+"/"+sc+"."+name)
	redirectMsg(w, r, back, "Index "+name+" dropped.")
}
