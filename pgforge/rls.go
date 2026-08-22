package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/lib/pq"
)

// Row Level Security management. RLS is the standard Postgres model for per-row
// authorization: enable it on a table (which denies all access by default) then
// add policies that reference the signed-in user via auth.uid()/auth.jwt().
// ForgeBase ships one-click enable + a few policy templates so a project can be
// made secure-by-default without hand-writing SQL. It stays OPT-IN: existing
// projects keep their table-level grants until an operator turns RLS on.

type rlsPolicy struct {
	Name, Cmd, Roles, Qual, Check string
	Permissive                    bool
}

type rlsTable struct {
	Name     string
	Enabled  bool
	Force    bool
	Policies []rlsPolicy
	Cols     []string
	Grants   []colGrantRow
}

// colGrantRow is one explicit column-level privilege (never table-wide grants).
type colGrantRow struct{ Col, Role, Priv string }

func (a *app) publicTableExists(db *sql.DB, table string) bool {
	var ok bool
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables
		WHERE table_schema='public' AND table_name=$1)`, table).Scan(&ok)
	return ok
}

func columnExists(db *sql.DB, table, col string) bool {
	var ok bool
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns
		WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, table, col).Scan(&ok)
	return ok
}

// rlsData returns every public table with its RLS state, policies and columns
// (columns feed the owner-policy column picker).
func (a *app) rlsData(slug string) []rlsTable {
	db, err := a.dbFor(slug)
	if err != nil {
		return nil
	}
	var out []rlsTable
	idx := map[string]int{}
	if rows, err := db.Query(`SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind='r' ORDER BY 1`); err == nil {
		for rows.Next() {
			var t rlsTable
			rows.Scan(&t.Name, &t.Enabled, &t.Force)
			idx[t.Name] = len(out)
			out = append(out, t)
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT tablename, policyname, cmd, permissive='PERMISSIVE',
			array_to_string(roles,', '), coalesce(qual,''), coalesce(with_check,'')
		FROM pg_policies WHERE schemaname='public' ORDER BY tablename, policyname`); err == nil {
		for rows.Next() {
			var tbl string
			var p rlsPolicy
			rows.Scan(&tbl, &p.Name, &p.Cmd, &p.Permissive, &p.Roles, &p.Qual, &p.Check)
			if i, ok := idx[tbl]; ok {
				out[i].Policies = append(out[i].Policies, p)
			}
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT c.relname, a.attname, g.grantee::regrole::text, g.privilege_type
		FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname='public'
		JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
		CROSS JOIN LATERAL aclexplode(a.attacl) g
		WHERE c.relkind='r' AND a.attacl IS NOT NULL
		ORDER BY c.relname, a.attname, 3, 4`); err == nil {
		for rows.Next() {
			var tbl string
			var g colGrantRow
			rows.Scan(&tbl, &g.Col, &g.Role, &g.Priv)
			if i, ok := idx[tbl]; ok {
				out[i].Grants = append(out[i].Grants, g)
			}
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema='public' ORDER BY table_name, ordinal_position`); err == nil {
		for rows.Next() {
			var tbl, col string
			rows.Scan(&tbl, &col)
			if i, ok := idx[tbl]; ok {
				out[i].Cols = append(out[i].Cols, col)
			}
		}
		rows.Close()
	}
	return out
}

// rlsBack routes RLS actions back to whichever page hosted the form.
func rlsBack(slug string, r *http.Request) string {
	if r.FormValue("__back") == "policies" {
		return "/p/" + slug + "/policies"
	}
	return "/p/" + slug + "/api"
}

