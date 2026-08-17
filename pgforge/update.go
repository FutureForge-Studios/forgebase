package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// In-app self-update (Coolify-style): check GitHub for a newer build, show the
// changelog, and apply it with one click - rebuild + atomic binary swap +
// restart + health-check + automatic rollback. Admin only.

const updateOwner = "FutureForge-Studios"
const updateRepo = "forgebase"

type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

type updateInfo struct {
	Current   string
	Latest    string
	LatestMsg string
	Behind    bool
	Changelog []string
	Err       string
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// updateStatus asks GitHub for recent commits on the default branch and compares
// them to the running build (version = the short SHA baked in at build time).
func (a *app) updateStatus() updateInfo {
	info := updateInfo{Current: version}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=15", updateOwner, updateRepo)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		info.Err = err.Error()
		return info
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		info.Err = fmt.Sprintf("GitHub returned %d", resp.StatusCode)
		return info
	}
	var commits []ghCommit
	if json.NewDecoder(resp.Body).Decode(&commits) != nil || len(commits) == 0 {
		info.Err = "could not read releases"
		return info
	}
	info.Latest = commits[0].SHA[:7]
	info.LatestMsg = firstLine(commits[0].Commit.Message)
	info.Behind = version != "dev" && !strings.HasPrefix(info.Latest, version) && !strings.HasPrefix(version, info.Latest)
	for _, c := range commits {
		if version != "dev" && strings.HasPrefix(c.SHA, version) {
			break
		}
		info.Changelog = append(info.Changelog, firstLine(c.Commit.Message))
		if len(info.Changelog) >= 12 {
			break
		}
	}
	return info
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
  echo ">> OK: updated to $VER"
else
  echo "!! health check failed - rolling back"
  install -m 0755 /opt/pgforge/bin/pgforged.prev /opt/pgforge/bin/pgforged
  systemctl restart pgforged
  echo ">> rolled back to previous build"
fi
`
	if err := os.WriteFile("/opt/pgforge/update.sh", []byte(script), 0o755); err != nil {
		redirectErr(w, r, "/system", "Could not stage updater: "+err.Error())
		return
	}
	// Transient unit -> own cgroup -> survives the pgforged restart. --collect
	// removes the unit afterwards even if it failed. Fall back to a detached child
	// where systemd-run is unavailable.
	if err := exec.Command("systemd-run", "--unit=forgebase-update", "--collect", "/opt/pgforge/update.sh").Run(); err != nil {
		exec.Command("setsid", "sh", "-c", "/opt/pgforge/update.sh").Start()
	}
	a.audit(r, "self-update", "started")
	redirectMsg(w, r, "/system", "Update started. The panel will rebuild and restart in a moment; refresh this page to see the result.")
}
