package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// version + buildTime are injected at build time via -ldflags:
//   -X main.version=<git-short-sha> -X main.buildTime=<iso>
var (
	version   = "dev"
	buildTime = "unknown"
)

const repoURL = "https://github.com/FutureForge-Studios/forgebase"

// systemPage shows the running version (git commit), service + DB health, and
// the resilience model. Global (platform-level) page.
func (a *app) systemPage(w http.ResponseWriter, r *http.Request) {
	// One timeout-bounded `docker inspect` per container (was two calls each, no
	// timeout - a wedged docker daemon could hang the page and F5 forked ~8
	// processes per render).
	dockerState := func(name string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Status}}", name).Output()
		if err != nil {
			return "unknown"
		}
		return strings.TrimSpace(string(out))
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	dbOK := a.db.Ping() == nil
	var pgVer, dbSize string
	a.db.QueryRow(`SHOW server_version`).Scan(&pgVer)
	a.db.QueryRow(`SELECT pg_size_pretty(sum(pg_database_size(datname))) FROM pg_database WHERE NOT datistemplate`).Scan(&dbSize)

	pgrstMu.Lock()
	activeAPIs := len(pgrstProc)
	pgrstMu.Unlock()

	dbS, pbS, cyS := dockerState("pgforge-db"), dockerState("pgforge-pgbouncer"), dockerState("pgforge-caddy")
	type svc struct {
		Name, State string
		OK          bool
	}
	svcs := []svc{
		{"pgforged (control plane)", "running", true},
		{"Postgres", dbS, dbS == "running"},
		{"PgBouncer (pooler)", pbS, pbS == "running"},
		{"Caddy (HTTPS)", cyS, cyS == "running"},
		{"PostgREST (Data API)", boolStr(exists("/usr/local/bin/postgrest"), "installed", "missing"), exists("/usr/local/bin/postgrest")},
		{"Deno (Edge Functions)", boolStr(exists("/usr/local/bin/deno"), "installed", "missing"), exists("/usr/local/bin/deno")},
	}

	content := renderContent(systemBody, map[string]any{
		"Version": version, "BuildTime": buildTime, "Commit": repoURL + "/commit/" + version,
		"DBOK": dbOK, "PGVer": pgVer, "DBSize": dbSize, "ActiveAPIs": activeAPIs, "Svcs": svcs,
		"Stats": a.hostStats(),
	})
	a.renderShell(w, r, shellData{Title: "System", Nav: "system",
		Crumbs: []crumb{{Label: "System"}}}, content)
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