func (a *app) toggleRLS(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, rlsBack(slug, r), err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, rlsBack(slug, r), "Unknown table.")
		return
	}
	verb := "ENABLE"
	if r.FormValue("action") == "disable" {
		verb = "DISABLE"
	}
	if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE public.%s %s ROW LEVEL SECURITY`,
		pq.QuoteIdentifier(table), verb)); err != nil {
		redirectErr(w, r, rlsBack(slug, r), err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "rls-toggle", slug+"/"+table+"="+verb)
	redirectMsg(w, r, rlsBack(slug, r), "Row Level Security "+verb+"D on "+table+".")
}

func (a *app) addPolicy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	tmpl := r.FormValue("template")
	col := r.FormValue("column")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, rlsBack(slug, r), err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, rlsBack(slug, r), "Unknown table.")
		return
	}
	qt := pq.QuoteIdentifier(table)
	// A policy only takes effect if the target role ALSO holds the matching table
	// privilege - RLS and GRANTs are independent layers in Postgres. Without this,
	// the write templates below create policies that can never be satisfied over
	// the API (the write hits "permission denied for table" before RLS is even
	// consulted). We grant the minimum privilege per template, on this one table,
	// so RLS is what actually gates access. This is scoped to the table an operator
	// explicitly adds a policy to, so it never opens up tables that have no policy.
	var name, stmt, grant string
	writePolicy := false
	switch tmpl {
	case "public-read":
		name = "public read"
		stmt = fmt.Sprintf(`CREATE POLICY %s ON public.%s FOR SELECT TO anon, authenticated USING (true)`,
			pq.QuoteIdentifier(name), qt)
		grant = fmt.Sprintf(`GRANT SELECT ON public.%s TO anon, authenticated`, qt)
	case "auth-read":
		name = "authenticated read"
		stmt = fmt.Sprintf(`CREATE POLICY %s ON public.%s FOR SELECT TO authenticated USING (true)`,
			pq.QuoteIdentifier(name), qt)
		grant = fmt.Sprintf(`GRANT SELECT ON public.%s TO authenticated`, qt)
	case "auth-write":
		name = "authenticated write"
		stmt = fmt.Sprintf(`CREATE POLICY %s ON public.%s FOR ALL TO authenticated USING (true) WITH CHECK (true)`,
			pq.QuoteIdentifier(name), qt)
		grant = fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON public.%s TO authenticated`, qt)
		writePolicy = true
	case "owner":
		if !columnExists(db, table, col) {
			redirectErr(w, r, rlsBack(slug, r), "Pick a valid owner column (uuid matching auth.uid()).")
			return
		}
		name = "owner access"
		qc := pq.QuoteIdentifier(col)
		stmt = fmt.Sprintf(`CREATE POLICY %s ON public.%s FOR ALL TO authenticated USING (auth.uid() = %s) WITH CHECK (auth.uid() = %s)`,
			pq.QuoteIdentifier(name), qt, qc, qc)
		grant = fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON public.%s TO authenticated`, qt)
		writePolicy = true
	default:
		redirectErr(w, r, rlsBack(slug, r), "Unknown policy template.")
		return
	}
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, rlsBack(slug, r), "Policy failed: "+err.Error())
		return
	}
	// A policy is inert unless RLS is on; turn it on so the template takes effect.
	db.Exec(fmt.Sprintf(`ALTER TABLE public.%s ENABLE ROW LEVEL SECURITY`, qt))
	// Grant the matching table privilege so the policy can actually be satisfied.
	if grant != "" {
		db.Exec(grant)
	}
	// Inserts into serial/identity columns also need sequence usage, or the write
	// fails with "permission denied for sequence" before RLS is even reached.
	if writePolicy {
		db.Exec(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO authenticated`)
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "rls-policy-add", slug+"/"+table+"/"+name)
	redirectMsg(w, r, rlsBack(slug, r), `Added "`+name+`" policy to `+table+" (RLS on).")
}

