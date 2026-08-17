package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// safeTail returns the last n bytes of s trimmed to a valid UTF-8 boundary, so
// storing a truncated error message can't raise "invalid byte sequence".
func safeTail(s string, n int) string {
	if len(s) > n {
		s = s[len(s)-n:]
	}
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[1:]
	}
	return s
}

// Clone & Sync: import any external Postgres into a new ForgeBase project from a
// connection string (async pg_dump -> pg_restore), and optionally keep it synced
// with the source via logical replication (near-instant) or manual refresh.

func maskURL(u string) string {
	// hide the password: scheme://user:PASS@host -> scheme://user:***@host
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if at := strings.IndexByte(rest, '@'); at >= 0 {
			creds := rest[:at]
			if c := strings.IndexByte(creds, ':'); c >= 0 {
				return u[:i+3] + creds[:c] + ":***@" + rest[at+1:]
			}
		}
	}
	return u
}

// directDSN builds the local (in-cluster) connection string for a project role.
func (a *app) directDSN(slug, pw string) string {
	return fmt.Sprintf("postgresql://%s:%s@127.0.0.1:5432/%s?sslmode=require", slug, pw, slug)
}

// ----------------------------------------------------------------- clone

func (a *app) cloneProject(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(strings.ToLower(r.FormValue("slug")))
	source := strings.TrimSpace(r.FormValue("source"))
	if !slugRe.MatchString(slug) || isReserved(slug) {
		redirectErr(w, r, "/", "Invalid or reserved name: letters, numbers and dash; start with a letter.")
		return
	}
	if a.projectExists(slug) {
		redirectErr(w, r, "/", "A project named "+slug+" already exists.")
		return
	}
	if !strings.HasPrefix(source, "postgres://") && !strings.HasPrefix(source, "postgresql://") {
		redirectErr(w, r, "/", "Source must be a postgres:// connection string.")
		return
	}
	pw, err := a.provisionProject(slug)
	if err != nil {
		redirectErr(w, r, "/", "Create failed: "+err.Error())
		return
	}
	a.db.Exec(`INSERT INTO db_imports(slug, source_enc, status) VALUES ($1, pgp_sym_encrypt($2,$3), 'cloning')
		ON CONFLICT (slug) DO UPDATE SET source_enc=pgp_sym_encrypt($2,$3), status='cloning', message=''`,
		slug, source, string(a.cfg.secret))
	a.db.Exec(`UPDATE projects SET status='cloning' WHERE slug=$1`, slug)
	a.audit(r, "clone-start", slug+" <- "+maskURL(source))
	go a.runClone(slug, source, a.directDSN(slug, pw), false)
	redirectMsg(w, r, "/p/"+slug+"/sync", "Cloning started. This page shows progress.")
}

