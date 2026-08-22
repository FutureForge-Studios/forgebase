package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lib/pq"
)

type projectView struct {
	Slug, Status, Created, Size string
	Conns                       int
	DirectURL, PooledURL        string
}

func (a *app) loadProjects() ([]projectView, error) {
	rows, err := a.db.Query(`
		SELECT p.slug, p.status, to_char(p.created_at,'Mon DD, YYYY'),
		       coalesce(pg_size_pretty(pg_database_size(d.oid)),'-'),
		       pgp_sym_decrypt(p.password_enc,$1),
		       coalesce(s.n,0)
		FROM projects p
		LEFT JOIN pg_database d ON d.datname=p.slug
		LEFT JOIN (SELECT datname,count(*) n FROM pg_stat_activity GROUP BY 1) s ON s.datname=p.slug
		WHERE p.parent IS NULL
		ORDER BY p.created_at`, string(a.cfg.secret))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []projectView
	for rows.Next() {
		var v projectView
		var pw string
		if err := rows.Scan(&v.Slug, &v.Status, &v.Created, &v.Size, &pw, &v.Conns); err != nil {
			return nil, err
		}
		v.DirectURL = fmt.Sprintf("postgresql://%s:%s@%s:5432/%s?sslmode=require", v.Slug, pw, a.cfg.domain, v.Slug)
		v.PooledURL = fmt.Sprintf("postgresql://%s:%s@%s:6543/%s", v.Slug, pw, a.cfg.domain, v.Slug)
		out = append(out, v)
	}
	return out, nil
}

func (a *app) projectCred(slug string) (string, string) {
	var status, pw string
	a.db.QueryRow(`SELECT status, pgp_sym_decrypt(password_enc,$1) FROM projects WHERE slug=$2`,
		string(a.cfg.secret), slug).Scan(&status, &pw)
	return status, pw
}

// ----------------------------------------------------------------- dashboard

func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	projects, err := a.loadProjects()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	anyCloning := false
	for _, p := range projects {
		if p.Status == "cloning" {
			anyCloning = true
			break
		}
	}
	content := renderContent(dashboardBody, map[string]any{
		"Projects": projects, "Stats": a.hostStats(), "AnyCloning": anyCloning,
	})
	a.renderShell(w, r, shellData{Title: "Projects", Nav: "projects",
		Crumbs: []crumb{{Label: "Projects"}}}, content)
}

// ----------------------------------------------------------------- provisioning

// provisionProject creates the database + login role + metadata row for a slug
// and returns the generated password. Shared by New Project and Clone.
func (a *app) provisionProject(slug string) (string, error) {
	pw := randHex(18)
	q := pq.QuoteIdentifier(slug)
	// 10 direct connections per new project (the pooled port multiplexes far
	// beyond this); existing projects keep their configured limit.
	steps := []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD %s CONNECTION LIMIT 10`, q, pq.QuoteLiteral(pw)),
		fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, q, q),
		fmt.Sprintf(`REVOKE CONNECT ON DATABASE %s FROM PUBLIC`, q),
	}
	for _, s := range steps {
		if _, err := a.db.Exec(s); err != nil {
			a.db.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, q))
			a.db.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS %s`, q))
			return "", err
		}
	}
	if _, err := a.db.Exec(
		`INSERT INTO projects(slug, role_name, password_enc) VALUES ($1,$1,pgp_sym_encrypt($2,$3))`,
		slug, pw, string(a.cfg.secret)); err != nil {
		return "", err
	}
	if err := a.rewriteUserlist(); err != nil {
		log.Printf("userlist rewrite: %v", err)
	}
	return pw, nil
}

func (a *app) rewriteUserlist() error {
	rows, err := a.db.Query(`SELECT slug, pgp_sym_decrypt(password_enc,$1) FROM projects`, string(a.cfg.secret))
	if err != nil {
		return err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var slug, pw string
		if err := rows.Scan(&slug, &pw); err != nil {
			return err
		}
		fmt.Fprintf(&b, "%q %q\n", slug, pw)
	}
	if err := os.WriteFile(a.cfg.userlistPath, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return exec.Command("docker", "kill", "-s", "HUP", a.cfg.pgbContainer).Run()
}

// uniqueSlug returns the slug itself if free, else the first available
// numbered variant (slug-2, slug-3, ...), so creating a project with a taken
// name just works instead of erroring.
func (a *app) uniqueSlug(slug string) string {
	if !a.projectExists(slug) {
		return slug
	}
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d", slug, i)
		if len(cand) > 40 { // keep within the slug length limit
			cand = fmt.Sprintf("%s-%d", slug[:40-len(fmt.Sprint(i))-1], i)
		}
		if !a.projectExists(cand) {
			return cand
		}
	}
	return slug + "-" + randHex(3)
}

