package main

// Versioned migrations: timestamped SQL applied atomically and recorded inside
// the project database itself (forgebase.migrations), so the history travels
// with every dump, restore, branch and clone of that database.

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (a *app) ensureMigrationsTable(slug string) error {
	db, err := a.dbFor(slug)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS forgebase`); err != nil {
		return err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS forgebase.migrations (
		version text PRIMARY KEY,
		name text NOT NULL,
		sql text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`)
	return err
}

type migRow struct {
	Version, Name, SQL, Applied string
}

func (a *app) listMigrations(slug string) []migRow {
	db, err := a.dbFor(slug)
	if err != nil {
		return nil
	}
	rows, err := db.Query(`SELECT version, name, sql, to_char(applied_at,'Mon DD, YYYY HH24:MI')
		FROM forgebase.migrations ORDER BY version DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []migRow
	for rows.Next() {
		var m migRow
		rows.Scan(&m.Version, &m.Name, &m.SQL, &m.Applied)
		out = append(out, m)
	}
	return out
}

func (a *app) migrationsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	a.ensureMigrationsTable(slug)
	content := renderContent(migrationsBody, map[string]any{
		"Slug": slug, "Migs": a.listMigrations(slug),
	})
	a.renderShell(w, r, shellData{Title: slug + " · Migrations", Nav: "migrations", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Migrations"}}}, content)
}

// applyMigration runs the SQL and records it in ONE transaction: either the
// schema change lands together with its history row, or neither does.
func (a *app) applyMigration(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/migrations"
	name := strings.TrimSpace(r.FormValue("name"))
	sqlText := strings.TrimSpace(r.FormValue("sql"))
	if name == "" || sqlText == "" {
		redirectErr(w, r, back, "Give the migration a name and its SQL.")
		return
	}
	if len(sqlText) > 1<<20 {
		redirectErr(w, r, back, "Migration too large (1 MB max) - split it up.")
		return
	}
	if err := a.ensureMigrationsTable(slug); err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	version := time.Now().UTC().Format("20060102150405")
	tx, err := db.Begin()
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if _, err := tx.Exec(sqlText); err != nil {
		tx.Rollback()
		redirectErr(w, r, back, "Migration failed (nothing applied): "+err.Error())
		return
	}
	if _, err := tx.Exec(`INSERT INTO forgebase.migrations(version, name, sql) VALUES ($1,$2,$3)`,
		version, name, sqlText); err != nil {
		tx.Rollback()
		redirectErr(w, r, back, "Could not record the migration (nothing applied): "+err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	a.reloadPostgRESTSchema(slug)
	a.audit(r, "migration-apply", slug+"/"+version+" "+name)
	redirectMsg(w, r, back, "Migration "+version+" ("+name+") applied.")
}

// migrationsSQL streams the full ordered history as one runnable .sql file -
// a portable rebuild script for any plain Postgres.
func (a *app) migrationsSQL(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	migs := a.listMigrations(slug)
	w.Header().Set("Content-Type", "application/sql")
	w.Header().Set("Content-Disposition", `attachment; filename="`+slug+`-migrations.sql"`)
	fmt.Fprintf(w, "-- %s: %d migration(s), exported by ForgeBase\n", slug, len(migs))
	for i := len(migs) - 1; i >= 0; i-- { // oldest first for replay
		m := migs[i]
		fmt.Fprintf(w, "\n-- %s %s (applied %s)\n%s\n", m.Version, m.Name, m.Applied, strings.TrimRight(m.SQL, "\n;")+";")
	}
}