// runClone performs the dump + restore in the background, updating db_imports.
// dropFirst (used by Refresh) drops the target's public schema ONLY after the
// source dump has succeeded, so an unreachable source never destroys existing
// data first.
func (a *app) runClone(slug, source, targetDSN string, dropFirst bool) {
	defer func() { recover() }() // a panic here must not take down the binary
	setErr := func(msg string) {
		a.db.Exec(`UPDATE db_imports SET status='error', message=$2, updated_at=now() WHERE slug=$1`,
			slug, safeTail(strings.TrimSpace(msg), 400))
	}
	dumpFile := "/tmp/clone-" + slug + ".dump"
	// Always clean up the scratch dump, even on early failure.
	defer exec.Command("docker", "exec", "pgforge-db", "rm", "-f", dumpFile).Run()

	dump := exec.Command("docker", "exec", "-e", "SRC="+source, "pgforge-db", "sh", "-c",
		`pg_dump "$SRC" -Fc --no-owner --no-acl -f `+dumpFile)
	if out, err := dump.CombinedOutput(); err != nil {
		setErr("dump failed: " + string(out))
		return
	}
	// Only now that we have a good dump is it safe to wipe the target.
	if dropFirst {
		if db, err := a.dbFor(slug); err == nil {
			if _, e := db.Exec(`DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); e != nil {
				setErr("could not reset target schema: " + e.Error())
				return
			}
		}
	}
	restore := exec.Command("docker", "exec", "-e", "DST="+targetDSN, "pgforge-db", "sh", "-c",
		`pg_restore --no-owner --no-acl -j 2 --dbname="$DST" `+dumpFile)
	restore.CombinedOutput() // pg_restore exits non-zero on ignorable warnings

	// sanity: did any tables land?
	var n int
	if db, err := a.dbFor(slug); err == nil {
		db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&n)
	}
	if n == 0 {
		setErr("restore produced no tables - check the source URL/permissions")
		return
	}
	a.db.Exec(`UPDATE db_imports SET status='done', message=$2, updated_at=now() WHERE slug=$1`,
		slug, fmt.Sprintf("%d tables imported", n))
	a.db.Exec(`UPDATE projects SET status='active' WHERE slug=$1`, slug)
	a.auditRaw("system", "-", "clone-done", slug)
}

// ----------------------------------------------------------------- sync page + actions

func (a *app) syncPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	var source, status, message string
	var syncOn bool
	a.db.QueryRow(`SELECT coalesce(pgp_sym_decrypt(source_enc,$2),''), status, coalesce(message,''), sync_enabled
		FROM db_imports WHERE slug=$1`, slug, string(a.cfg.secret)).Scan(&source, &status, &message, &syncOn)
	content := renderContent(syncBody, map[string]any{
		"Slug": slug, "HasSource": source != "", "Source": maskURL(source),
		"Status": status, "Message": message, "SyncOn": syncOn,
	})
	a.renderShell(w, r, shellData{Title: slug + " · Sync", Nav: "sync", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Sync"}}}, content)
}

// refreshSync re-runs the dump+restore from the stored source (drops+recreates).
func (a *app) refreshSync(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	var source, pw string
	a.db.QueryRow(`SELECT pgp_sym_decrypt(source_enc,$2) FROM db_imports WHERE slug=$1`, slug, string(a.cfg.secret)).Scan(&source)
	a.db.QueryRow(`SELECT pgp_sym_decrypt(password_enc,$2) FROM projects WHERE slug=$1`, slug, string(a.cfg.secret)).Scan(&pw)
	if source == "" {
		redirectErr(w, r, "/p/"+slug+"/sync", "No source configured for this project.")
		return
	}
	a.db.Exec(`UPDATE db_imports SET status='cloning', message='refreshing' WHERE slug=$1`, slug)
	a.audit(r, "sync-refresh", slug)
	// The target schema is dropped inside runClone AFTER the source dump
	// succeeds, so a failed/unreachable source can no longer wipe the data first.
	go a.runClone(slug, source, a.directDSN(slug, pw), true)
	redirectMsg(w, r, "/p/"+slug+"/sync", "Refresh started.")
}

// enableLiveSync sets up logical replication from the source (near-instant sync).
func (a *app) enableLiveSync(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	var source string
	a.db.QueryRow(`SELECT pgp_sym_decrypt(source_enc,$2) FROM db_imports WHERE slug=$1`, slug, string(a.cfg.secret)).Scan(&source)
	if source == "" {
		redirectErr(w, r, "/p/"+slug+"/sync", "No source configured.")
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/sync", err.Error())
		return
	}
	// try to create a publication on the source (needs logical replication on the
	// source). CREATE PUBLICATION has no IF NOT EXISTS clause, so guard with a
	// catalog check in a DO block instead (the old IF NOT EXISTS was invalid SQL
	// and made this always fail).
	pubCmd := exec.Command("docker", "exec", "-e", "SRC="+source, "pgforge-db", "sh", "-c",
		`psql "$SRC" -v ON_ERROR_STOP=1 -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname='forgebase_pub') THEN CREATE PUBLICATION forgebase_pub FOR ALL TABLES; END IF; END \$\$;"`)
	if out, err := pubCmd.CombinedOutput(); err != nil {
		redirectErr(w, r, "/p/"+slug+"/sync",
			"Could not create a publication on the source (it must have logical replication / wal_level=logical enabled): "+lastLine(string(out)))
		return
	}
	// subscribe on the ForgeBase side; copy_data=false since we already cloned
	_, err = db.Exec(fmt.Sprintf(`CREATE SUBSCRIPTION forgebase_sub CONNECTION %s PUBLICATION forgebase_pub WITH (copy_data = false)`,
		quoteLiteral(source)))
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/sync", "Subscription failed: "+err.Error())
		return
	}
	a.db.Exec(`UPDATE db_imports SET sync_enabled=true WHERE slug=$1`, slug)
	a.audit(r, "livesync-enable", slug)
	redirectMsg(w, r, "/p/"+slug+"/sync", "Live sync enabled - changes on the source now stream in continuously.")
}

func (a *app) disableLiveSync(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if db, err := a.dbFor(slug); err == nil {
		a.dropSubscription(db)
	}
	a.db.Exec(`UPDATE db_imports SET sync_enabled=false WHERE slug=$1`, slug)
	a.audit(r, "livesync-disable", slug)
	redirectMsg(w, r, "/p/"+slug+"/sync", "Live sync disabled.")
}

// dropSubscription detaches the replication slot before dropping the
// subscription. Without SET (slot_name = NONE), DROP SUBSCRIPTION tries to reach
// the (possibly dead) publisher to drop the remote slot and blocks forever;
// each statement also gets a short timeout as a backstop. Safe to call when no
// subscription exists (the ALTERs just error and are ignored).
func (a *app) dropSubscription(db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db.ExecContext(ctx, `ALTER SUBSCRIPTION forgebase_sub DISABLE`)
	db.ExecContext(ctx, `ALTER SUBSCRIPTION forgebase_sub SET (slot_name = NONE)`)
	db.ExecContext(ctx, `DROP SUBSCRIPTION IF EXISTS forgebase_sub`)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
