package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
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

// updateStatus reads the published CHANGELOG.md from GitHub and compares its
// latest released version to the running one. Updates are release-based (they
// track version bumps + changelog entries), and we show the actual release notes -
// not raw git commit subjects, which mean nothing to an operator.
func (a *app) updateStatus() updateInfo {
	info := updateInfo{Current: appVersion}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// The unique query param busts GitHub's raw-CDN cache (up to ~5 min per
	// edge), so a check always sees a just-pushed release immediately.
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/CHANGELOG.md?cb=%d", updateOwner, updateRepo, time.Now().Unix())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		info.Err = err.Error()
		return info
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		info.Err = fmt.Sprintf("could not fetch the changelog (%d)", resp.StatusCode)
		return info
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	latest, sections := parseChangelog(string(body), appVersion)
	info.Latest = latest
	info.Behind = latest != "" && semverLess(appVersion, latest)
	if info.Behind {
		info.Changelog = sections
	}
	return info
}

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
	repoDir := ""
	if b, err := os.ReadFile("/opt/pgforge/repo_dir"); err == nil {
		repoDir = strings.TrimSpace(string(b))
	}
	if repoDir == "" {
		redirectErr(w, r, "/system", "Update source not configured (this box was not installed from a git checkout).")
		return
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
else
  echo "!! health check failed - rolling back"
  install -m 0755 /opt/pgforge/bin/pgforged.prev /opt/pgforge/bin/pgforged
  systemctl restart pgforged
  echo ">> rolled back to previous build"
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
		redirectErr(w, r, "/system", "Could not stage updater: "+err.Error())
		return
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
	a.audit(r, "self-update", "started")
	redirectMsg(w, r, "/system", "Update started. Watch the live log below - this page refreshes on its own until it finishes.")
}
