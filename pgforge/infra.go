package main

import (
	"log"
	"os"
	"os/exec"
	"strings"
)

// infraRev marks the revision of on-box infrastructure (scripts/, systemd/)
// this binary expects. The self-updater only swaps the binary, so after an
// update the box's scripts and units can be stale; on boot, if the recorded
// revision differs, the reconciler runs scripts/apply-infra.sh --safe from the
// repo checkout to bring them current. Bump this whenever scripts/ or systemd/
// change in a release.
const infraRev = "1"

// reconcileInfra runs apply-infra.sh --safe when the box's installed infra is
// out of date, then applies zero-downtime Postgres safety settings. Runs in a
// goroutine after boot so it never delays serving.
func (a *app) reconcileInfra() {
	repoDir := ""
	if b, err := os.ReadFile("/opt/pgforge/repo_dir"); err == nil {
		repoDir = strings.TrimSpace(string(b))
	}
	if repoDir != "" {
		cur, _ := os.ReadFile("/opt/pgforge/infra_rev")
		if strings.TrimSpace(string(cur)) != infraRev {
			script := repoDir + "/scripts/apply-infra.sh"
			if _, err := os.Stat(script); err == nil {
				if out, err := exec.Command("sh", script, "--safe").CombinedOutput(); err != nil {
					log.Printf("infra reconcile failed: %v: %s", err, tail(string(out), 400))
				} else {
					// record the binary's expectation, superseding the git sha the
					// script wrote, so this only runs again after the next bump.
					os.WriteFile("/opt/pgforge/infra_rev", []byte(infraRev+"\n"), 0o644)
					log.Printf("infra reconciled to rev %s", infraRev)
				}
			}
		}
	}

	// WAL safety settings. ALTER SYSTEM (not compose -c flags, which would
	// override it) so this works on every existing box with zero downtime -
	// all three are reloadable. Prevents a repeat of the 2026-08-22 disk-full
	// crash: bounded pg_wal, compressed WAL, small idle floor.
	for _, q := range []string{
		`ALTER SYSTEM SET max_wal_size = '1GB'`,
		`ALTER SYSTEM SET min_wal_size = '128MB'`,
		`ALTER SYSTEM SET wal_compression = 'on'`,
	} {
		if _, err := a.db.Exec(q); err != nil {
			log.Printf("infra: %s: %v", q, err)
		}
	}
	a.db.Exec(`SELECT pg_reload_conf()`)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