func (a *app) dropPolicy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	policy := r.FormValue("policy")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, rlsBack(slug, r), err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, rlsBack(slug, r), "Unknown table.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP POLICY IF EXISTS %s ON public.%s`,
		pq.QuoteIdentifier(policy), pq.QuoteIdentifier(table))); err != nil {
		redirectErr(w, r, rlsBack(slug, r), "Drop failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "rls-policy-drop", slug+"/"+table+"/"+policy)
	redirectMsg(w, r, rlsBack(slug, r), "Policy dropped.")
}

// ----------------------------------------------------------- policies page

func (a *app) policiesPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	content := renderContent(policiesBody, map[string]any{
		"Slug": slug, "Tables": a.rlsData(slug),
	})
	a.renderShell(w, r, shellData{Title: slug + " · Policies", Nav: "policies", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Policies"}}}, content)
}

var policyCmds = map[string]bool{"ALL": true, "SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true}
var policyRoles = map[string]bool{"anon": true, "authenticated": true, "service_role": true, "public": true}

// grants that make a policy of a given command actually reachable over the API
var cmdGrants = map[string]string{
	"ALL": "SELECT, INSERT, UPDATE, DELETE", "SELECT": "SELECT",
	"INSERT": "INSERT", "UPDATE": "SELECT, UPDATE", "DELETE": "SELECT, DELETE",
}

// policyCreate is the custom policy builder: any command, role set, USING and
// WITH CHECK expressions, permissive or restrictive.
func (a *app) policyCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/policies"
	table := r.FormValue("table")
	name := strings.TrimSpace(r.FormValue("name"))
	cmd := strings.ToUpper(r.FormValue("cmd"))
	using := strings.TrimSpace(r.FormValue("using"))
	check := strings.TrimSpace(r.FormValue("check"))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if !a.publicTableExists(db, table) || name == "" || !policyCmds[cmd] {
		redirectErr(w, r, back, "Fill in table, policy name and command.")
		return
	}
	r.ParseForm()
	var roles []string
	for _, ro := range r.Form["roles"] {
		if policyRoles[ro] {
			roles = append(roles, pq.QuoteIdentifier(ro))
		}
	}
	if len(roles) == 0 {
		redirectErr(w, r, back, "Pick at least one role.")
		return
	}
	if cmd == "INSERT" && check == "" {
		redirectErr(w, r, back, "INSERT policies need a WITH CHECK expression.")
		return
	}
	if cmd != "INSERT" && using == "" {
		redirectErr(w, r, back, "This command needs a USING expression.")
		return
	}
	stmt := "CREATE POLICY " + pq.QuoteIdentifier(name) + " ON public." + pq.QuoteIdentifier(table)
	if r.FormValue("restrictive") == "on" {
		stmt += " AS RESTRICTIVE"
	}
	stmt += " FOR " + cmd + " TO " + strings.Join(roles, ", ")
	if using != "" && cmd != "INSERT" {
		stmt += " USING (" + using + ")"
	}
	if check != "" && cmd != "SELECT" && cmd != "DELETE" {
		stmt += " WITH CHECK (" + check + ")"
	}
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, back, "Create failed: "+err.Error())
		return
	}
	db.Exec(fmt.Sprintf(`ALTER TABLE public.%s ENABLE ROW LEVEL SECURITY`, pq.QuoteIdentifier(table)))
	if r.FormValue("grant") == "on" {
		var gr []string
		for _, ro := range r.Form["roles"] {
			if policyRoles[ro] && ro != "public" {
				gr = append(gr, pq.QuoteIdentifier(ro))
			}
		}
		if len(gr) > 0 {
			db.Exec(fmt.Sprintf(`GRANT %s ON public.%s TO %s`,
				cmdGrants[cmd], pq.QuoteIdentifier(table), strings.Join(gr, ", ")))
			if cmd == "ALL" || cmd == "INSERT" || cmd == "UPDATE" {
				db.Exec(fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, strings.Join(gr, ", ")))
			}
		}
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "rls-policy-create", slug+"/"+table+"/"+name)
	redirectMsg(w, r, back, `Policy "`+name+`" created on `+table+" (RLS on).")
}

