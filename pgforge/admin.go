package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// dumpDateRe matches the date that follows the slug in a dump filename. It is
// what stops project "app" from restoring branch "app-dev"'s dump: after the
// "app-" prefix the next segment must be a date, not another project's name.
var dumpDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

func projectDumpOK(slug, file string) bool {
	if !strings.HasSuffix(file, ".dump") {
		return false
	}
	rest, ok := strings.CutPrefix(file, slug+"-")
	return ok && dumpDateRe.MatchString(rest)
}

// ----------------------------------------------------------------- database

func (a *app) databasePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// every extension the server can install, with its official description
	type extView struct {
		Name, Version, Comment string
		Installed              bool
	}
	var exts []extView
	rows, _ := db.Query(`SELECT ae.name, ae.default_version, coalesce(ae.comment,''),
			(e.extname IS NOT NULL) AS installed
		FROM pg_available_extensions ae
		LEFT JOIN pg_extension e ON e.extname = ae.name
		ORDER BY installed DESC, ae.name`)
	if rows != nil {
		for rows.Next() {
			var e extView
			rows.Scan(&e.Name, &e.Version, &e.Comment, &e.Installed)
			exts = append(exts, e)
		}
		rows.Close()
	}
	var nInstalled int
	for _, e := range exts {
		if e.Installed {
			nInstalled++
		}
	}

	// size / connection facts
	var size string
	var conns, maxConns, connLimit int
	var version string
	var canCreateDB bool
	a.db.QueryRow(`SELECT pg_size_pretty(pg_database_size($1))`, slug).Scan(&size)
	a.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=$1`, slug).Scan(&conns)
	a.db.QueryRow(`SELECT setting::int FROM pg_settings WHERE name='max_connections'`).Scan(&maxConns)
	a.db.QueryRow(`SELECT rolconnlimit FROM pg_roles WHERE rolname=$1`, slug).Scan(&connLimit)
	a.db.QueryRow(`SELECT rolcreatedb FROM pg_roles WHERE rolname=$1`, slug).Scan(&canCreateDB)
	a.db.QueryRow(`SHOW server_version`).Scan(&version)
	if i := strings.IndexByte(version, ' '); i > 0 {
		version = version[:i]
	}

	// roles inside the project database (owner + any auth/api roles present)
	type roleRow struct{ Name, Attrs string }
	var dbRoles []roleRow
	if rows, err := db.Query(`SELECT rolname,
			array_to_string(array_remove(ARRAY[
				CASE WHEN rolsuper THEN 'superuser' END,
				CASE WHEN rolcanlogin THEN 'login' ELSE 'nologin' END,
				CASE WHEN rolcreatedb THEN 'createdb' END,
				CASE WHEN rolbypassrls THEN 'bypassrls' END], NULL), ', ')
		FROM pg_roles WHERE rolname IN ($1,'anon','authenticated','service_role')
		ORDER BY rolname`, slug); err == nil {
		for rows.Next() {
			var rr roleRow
			rows.Scan(&rr.Name, &rr.Attrs)
			dbRoles = append(dbRoles, rr)
		}
		rows.Close()
	}

	content := renderContent(databaseBody, map[string]any{
		"Slug": slug, "Exts": exts, "Size": size, "Conns": conns,
		"NInstalled": nInstalled, "NAvail": len(exts),
		"MaxConns": maxConns, "ConnLimit": connLimit, "Version": version,
		"Roles": dbRoles, "Domain": a.cfg.domain, "CanAdmin": a.atLeast(r, "admin"),
		"CanCreateDB": canCreateDB,
	})
	a.renderShell(w, r, shellData{Title: slug + " · Database", Nav: "database", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Database"}}}, content)
}

func (a *app) changeDBPassword(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	newpw := strings.TrimSpace(r.FormValue("password"))
	if newpw == "" {
		newpw = randHex(18) // rotate to a fresh strong one
	}
	if len(newpw) < 10 {
		redirectErr(w, r, "/p/"+slug+"/database", "Password must be at least 10 characters.")
		return
	}
	q := pq.QuoteIdentifier(slug)
	if _, err := a.db.Exec(fmt.Sprintf(`ALTER ROLE %s PASSWORD %s`, q, pq.QuoteLiteral(newpw))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/database", "Change failed: "+err.Error())
		return
	}
	a.db.Exec(`UPDATE projects SET password_enc=pgp_sym_encrypt($1,$2) WHERE slug=$3`,
		newpw, string(a.cfg.secret), slug)
	a.rewriteUserlist()
	a.audit(r, "db-password", slug)
	redirectMsg(w, r, "/p/"+slug, "Database password rotated. Copy the new connection string.")
}

func (a *app) toggleExtension(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	ext := r.FormValue("ext")
	action := r.FormValue("action")
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// validate against the server's real available-extension list
	var ok bool
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_available_extensions WHERE name=$1)`, ext).Scan(&ok)
	if !ok {
		redirectErr(w, r, "/p/"+slug+"/database", "Unknown extension.")
		return
	}
	q := pq.QuoteIdentifier(ext)
	if action == "enable" {
		_, err = db.Exec(fmt.Sprintf(`CREATE EXTENSION IF NOT EXISTS %s`, q))
	} else {
		_, err = db.Exec(fmt.Sprintf(`DROP EXTENSION IF EXISTS %s`, q))
	}
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/database", ext+": "+err.Error())
		return
	}
	a.audit(r, "extension-"+action, slug+"/"+ext)
	redirectMsg(w, r, "/p/"+slug+"/database", ext+" "+action+"d.")
}

