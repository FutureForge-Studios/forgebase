// SPDX-License-Identifier: AGPL-3.0-only
//
// pgforged - the ForgeBase control plane.
//
// One Go binary: the whole dashboard (projects, table editor, SQL editor,
// database admin, backups, settings) + provisioning (database + role + pooler
// entry) + Caddy on-demand-TLS check + host metrics. Projects are rows in the
// pgforge metadata DB; infrastructure is shared. UI is server-rendered with a
// "Light Editorial" design system (see assets.go).
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// ----------------------------------------------------------------- config

type config struct {
	dsn          string
	panelUser    string
	panelPass    string
	secret       []byte
	domain       string
	listen       string
	userlistPath string
	pgbContainer string
}

func loadConfig() config {
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Fatalf("missing env %s", k)
		}
		return v
	}
	opt := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	secret := need("SESSION_SECRET")
	// SESSION_SECRET is the HMAC key for sessions/invites/signed URLs AND the
	// passphrase that encrypts every project's stored DB password. A weak value
	// makes sessions forgeable, so refuse to start with an obviously weak one.
	if len(secret) < 16 {
		log.Fatalf("SESSION_SECRET must be at least 16 characters (got %d)", len(secret))
	}
	return config{
		dsn:          need("PGFORGE_DSN"),
		panelUser:    need("PANEL_USER"),
		panelPass:    need("PANEL_PASS"),
		secret:       []byte(secret),
		domain:       need("DOMAIN"),
		listen:       opt("LISTEN", "127.0.0.1:8080"),
		userlistPath: opt("USERLIST_PATH", "/opt/pgforge/pgbouncer/userlist.txt"),
		pgbContainer: opt("PGBOUNCER_CONTAINER", "pgforge-pgbouncer"),
	}
}

// ----------------------------------------------------------------- app

type app struct {
	cfg     config
	db      *sql.DB
	baseURL *url.URL // PGFORGE_DSN parsed; database path swapped per project

	mu           sync.Mutex
	attempts     map[string][]time.Time // panel login rate limiting per IP
	authAttempts map[string][]time.Time // end-user /auth/v1 rate limiting per IP
}

// slug is validated after lowercasing: letters, digits and dash only (NO
// underscore - it's illegal in DNS hostnames and the slug becomes a subdomain);
// must start with a letter, end alphanumeric, and fit the 63-byte identifier
// limit with room for a branch suffix.
var slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

var reserved = map[string]bool{
	"postgres": true, "pgforge": true, "template0": true, "template1": true,
	"admin": true, "root": true, "db": true, "www": true, "mail": true,
	"api": true, "auth": true, "storage": true, "realtime": true, "panel": true,
	"graphql": true, "rest": true, "functions": true, "pgbouncer": true,
	"edge": true, "internal": true, "healthz": true,
}

// isReserved also blocks the whole pg_* namespace, which Postgres reserves for
// roles (CREATE ROLE pg_toast fails with a raw error otherwise).
func isReserved(slug string) bool {
	return reserved[slug] || strings.HasPrefix(slug, "pg_")
}