func (a *app) createProject(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(strings.ToLower(r.FormValue("slug")))
	if !slugRe.MatchString(slug) || isReserved(slug) {
		redirectErr(w, r, "/", "Invalid or reserved name: 3-40 chars, a-z 0-9 and dash, must start with a letter.")
		return
	}
	asked := slug
	slug = a.uniqueSlug(slug)
	if _, err := a.provisionProject(slug); err != nil {
		redirectErr(w, r, "/", "Create failed: "+err.Error())
		return
	}
	a.audit(r, "create", slug)
	// connection-budget heads-up: warn (never block) when the sum of per-project
	// limits approaches the cluster's max_connections
	var budgetNote string
	var sumLimits, maxConns int
	a.db.QueryRow(`SELECT coalesce(sum(rolconnlimit),0) FROM pg_roles r
		JOIN projects p ON p.slug = r.rolname WHERE rolconnlimit > 0`).Scan(&sumLimits)
	a.db.QueryRow(`SELECT setting::int FROM pg_settings WHERE name='max_connections'`).Scan(&maxConns)
	if maxConns > 0 && sumLimits > maxConns-15 {
		budgetNote = fmt.Sprintf(" Note: project connection limits now total %d of %d cluster slots - consider raising max_connections on the Database page.", sumLimits, maxConns)
	}
	msg := "Project " + slug + " is ready." + budgetNote
	if slug != asked {
		msg = "\"" + asked + "\" was taken, so your project was created as " + slug + "."
	}
	http.Redirect(w, r, "/p/"+slug+"?m="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}

// dropProjectFully removes a single project/branch and ALL of its resources:
// database, role, every per-project metadata row, storage files and functions,
// and its lazily-started processes. Does not rewrite the userlist (caller does).
func (a *app) dropProjectFully(slug string) error {
	q := pq.QuoteIdentifier(slug)
	a.stopPostgREST(slug)
	a.stopRealtimeHub(slug) // stop the LISTEN hub before the database goes away
	// drop any live-sync subscription first so its replication slot is released
	// on the source (e.g. Neon) instead of being orphaned when the DB is dropped.
	// dropSubscription detaches the slot first so this can't hang on a dead source.
	if db, err := a.dbFor(slug); err == nil {
		a.dropSubscription(db)
	}
	closeConn(slug)
	a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, slug)
	if _, err := a.db.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, q)); err != nil {
		return err
	}
	a.db.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS %s`, q))
	// every per-project table keyed by slug
	for _, t := range []string{"projects", "api_config", "auth_config", "realtime_config",
		"oauth_providers", "webhooks", "edge_functions", "saved_queries",
		"storage_buckets", "storage_objects", "metrics_samples", "db_imports"} {
		a.db.Exec(`DELETE FROM `+t+` WHERE slug=$1`, slug)
	}
	os.RemoveAll(filepath.Join(storageRoot, slug))
	os.RemoveAll(filepath.Join(funcRoot, slug))
	return nil
}

func (a *app) deleteProject(w http.ResponseWriter, r *http.Request) {
	slug := r.FormValue("slug")
	if r.FormValue("confirm") != slug {
		redirectErr(w, r, "/", "Confirmation text did not match.")
		return
	}
	if !a.projectExists(slug) {
		redirectErr(w, r, "/", "No such project.")
		return
	}
	// cascade to this project's branches (databases named "<slug>-*")
	var branches []string
	if rows, err := a.db.Query(`SELECT slug FROM projects WHERE parent=$1`, slug); err == nil {
		for rows.Next() {
			var b string
			rows.Scan(&b)
			branches = append(branches, b)
		}
		rows.Close()
	}
	for _, b := range branches {
		if err := a.dropProjectFully(b); err != nil {
			log.Printf("drop branch %s: %v", b, err)
		}
	}
	if err := a.dropProjectFully(slug); err != nil {
		redirectErr(w, r, "/", "Drop database failed: "+err.Error())
		return
	}
	if err := a.rewriteUserlist(); err != nil {
		log.Printf("userlist rewrite: %v", err)
	}
	a.audit(r, "delete", slug)
	msg := "Project " + slug + " deleted."
	if len(branches) > 0 {
		msg = fmt.Sprintf("Project %s and %d branch(es) deleted.", slug, len(branches))
	}
	redirectMsg(w, r, "/", msg)
}

func (a *app) pauseProject(w http.ResponseWriter, r *http.Request) {
	slug := r.FormValue("slug")
	q := pq.QuoteIdentifier(slug)
	if _, err := a.db.Exec(fmt.Sprintf(`ALTER ROLE %s NOLOGIN`, q)); err != nil {
		redirectErr(w, r, "/", "Pause failed: "+err.Error())
		return
	}
	a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, slug)
	a.db.Exec(`UPDATE projects SET status='paused' WHERE slug=$1`, slug)
	a.audit(r, "pause", slug)
	redirectMsg(w, r, "/", slug+" paused. Data kept; connections blocked.")
}

// sleepProject releases a project's resources immediately (same as auto-sleep:
// API sidecar, realtime listener, cached pools) without waiting for the idle
// window. Nothing is blocked - it wakes on the next request like any sleeper.
func (a *app) sleepProject(w http.ResponseWriter, r *http.Request) {
	slug := r.FormValue("slug")
	if !a.projectExists(slug) {
		redirectErr(w, r, "/", "No such project.")
		return
	}
	a.stopPostgREST(slug)
	a.stopRealtimeHub(slug)
	closeConn(slug)
	a.db.Exec(`UPDATE projects SET status='suspended' WHERE slug=$1 AND status='active'`, slug)
	a.audit(r, "manual-sleep", slug)
	redirectMsg(w, r, "/", slug+" is sleeping. It wakes automatically on the next request.")
}

// wakeProject flips a sleeping project active and re-warms webhook delivery.
func (a *app) wakeProject(w http.ResponseWriter, r *http.Request) {
	slug := r.FormValue("slug")
	if !a.projectExists(slug) {
		redirectErr(w, r, "/", "No such project.")
		return
	}
	// clear a legacy hard-suspend NOLOGIN if one exists
	a.db.Exec(fmt.Sprintf(`ALTER ROLE %s LOGIN`, pq.QuoteIdentifier(slug)))
	a.db.Exec(`UPDATE projects SET status='active', last_active=now() WHERE slug=$1 AND status='suspended'`, slug)
	if a.hasWebhooks(slug) {
		a.rtGetHub(slug)
	}
	a.audit(r, "manual-wake", slug)
	redirectMsg(w, r, "/", slug+" is awake.")
}

func (a *app) resumeProject(w http.ResponseWriter, r *http.Request) {
	slug := r.FormValue("slug")
	q := pq.QuoteIdentifier(slug)
	if _, err := a.db.Exec(fmt.Sprintf(`ALTER ROLE %s LOGIN`, q)); err != nil {
		redirectErr(w, r, "/", "Resume failed: "+err.Error())
		return
	}
	a.db.Exec(`UPDATE projects SET status='active' WHERE slug=$1`, slug)
	a.audit(r, "resume", slug)
	redirectMsg(w, r, "/", slug+" resumed.")
}

// ----------------------------------------------------------------- project home

func (a *app) projectHome(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	a.touchAndResume(slug) // opening a project wakes it if it was auto-suspended
	status, pw := a.projectCred(slug)
	var size, created, version, retention string
	var tables, conns, branches int
	var apiEnabled bool
	a.db.QueryRow(`SELECT pg_size_pretty(pg_database_size($1))`, slug).Scan(&size)
	a.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=$1`, slug).Scan(&conns)
	a.db.QueryRow(`SELECT to_char(created_at,'Mon DD, YYYY') FROM projects WHERE slug=$1`, slug).Scan(&created)
	a.db.QueryRow(`SELECT count(*) FROM projects WHERE parent=$1`, slug).Scan(&branches)
	a.db.QueryRow(`SHOW server_version`).Scan(&version)
	a.db.QueryRow(`SELECT value FROM settings WHERE key='retention_days'`).Scan(&retention)
	a.db.QueryRow(`SELECT enabled FROM api_config WHERE slug=$1`, slug).Scan(&apiEnabled)
	if pdb, err := a.dbFor(slug); err == nil {
		pdb.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables)
	}
	if i := strings.IndexByte(version, ' '); i > 0 {
		version = version[:i]
	}
	content := renderContent(projectHomeBody, map[string]any{
		"Slug": slug, "Status": status, "Size": size, "Tables": tables, "Conns": conns,
		"Created": created, "Branches": branches, "Version": version, "Retention": retention,
		"APIEnabled": apiEnabled, "Domain": a.cfg.domain,
		"Direct": fmt.Sprintf("postgresql://%s:%s@%s:5432/%s?sslmode=require", slug, pw, a.cfg.domain, slug),
		"Pooled": fmt.Sprintf("postgresql://%s:%s@%s:6543/%s", slug, pw, a.cfg.domain, slug),
	})
	a.renderShell(w, r, shellData{Title: slug, Nav: "home", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug}}}, content)
}
