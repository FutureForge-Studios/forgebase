package main

// Foreign-data wrappers UI (postgres_fdw): connect an external Postgres,
// import its tables as foreign tables, and query across databases as if
// they were local. The panel manages servers, a PUBLIC user mapping and
// schema imports; everything else is plain SQL.

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

type fdwServer struct {
	Name, Host, DB string
	Tables         int
}

// qopt escapes a value for use inside an OPTIONS ('...') literal.
func qopt(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func (a *app) listFDWServers(slug string) []fdwServer {
	db, err := a.dbFor(slug)
	if err != nil {
		return nil
	}
	rows, err := db.Query(`SELECT s.srvname,
			coalesce((SELECT option_value FROM pg_options_to_table(s.srvoptions) WHERE option_name='host'), ''),
			coalesce((SELECT option_value FROM pg_options_to_table(s.srvoptions) WHERE option_name='dbname'), ''),
			(SELECT count(*) FROM pg_foreign_table ft JOIN pg_class c ON c.oid = ft.ftrelid WHERE ft.ftserver = s.oid)
		FROM pg_foreign_server s ORDER BY s.srvname`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []fdwServer
	for rows.Next() {
		var f fdwServer
		rows.Scan(&f.Name, &f.Host, &f.DB, &f.Tables)
		out = append(out, f)
	}
	return out
}

func (a *app) fdwCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/database"
	name := sanitizeIdent(r.FormValue("name"))
	host := strings.TrimSpace(r.FormValue("host"))
	dbname := strings.TrimSpace(r.FormValue("dbname"))
	user := strings.TrimSpace(r.FormValue("remote_user"))
	pass := r.FormValue("remote_pass")
	port, perr := strconv.Atoi(r.FormValue("port"))
	if name == "" || host == "" || dbname == "" || user == "" || perr != nil || port < 1 || port > 65535 {
		redirectErr(w, r, back, "Server name, host, port, database and remote user are all required.")
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if _, err := db.Exec(`CREATE EXTENSION IF NOT EXISTS postgres_fdw`); err != nil {
		redirectErr(w, r, back, "postgres_fdw unavailable: "+err.Error())
		return
	}
	q := pq.QuoteIdentifier
	stmts := []string{
		fmt.Sprintf(`CREATE SERVER %s FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host '%s', port '%d', dbname '%s')`,
			q(name), qopt(host), port, qopt(dbname)),
		fmt.Sprintf(`CREATE USER MAPPING FOR PUBLIC SERVER %s OPTIONS (user '%s', password '%s')`,
			q(name), qopt(user), qopt(pass)),
		fmt.Sprintf(`GRANT USAGE ON FOREIGN SERVER %s TO %s`, q(name), q(a.roleFor(slug))),
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Exec(fmt.Sprintf(`DROP SERVER IF EXISTS %s CASCADE`, q(name)))
			redirectErr(w, r, back, err.Error())
			return
		}
	}
	a.audit(r, "fdw-create", slug+"/"+name)
	redirectMsg(w, r, back, "Foreign server "+name+" connected. Import a schema below to start querying it.")
}

func (a *app) fdwImport(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/database"
	server := sanitizeIdent(r.FormValue("server"))
	remote := sanitizeIdent(r.FormValue("remote_schema"))
	local := sanitizeIdent(r.FormValue("local_schema"))
	if server == "" || remote == "" {
		redirectErr(w, r, back, "Server and remote schema are required.")
		return
	}
	if local == "" {
		local = server
	}
	if reservedSchemas[local] {
		redirectErr(w, r, back, "That local schema name is reserved.")
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	q := pq.QuoteIdentifier
	role := q(a.roleFor(slug))
	db.Exec(fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, q(local)))
	if _, err := db.Exec(fmt.Sprintf(`IMPORT FOREIGN SCHEMA %s FROM SERVER %s INTO %s`,
		q(remote), q(server), q(local))); err != nil {
		redirectErr(w, r, back, "Import failed: "+err.Error())
		return
	}
	db.Exec(fmt.Sprintf(`GRANT USAGE ON SCHEMA %s TO %s`, q(local), role))
	db.Exec(fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA %s TO %s`, q(local), role))
	a.audit(r, "fdw-import", slug+"/"+server+"."+remote)
	redirectMsg(w, r, back, "Imported "+remote+" from "+server+" into schema "+local+". Its tables are queryable now.")
}

func (a *app) fdwDrop(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/database"
	name := sanitizeIdent(r.FormValue("name"))
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP SERVER IF EXISTS %s CASCADE`, pq.QuoteIdentifier(name))); err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	a.audit(r, "fdw-drop", slug+"/"+name)
	redirectMsg(w, r, back, "Foreign server "+name+" removed (its foreign tables went with it).")
}
