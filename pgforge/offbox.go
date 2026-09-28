package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Off-box archive browsing + restore. The nightly backup syncs to an rclone
// remote (/opt/pgforge/backup_remote). Local retention is a shallow working set,
// so anything older than a few days exists ONLY off-box, and this is the path
// that reaches it: list a project's off-box dumps and restore one into a NEW
// project (never over the source), no SSH required.
//
// TWO tiers live on the remote and both are listed here:
//   dumps/           the mirror of the local working set, bounded by local retention
//   weekly/<date>/   one complete dump set per week, bounded by offbox_keep_days
// The weekly prefix is what the nightly rclone sync deliberately excludes, so it
// is the only thing on the remote that outlives local pruning.

type offboxFile struct {
	Name, Size, Date string
	// Path is the file's location on the remote, relative to its root
	// ("dumps/x.dump" or "weekly/2026-09-28/x.dump"). Restore submits THIS, not
	// Name, because the same dump can exist in both tiers.
	Path string
	Tier string // "Working set" or "Weekly archive", for the UI
}

func backupRemote() string {
	b, _ := os.ReadFile("/opt/pgforge/backup_remote")
	return strings.TrimSpace(string(b))
}

// offboxPathRe is the security boundary for anything built from a submitted
// remote path. Anchored, exactly two or three segments, no traversal: either
// "dumps/<file>" or "weekly/<YYYY-MM-DD>/<file>". Without the anchors a crafted
// value could walk out of the backup prefix, since the path is concatenated onto
// the remote before being handed to rclone.
var offboxPathRe = regexp.MustCompile(`^(dumps|weekly/[0-9]{4}-[0-9]{2}-[0-9]{2})/[^/]+$`)

func offboxPathOK(p string) bool { return offboxPathRe.MatchString(p) }

// offboxList returns this project's dumps present on the remote, newest first.
func (a *app) offboxList(slug string) []offboxFile {
	remote := backupRemote()
	if remote == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// One recursive call covers both tiers. -R is why the weekly/<date>/ prefix
	// is visible at all: a non-recursive listing of dumps/ cannot see it, which
	// would make the deep archive unreachable from the panel.
	out, err := exec.CommandContext(ctx, "rclone", "lsjson", "--files-only", "-R", remote).Output()
	if err != nil {
		return nil
	}
	var raw []struct {
		Path    string `json:"Path"`
		Name    string `json:"Name"`
		Size    int64  `json:"Size"`
		ModTime string `json:"ModTime"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	var files []offboxFile
	for _, f := range raw {
		if !offboxPathOK(f.Path) || !projectDumpOK(slug, f.Name) {
			continue
		}
		date := ""
		if t, err := time.Parse(time.RFC3339, f.ModTime); err == nil {
			date = t.Format("Jan 02, 2006")
		}
		tier := "Working set"
		if strings.HasPrefix(f.Path, "weekly/") {
			tier = "Weekly archive"
		}
		files = append(files, offboxFile{
			Name: f.Name, Size: humanBytes(f.Size), Date: date, Path: f.Path, Tier: tier,
		})
	}
	// lsjson order is arbitrary; dump names embed the date, so sort by name desc,
	// then by path so the working-set copy of a given night sorts before the
	// weekly one rather than at random.
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if files[j].Name > files[i].Name ||
				(files[j].Name == files[i].Name && files[j].Path < files[i].Path) {
				files[i], files[j] = files[j], files[i]
			}
		}
	}
	if len(files) > 30 {
		files = files[:30]
	}
	return files
}

// offboxRestore pulls a dump from the remote and restores it into a NEW
// project named <slug>-restored-<date>. Runs in the background (large dumps
// take minutes); the new project shows as "cloning" until it is ready.
func (a *app) offboxRestore(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	// The form submits the full remote path now, because the same dump can exist
	// in both the working-set mirror and the weekly archive. Validate the PATH
	// with the anchored pattern AND the basename against the project, so neither
	// check can be bypassed by the other.
	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		path = "dumps/" + filepath.Base(r.FormValue("file")) // older form posts
	}
	file := filepath.Base(path)
	if !offboxPathOK(path) || !projectDumpOK(slug, file) {
		redirectErr(w, r, "/p/"+slug+"/backups", "That backup does not belong to this project.")
		return
	}
	remote := backupRemote()
	if remote == "" {
		redirectErr(w, r, "/p/"+slug+"/backups", "No off-box remote configured.")
		return
	}
	// dump date -> restore name (owner-chosen scheme: slug-restored-DATE)
	date := strings.TrimSuffix(strings.TrimPrefix(file, slug+"-"), ".dump")
	if len(date) > 10 {
		date = date[:10]
	}
	newSlug := a.uniqueSlug(fmt.Sprintf("%.28s-restored-%s", slug, date))
	if !slugRe.MatchString(newSlug) {
		redirectErr(w, r, "/p/"+slug+"/backups", "Could not derive a valid name for the restored project.")
		return
	}
	if _, err := a.provisionProject(newSlug); err != nil {
		redirectErr(w, r, "/p/"+slug+"/backups", "Could not create the target project: "+err.Error())
		return
	}
	a.db.Exec(`UPDATE projects SET status='cloning' WHERE slug=$1`, newSlug)
	a.audit(r, "offbox-restore", file+" -> "+newSlug)

	go func() {
		defer func() { recover() }()
		tmp := "/opt/pgforge-backups/pitr/" + file // pitr/ has 2-day retention
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		fail := func(why string, err error) {
			a.dropProjectFully(newSlug)
			a.rewriteUserlist()
			a.auditRaw("system", "-", "offbox-restore-failed", fmt.Sprintf("%s: %s: %v", newSlug, why, err))
			a.notifyDiscord("WARNING ForgeBase: off-box restore of " + file + " failed (" + why + ").")
		}
		os.MkdirAll("/opt/pgforge-backups/pitr", 0o755)
		if out, err := exec.CommandContext(ctx, "rclone", "copyto", remote+"/"+path, tmp).CombinedOutput(); err != nil {
			fail("download", fmt.Errorf("%v: %s", err, tail(string(out), 200)))
			return
		}
		defer os.Remove(tmp)
		cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf(
			`docker exec -i pgforge-db pg_restore -U postgres --no-owner --role %q -d %q < %q`, newSlug, newSlug, tmp))
		if out, err := cmd.CombinedOutput(); err != nil {
			// pg_restore reports non-fatal per-object errors with exit 1; require
			// the target to actually have tables before calling it a failure
			var tables int
			if db, derr := a.dbFor(newSlug); derr == nil {
				db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables)
			}
			if tables == 0 {
				fail("restore", fmt.Errorf("%v: %s", err, tail(string(out), 200)))
				return
			}
		}
		a.db.Exec(`UPDATE projects SET status='active' WHERE slug=$1`, newSlug)
		a.auditRaw("system", "-", "offbox-restore-done", newSlug)
		a.notifyDiscord("ForgeBase: off-box restore finished - project " + newSlug + " is ready.")
	}()

	redirectMsg(w, r, "/", "Restoring "+file+" into new project "+newSlug+" - it appears here and goes active when ready (large dumps take a few minutes).")
}