// setConnLimit updates the project role's CONNECTION LIMIT. Applies instantly
// to new connections; -1 means unlimited (still capped by cluster max).
func (a *app) setConnLimit(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("limit")))
	if err != nil || n < 1 || n > 100 {
		redirectErr(w, r, "/p/"+slug+"/database", "Connection limit must be 1-100 (the pooled port multiplexes beyond it; unlimited is no longer offered from the panel).")
		return
	}
	if _, err := a.db.Exec(fmt.Sprintf(`ALTER ROLE %s CONNECTION LIMIT %d`,
		pq.QuoteIdentifier(slug), n)); err != nil {
		redirectErr(w, r, "/p/"+slug+"/database", "Change failed: "+err.Error())
		return
	}
	a.audit(r, "conn-limit", fmt.Sprintf("%s=%d", slug, n))
	if n == -1 {
		redirectMsg(w, r, "/p/"+slug+"/database", "Project connection limit removed (unlimited).")
		return
	}
	redirectMsg(w, r, "/p/"+slug+"/database", fmt.Sprintf("Project connection limit set to %d.", n))
}

// setCreateDB toggles CREATEDB on the project role. Off by default; needed by
// tools that make scratch databases (e.g. the prisma migrate dev shadow DB).
// Databases created this way are owned by the role but are NOT ForgeBase
// projects: they get nightly-dumped like everything else, yet have no panel,
// metrics or API of their own.
func (a *app) setCreateDB(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	verb, msg := "NOCREATEDB", "Database creation revoked for role "+slug+"."
	if r.FormValue("action") == "allow" {
		verb, msg = "CREATEDB", "Role "+slug+" may now create databases (Prisma shadow DBs will work)."
	}
	if _, err := a.db.Exec(fmt.Sprintf(`ALTER ROLE %s %s`, pq.QuoteIdentifier(slug), verb)); err != nil {
		redirectErr(w, r, "/p/"+slug+"/database", "Change failed: "+err.Error())
		return
	}
	a.audit(r, "createdb", slug+"="+verb)
	redirectMsg(w, r, "/p/"+slug+"/database", msg)
}

