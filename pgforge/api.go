package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lib/pq"
)

// graphqlDepth returns the maximum selection-set nesting depth of a query, used
// to reject pathologically deep queries before they hit the database.
func graphqlDepth(q string) int {
	depth, max := 0, 0
	for _, c := range q {
		switch c {
		case '{':
			depth++
			if depth > max {
				max = depth
			}
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return max
}

// Data API = an auto REST API per project, served by a lazily-started PostgREST
// process. Public at https://<slug>.<domain>/rest/v1/... using per-project
// anon / service_role JWT keys. pgforged reverse-proxies to the local PostgREST.

type pgrst struct {
	port  int
	cmd   *exec.Cmd
	proxy *httputil.ReverseProxy
	// lastReq is the unix-nano time of the last proxied request. Atomic because
	// the hot path writes it without the pgrstMu lock the reaper reads under.
	lastReq atomic.Int64
}

// touchAndResume marks a project active on any request, and wakes it if it was
// sleeping ("scale to zero" -> start on request). One statement on the hot
// path: the row updates only when waking or when the 2-minute last_active
// throttle has elapsed, and RETURNING the pre-update status tells us whether
// this request was the wake-up. Paused projects never auto-resume.
func (a *app) touchAndResume(slug string) {
	var old string
	err := a.db.QueryRow(`UPDATE projects p SET
			status = CASE WHEN o.st = 'suspended' THEN 'active' ELSE p.status END,
			last_active = now()
		FROM (SELECT status AS st FROM projects WHERE slug=$1 FOR UPDATE) o
		WHERE p.slug = $1 AND (o.st = 'suspended' OR p.last_active < now()-interval '2 minutes')
		RETURNING o.st`, slug).Scan(&old)
	if err != nil || old != "suspended" {
		return // fast path: nothing to do, or just a routine activity bump
	}
	// Waking. Older versions hard-suspended with NOLOGIN - clear it so those
	// rows come back too (soft-sleep never sets it).
	a.db.Exec(fmt.Sprintf(`ALTER ROLE %s LOGIN`, pq.QuoteIdentifier(slug)))
	a.auditRaw("system", "-", "auto-resume", slug)
	if a.hasWebhooks(slug) {
		a.rtGetHub(slug) // restart webhook delivery
	}
}

// reapPostgREST stops Data API processes with no requests for `idle`, freeing
// their RAM. They start again automatically on the next request.
func (a *app) reapPostgREST(idle time.Duration, pinned map[string]bool) {
	pgrstMu.Lock()
	defer pgrstMu.Unlock()
	for slug, p := range pgrstProc {
		if pinned[slug] {
			continue // kept warm - no cold starts for pinned projects
		}
		if time.Since(time.Unix(0, p.lastReq.Load())) > idle {
			killAndReap(p.cmd)
			delete(pgrstProc, slug)
			freePgrstPort(p.port)
		}
	}
}

// killAndReap kills a process AND waits for it, so it doesn't linger as a zombie
// (every un-Wait()ed child stays in the process table until the parent exits).
func killAndReap(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	cmd.Process.Kill()
	go func() { _ = cmd.Wait() }()
}

var (
	pgrstMu   sync.Mutex
	pgrstProc = map[string]*pgrst{}
	pgrstNext = 3001
	pgrstFree []int // ports released by dead PostgRESTs, reused before pgrstNext
)

// allocPgrstPort returns a port for a new PostgREST. Freed ports are recycled
// first; the counter path skips any port still held by a live entry, so a wrap
// after long uptime can never hand out a port another project is serving on
// (which previously let a readiness probe succeed against the WRONG project's
// PostgREST and cache a cross-tenant proxy). Caller must hold pgrstMu.
func allocPgrstPort() int {
	if n := len(pgrstFree); n > 0 {
		p := pgrstFree[n-1]
		pgrstFree = pgrstFree[:n-1]
		return p
	}
	for {
		if pgrstNext > 65000 {
			pgrstNext = 3001
		}
		port := pgrstNext
		pgrstNext++
		inUse := false
		for _, p := range pgrstProc {
			if p.port == port {
				inUse = true
				break
			}
		}
		if !inUse {
			return port
		}
	}
}

// freePgrstPort returns a port to the reuse pool. Caller must hold pgrstMu.
func freePgrstPort(port int) { pgrstFree = append(pgrstFree, port) }

// apiConfig returns (secret, enabled) for a project's Data API.
func (a *app) apiConfig(slug string) (string, bool) {
	var secret string
	var enabled bool
	a.db.QueryRow(`SELECT jwt_secret, enabled FROM api_config WHERE slug=$1`, slug).Scan(&secret, &enabled)
	return secret, enabled
}

func (a *app) apiKeys(slug string) (anon, service, secret string, enabled bool) {
	secret, enabled = a.apiConfig(slug)
	if secret == "" {
		return "", "", "", false
	}
	return signJWT([]byte(secret), "anon"), signJWT([]byte(secret), "service_role"), secret, enabled
}

// enableAPI creates the anon/service_role roles + grants in the project DB,
// generates a JWT secret, and marks the Data API enabled.
func (a *app) enableAPI(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/api", err.Error())
		return
	}
	if err := a.ensureAPIRoles(db, slug); err != nil {
		redirectErr(w, r, "/p/"+slug+"/api", "Role setup failed: "+err.Error())
		return
	}
	secret := randHex(32)
	a.db.Exec(`INSERT INTO api_config(slug, jwt_secret, enabled) VALUES ($1,$2,true)
		ON CONFLICT (slug) DO UPDATE SET enabled=true`, slug, secret)
	a.audit(r, "api-enable", slug)
	redirectMsg(w, r, "/p/"+slug+"/api", "Data API enabled.")
}

func (a *app) disableAPI(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a.db.Exec(`UPDATE api_config SET enabled=false WHERE slug=$1`, slug)
	a.stopPostgREST(slug)
	a.audit(r, "api-disable", slug)
	redirectMsg(w, r, "/p/"+slug+"/api", "Data API disabled.")
}

func (a *app) ensureAPIRoles(db *sql.DB, owner string) error {
	stmts := []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='anon') THEN CREATE ROLE anon NOLOGIN NOINHERIT; END IF; END $$`,
		`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='service_role') THEN CREATE ROLE service_role NOLOGIN NOINHERIT BYPASSRLS; END IF; END $$`,
		`GRANT USAGE ON SCHEMA public TO anon, service_role`,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO anon`,
		`GRANT ALL ON ALL TABLES IN SCHEMA public TO service_role`,
		`GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO service_role`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO anon`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO service_role`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO service_role`,
		// the project owner acts as PostgREST's authenticator and must be able to switch roles
		fmt.Sprintf(`GRANT anon, service_role TO %s`, pq.QuoteIdentifier(owner)),
		// GraphQL (pg_graphql) - one endpoint reflecting the whole schema
		`CREATE EXTENSION IF NOT EXISTS pg_graphql`,
		`GRANT USAGE ON SCHEMA graphql TO anon, service_role`,
		`GRANT ALL ON ALL FUNCTIONS IN SCHEMA graphql TO anon, service_role`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return a.ensureAuthHelpers(db)
}

// authHelperStmts install the auth.* helpers (auth.uid/jwt/role/email) that RLS
// policies reference. PostgREST (and serveGraphQL) set request.jwt.claims from
// the verified JWT, so these read the signed-in user's id/role/email inside SQL:
//
//	create policy "own rows" on t using (auth.uid() = user_id);
var authHelperStmts = []string{
	`CREATE SCHEMA IF NOT EXISTS auth`,
	`CREATE OR REPLACE FUNCTION auth.jwt() RETURNS jsonb LANGUAGE sql STABLE AS $$ SELECT coalesce(nullif(current_setting('request.jwt.claims', true), ''), '{}')::jsonb $$`,
	`CREATE OR REPLACE FUNCTION auth.uid() RETURNS uuid LANGUAGE sql STABLE AS $$ SELECT nullif(auth.jwt()->>'sub','')::uuid $$`,
	`CREATE OR REPLACE FUNCTION auth.role() RETURNS text LANGUAGE sql STABLE AS $$ SELECT auth.jwt()->>'role' $$`,
	`CREATE OR REPLACE FUNCTION auth.email() RETURNS text LANGUAGE sql STABLE AS $$ SELECT auth.jwt()->>'email' $$`,
	`GRANT USAGE ON SCHEMA auth TO public`,
	`GRANT EXECUTE ON FUNCTION auth.jwt(), auth.uid(), auth.role(), auth.email() TO public`,
}

func (a *app) ensureAuthHelpers(db *sql.DB) error {
	for _, s := range authHelperStmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// reloadPostgRESTSchema tells a project's PostgREST to refresh its schema cache
// (default channel "pgrst") so a table/column created seconds ago is queryable
// over REST immediately instead of 404-ing until the process is restarted.
func (a *app) reloadPostgRESTSchema(slug string) {
	if db, err := a.dbFor(slug); err == nil {
		db.Exec(`NOTIFY pgrst, 'reload schema'`)
	}
}

// ensurePostgREST starts (or returns) the PostgREST process for a project. The
// global lock is held only long enough to claim the slot; the (up to 6s)
// readiness wait happens WITHOUT the lock, so one project's cold start no longer
// blocks every other project's API request or the reaper.
func (a *app) ensurePostgREST(slug string) (*pgrst, error) {
	pgrstMu.Lock()
	if p, ok := pgrstProc[slug]; ok {
		pgrstMu.Unlock()
		return p, nil
	}
	secret, enabled := a.apiConfig(slug)
	if !enabled || secret == "" {
		pgrstMu.Unlock()
		return nil, fmt.Errorf("data api not enabled")
	}
	_, pw := a.projectCred(slug)
	port := allocPgrstPort()
	dbURI := fmt.Sprintf("postgres://%s:%s@127.0.0.1:5432/%s?sslmode=require&application_name=pgforge-rest",
		url.QueryEscape(slug), url.QueryEscape(pw), url.QueryEscape(slug))
	cmd := exec.Command("/usr/local/bin/postgrest")
	cmd.Env = append([]string{},
		"PGRST_DB_URI="+dbURI,
		"PGRST_DB_SCHEMAS=public",
		"PGRST_DB_ANON_ROLE=anon",
		"PGRST_JWT_SECRET="+secret,
		fmt.Sprintf("PGRST_SERVER_PORT=%d", port),
		"PGRST_SERVER_HOST=127.0.0.1",
		"PGRST_DB_POOL=2",
		"PGRST_LOG_LEVEL=error",
	)
	if err := cmd.Start(); err != nil {
		pgrstMu.Unlock()
		return nil, err
	}
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"message":"data api is starting or unavailable, retry shortly"}`)
	}
	p := &pgrst{port: port, cmd: cmd, proxy: proxy}
	p.lastReq.Store(time.Now().UnixNano())
	pgrstProc[slug] = p
	pgrstMu.Unlock() // release BEFORE the readiness wait

	// Readiness must prove IDENTITY, not just a listening socket: probe with
	// this project's own anon JWT and require 200. A stale PostgREST from a
	// different project on the same port rejects the token (401, different
	// secret); one still connecting to its DB returns 503. Both keep retrying,
	// so we can never cache a proxy pointing at another tenant's instance.
	probe, _ := http.NewRequest("GET", target.String()+"/", nil)
	probe.Header.Set("Authorization", "Bearer "+signJWT([]byte(secret), "anon"))
	client := &http.Client{Timeout: time.Second}
	ready := false
	for i := 0; i < 40; i++ {
		if resp, err := client.Do(probe); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !ready {
		// It never came up (bad port, wrong password, DB down). Don't cache a
		// dead process - kill+reap it and drop the entry so the next request
		// starts fresh instead of 502-ing forever.
		pgrstMu.Lock()
		if cur, ok := pgrstProc[slug]; ok && cur == p {
			killAndReap(p.cmd)
			delete(pgrstProc, slug)
			freePgrstPort(p.port)
		}
		pgrstMu.Unlock()
		return nil, fmt.Errorf("data api failed to start")
	}
	return p, nil
}

