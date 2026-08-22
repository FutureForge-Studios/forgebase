package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Off-box archive browsing + restore. The nightly backup syncs to an rclone
// remote (/opt/pgforge/backup_remote); local retention is tight, so older
// dumps exist ONLY off-box. This lets the Backups page list a project's
// off-box dumps and restore one into a NEW project (never over the source) -
// no SSH required.

type offboxFile struct {
	Name, Size, Date string
}

func backupRemote() string {
	b, _ := os.ReadFile("/opt/pgforge/backup_remote")
	return strings.TrimSpace(string(b))
}

// offboxList returns this project's dumps present on the remote, newest first.
func (a *app) offboxList(slug string) []offboxFile {
	remote := backupRemote()
	if remote == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "rclone", "lsjson", "--files-only", remote+"/dumps").Output()
	if err != nil {
		return nil
	}
	var raw []struct {
		Name    string `json:"Name"`
		Size    int64  `json:"Size"`
		ModTime string `json:"ModTime"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	var files []offboxFile
	for _, f := range raw {
		if !projectDumpOK(slug, f.Name) {
			continue
		}
		date := ""
		if t, err := time.Parse(time.RFC3339, f.ModTime); err == nil {
			date = t.Format("Jan 02, 2006")
		}
		files = append(files, offboxFile{Name: f.Name, Size: humanBytes(f.Size), Date: date})
	}
	// lsjson order is arbitrary; dump names embed the date, so sort by name desc
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if files[j].Name > files[i].Name {
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
	file := filepath.Base(r.FormValue("file"))
	if !projectDumpOK(slug, file) {
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
		if out, err := exec.CommandContext(ctx, "rclone", "copyto", remote+"/dumps/"+file, tmp).CombinedOutput(); err != nil {
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