func main() {
	cfg := loadConfig()
	db, err := sql.Open("postgres", cfg.dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(5)
	// Postgres may still be starting (compose brings it up alongside pgforged, and
	// after a reboot the container races us). Retry rather than crash-loop.
	pinged := false
	for i := 0; i < 30; i++ {
		if err := db.Ping(); err == nil {
			pinged = true
			break
		}
		log.Printf("waiting for postgres (attempt %d/30)...", i+1)
		time.Sleep(2 * time.Second)
	}
	if !pinged {
		log.Fatalf("cannot reach postgres after 60s")
	}
	base, err := url.Parse(cfg.dsn)
	if err != nil {
		log.Fatalf("bad PGFORGE_DSN: %v", err)
	}
	a := &app{cfg: cfg, db: db, baseURL: base,
		attempts: map[string][]time.Time{}, authAttempts: map[string][]time.Time{}}
	if err := a.ensureSchema(); err != nil {
		log.Fatalf("schema init: %v", err)
	}
	a.startSampler()
	a.startWebhookPumps()
	a.startRateLimitPruner()
	go a.migrateAuthProjects() // apply new auth columns to already-enabled projects
	go a.reconcileInfra()      // bring on-box scripts/units current after a self-update

	mux := http.NewServeMux()
	// public
	mux.HandleFunc("GET /favicon.svg", faviconHandler)
	mux.HandleFunc("GET /favicon.ico", faviconHandler)
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginSubmit)
	mux.HandleFunc("GET /register", a.registerPage)
	mux.HandleFunc("POST /register", a.registerSubmit)
	mux.HandleFunc("GET /invite", a.invitePage)
	mux.HandleFunc("POST /invite", a.inviteSubmit)
	mux.HandleFunc("POST /logout", a.logout)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("GET /internal/tls-check", a.tlsCheck)
	// account
	mux.HandleFunc("GET /account", a.auth(a.accountPage))
	mux.HandleFunc("POST /account/password", a.auth(a.changePassword))
	mux.HandleFunc("POST /account/profile", a.auth(a.updateProfile))
	mux.HandleFunc("POST /account/email", a.auth(a.changeEmail))
	mux.HandleFunc("POST /account/apikey-create", a.auth(a.createAPIKey))
	mux.HandleFunc("POST /account/apikey-revoke", a.auth(a.revokeAPIKey))
	mux.HandleFunc("GET /docs", a.auth(a.globalDocsPage))
	mux.HandleFunc("GET /audit", a.auth(a.globalAuditPage))
	mux.HandleFunc("GET /system", a.auth(a.systemPage))
	mux.HandleFunc("GET /changelog", a.auth(a.changelogPage))
	mux.HandleFunc("POST /system/update", a.auth(a.requireRole("owner", a.applyUpdate)))
	// team (owner-only management)
	mux.HandleFunc("GET /people", a.auth(a.peoplePage))
	mux.HandleFunc("POST /people/add", a.auth(a.requireRole("owner", a.addMember)))
	mux.HandleFunc("POST /people/role", a.auth(a.requireRole("owner", a.setMemberRole)))
	mux.HandleFunc("POST /people/remove", a.auth(a.requireRole("owner", a.removeMember)))
	// projects (global) - create/delete are admin+; browsing is open to members
	mux.HandleFunc("GET /{$}", a.auth(a.dashboard))
	mux.HandleFunc("POST /create", a.auth(a.requireRole("admin", a.createProject)))
	mux.HandleFunc("POST /clone", a.auth(a.requireRole("admin", a.cloneProject)))
	mux.HandleFunc("POST /delete", a.auth(a.requireRole("admin", a.deleteProject)))
	mux.HandleFunc("POST /pause", a.auth(a.requireRole("admin", a.pauseProject)))
	mux.HandleFunc("POST /resume", a.auth(a.requireRole("admin", a.resumeProject)))
	// project-scoped. proj() = slug-valid + project-exists gate on every route
	// (also the single guard that stops dbFor from caching a bogus slug). admin()
	// additionally requires the admin role for destructive / platform-affecting
	// or code-executing actions.
	proj := a.proj
	admin := func(h http.HandlerFunc) http.HandlerFunc { return a.proj(a.requireRole("admin", h)) }
	mux.HandleFunc("GET /p/{slug}", a.auth(proj(a.projectHome)))
	mux.HandleFunc("GET /p/{slug}/tables", a.auth(proj(a.tablesPage)))
	mux.HandleFunc("POST /p/{slug}/row-insert", a.auth(proj(a.rowInsert)))
	mux.HandleFunc("POST /p/{slug}/row-update", a.auth(proj(a.rowUpdate)))
	mux.HandleFunc("POST /p/{slug}/row-delete", a.auth(proj(a.rowDelete)))
	mux.HandleFunc("POST /p/{slug}/import", a.auth(proj(a.importCSV)))
	mux.HandleFunc("GET /p/{slug}/export", a.auth(proj(a.exportCSV)))
	mux.HandleFunc("POST /p/{slug}/table-create", a.auth(proj(a.createTable)))
	mux.HandleFunc("POST /p/{slug}/table-drop", a.auth(proj(a.dropTable)))
	mux.HandleFunc("POST /p/{slug}/column-add", a.auth(proj(a.addColumn)))
	mux.HandleFunc("POST /p/{slug}/column-drop", a.auth(proj(a.dropColumn)))
	mux.HandleFunc("GET /p/{slug}/sql", a.auth(proj(a.sqlPage)))
	mux.HandleFunc("POST /p/{slug}/sql", a.auth(proj(a.sqlRun)))
	mux.HandleFunc("POST /p/{slug}/sql/save", a.auth(proj(a.saveQuery)))
	mux.HandleFunc("POST /p/{slug}/sql/delete", a.auth(proj(a.deleteSavedQuery)))
	mux.HandleFunc("GET /p/{slug}/database", a.auth(proj(a.databasePage)))
	mux.HandleFunc("POST /p/{slug}/db-password", a.auth(admin(a.changeDBPassword)))
	mux.HandleFunc("POST /p/{slug}/extension", a.auth(proj(a.toggleExtension)))
	mux.HandleFunc("POST /p/{slug}/conn-limit", a.auth(proj(a.setConnLimit)))
	mux.HandleFunc("POST /p/{slug}/createdb", a.auth(admin(a.setCreateDB)))
	mux.HandleFunc("POST /p/{slug}/max-conns", a.auth(admin(a.setMaxConns)))
	mux.HandleFunc("GET /p/{slug}/backups", a.auth(proj(a.backupsPage)))
	mux.HandleFunc("POST /p/{slug}/backup-now", a.auth(proj(a.backupNow)))
	mux.HandleFunc("POST /p/{slug}/restore", a.auth(admin(a.restoreBackup)))
	mux.HandleFunc("POST /p/{slug}/pitr", a.auth(admin(a.pitrRestore)))
	mux.HandleFunc("POST /p/{slug}/retention", a.auth(admin(a.setRetention)))
	mux.HandleFunc("GET /p/{slug}/monitoring", a.auth(proj(a.monitoringPage)))
	mux.HandleFunc("GET /p/{slug}/logs", a.auth(proj(a.logsPage)))
	mux.HandleFunc("GET /p/{slug}/branches", a.auth(proj(a.branchesPage)))
	mux.HandleFunc("POST /p/{slug}/branch-create", a.auth(admin(a.branchCreate)))
	mux.HandleFunc("GET /p/{slug}/api", a.auth(proj(a.apiPage)))
	mux.HandleFunc("POST /p/{slug}/api-enable", a.auth(proj(a.enableAPI)))
	mux.HandleFunc("POST /p/{slug}/api-disable", a.auth(proj(a.disableAPI)))
	mux.HandleFunc("POST /p/{slug}/rls/toggle", a.auth(admin(a.toggleRLS)))
	mux.HandleFunc("POST /p/{slug}/rls/policy", a.auth(admin(a.addPolicy)))
	mux.HandleFunc("POST /p/{slug}/rls/policy-drop", a.auth(admin(a.dropPolicy)))
	mux.HandleFunc("GET /p/{slug}/auth", a.auth(proj(a.authPage)))
	mux.HandleFunc("POST /p/{slug}/auth-enable", a.auth(proj(a.enableAuth)))
	mux.HandleFunc("POST /p/{slug}/auth-disable", a.auth(proj(a.disableAuth)))
	mux.HandleFunc("POST /p/{slug}/auth-user-add", a.auth(proj(a.addAuthUser)))
	mux.HandleFunc("POST /p/{slug}/auth-user-password", a.auth(proj(a.setAuthUserPassword)))
	mux.HandleFunc("POST /p/{slug}/auth-user-delete", a.auth(proj(a.deleteAuthUser)))
	mux.HandleFunc("POST /p/{slug}/oauth-save", a.auth(admin(a.saveOAuth)))
	mux.HandleFunc("POST /p/{slug}/auth-smtp", a.auth(admin(a.saveAuthEmail)))
	mux.HandleFunc("GET /p/{slug}/realtime", a.auth(proj(a.realtimePage)))
	mux.HandleFunc("POST /p/{slug}/realtime-enable", a.auth(proj(a.enableRealtime)))
	mux.HandleFunc("POST /p/{slug}/realtime-disable", a.auth(proj(a.disableRealtime)))
	mux.HandleFunc("POST /p/{slug}/realtime-auth", a.auth(proj(a.setRealtimeAuth)))
	mux.HandleFunc("GET /p/{slug}/webhooks", a.auth(proj(a.webhooksPage)))
	mux.HandleFunc("POST /p/{slug}/webhook-create", a.auth(proj(a.createWebhook)))
	mux.HandleFunc("POST /p/{slug}/webhook-delete", a.auth(proj(a.deleteWebhook)))
	mux.HandleFunc("GET /p/{slug}/functions", a.auth(proj(a.edgePage)))
	mux.HandleFunc("POST /p/{slug}/function-save", a.auth(admin(a.saveFunction)))
	mux.HandleFunc("POST /p/{slug}/function-delete", a.auth(admin(a.deleteFunction)))
	mux.HandleFunc("POST /p/{slug}/edge-secret", a.auth(admin(a.addEdgeSecret)))
	mux.HandleFunc("POST /p/{slug}/edge-secret-delete", a.auth(admin(a.deleteEdgeSecret)))
	mux.HandleFunc("GET /p/{slug}/docs", a.auth(proj(a.docsPage)))
	mux.HandleFunc("GET /p/{slug}/storage", a.auth(proj(a.storagePage)))
	mux.HandleFunc("POST /p/{slug}/storage/bucket", a.auth(proj(a.createBucket)))
	mux.HandleFunc("POST /p/{slug}/storage/upload", a.auth(proj(a.uploadObject)))
	mux.HandleFunc("POST /p/{slug}/storage/delete", a.auth(proj(a.deleteObject)))
	mux.HandleFunc("POST /p/{slug}/storage/bucket-delete", a.auth(admin(a.deleteBucket)))
	mux.HandleFunc("GET /p/{slug}/sync", a.auth(proj(a.syncPage)))
	mux.HandleFunc("POST /p/{slug}/sync-refresh", a.auth(admin(a.refreshSync)))
	mux.HandleFunc("POST /p/{slug}/livesync-enable", a.auth(admin(a.enableLiveSync)))
	mux.HandleFunc("POST /p/{slug}/livesync-disable", a.auth(admin(a.disableLiveSync)))
	mux.HandleFunc("GET /p/{slug}/settings", a.auth(a.settingsPage))

	log.Printf("pgforged listening on %s (domain %s)", cfg.listen, cfg.domain)
	srv := &http.Server{
		Addr:    cfg.listen,
		Handler: a.securityHeaders(a.limitBody(a.rootHandler(mux))),
		// Timeouts so a slow-loris or a client that never reads can't pin
		// goroutines/FDs forever. WriteTimeout is generous to allow large
		// storage downloads and restore/backup responses.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      600 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// rootHandler routes public project-subdomain traffic (<slug>.<domain>) to the
// Data API, and everything on the apex domain to the panel mux.
func (a *app) rootHandler(panel http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		if host != a.cfg.domain && strings.HasSuffix(host, "."+a.cfg.domain) {
			slug := strings.TrimSuffix(host, "."+a.cfg.domain)
			if slug != "db" && a.projectExists(slug) {
				a.serveAPI(w, r, slug)
				return
			}
		}
		panel.ServeHTTP(w, r)
	})
}

