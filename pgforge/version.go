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
//
//	-X main.version=<git-short-sha> -X main.buildTime=<iso>
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

	// The update check makes a network call to GitHub, so only run it when the
	// user explicitly asks (the "Check for updates" button links to ?check=1).
	var upd updateInfo
	checked := r.URL.Query().Get("check") == "1"
	if checked {
		upd = a.updateStatus()
		refreshUpdateCache(upd)
	} else if info, _ := cachedUpdate(); info.Behind {
		// the background checker already knows an update exists - show it
		// without requiring a manual "Check for updates" click
		upd, checked = info, true
		if len(upd.Changelog) == 0 {
			// The tag was seen before the changelog file propagated (GitHub's
			// raw copy lags a push by a couple of minutes). Refetch live -
			// by the time someone looks at this page it is usually there.
			if fresh := a.updateStatus(); fresh.Behind {
				upd = fresh
				refreshUpdateCache(fresh)
			}
		}
	}

	// If an update ran (or is running), show the tail of its log so the operator
	// can watch progress and see the rollback result.
	updLog := ""
	if b, err := os.ReadFile("/opt/pgforge/update.log"); err == nil {
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		updLog = strings.Join(lines, "\n")
	}
	// An update is "running" only while its log is non-terminal AND recent, so a
	// wedged or abandoned update can't pin this page on "updating..." forever.
	updateRunning := updateInFlight()

	// watchdog alert files -> red banner (wal-prune.sh writes/clears these)
	var alerts []string
	if ents, err := os.ReadDir("/opt/pgforge/alerts"); err == nil {
		for _, e := range ents {
			if b, err := os.ReadFile("/opt/pgforge/alerts/" + e.Name()); err == nil {
				alerts = append(alerts, strings.TrimSpace(string(b)))
			}
		}
	}

	var incTitle, incNote string
	a.db.QueryRow(`SELECT title, note FROM incidents WHERE resolved_at IS NULL ORDER BY started_at DESC LIMIT 1`).Scan(&incTitle, &incNote)

	hook := a.discordHook()
	hookMasked := ""
	if hook != "" {
		hookMasked = "..." + hook[max(0, len(hook)-18):]
	}
	content := renderContent(systemBody, map[string]any{
		"Version": version, "BuildTime": buildTime, "Commit": repoURL + "/commit/" + version, "DiscordHook": hookMasked,
		"DBOK": dbOK, "PGVer": pgVer, "DBSize": dbSize, "ActiveAPIs": activeAPIs, "Svcs": svcs,
		"Stats": a.hostStats(), "AppVersion": appVersion, "IsOwner": a.atLeast(r, "owner"),
		"Checked": checked, "Upd": upd, "UpdateLog": updLog, "UpdateRunning": updateRunning,
		"Alerts": alerts, "ActiveIncident": incTitle, "ActiveIncidentNote": incNote, "StorageRemote": a.storageRemote(),
		"AutoUpd": a.settingOn("auto_update"), "Domain": a.cfg.domain, "StatusDomain": a.statusCustomDomain(), "SecondaryDomain": a.secondaryDomain(), "PanelRedirect": a.panelRedirectOn(), "StatusTitle": func() string {
			var v string
			a.db.QueryRow(`SELECT value FROM settings WHERE key='status_title'`).Scan(&v)
			return v
		}(),
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