// policyAlter edits an existing policy's roles and expressions (Postgres
// cannot change a policy's command or permissiveness in place).
func (a *app) policyAlter(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/policies"
	table := r.FormValue("table")
	name := r.FormValue("policy")
	using := strings.TrimSpace(r.FormValue("using"))
	check := strings.TrimSpace(r.FormValue("check"))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, back, "Unknown table.")
		return
	}
	found := false
	for _, t := range a.rlsData(slug) {
		if t.Name == table {
			for _, p := range t.Policies {
				if p.Name == name {
					found = true
				}
			}
		}
	}
	if !found {
		redirectErr(w, r, back, "Unknown policy.")
		return
	}
	r.ParseForm()
	var roles []string
	for _, ro := range r.Form["roles"] {
		if policyRoles[ro] {
			roles = append(roles, pq.QuoteIdentifier(ro))
		}
	}
	stmt := "ALTER POLICY " + pq.QuoteIdentifier(name) + " ON public." + pq.QuoteIdentifier(table)
	if len(roles) > 0 {
		stmt += " TO " + strings.Join(roles, ", ")
	}
	if using != "" {
		stmt += " USING (" + using + ")"
	}
	if check != "" {
		stmt += " WITH CHECK (" + check + ")"
	}
	if len(roles) == 0 && using == "" && check == "" {
		redirectErr(w, r, back, "Nothing to change.")
		return
	}
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, back, "Alter failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "rls-policy-alter", slug+"/"+table+"/"+name)
	redirectMsg(w, r, back, `Policy "`+name+`" updated.`)
}

// toggleForceRLS applies RLS to the table owner too (FORCE) - useful when the
// app connects as the owning role and policies must still bite.
func (a *app) toggleForceRLS(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/policies"
	table := r.FormValue("table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, back, "Unknown table.")
		return
	}
	verb := "FORCE"
	if r.FormValue("action") == "noforce" {
		verb = "NO FORCE"
	}
	if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE public.%s %s ROW LEVEL SECURITY`,
		pq.QuoteIdentifier(table), verb)); err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	a.audit(r, "rls-force", slug+"/"+table+"="+verb)
	redirectMsg(w, r, back, verb+" row level security set on "+table+".")
}

var colPrivs = map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "REFERENCES": true}

// colGrant grants a privilege on specific COLUMNS only - finer than table-wide
// grants, e.g. hide a "cost" column from anon while exposing the rest.
func (a *app) colGrant(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/policies"
	table := r.FormValue("table")
	role := r.FormValue("role")
	priv := strings.ToUpper(r.FormValue("priv"))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if !a.publicTableExists(db, table) || !policyRoles[role] || role == "public" || !colPrivs[priv] {
		redirectErr(w, r, back, "Fill in table, role and privilege.")
		return
	}
	colSet := map[string]bool{}
	for _, c := range a.tableCols(db, "public", table) {
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
	if _, err := db.Exec(fmt.Sprintf(`GRANT %s (%s) ON public.%s TO %s`,
		priv, strings.Join(cols, ", "), pq.QuoteIdentifier(table), pq.QuoteIdentifier(role))); err != nil {
		redirectErr(w, r, back, "Grant failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "col-grant", slug+"/"+table+" "+priv+"("+strings.Join(cols, ",")+") -> "+role)
	redirectMsg(w, r, back, priv+" granted on "+table+" columns to "+role+".")
}

func (a *app) colRevoke(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/policies"
	table := r.FormValue("table")
	role := r.FormValue("role")
	priv := strings.ToUpper(r.FormValue("priv"))
	col := r.FormValue("column")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if !a.publicTableExists(db, table) || !columnExists(db, table, col) || !colPrivs[priv] {
		redirectErr(w, r, back, "Unknown table, column or privilege.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`REVOKE %s (%s) ON public.%s FROM %s`,
		priv, pq.QuoteIdentifier(col), pq.QuoteIdentifier(table), pq.QuoteIdentifier(role))); err != nil {
		redirectErr(w, r, back, "Revoke failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "col-revoke", slug+"/"+table+"."+col+" "+priv+" from "+role)
	redirectMsg(w, r, back, priv+" on "+table+"."+col+" revoked from "+role+".")
}