func (a *app) stopPostgREST(slug string) {
	pgrstMu.Lock()
	defer pgrstMu.Unlock()
	if p, ok := pgrstProc[slug]; ok {
		killAndReap(p.cmd)
		delete(pgrstProc, slug)
		freePgrstPort(p.port)
	}
}

// roleForKey returns the Postgres role implied by an apikey/JWT (default anon).
func roleForKey(secret []byte, token string) string {
	if claims, ok := verifyUserJWT(secret, token); ok {
		if rl, ok := claims["role"].(string); ok {
			switch rl {
			case "anon", "authenticated", "service_role":
				return rl
			}
		}
	}
	return "anon"
}

// serveGraphQL runs a GraphQL query through pg_graphql under the caller's role.
func (a *app) serveGraphQL(w http.ResponseWriter, r *http.Request, slug string) {
	secret, enabled := a.apiConfig(slug)
	if !enabled || secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "data api not enabled"})
		return
	}
	var body struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Query == "" {
		writeJSON(w, 400, map[string]any{"errors": []map[string]string{{"message": "missing query"}}})
		return
	}
	// Reject oversized / pathologically deep queries before touching the DB.
	if len(body.Query) > 50000 || graphqlDepth(body.Query) > 15 {
		writeJSON(w, 400, map[string]any{"errors": []map[string]string{{"message": "query too large or too deeply nested"}}})
		return
	}
	key := r.Header.Get("apikey")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	// Resolve BOTH the role and the full claims, so RLS policies using
	// auth.uid()/auth.jwt() see the signed-in user through GraphQL, not just the
	// coarse role (previously GraphQL dropped the user claims).
	claims, _ := verifyUserJWT([]byte(secret), key)
	role := "anon"
	if rl, ok := claims["role"].(string); ok {
		switch rl {
		case "anon", "authenticated", "service_role":
			role = rl
		}
	}
	claimsJSON := "{}"
	if len(claims) > 0 {
		if b, e := json.Marshal(claims); e == nil {
			claimsJSON = string(b)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	db, err := a.dbFor(slug)
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": err.Error()})
		return
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": err.Error()})
		return
	}
	defer tx.Rollback()
	tx.ExecContext(ctx, "SET LOCAL statement_timeout = 30000")
	tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL ROLE %s", pq.QuoteIdentifier(role)))
	tx.ExecContext(ctx, "SELECT set_config('request.jwt.claims', $1, true)", claimsJSON)
	vars := string(body.Variables)
	if vars == "" || vars == "null" {
		vars = "{}"
	}
	var out string
	if err := tx.QueryRowContext(ctx, `SELECT graphql.resolve($1, $2::jsonb)::text`, body.Query, vars).Scan(&out); err != nil {
		writeJSON(w, 400, map[string]any{"errors": []map[string]string{{"message": err.Error()}}})
		return
	}
	tx.Commit()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(out))
}