// setMaxConns changes cluster-wide max_connections via ALTER SYSTEM, then
// restarts Postgres to apply it (max_connections cannot be reloaded live).
// The compose file deliberately does not pin max_connections so the value in
// postgresql.auto.conf written here is the one that wins.
func (a *app) setMaxConns(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("max")))
	if err != nil || n < 10 || n > 2000 {
		redirectErr(w, r, "/p/"+slug+"/database", "Cluster max connections must be between 10 and 2000.")
		return
	}
	if _, err := a.db.Exec(fmt.Sprintf(`ALTER SYSTEM SET max_connections = %d`, n)); err != nil {
		redirectErr(w, r, "/p/"+slug+"/database", "Change failed: "+err.Error())
		return
	}
	a.audit(r, "max-connections", fmt.Sprint(n))
	go func() {
		time.Sleep(500 * time.Millisecond) // let the redirect land first
		exec.Command("docker", "restart", "pgforge-db").Run()
	}()
	redirectMsg(w, r, "/p/"+slug+"/database",
		fmt.Sprintf("Cluster max connections set to %d. Postgres is restarting; give it ~10 seconds.", n))
}

// ----------------------------------------------------------------- backups

type backupFile struct {
	Name, Size, Age string
}

func (a *app) backupsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	var files []backupFile
	dir := "/opt/pgforge-backups/dumps"
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), slug+"-") || !strings.HasSuffix(e.Name(), ".dump") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, backupFile{
			Name: e.Name(),
			Size: humanBytes(info.Size()),
			Age:  info.ModTime().Format("Jan 02, 15:04"),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name > files[j].Name })

	var retention string
	a.db.QueryRow(`SELECT value FROM settings WHERE key='retention_days'`).Scan(&retention)
	var remote string
	if b, err := os.ReadFile("/opt/pgforge/backup_remote"); err == nil {
		remote = strings.TrimSpace(string(b))
	}
	// tier settings + per-tier disk usage for the retention card
	setting := func(key, def string) string {
		v := def
		a.db.QueryRow(`SELECT value FROM settings WHERE key=$1`, key).Scan(&v)
		return v
	}
	type tierSize struct{ Name, Size string }
	var tiers []tierSize
	for _, t := range []struct{ name, path string }{
		{"Dumps", "/opt/pgforge-backups/dumps"}, {"Snapshots", "/opt/pgforge-backups/physical"},
		{"WAL archive", "/opt/pgforge-backups/wal"}, {"Files", "/opt/pgforge-backups/files"},
	} {
		if out, err := exec.Command("du", "-sh", t.path).Output(); err == nil {
			if f := strings.Fields(string(out)); len(f) > 0 {
				tiers = append(tiers, tierSize{t.name, f[0]})
			}
		}
	}

	// PITR window starts at the oldest kept cluster snapshot
	pitrFrom := ""
	if ents, err := os.ReadDir("/opt/pgforge-backups/physical"); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "base-") {
				d := strings.TrimPrefix(e.Name(), "base-")
				if pitrFrom == "" || d < pitrFrom {
					pitrFrom = d
				}
			}
		}
	}
	// off-box listing is a network call - only on explicit request (?offbox=1)
	var offbox []offboxFile
	offboxLoaded := r.URL.Query().Get("offbox") == "1"
	if offboxLoaded && remote != "" {
		offbox = a.offboxList(slug)
	}
	content := renderContent(backupsBody, map[string]any{
		"Slug": slug, "Files": files, "Retention": retention, "Remote": remote,
		"Offbox": offbox, "OffboxLoaded": offboxLoaded,
		"PITRFrom": pitrFrom, "Tiers": tiers,
		"KeepDaily": setting("dump_keep_daily", "7"), "KeepWeekly": setting("dump_keep_weekly", "4"),
		"KeepBase": setting("basebackup_keep", "2"),
	})
	a.renderShell(w, r, shellData{Title: slug + " · Backups", Nav: "backups", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Backups"}}}, content)
}

func (a *app) setRetention(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	days := strings.TrimSpace(r.FormValue("days"))
	n := 0
	fmt.Sscanf(days, "%d", &n)
	if n < 1 || n > 365 {
		redirectErr(w, r, "/p/"+slug+"/backups", "Retention must be between 1 and 365 days.")
		return
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('retention_days',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, fmt.Sprint(n))
	os.WriteFile("/opt/pgforge/retention_days", []byte(fmt.Sprint(n)), 0o644)
	a.audit(r, "retention", fmt.Sprintf("%d days", n))
	redirectMsg(w, r, "/p/"+slug+"/backups", fmt.Sprintf("Retention set to %d days (applies platform-wide).", n))
}

// setKeepAwake toggles the per-project auto-sleep exemption (for production
// apps that must never be marked sleeping, e.g. ones on direct connections
// only where even the <=5-min status lag matters).
func (a *app) setKeepAwake(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	on := r.FormValue("keep_awake") == "on"
	a.db.Exec(`UPDATE projects SET keep_awake=$2 WHERE slug=$1`, slug, on)
	a.audit(r, "keep-awake", fmt.Sprintf("%s=%v", slug, on))
	msg := slug + " will now auto-sleep when idle."
	if on {
		msg = slug + " is pinned awake - it will never auto-sleep."
	}
	redirectMsg(w, r, "/p/"+slug+"/settings", msg)
}

// setSuspendHours sets the platform-wide idle window before a project sleeps.
func (a *app) setSuspendHours(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	n := -1
	fmt.Sscanf(strings.TrimSpace(r.FormValue("hours")), "%d", &n)
	if n < 0 || n > 8760 {
		redirectErr(w, r, "/p/"+slug+"/settings", "Sleep window must be 0 (never) to 8760 hours.")
		return
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('suspend_hours',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, fmt.Sprint(n))
	a.audit(r, "suspend-hours", fmt.Sprint(n))
	msg := fmt.Sprintf("Projects now sleep after %d hours idle (applies platform-wide).", n)
	if n == 0 {
		msg = "Auto-sleep disabled platform-wide."
	}
	redirectMsg(w, r, "/p/"+slug+"/settings", msg)
}

// setRetentionTiers updates the tiered backup knobs: daily dumps kept, weekly
// dumps kept, and standing cluster snapshots. Each is stored in settings AND
// mirrored to /opt/pgforge/<key>, which is what backup.sh actually reads.
func (a *app) setRetentionTiers(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	get := func(name string, lo, hi int) (int, bool) {
		n := -1
		fmt.Sscanf(strings.TrimSpace(r.FormValue(name)), "%d", &n)
		return n, n >= lo && n <= hi
	}
	daily, ok1 := get("daily", 1, 30)
	weekly, ok2 := get("weekly", 0, 12)
	base, ok3 := get("basebackups", 1, 7)
	if !ok1 || !ok2 || !ok3 {
		redirectErr(w, r, "/p/"+slug+"/backups", "Valid ranges: daily 1-30, weekly 0-12, snapshots 1-7.")
		return
	}
	for k, v := range map[string]int{"dump_keep_daily": daily, "dump_keep_weekly": weekly, "basebackup_keep": base} {
		a.db.Exec(`INSERT INTO settings(key,value) VALUES ($1,$2)
			ON CONFLICT (key) DO UPDATE SET value=$2`, k, fmt.Sprint(v))
		os.WriteFile("/opt/pgforge/"+k, []byte(fmt.Sprint(v)), 0o644)
	}
	a.audit(r, "retention-tiers", fmt.Sprintf("daily=%d weekly=%d base=%d", daily, weekly, base))
	redirectMsg(w, r, "/p/"+slug+"/backups", "Backup tiers updated (applies platform-wide from tonight's run).")
}

func (a *app) backupNow(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	out := "/opt/pgforge-backups/dumps"
	os.MkdirAll(out, 0o755)
	// A timestamped manual filename never collides with the nightly
	// <slug>-<date>.dump, and we dump to a .partial then atomically rename, so a
	// failed or in-progress dump can never truncate or masquerade as a good one.
	stamp := time.Now().UTC().Format("2006-01-02-150405")
	final := fmt.Sprintf("%s/%s-%s.dump", out, slug, stamp)
	tmp := final + ".partial"
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c",
		fmt.Sprintf(`docker exec pgforge-db pg_dump -U postgres -Fc -d %q > %q`, slug, tmp))
	cmbOut, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		msg := strings.TrimSpace(string(cmbOut))
		if msg == "" {
			msg = err.Error()
		}
		redirectErr(w, r, "/p/"+slug+"/backups", "Backup failed: "+msg)
		return
	}
	if fi, statErr := os.Stat(tmp); statErr != nil || fi.Size() == 0 {
		os.Remove(tmp)
		redirectErr(w, r, "/p/"+slug+"/backups", "Backup produced no data; nothing saved.")
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		redirectErr(w, r, "/p/"+slug+"/backups", "Backup failed to finalize: "+err.Error())
		return
	}
	fi, _ := os.Stat(final)
	a.audit(r, "backup-now", slug)
	redirectMsg(w, r, "/p/"+slug+"/backups",
		fmt.Sprintf("Backup created: %s (%s).", filepath.Base(final), humanBytes(fi.Size())))
}

// restoreBackup restores a chosen dump back into the project database with
// pg_restore --clean (drops and recreates objects, replacing current data).
func (a *app) restoreBackup(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	file := filepath.Base(r.FormValue("file")) // strip any path
	if !projectDumpOK(slug, file) {
		redirectErr(w, r, "/p/"+slug+"/backups", "That backup does not belong to this project.")
		return
	}
	path := "/opt/pgforge-backups/dumps/" + file
	if _, err := os.Stat(path); err != nil {
		redirectErr(w, r, "/p/"+slug+"/backups", "Backup file not found.")
		return
	}
	// free the database: kick other sessions and drop our cached pool
	a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, slug)
	closeConn(slug)
	// NOTE: no --no-owner. That flag left every restored object owned by
	// postgres with no grants, so the project's own role (and anon/service) got
	// "permission denied" and the app was dead after a restore. Restoring the
	// original ownership + ACLs keeps the app working.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf(
		`docker exec -i pgforge-db pg_restore -U postgres --clean --if-exists -d %q < %q`, slug, path))
	out, err := cmd.CombinedOutput()
	// Re-apply API/Auth roles + grants so the app can read its tables even if the
	// dump predates them or their grants didn't restore cleanly.
	if db, derr := a.dbFor(slug); derr == nil {
		if _, en := a.apiConfig(slug); en {
			a.ensureAPIRoles(db, slug)
		}
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = "..." + msg[len(msg)-200:]
		}
		redirectMsg(w, r, "/p/"+slug+"/backups", "Restore completed with warnings: "+msg)
		a.audit(r, "restore", file)
		return
	}
	a.audit(r, "restore", file)
	redirectMsg(w, r, "/p/"+slug+"/backups", "Restored "+file+" into "+slug+".")
}

