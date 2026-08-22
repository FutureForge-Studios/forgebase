package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// In-app self-update (Coolify-style): check GitHub for a newer release, show the
// human-readable changelog for it, and apply it with one click - rebuild + atomic
// binary swap + restart + health-check + automatic rollback. Admin only.

const updateOwner = "FutureForge-Studios"
const updateRepo = "forgebase"

// updSection is one release worth of human-readable notes shown in the panel.
type updSection struct {
	Version string
	Items   []string
}

type updateInfo struct {
	Current   string       // running release, e.g. "1.0.3"
	Latest    string       // newest released version on GitHub
	Behind    bool         // a newer release exists
	Changelog []updSection // release notes between Current and Latest, newest first
	Err       string
}

// updateStatus determines the newest released version and its notes. The
// SOURCE OF TRUTH for "what is the latest version" is the GitHub Tags API -
// it reflects a new tag within seconds. CHANGELOG.md (raw, cache-busted) is
// fetched for the human-readable notes; raw can lag a push by a couple of
// minutes, so when the API knows a newer tag than the changelog does, we still
// report the update and show whatever notes are available.
func (a *app) updateStatus() updateInfo {
	info := updateInfo{Current: appVersion}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 1) real-time latest from tags
	tagLatest := ""
	tagURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/tags?per_page=15", updateOwner, updateRepo)
	if req, _ := http.NewRequestWithContext(ctx, "GET", tagURL, nil); req != nil {
		req.Header.Set("Accept", "application/vnd.github+json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				var tags []struct {
					Name string `json:"name"`
				}
				if json.Unmarshal(body, &tags) == nil {
					for _, t := range tags {
						v := strings.TrimPrefix(t.Name, "v")
						if semverRe.MatchString(v) && (tagLatest == "" || semverLess(tagLatest, v)) {
							tagLatest = v
						}
					}
				}
			}
		}
	}

	// 2) notes (and fallback latest) from the changelog
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/CHANGELOG.md?cb=%d", updateOwner, updateRepo, time.Now().Unix())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if tagLatest == "" {
			info.Err = err.Error()
			return info
		}
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != 200 && tagLatest == "" {
			info.Err = fmt.Sprintf("could not fetch the changelog (%d)", resp.StatusCode)
			return info
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		clLatest, sections := parseChangelog(string(body), appVersion)
		info.Latest = clLatest
		info.Changelog = sections
	}
	// the tags API wins when it knows a newer version than the changelog copy
	if tagLatest != "" && (info.Latest == "" || semverLess(info.Latest, tagLatest)) {
		info.Latest = tagLatest
	}
	info.Behind = info.Latest != "" && semverLess(appVersion, info.Latest)
	if !info.Behind {
		info.Changelog = nil
	}
	return info
}

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// parseChangelog reads a Keep-a-Changelog document and returns the newest released
// version plus the notes for every release strictly newer than `current` (newest
// first). Wrapped bullet lines are re-joined so each item reads as one sentence.
func parseChangelog(md, current string) (latest string, out []updSection) {
	var cur *updSection
	include := false
	flush := func() {
		if cur != nil && len(cur.Items) > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, ln := range strings.Split(md, "\n") {
		if strings.HasPrefix(ln, "## [") {
			flush()
			ver := ln[strings.Index(ln, "[")+1:]
			if i := strings.Index(ver, "]"); i >= 0 {
				ver = ver[:i]
			}
			if ver == "Unreleased" {
				include = false
				continue
			}
			if latest == "" {
				latest = ver
			}
			include = semverLess(current, ver)
			if include {
				cur = &updSection{Version: ver}
			}
			continue
		}
		if !include || cur == nil {
			continue
		}
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "- "):
			cur.Items = append(cur.Items, strings.TrimSpace(t[2:]))
		case t != "" && !strings.HasPrefix(t, "#") && len(cur.Items) > 0:
			cur.Items[len(cur.Items)-1] += " " + t // continuation of a wrapped bullet
		}
	}
	flush()
	return latest, out
}

