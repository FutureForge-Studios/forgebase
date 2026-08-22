package main

// Database cron jobs via pg_cron. The extension lives in the control-plane
// database (cron.database_name=pgforge) and every job targets a project's
// database through cron.schedule_in_database, so each project manages only
// its own jobs while one scheduler serves the whole cluster.

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type cronJob struct {
	ID       int64
	Name     string
	Schedule string
	Command  string
	Active   bool
}

type cronRun struct {
	Job, Status, Message string
	Started              string
	Took                 string
}

// cronAvailable installs pg_cron on demand and reports whether it is usable.
func (a *app) cronAvailable() bool {
	a.db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_cron`)
	var ok bool
	a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_cron')`).Scan(&ok)
	return ok
}

func (a *app) cronJobs(dbName string) []cronJob {
	rows, err := a.db.Query(`SELECT jobid, jobname, schedule, command, active
		FROM cron.job WHERE database = $1 ORDER BY jobid`, dbName)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []cronJob
	for rows.Next() {
		var j cronJob
		rows.Scan(&j.ID, &j.Name, &j.Schedule, &j.Command, &j.Active)
		out = append(out, j)
	}
	return out
}

// cronOwns reports whether a job id belongs to this project's database - every
// mutating handler checks it so one project can never touch another's jobs.
func (a *app) cronOwns(dbName string, id string) bool {
	var ok bool
	a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM cron.job WHERE jobid = $1::bigint AND database = $2)`,
		id, dbName).Scan(&ok)
	return ok
}

func (a *app) cronRuns(dbName string) []cronRun {
	rows, err := a.db.Query(`SELECT coalesce(j.jobname,''), d.status, coalesce(d.return_message,''),
			to_char(d.start_time, 'YYYY-MM-DD HH24:MI:SS'),
			coalesce(round(extract(epoch FROM d.end_time - d.start_time)::numeric, 2)::text, '')
		FROM cron.job_run_details d
		JOIN cron.job j ON j.jobid = d.jobid
		WHERE j.database = $1 ORDER BY d.start_time DESC LIMIT 50`, dbName)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []cronRun
	for rows.Next() {
		var r cronRun
		rows.Scan(&r.Job, &r.Status, &r.Message, &r.Started, &r.Took)
		if r.Took != "" {
			r.Took += "s"
		}
		out = append(out, r)
	}
	return out
}

func (a *app) cronPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Slug": slug}
	switch {
	case a.projectMode(slug) == "instance":
		data["Unavailable"] = "Dedicated-instance projects run their own Postgres; database cron is currently for shared-cluster projects."
	case !a.cronAvailable():
		data["Unavailable"] = "pg_cron is not available on this server yet (it arrives with the current database image)."
	default:
		data["Jobs"] = a.cronJobs(slug)
		data["Runs"] = a.cronRuns(slug)
		// keep run history from growing without bound
		a.db.Exec(`DELETE FROM cron.job_run_details WHERE end_time < now() - interval '14 days'`)
	}
	content := renderContent(cronBody, data)
	a.renderShell(w, r, shellData{Title: slug + " · Cron", Nav: "cron", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Cron Jobs"}}}, content)
}

// five space-separated cron fields (pg_cron also accepts step/range/list syntax)
var cronScheduleRe = regexp.MustCompile(`^\s*\S+\s+\S+\s+\S+\s+\S+\s+\S+\s*$`)

func (a *app) cronCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/cron"
	name := strings.TrimSpace(r.FormValue("name"))
	sched := strings.TrimSpace(r.FormValue("schedule"))
	if c := strings.TrimSpace(r.FormValue("custom")); r.FormValue("schedule") == "custom" {
		sched = c
	}
	cmd := strings.TrimSpace(r.FormValue("command"))
	if a.projectMode(slug) == "instance" || !a.cronAvailable() {
		redirectErr(w, r, back, "Cron is not available for this project.")
		return
	}
	if name == "" || cmd == "" {
		redirectErr(w, r, back, "Fill in a name and the SQL command.")
		return
	}
	if !cronScheduleRe.MatchString(sched) {
		redirectErr(w, r, back, "The schedule must be a 5-field cron expression (minute hour day month weekday).")
		return
	}
	if len(a.cronJobs(slug)) >= 50 {
		redirectErr(w, r, back, "Job limit reached (50 per project).")
		return
	}
	// name is namespaced by project so two projects can both have "cleanup"
	if _, err := a.db.Exec(`SELECT cron.schedule_in_database($1, $2, $3, $4)`,
		slug+": "+name, sched, cmd, slug); err != nil {
		redirectErr(w, r, back, "Schedule failed: "+err.Error())
		return
	}
	a.audit(r, "cron-create", slug+"/"+name+" ("+sched+")")
	redirectMsg(w, r, back, "Job scheduled. It runs on \""+sched+"\" against this project's database.")
}

func (a *app) cronToggle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/cron"
	id := r.FormValue("id")
	if !a.cronOwns(slug, id) {
		redirectErr(w, r, back, "Unknown job.")
		return
	}
	active := r.FormValue("action") == "enable"
	if _, err := a.db.Exec(`SELECT cron.alter_job(job_id := $1::bigint, active := $2)`, id, active); err != nil {
		redirectErr(w, r, back, "Change failed: "+err.Error())
		return
	}
	a.audit(r, "cron-toggle", slug+"/#"+id)
	redirectMsg(w, r, back, "Job updated.")
}

func (a *app) cronDelete(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/cron"
	id := r.FormValue("id")
	if !a.cronOwns(slug, id) {
		redirectErr(w, r, back, "Unknown job.")
		return
	}
	if _, err := a.db.Exec(`SELECT cron.unschedule($1::bigint)`, id); err != nil {
		redirectErr(w, r, back, "Delete failed: "+err.Error())
		return
	}
	a.audit(r, "cron-delete", slug+"/#"+id)
	redirectMsg(w, r, back, "Job removed.")
}

// cronRunNow fires a job's command once, immediately, against the project
// database (the scheduled cadence is untouched). 60s cap.
func (a *app) cronRunNow(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/cron"
	id := r.FormValue("id")
	if !a.cronOwns(slug, id) {
		redirectErr(w, r, back, "Unknown job.")
		return
	}
	var cmd string
	a.db.QueryRow(`SELECT command FROM cron.job WHERE jobid = $1::bigint`, id).Scan(&cmd)
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, cmd); err != nil {
		redirectErr(w, r, back, "Run failed: "+err.Error())
		return
	}
	a.audit(r, "cron-run-now", slug+"/#"+id)
	redirectMsg(w, r, back, "Job ran once, successfully.")
}
