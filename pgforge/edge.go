package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Edge Functions: per-project Deno functions invoked at
// <slug>.<domain>/functions/v1/<name>. Code is stored in the meta DB and
// mirrored to /opt/pgforge-functions/<slug>/<name>.ts; pgforged runs it through
// edge-runner.ts on each invoke (Deno sandbox: net + env only).

const funcRoot = "/opt/pgforge-functions"
const edgeRunner = "/opt/pgforge/edge-runner.ts"

var funcNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,40}$`)

// edgeSlots bounds concurrent Deno invocations platform-wide (default 4,
// EDGE_MAX_CONCURRENCY overrides). Combined with the per-invocation 128MB V8
// heap cap this bounds worst-case edge RAM at roughly slots x ~200MB.
var edgeSlots = func() chan struct{} {
	n := 4
	if v := os.Getenv("EDGE_MAX_CONCURRENCY"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 && i <= 64 {
			n = i
		}
	}
	return make(chan struct{}, n)
}()

// Per-project slots (half the global cap, min 1) stop one busy or hung tenant
// from monopolizing the whole pool - a noisy neighbor now saturates only its
// own project's functions while everyone else keeps 2+ slots available.
var (
	edgeSlugMu    sync.Mutex
	edgeSlugSlots = map[string]chan struct{}{}
)

func edgeSlotFor(slug string) chan struct{} {
	edgeSlugMu.Lock()
	defer edgeSlugMu.Unlock()
	c, ok := edgeSlugSlots[slug]
	if !ok {
		n := cap(edgeSlots) / 2
		if n < 1 {
			n = 1
		}
		c = make(chan struct{}, n)
		edgeSlugSlots[slug] = c
	}
	return c
}

const defaultFunc = `export default async (req: Request): Promise<Response> => {
  const url = new URL(req.url);
  const name = url.searchParams.get("name") ?? "world";
  return new Response(JSON.stringify({ hello: name }), {
    headers: { "content-type": "application/json" },
  });
};`

func (a *app) edgePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	type fn struct {
		Name, Created string
		VerifyJWT     bool
	}
	var fns []fn
	rows, _ := a.db.Query(`SELECT name, to_char(created_at,'Mon DD, YYYY'), verify_jwt FROM edge_functions WHERE slug=$1 ORDER BY name`, slug)
	if rows != nil {
		for rows.Next() {
			var f fn
			rows.Scan(&f.Name, &f.Created, &f.VerifyJWT)
			fns = append(fns, f)
		}
		rows.Close()
	}
	edit := r.URL.Query().Get("fn")
	code := defaultFunc
	verifyJWT := true // new functions require a JWT by default
	if edit != "" {
		a.db.QueryRow(`SELECT code, verify_jwt FROM edge_functions WHERE slug=$1 AND name=$2`, slug, edit).Scan(&code, &verifyJWT)
	}
	var secrets []string
	if rows, _ := a.db.Query(`SELECT name FROM edge_secrets WHERE slug=$1 ORDER BY name`, slug); rows != nil {
		for rows.Next() {
			var n string
			rows.Scan(&n)
			secrets = append(secrets, n)
		}
		rows.Close()
	}
	type elog struct{ Name, At, Error string }
	var logs []elog
	if rows, _ := a.db.Query(`SELECT name, to_char(at,'Mon DD HH24:MI'), coalesce(error,'') FROM edge_logs WHERE slug=$1 ORDER BY at DESC LIMIT 20`, slug); rows != nil {
		for rows.Next() {
			var l elog
			rows.Scan(&l.Name, &l.At, &l.Error)
			logs = append(logs, l)
		}
		rows.Close()
	}
	content := renderContent(edgeBody, map[string]any{
		"Slug": slug, "Fns": fns, "Edit": edit, "Code": code, "Secrets": secrets, "Logs": logs,
		"VerifyJWT": verifyJWT,
		"Base":      "https://" + slug + "." + a.cfg.domain + "/functions/v1",
	})
	a.renderShell(w, r, shellData{Title: slug + " · Functions", Nav: "edge", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Edge Functions"}}}, content)
}

func (a *app) saveFunction(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	code := r.FormValue("code")
	verifyJWT := r.FormValue("verify_jwt") == "on"
	if !funcNameRe.MatchString(name) {
		redirectErr(w, r, "/p/"+slug+"/functions", "Function name: 2-41 chars, a-z 0-9 _ -.")
		return
	}
	if strings.TrimSpace(code) == "" {
		redirectErr(w, r, "/p/"+slug+"/functions", "Function code is empty.")
		return
	}
	dir := filepath.Join(funcRoot, slug)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, name+".ts"), []byte(code), 0o644); err != nil {
		redirectErr(w, r, "/p/"+slug+"/functions", "Could not write function: "+err.Error())
		return
	}
	a.db.Exec(`INSERT INTO edge_functions(slug,name,code,verify_jwt) VALUES ($1,$2,$3,$4)
		ON CONFLICT (slug,name) DO UPDATE SET code=$3, verify_jwt=$4`, slug, name, code, verifyJWT)
	a.audit(r, "function-save", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/functions?fn="+name, "Function "+name+" deployed.")
}

var edgeSecretRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,60}$`)