// semverLess reports whether version a is older than b (major.minor.patch).
func semverLess(a, b string) bool {
	pa, pb := parseSemver(a), parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parseSemver(s string) [3]int {
	var v [3]int
	for i, p := range strings.SplitN(strings.TrimSpace(s), ".", 3) {
		if i > 2 {
			break
		}
		fmt.Sscanf(strings.TrimSpace(p), "%d", &v[i])
	}
	return v
}

// updateInFlight reports whether a self-update is actually running right now. It
// is true only when the update log exists, has NOT reached a terminal line
// (OK / rolled back / failed), AND was written recently. The recency check is
// what makes this safe: without it, an updater that wedges or never launches
// leaves a permanently non-terminal log, which would pin the System page on
// "updating..." forever and block every future update as "already in progress".
// A real update writes its log continuously and finishes well within the window.
func updateInFlight() bool {
	fi, err := os.Stat("/opt/pgforge/update.log")
	if err != nil {
		return false
	}
	if time.Since(fi.ModTime()) > 10*time.Minute {
		return false // stale: treat as finished/abandoned, not running
	}
	b, err := os.ReadFile("/opt/pgforge/update.log")
	if err != nil {
		return false
	}
	s := string(b)
	return !strings.Contains(s, "OK: updated") &&
		!strings.Contains(s, "rolled back") &&
		!strings.Contains(s, "failed")
}

// Background update awareness: the panel should TELL the operator an update
// exists without them clicking "Check". A cached check runs shortly after boot
// and every 6 hours; the sidebar shows a dot on System while behind, Discord
// gets one ping per new version, and - only when the operator opts in via the
// auto_update setting - new releases install themselves in the 03:00-05:00 UTC
// quiet window.
var updCheck struct {
	mu       sync.Mutex
	info     updateInfo
	at       time.Time
	notified string // last version announced to Discord (once per version)
}

func (a *app) startUpdateChecker() {
	go func() {
		time.Sleep(30 * time.Second) // let boot settle first
		for {
			info := a.updateStatus()
			updCheck.mu.Lock()
			updCheck.info, updCheck.at = info, time.Now()
			announce := info.Behind && updCheck.notified != info.Latest
			if announce {
				updCheck.notified = info.Latest
			}
			updCheck.mu.Unlock()
			if announce {
				go a.notifyDiscord(fmt.Sprintf("⬆️ ForgeBase v%s is available (running v%s). Install it from the System page.", info.Latest, info.Current))
			}
			if info.Behind && a.settingOn("auto_update") && !updateInFlight() {
				if h := time.Now().UTC().Hour(); h >= 3 && h < 5 {
					a.auditRaw("system", "-", "auto-update", info.Latest)
					go a.notifyDiscord("🔄 Auto-installing ForgeBase v" + info.Latest + " (auto-update is on)...")
					a.launchUpdate()
				}
			}
			time.Sleep(6 * time.Hour)
		}
	}()
}

// refreshUpdateCache stores a manual check's result so the sidebar dot and the
// cached System-page state stay in sync with what the operator just saw.
func refreshUpdateCache(info updateInfo) {
	updCheck.mu.Lock()
	updCheck.info, updCheck.at = info, time.Now()
	updCheck.mu.Unlock()
}

// cachedUpdate returns the last background check result (zero value if none yet).
func cachedUpdate() (updateInfo, time.Time) {
	updCheck.mu.Lock()
	defer updCheck.mu.Unlock()
	return updCheck.info, updCheck.at
}

// updateAvail is the cheap sidebar-badge check.
func updateAvail() bool {
	updCheck.mu.Lock()
	defer updCheck.mu.Unlock()
	return updCheck.info.Behind
}

func (a *app) settingOn(key string) bool {
	var v string
	a.db.QueryRow(`SELECT value FROM settings WHERE key=$1`, key).Scan(&v)
	return v == "1"
}

// setAutoUpdate toggles nightly self-installation of new releases.
func (a *app) setAutoUpdate(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("auto") == "on"
	val := "0"
	if on {
		val = "1"
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('auto_update',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, val)
	a.audit(r, "auto-update-setting", val)
	msg := "Auto-install disabled - updates wait for your click."
	if on {
		msg = "Auto-install enabled: new releases install automatically between 03:00-05:00 UTC."
	}
	redirectMsg(w, r, "/system", msg)
}

// applyUpdate stages an updater script and launches it as a TRANSIENT systemd
// unit (systemd-run). That is deliberate: the script restarts pgforged, and a
// plain background child would sit in pgforged's own cgroup and be killed by that
// restart (default KillMode=control-group) before the health-check and rollback
// could run. A transient unit gets its own cgroup and survives.
//
// The script pulls, rebuilds, atomically swaps the binary, restarts, health-
// checks the real listen port, and rolls back to pgforged.prev on failure. All
// progress goes to /opt/pgforge/update.log, tailed on the System page.
func (a *app) applyUpdate(w http.ResponseWriter, r *http.Request) {
	// Refuse to start a second update while one is genuinely in flight. This
	// backstops the UI (which hides the button while running) against a second tab
	// or a direct POST launching a concurrent updater on the same binary. A stale
	// log (see updateInFlight) does not block, so a past wedged update can't lock
	// updates out permanently.
	if updateInFlight() {
		redirectMsg(w, r, "/system", "An update is already in progress.")
		return
	}
	if err := a.launchUpdate(); err != nil {
		redirectErr(w, r, "/system", err.Error())
		return
	}
	a.audit(r, "self-update", "started")
	redirectMsg(w, r, "/system", "Update started. Watch the live log below - this page refreshes on its own until it finishes.")
}

// launchUpdate stages the updater script and starts it in a transient unit.
// Shared by the manual button and the opt-in nightly auto-installer.
func (a *app) launchUpdate() error {
	repoDir := ""
	if b, err := os.ReadFile("/opt/pgforge/repo_dir"); err == nil {
		repoDir = strings.TrimSpace(string(b))
	}
	if repoDir == "" {
		return fmt.Errorf("update source not configured (this box was not installed from a git checkout)")
	}
	script := `#!/bin/sh
LOG=/opt/pgforge/update.log
echo "== update started $(date -u '+%F %T') UTC ==" > "$LOG"
exec >> "$LOG" 2>&1
set -x
# A transient systemd unit runs with a minimal environment, so give the Go
# toolchain an explicit HOME and module/build cache; without these it fails with
# "module cache not found: neither GOMODCACHE nor GOPATH is set".
export HOME="${HOME:-/root}"
export GOPATH="${GOPATH:-$HOME/go}"
export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"
export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
export PATH="$PATH:/usr/local/go/bin:/usr/local/bin"
LISTEN="$(sed -n 's/^LISTEN=//p' /opt/pgforge/pgforged.env | head -1)"
[ -z "$LISTEN" ] && LISTEN=127.0.0.1:8080
cd "` + repoDir + `" || { echo "!! repo dir missing"; exit 1; }
git pull --ff-only || { echo "!! git pull failed"; exit 1; }
VER="$(git rev-parse --short HEAD)"
echo ">> building $VER"
( cd pgforge && go build -ldflags "-X main.version=$VER -X main.buildTime=$(date -u +%FT%TZ)" -o /tmp/pgforged.upd . ) || { echo "!! build failed"; exit 1; }
cp -a /opt/pgforge/bin/pgforged /opt/pgforge/bin/pgforged.prev
install -m 0755 /tmp/pgforged.upd /opt/pgforge/bin/pgforged
echo ">> restarting pgforged"
systemctl restart pgforged
sleep 4
if curl -fsS -o /dev/null "http://$LISTEN/healthz"; then
  echo ">> applying infra (scripts + systemd units)"
  sh "` + repoDir + `/scripts/apply-infra.sh" --safe || echo "!! infra apply failed (binary is fine)"
  echo ">> OK: updated to $VER"
  sh /opt/pgforge/bin/alert-notify.sh "ForgeBase updated successfully to $VER." || true
else
  echo "!! health check failed - rolling back"
  install -m 0755 /opt/pgforge/bin/pgforged.prev /opt/pgforge/bin/pgforged
  systemctl restart pgforged
  echo ">> rolled back to previous build"
  sh /opt/pgforge/bin/alert-notify.sh "WARNING ForgeBase: update to $VER FAILED its health check and was rolled back automatically. The previous version is running." || true
fi
# hygiene: the build cache regrows on every update and nothing else prunes it;
# the module cache is kept unless it has grown past 1GB.
rm -f /tmp/pgforged.upd
go clean -cache >/dev/null 2>&1 || true
MOD=$(du -sm "$GOMODCACHE" 2>/dev/null | cut -f1)
[ "${MOD:-0}" -gt 1024 ] && go clean -modcache >/dev/null 2>&1
exit 0
`
	if err := os.WriteFile("/opt/pgforge/update.sh", []byte(script), 0o755); err != nil {
		return fmt.Errorf("could not stage updater: %v", err)
	}
	// Write an immediate "running" marker BEFORE launching the updater. The System
	// page derives its "updating..." state from this log, and the transient unit
	// takes a moment to spawn and write its own first line - without this seed the
	// redirect right after the click would briefly render as "not updating". The
	// script truncates and rewrites the log on start, so this is only the seed.
	os.WriteFile("/opt/pgforge/update.log",
		[]byte("== update requested "+time.Now().UTC().Format("2006-01-02 15:04:05")+" UTC ==\n>> launching updater, this can take a minute...\n"),
		0o644)
	// Transient unit -> own cgroup -> survives the pgforged restart. --collect
	// removes the unit afterwards even if it failed. Fall back to a detached child
	// where systemd-run is unavailable.
	if err := exec.Command("systemd-run", "--unit=forgebase-update", "--collect", "/opt/pgforge/update.sh").Run(); err != nil {
		exec.Command("setsid", "sh", "-c", "/opt/pgforge/update.sh").Start()
	}
	return nil
}
