package main

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/lib/pq"
)

// Row Level Security management. RLS is the standard Postgres model for per-row
// authorization: enable it on a table (which denies all access by default) then
// add policies that reference the signed-in user via auth.uid()/auth.jwt().
// ForgeBase ships one-click enable + a few policy templates so a project can be
// made secure-by-default without hand-writing SQL. It stays OPT-IN: existing
// projects keep their table-level grants until an operator turns RLS on.

type rlsPolicy struct{ Name, Cmd string }

type rlsTable struct {
	Name     string
	Enabled  bool
	Policies []rlsPolicy
	Cols     []string
}

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
	if rows, err := db.Query(`SELECT c.relname, c.relrowsecurity
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind='r' ORDER BY 1`); err == nil {
		for rows.Next() {
			var t rlsTable
			rows.Scan(&t.Name, &t.Enabled)
			idx[t.Name] = len(out)
			out = append(out, t)
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT tablename, policyname, cmd FROM pg_policies
		WHERE schemaname='public' ORDER BY tablename, policyname`); err == nil {
		for rows.Next() {
			var tbl, pol, cmd string
			rows.Scan(&tbl, &pol, &cmd)
			if i, ok := idx[tbl]; ok {
				out[i].Policies = append(out[i].Policies, rlsPolicy{pol, cmd})
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

func (a *app) toggleRLS(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/api", err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, "/p/"+slug+"/api", "Unknown table.")
		return
	}
	verb := "ENABLE"
	if r.FormValue("action") == "disable" {
		verb = "DISABLE"
	}
	if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE public.%s %s ROW LEVEL SECURITY`,
		pq.QuoteIdentifier(table), verb)); err != nil {
		redirectErr(w, r, "/p/"+slug+"/api", err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "rls-toggle", slug+"/"+table+"="+verb)
	redirectMsg(w, r, "/p/"+slug+"/api", "Row Level Security "+verb+"D on "+table+".")
}

func (a *app) addPolicy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	tmpl := r.FormValue("template")
	col := r.FormValue("column")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/api", err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, "/p/"+slug+"/api", "Unknown table.")
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
			redirectErr(w, r, "/p/"+slug+"/api", "Pick a valid owner column (uuid matching auth.uid()).")
			return
		}
		name = "owner access"
		qc := pq.QuoteIdentifier(col)
		stmt = fmt.Sprintf(`CREATE POLICY %s ON public.%s FOR ALL TO authenticated USING (auth.uid() = %s) WITH CHECK (auth.uid() = %s)`,
			pq.QuoteIdentifier(name), qt, qc, qc)
		grant = fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON public.%s TO authenticated`, qt)
		writePolicy = true
	default:
		redirectErr(w, r, "/p/"+slug+"/api", "Unknown policy template.")
		return
	}
	if _, err := db.Exec(stmt); err != nil {
		redirectErr(w, r, "/p/"+slug+"/api", "Policy failed: "+err.Error())
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
	redirectMsg(w, r, "/p/"+slug+"/api", `Added "`+name+`" policy to `+table+" (RLS on).")
}

func (a *app) dropPolicy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	policy := r.FormValue("policy")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/api", err.Error())
		return
	}
	if !a.publicTableExists(db, table) {
		redirectErr(w, r, "/p/"+slug+"/api", "Unknown table.")
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP POLICY IF EXISTS %s ON public.%s`,
		pq.QuoteIdentifier(policy), pq.QuoteIdentifier(table))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/api", "Drop failed: "+err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "rls-policy-drop", slug+"/"+table+"/"+policy)
	redirectMsg(w, r, "/p/"+slug+"/api", "Policy dropped.")
}