// serveAPI handles requests to <slug>.<domain> (public, no panel session).
func (a *app) serveAPI(w http.ResponseWriter, r *http.Request, slug string) {
	a.touchAndResume(slug) // any request keeps the project alive / wakes it
	if strings.HasPrefix(r.URL.Path, "/graphql/v1") {
		a.serveGraphQL(w, r, slug)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/storage/v1/object/") {
		a.serveStorage(w, r, slug)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/auth/v1") {
		a.serveAuth(w, r, slug)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/realtime/v1") {
		a.serveRealtime(w, r, slug)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/functions/v1/") {
		a.serveFunction(w, r, slug)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/rest/v1") {
		p, err := a.ensurePostgREST(slug)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"message":"%s"}`, err.Error())
			return
		}
		// Compatibility: many clients send the key in an `apikey` header.
		// Raw PostgREST only reads Authorization: Bearer, so map it across.
		if r.Header.Get("Authorization") == "" {
			if k := r.Header.Get("apikey"); k != "" {
				r.Header.Set("Authorization", "Bearer "+k)
			}
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/rest/v1")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		p.lastReq.Store(time.Now().UnixNano())
		p.proxy.ServeHTTP(w, r)
		return
	}
	// landing: tiny JSON describing the project API
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"name":    slug,
		"rest":    "/rest/v1",
		"docs":    "https://" + a.cfg.domain + "/p/" + slug + "/api",
		"powered": "ForgeBase",
	})
}

// ----------------------------------------------------------------- page

func (a *app) apiPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	anon, service, _, enabled := a.apiKeys(slug)
	base := "https://" + slug + "." + a.cfg.domain + "/rest/v1"
	var tables []string
	var rls []rlsTable
	if enabled {
		if db, err := a.dbFor(slug); err == nil {
			tables = a.listTables(db)
		}
		rls = a.rlsData(slug)
	}
	content := renderContent(apiBody, map[string]any{
		"Slug": slug, "Enabled": enabled, "Base": base,
		"Anon": anon, "Service": service, "Domain": a.cfg.domain, "Tables": tables,
		"GraphQL": "https://" + slug + "." + a.cfg.domain + "/graphql/v1",
		"RLS":     rls, "CanAdmin": a.atLeast(r, "admin"),
	})
	a.renderShell(w, r, shellData{Title: slug + " · Data API", Nav: "api", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Data API"}}}, content)
}
