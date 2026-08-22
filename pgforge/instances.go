package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Dedicated-instance mode: a project can run as its OWN Postgres container on
// copy-on-write storage instead of a database inside the shared cluster.
// What that buys (and what the shared cluster cannot do):
//   - instant branching: a branch is a btrfs snapshot + container (~2s),
//     copy-on-write so it shares unchanged blocks with its parent
//   - true scale-to-zero: the idle reaper STOPS the container (0 RAM, 0 CPU);
//     the cold-start proxy on :5433 restarts it on the next connection (~1.3s)
//   - full isolation: its own postmaster, its own superuser (the project
//     role), its own crash domain - a problem there touches nothing else
//
// The engine lives in scripts/pg-instance.sh (+ tools/pgproxy, systemd units);
// this file is the panel's bridge to it. projects.mode records the choice:
// 'shared' (default) or 'instance'.

const pgInstanceScript = "/opt/pgforge/bin/pg-instance.sh"
const instancesRoot = "/opt/pgforge/instances"
const instancePort = 5433 // the cold-start proxy; routes by database name

// instanceModeAvailable reports whether this box has the CoW store set up
// (INSTANCES=1 at install time, or setup-instances.sh run later).
func instanceModeAvailable() bool {
	if _, err := os.Stat(pgInstanceScript); err != nil {
		return false
	}
	if _, err := os.Stat(instancesRoot + "/.ports"); err == nil {
		return true
	}
	// a mounted-but-unused store has no .ports yet; check the mountpoint
	out, err := exec.Command("mountpoint", "-q", instancesRoot).CombinedOutput()
	_ = out
	return err == nil
}

// projectMode returns 'shared' or 'instance' for a slug.
func (a *app) projectMode(slug string) string {
	mode := "shared"
	a.db.QueryRow(`SELECT mode FROM projects WHERE slug=$1`, slug).Scan(&mode)
	if mode == "" {
		mode = "shared"
	}
	return mode
}

func pgInstance(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", append([]string{pgInstanceScript}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("pg-instance %s: %v: %s", args[0], err, tail(string(out), 200))
	}
	return strings.TrimSpace(string(out)), nil
}

// provisionInstanceProject creates a dedicated-instance project: its own
// Postgres container whose superuser IS the project role, database named after
// the slug (the proxy routes by it). Mirrors provisionProject's contract.
func (a *app) provisionInstanceProject(slug string) (string, error) {
	pw := randHex(18)
	if _, err := pgInstance(3*time.Minute, "create", slug, pw); err != nil {
		pgInstance(time.Minute, "delete", slug)
		return "", err
	}
	if _, err := a.db.Exec(
		`INSERT INTO projects(slug, role_name, password_enc, mode) VALUES ($1,$1,pgp_sym_encrypt($2,$3),'instance')`,
		slug, pw, string(a.cfg.secret)); err != nil {
		pgInstance(time.Minute, "delete", slug)
		return "", err
	}
	return pw, nil
}

// instanceDSN is the control plane's path to an instance database - through
// the cold-start proxy, so any panel/API touch wakes a sleeping instance.
func (a *app) instanceDSN(slug, pw string) string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable&application_name=pgforged",
		url.QueryEscape(slug), url.QueryEscape(pw), instancePort, slug)
}

// syncInstanceStatus reconciles instance projects' panel status with their
// container state: running = active, stopped by the reaper = suspended
// (sleeping). Called from the sampler. One docker ps for all of them.
func (a *app) syncInstanceStatus() {
	rows, err := a.db.Query(`SELECT slug FROM projects WHERE mode='instance' AND status IN ('active','suspended')`)
	if err != nil {
		return
	}
	var slugs []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		slugs = append(slugs, s)
	}
	rows.Close()
	if len(slugs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		return
	}
	running := map[string]bool{}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(ln, "pgi-") {
			running[strings.TrimPrefix(strings.TrimSpace(ln), "pgi-")] = true
		}
	}
	for _, s := range slugs {
		if running[s] {
			a.db.Exec(`UPDATE projects SET status='active', last_active=now()
				WHERE slug=$1 AND status='suspended'`, s)
		} else {
			a.db.Exec(`UPDATE projects SET status='suspended'
				WHERE slug=$1 AND status='active'`, s)
		}
	}
}

// migrateToInstance moves a shared-cluster project onto its own dedicated
// Postgres container: create the instance, copy every byte with
// pg_dump | pg_restore, then flip the panel over. The shared database is
// RENAMED (slug_premig), never dropped - a failed migration leaves the
// project exactly as it was, and a finished one keeps the old copy around
// until the owner deletes it by hand.
func (a *app) migrateToInstance(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/settings"
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	if !instanceModeAvailable() {
		redirectErr(w, r, back, "This server has no dedicated-instance store (run setup-instances.sh first).")
		return
	}
	if a.projectMode(slug) != "shared" {
		redirectErr(w, r, back, "Already running as a dedicated instance.")
		return
	}
	_, pw := a.projectCred(slug)
	if pw == "" {
		redirectErr(w, r, back, "Could not read the project credential.")
		return
	}
	if _, err := pgInstance(3*time.Minute, "create", slug, pw); err != nil {
		redirectErr(w, r, back, "Instance create failed: "+err.Error())
		return
	}
	// copy the data: shared cluster -> new instance (both containers local)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pipe := fmt.Sprintf(
		`set -o pipefail; docker exec pgforge-db pg_dump -U postgres -Fc -d %q | docker exec -i pgi-%s pg_restore -U %q -d %q --no-owner --role %q`,
		slug, slug, slug, slug, slug)
	if out, err := exec.CommandContext(ctx, "bash", "-c", pipe).CombinedOutput(); err != nil {
		pgInstance(time.Minute, "delete", slug)
		redirectErr(w, r, back, "Data copy failed (shared copy untouched): "+tail(string(out), 300))
		return
	}
	// flip: park the shared copy, point everything at the instance
	a.stopPostgREST(slug)
	a.stopRealtimeHub(slug)
	closeConn(slug)
	a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, slug)
	if _, err := a.db.Exec(fmt.Sprintf(`ALTER DATABASE %q RENAME TO %q`, slug, slug+"_premig")); err != nil {
		redirectErr(w, r, back, "Copy done but could not park the shared database: "+err.Error())
		return
	}
	a.db.Exec(`UPDATE projects SET mode='instance', status='active', last_active=now() WHERE slug=$1`, slug)
	a.audit(r, "migrate-instance", slug)
	redirectMsg(w, r, back, "Migrated to a dedicated instance. The old shared copy is parked as "+slug+"_premig; delete it from the Database page when you are confident.")
}
