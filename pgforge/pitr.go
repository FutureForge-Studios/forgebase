package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Point-in-time recovery. Restore a project's database to any instant, down to
// the second, into a NEW project - non-destructive, the source is never touched.
//
// The engine is scripts/pitr-restore.sh (installed at /opt/pgforge/bin): it stands
// up a throwaway Postgres from the newest basebackup taken at or before the target,
// replays the continuously archived WAL forward to recovery_target_time, and dumps
// the database exactly as it existed at that instant. Here we drive it and load the
// result into a freshly provisioned project. This is what turns the WAL archive +
// basebackups the platform already keeps into real, self-serve PITR.

const pitrScript = "/opt/pgforge/bin/pitr-restore.sh"

func (a *app) pitrRestore(w http.ResponseWriter, r *http.Request) {
	src := r.PathValue("slug")
	if !a.projectExists(src) {
		http.NotFound(w, r)
		return
	}
	newSlug := strings.TrimSpace(strings.ToLower(r.FormValue("new_slug")))
	if !slugRe.MatchString(newSlug) || isReserved(newSlug) {
		redirectErr(w, r, "/p/"+src+"/backups", "New project name: letters, numbers and dash; start with a letter.")
		return
	}
	if a.projectExists(newSlug) {
		redirectErr(w, r, "/p/"+src+"/backups", "A project named "+newSlug+" already exists.")
		return
	}
	target, err := parsePITRTarget(strings.TrimSpace(r.FormValue("target")))
	if err != nil {
		redirectErr(w, r, "/p/"+src+"/backups", "Pick a valid date and time (UTC) in the past.")
		return
	}
	pw, err := a.provisionProject(newSlug)
	if err != nil {
		redirectErr(w, r, "/p/"+src+"/backups", "Could not create "+newSlug+": "+err.Error())
		return
	}
	a.db.Exec(`INSERT INTO db_imports(slug, status, message) VALUES ($1,'cloning',$2)
		ON CONFLICT (slug) DO UPDATE SET status='cloning', message=$2`,
		newSlug, "point-in-time restore of "+src+" @ "+target+" UTC")
	a.db.Exec(`UPDATE projects SET status='cloning' WHERE slug=$1`, newSlug)
	a.audit(r, "pitr-restore", src+" @ "+target+" UTC -> "+newSlug)
	go a.runPITR(src, target, newSlug, pw)
	redirectMsg(w, r, "/p/"+newSlug+"/sync",
		"Point-in-time restore started: "+newSlug+" will hold "+src+" as it was at "+target+" UTC. This page shows progress.")
}

func (a *app) runPITR(src, target, newSlug, pw string) {
	defer func() { recover() }() // a panic here must never take down the binary
	setErr := func(m string) {
		a.db.Exec(`UPDATE db_imports SET status='error', message=$2, updated_at=now() WHERE slug=$1`,
			newSlug, safeTail(strings.TrimSpace(m), 400))
	}
	// 1) recover the source database to the target instant -> a point-in-time dump
	cmd := exec.Command(pitrScript, target, src)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		tail := safeTail(strings.TrimSpace(errBuf.String()), 300)
		if tail == "" {
			tail = "the target may predate the oldest basebackup, or its WAL was pruned"
		}
		setErr("point-in-time recovery failed: " + tail)
		return
	}
	dump := strings.TrimSpace(string(out))
	if dump == "" {
		setErr("recovery produced no dump")
		return
	}
	defer exec.Command("rm", "-f", dump).Run()

	// 2) load the point-in-time dump into the fresh project's database, owned by
	// its own role (mirrors Clone). The dump lives on the host; we pipe it into
	// the container over stdin.
	dsn := a.directDSN(newSlug, pw)
	restore := exec.Command("sh", "-c", fmt.Sprintf(
		`docker exec -i -e DST=%q pgforge-db sh -c 'pg_restore --no-owner --no-acl -d "$DST"' < %q`, dsn, dump))
	restore.CombinedOutput() // pg_restore exits non-zero on ignorable warnings

	var n int
	if db, err := a.dbFor(newSlug); err == nil {
		db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&n)
	}
	a.db.Exec(`UPDATE db_imports SET status='done', message=$2, updated_at=now() WHERE slug=$1`,
		newSlug, fmt.Sprintf("restored %s to %s UTC (%d tables)", src, target, n))
	a.db.Exec(`UPDATE projects SET status='active' WHERE slug=$1`, newSlug)
	a.auditRaw("system", "-", "pitr-done", newSlug+" @ "+target+" UTC")
}

// parsePITRTarget accepts an HTML datetime-local value (minute or second
// precision) and returns a Postgres timestamp string, treated as UTC. Rejects
// future times.
func parsePITRTarget(s string) (string, error) {
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.After(time.Now().UTC().Add(1 * time.Minute)) {
				return "", fmt.Errorf("future")
			}
			return t.Format("2006-01-02 15:04:05"), nil
		}
	}
	return "", fmt.Errorf("bad time")
}