// ----------------------------------------------------------------- settings

func (a *app) settingsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	status, _ := a.projectCred(slug)
	var created, size, version, lastActive string
	a.db.QueryRow(`SELECT to_char(created_at,'Mon DD, YYYY'), to_char(last_active,'Mon DD, HH24:MI')
		FROM projects WHERE slug=$1`, slug).Scan(&created, &lastActive)
	a.db.QueryRow(`SELECT pg_size_pretty(pg_database_size($1))`, slug).Scan(&size)
	a.db.QueryRow(`SHOW server_version`).Scan(&version)
	if i := strings.IndexByte(version, ' '); i > 0 {
		version = version[:i]
	}
	// feature status
	feat := func(table string) bool {
		var on bool
		a.db.QueryRow(`SELECT enabled FROM `+table+` WHERE slug=$1`, slug).Scan(&on)
		return on
	}
	var keepAwake, publicStatus bool
	a.db.QueryRow(`SELECT keep_awake, public_status FROM projects WHERE slug=$1`, slug).Scan(&keepAwake, &publicStatus)
	suspendHours := "168"
	a.db.QueryRow(`SELECT value FROM settings WHERE key='suspend_hours'`).Scan(&suspendHours)
	content := renderContent(settingsBody, map[string]any{
		"Slug": slug, "Status": status, "Created": created, "Size": size,
		"Version": version, "LastActive": lastActive, "Domain": a.cfg.domain,
		"API": feat("api_config"), "Auth": feat("auth_config"), "Realtime": feat("realtime_config"),
		"KeepAwake": keepAwake, "SuspendHours": suspendHours, "PublicStatus": publicStatus,
	})
	a.renderShell(w, r, shellData{Title: slug + " · Settings", Nav: "settings", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Settings"}}}, content)
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