func (a *app) addEdgeSecret(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := strings.ToUpper(strings.TrimSpace(r.FormValue("name")))
	if !edgeSecretRe.MatchString(name) || strings.HasPrefix(name, "FORGEBASE_") {
		redirectErr(w, r, "/p/"+slug+"/functions", "Secret name: A-Z 0-9 _ (not FORGEBASE_*).")
		return
	}
	a.db.Exec(`INSERT INTO edge_secrets(slug,name,value) VALUES ($1,$2,$3)
		ON CONFLICT (slug,name) DO UPDATE SET value=$3`, slug, name, r.FormValue("value"))
	a.audit(r, "edge-secret-set", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/functions", "Secret "+name+" saved.")
}

func (a *app) deleteEdgeSecret(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.FormValue("name")
	a.db.Exec(`DELETE FROM edge_secrets WHERE slug=$1 AND name=$2`, slug, name)
	a.audit(r, "edge-secret-del", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/functions", "Secret "+name+" deleted.")
}

func (a *app) deleteFunction(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.FormValue("name")
	os.Remove(filepath.Join(funcRoot, slug, name+".ts"))
	a.db.Exec(`DELETE FROM edge_functions WHERE slug=$1 AND name=$2`, slug, name)
	a.audit(r, "function-delete", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/functions", "Function "+name+" deleted.")
}

// serveFunction runs a function for a public request on the project subdomain.
func (a *app) serveFunction(w http.ResponseWriter, r *http.Request, slug string) {
	rest := strings.TrimPrefix(r.URL.Path, "/functions/v1/")
	name := rest
	subpath := "/"
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		name = rest[:i]
		subpath = rest[i:]
	}
	if !funcNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	file := filepath.Join(funcRoot, slug, name+".ts")
	if _, err := os.Stat(file); err != nil {
		http.Error(w, `{"message":"function not found"}`, http.StatusNotFound)
		return
	}
	// If this function requires a JWT, verify one before running it. This is the
	// safe default for new functions: without it, a public function that uses the
	// injected service-role key is reachable by anonymous callers.
	var verifyJWT bool
	a.db.QueryRow(`SELECT verify_jwt FROM edge_functions WHERE slug=$1 AND name=$2`, slug, name).Scan(&verifyJWT)
	if verifyJWT {
		secret, _ := a.apiConfig(slug)
		key := r.Header.Get("apikey")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if _, ok := verifyUserJWT([]byte(secret), key); secret == "" || !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"missing or invalid JWT"}`))
			return
		}
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	hdr := map[string]string{}
	for k := range r.Header {
		hdr[strings.ToLower(k)] = r.Header.Get(k)
	}
	rq := subpath
	if r.URL.RawQuery != "" {
		rq += "?" + r.URL.RawQuery
	}
	input, _ := json.Marshal(map[string]any{
		"method": r.Method, "url": rq, "headers": hdr, "body": string(body),
	})

	// Concurrency caps: each invocation is a fresh Deno process (~40-80MB), so
	// unbounded parallel invocations were the box's most likely OOM trigger.
	// The PER-PROJECT slot is taken first (a hung tenant saturates only its
	// own functions), then the platform-wide slot. Callers wait up to 5s, then
	// get a clean 429 retry signal.
	slugSlot := edgeSlotFor(slug)
	select {
	case slugSlot <- struct{}{}:
		defer func() { <-slugSlot }()
	case <-time.After(5 * time.Second):
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message":"this project's function capacity is busy, retry shortly"}`))
		return
	case <-r.Context().Done():
		return
	}
	select {
	case edgeSlots <- struct{}{}:
		defer func() { <-edgeSlots }()
	case <-time.After(5 * time.Second):
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message":"function capacity busy, retry shortly"}`))
		return
	case <-r.Context().Done():
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// Scoped env: NEVER inherit os.Environ() (it holds PANEL_PASS, SESSION_SECRET
	// and the control-plane DSN). The function gets only its project context, the
	// project's own API keys (so it can call its own REST/Auth with the
	// injected FORGEBASE_URL/ANON_KEY), and its configured secrets - and
	// --allow-env is restricted to exactly those names.
	anon, service, _, _ := a.apiKeys(slug)
	env := []string{
		"DENO_DIR=/opt/pgforge/deno-cache", // bounded location (backup.sh prunes >500MB)
		"FORGEBASE_PROJECT=" + slug,
		"FORGEBASE_URL=https://" + slug + "." + a.cfg.domain,
		"FORGEBASE_ANON_KEY=" + anon,
		"FORGEBASE_SERVICE_KEY=" + service,
	}
	allow := "FORGEBASE_PROJECT,FORGEBASE_URL,FORGEBASE_ANON_KEY,FORGEBASE_SERVICE_KEY"
	if rows, err := a.db.Query(`SELECT name, value FROM edge_secrets WHERE slug=$1`, slug); err == nil {
		for rows.Next() {
			var k, v string
			rows.Scan(&k, &v)
			env = append(env, k+"="+v)
			allow += "," + k
		}
		rows.Close()
	}
	cmd := exec.CommandContext(ctx, "/usr/local/bin/deno", "run", "--quiet",
		"--v8-flags=--max-old-space-size=128", // cap each invocation's JS heap
		"--allow-net", "--allow-env="+allow, "--allow-read="+funcRoot, edgeRunner, file)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		a.db.Exec(`INSERT INTO edge_logs(slug, name, error) VALUES ($1,$2,$3)`,
			slug, name, safeTail(strings.TrimSpace(stderr.String()), 2000))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		w.Write([]byte(`{"message":"function error or timeout"}`))
		return
	}
	var res struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if json.Unmarshal(out, &res) != nil {
		w.WriteHeader(502)
		w.Write([]byte(`{"message":"bad function response"}`))
		return
	}
	for k, v := range res.Headers {
		w.Header().Set(k, v)
	}
	if res.Status == 0 {
		res.Status = 200
	}
	w.WriteHeader(res.Status)
	w.Write([]byte(res.Body))
}