// limitBody caps request bodies so a single large POST (login, JSON auth, SQL,
// storage upload, CSV import) can't OOM the one binary and take down every
// project. Uploads/imports get a larger cap (MAX_UPLOAD_MB, default 100); every
// other request is held to a small default.
func (a *app) limitBody(next http.Handler) http.Handler {
	const def = 4 << 20 // 4 MB
	large := int64(100) << 20
	if v := os.Getenv("MAX_UPLOAD_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			large = int64(n) << 20
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			lim := int64(def)
			if p := r.URL.Path; strings.HasSuffix(p, "/storage/upload") || strings.HasSuffix(p, "/import") || strings.HasPrefix(p, "/storage/v1/object/") {
				lim = large
			}
			r.Body = http.MaxBytesReader(w, r.Body, lim)
		}
		next.ServeHTTP(w, r)
	})
}

// ----------------------------------------------------------------- schema

// ensureSchema runs idempotent DDL on every boot (the init SQL only runs on a
// fresh cluster). Adds the users table for panel auth + a retention setting.
func (a *app) ensureSchema() error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS pgcrypto`,
		`CREATE TABLE IF NOT EXISTS users (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			name text NOT NULL,
			email text UNIQUE NOT NULL,
			pass_hash text NOT NULL,
			role text NOT NULL DEFAULT 'owner',
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key text PRIMARY KEY,
			value text NOT NULL
		)`,
		`INSERT INTO settings(key,value) VALUES ('retention_days','30')
			ON CONFLICT (key) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS api_config (
			slug text PRIMARY KEY,
			jwt_secret text NOT NULL,
			enabled boolean NOT NULL DEFAULT true,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS auth_config (
			slug text PRIMARY KEY,
			enabled boolean NOT NULL DEFAULT false,
			smtp_host text NOT NULL DEFAULT '',
			smtp_port integer NOT NULL DEFAULT 587,
			smtp_user text NOT NULL DEFAULT '',
			smtp_pass_enc bytea,
			smtp_from text NOT NULL DEFAULT '',
			confirm_email boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE auth_config ADD COLUMN IF NOT EXISTS smtp_host text NOT NULL DEFAULT ''`,
		`ALTER TABLE auth_config ADD COLUMN IF NOT EXISTS smtp_port integer NOT NULL DEFAULT 587`,
		`ALTER TABLE auth_config ADD COLUMN IF NOT EXISTS smtp_user text NOT NULL DEFAULT ''`,
		`ALTER TABLE auth_config ADD COLUMN IF NOT EXISTS smtp_pass_enc bytea`,
		`ALTER TABLE auth_config ADD COLUMN IF NOT EXISTS smtp_from text NOT NULL DEFAULT ''`,
		`ALTER TABLE auth_config ADD COLUMN IF NOT EXISTS confirm_email boolean NOT NULL DEFAULT false`,
		`CREATE TABLE IF NOT EXISTS realtime_config (
			slug text PRIMARY KEY,
			enabled boolean NOT NULL DEFAULT false,
			require_auth boolean NOT NULL DEFAULT true,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE realtime_config ADD COLUMN IF NOT EXISTS require_auth boolean NOT NULL DEFAULT true`,
		`CREATE TABLE IF NOT EXISTS oauth_providers (
			slug text NOT NULL, provider text NOT NULL,
			client_id text NOT NULL DEFAULT '', client_secret text NOT NULL DEFAULT '',
			enabled boolean NOT NULL DEFAULT false,
			PRIMARY KEY (slug, provider)
		)`,
		`CREATE TABLE IF NOT EXISTS webhooks (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			slug text NOT NULL, name text NOT NULL, url text NOT NULL,
			table_name text NOT NULL DEFAULT '',
			events text NOT NULL DEFAULT 'INSERT,UPDATE,DELETE',
			method text NOT NULL DEFAULT 'POST',
			headers text NOT NULL DEFAULT '',
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE webhooks ADD COLUMN IF NOT EXISTS method text NOT NULL DEFAULT 'POST'`,
		`ALTER TABLE webhooks ADD COLUMN IF NOT EXISTS headers text NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS first_name text`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS last_name text`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS invite_pending boolean NOT NULL DEFAULT false`,
		`ALTER TABLE webhooks ADD COLUMN IF NOT EXISTS secret text`,
		`CREATE TABLE IF NOT EXISTS webhook_deliveries (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			webhook_id uuid, slug text NOT NULL,
			status_code int, ok boolean, attempt int, error text,
			at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS edge_secrets (
			slug text NOT NULL, name text NOT NULL, value text NOT NULL,
			PRIMARY KEY (slug, name)
		)`,
		`CREATE TABLE IF NOT EXISTS edge_logs (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			slug text NOT NULL, name text NOT NULL, error text,
			at timestamptz NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS ip text`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_active timestamptz NOT NULL DEFAULT now()`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS parent text`,
		`CREATE TABLE IF NOT EXISTS metrics_samples (
			at timestamptz NOT NULL DEFAULT now(),
			slug text NOT NULL,
			db_size bigint,
			conns int,
			ram_used int,
			cpu_load real
		)`,
		`CREATE INDEX IF NOT EXISTS metrics_samples_slug_at ON metrics_samples(slug, at)`,
		// the retention DELETE filters on `at` alone; without this it seq-scans
		`CREATE INDEX IF NOT EXISTS metrics_samples_at ON metrics_samples(at)`,
		`CREATE TABLE IF NOT EXISTS db_imports (
			slug text PRIMARY KEY,
			source_enc bytea,
			status text NOT NULL DEFAULT 'done',
			message text DEFAULT '',
			sync_enabled boolean NOT NULL DEFAULT false,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS edge_functions (
			slug text NOT NULL, name text NOT NULL, code text NOT NULL,
			verify_jwt boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (slug, name)
		)`,
		// Existing installs: add the column. Default false so already-deployed
		// public functions keep working; new functions opt into JWT by default.
		`ALTER TABLE edge_functions ADD COLUMN IF NOT EXISTS verify_jwt boolean NOT NULL DEFAULT false`,
		`CREATE TABLE IF NOT EXISTS saved_queries (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			slug text NOT NULL, name text NOT NULL, sql text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS storage_buckets (
			slug text NOT NULL, bucket text NOT NULL,
			public boolean NOT NULL DEFAULT false,
			max_size_mb integer NOT NULL DEFAULT 0,
			allowed_mime text NOT NULL DEFAULT '',
			created_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (slug, bucket)
		)`,
		`ALTER TABLE storage_buckets ADD COLUMN IF NOT EXISTS max_size_mb integer NOT NULL DEFAULT 0`,
		`ALTER TABLE storage_buckets ADD COLUMN IF NOT EXISTS allowed_mime text NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS storage_objects (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			slug text NOT NULL, bucket text NOT NULL, path text NOT NULL,
			size bigint NOT NULL DEFAULT 0, mime text,
			created_at timestamptz NOT NULL DEFAULT now(),
			UNIQUE (slug, bucket, path)
		)`,
		`CREATE TABLE IF NOT EXISTS user_api_keys (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			owner_email text NOT NULL,
			name text NOT NULL,
			token_prefix text NOT NULL,
			token_hash text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			last_used timestamptz
		)`,
	}
	// Every statement is idempotent. Log-and-continue rather than abort so a
	// partially-migrated or externally-managed schema can't send pgforged into a
	// crash loop under Restart=always (a failed ALTER before this left half the
	// migration applied and no retry).
	for _, s := range stmts {
		if _, err := a.db.Exec(s); err != nil {
			log.Printf("ensureSchema: %v (statement skipped)", err)
		}
	}
	return nil
}

// ----------------------------------------------------------------- rate limit

// remoteHost returns the peer IP from RemoteAddr (no port).
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// fromTrustedProxy reports whether the request came from our local reverse
// proxy (Caddy on loopback). Only then may we believe its forwarded headers;
// otherwise a remote client could spoof X-Forwarded-For / X-Real-IP to bypass
// the login rate limiter, poison the audit log, and get arbitrary IPs banned.
func fromTrustedProxy(r *http.Request) bool {
	if ip := net.ParseIP(remoteHost(r)); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// clientIP returns the real client address, trusting proxy headers only from a
// local proxy. Caddy sets a single X-Real-IP; if only X-Forwarded-For is
// present, the LAST entry is the address Caddy actually observed (a client can
// prepend fakes, but cannot append past Caddy).
func clientIP(r *http.Request) string {
	if fromTrustedProxy(r) {
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return remoteHost(r)
}

// requestIsHTTPS reports whether the client reached us over TLS, so the session
// cookie can be marked Secure in production (behind Caddy) yet still work on a
// plain-HTTP dev/test box. Only a trusted local proxy may assert the scheme.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if fromTrustedProxy(r) {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}

func (a *app) rateLimited(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cut := time.Now().Add(-10 * time.Minute)
	keep := a.attempts[ip][:0]
	for _, t := range a.attempts[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(a.attempts, ip) // don't retain empty slices forever
	} else {
		a.attempts[ip] = keep
	}
	return len(keep) >= 8
}

// startRateLimitPruner periodically drops stale IP buckets so the map can't grow
// without bound (belt-and-braces with the delete above).
func (a *app) startRateLimitPruner() {
	go func() {
		for range time.Tick(15 * time.Minute) {
			cut := time.Now().Add(-10 * time.Minute)
			a.mu.Lock()
			for _, m := range []map[string][]time.Time{a.attempts, a.authAttempts} {
				for ip, ts := range m {
					fresh := false
					for _, t := range ts {
						if t.After(cut) {
							fresh = true
							break
						}
					}
					if !fresh {
						delete(m, ip)
					}
				}
			}
			a.mu.Unlock()
		}
	}()
}

func (a *app) recordAttempt(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attempts[ip] = append(a.attempts[ip], time.Now())
}

// authRateLimited throttles the unauthenticated end-user auth endpoints
// (/auth/v1/signup and /token) per IP. Each call runs bcrypt (~80 ms of CPU),
// so without a cap a single client could exhaust the host's CPU. The window is
// more generous than the panel login limiter since real apps sign users in.
func (a *app) authRateLimited(ip string) bool {
	const window = time.Minute
	const max = 30
	a.mu.Lock()
	defer a.mu.Unlock()
	cut := time.Now().Add(-window)
	keep := a.authAttempts[ip][:0]
	for _, t := range a.authAttempts[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	keep = append(keep, time.Now())
	a.authAttempts[ip] = keep
	return len(keep) > max
}

// ----------------------------------------------------------------- caddy

// tlsCheck lets Caddy issue certs on demand only for hostnames we know.
func (a *app) tlsCheck(w http.ResponseWriter, r *http.Request) {
	d := r.URL.Query().Get("domain")
	if d == a.cfg.domain || d == "db."+a.cfg.domain {
		w.WriteHeader(200)
		return
	}
	if strings.HasSuffix(d, "."+a.cfg.domain) {
		slug := strings.TrimSuffix(d, "."+a.cfg.domain)
		if a.projectExists(slug) {
			w.WriteHeader(200)
			return
		}
	}
	w.WriteHeader(404)
}

// ----------------------------------------------------------------- host stats

type hostStat struct {
	RAMUsed, RAMTotal, DiskFree, PGVersion string
	NProjects                              int
}

func (a *app) hostStats() hostStat {
	var s hostStat
	// count top-level projects only (branches show under their parent, matching
	// the Projects grid which hides branches)
	a.db.QueryRow(`SELECT count(*) FROM projects WHERE parent IS NULL`).Scan(&s.NProjects)
	a.db.QueryRow(`SHOW server_version`).Scan(&s.PGVersion)
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var tot, avail int
		for _, ln := range strings.Split(string(b), "\n") {
			f := strings.Fields(ln)
			if len(f) >= 2 {
				if f[0] == "MemTotal:" {
					tot, _ = strconv.Atoi(f[1])
				}
				if f[0] == "MemAvailable:" {
					avail, _ = strconv.Atoi(f[1])
				}
			}
		}
		s.RAMUsed = fmt.Sprintf("%d MB", (tot-avail)/1024)
		s.RAMTotal = fmt.Sprintf("%d MB", tot/1024)
	}
	if out, err := exec.Command("df", "-h", "--output=avail", "/").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		s.DiskFree = strings.TrimSpace(lines[len(lines)-1])
	}
	return s
}

// ----------------------------------------------------------------- helpers

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// audit records a security/activity event with the actor and source IP taken
// from the request. Use auditRaw when there is no session yet (login/register).
func (a *app) audit(r *http.Request, action, target string) {
	actor := currentUser(r)
	if actor == "" {
		actor = "-"
	}
	a.auditRaw(actor, clientIP(r), action, target)
}

func (a *app) auditRaw(actor, ip, action, target string) {
	a.db.Exec(`INSERT INTO audit_log(actor, ip, action, detail)
		VALUES ($1,$2,$3,jsonb_build_object('target',$4::text))`, actor, ip, action, target)
}

func (a *app) projectExists(slug string) bool {
	var ok bool
	a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM projects WHERE slug=$1)`, slug).Scan(&ok)
	return ok
}
