package main

import (
	"fmt"
	"html/template"
	"os"
	"strconv"
	"strings"
	"time"
)

// Background metrics sampler: every few minutes it records per-project and host
// metrics into metrics_samples, so Monitoring can show 7-day usage graphs.

const hostSlug = "__host__"

func (a *app) startSampler() {
	go func() {
		a.sampleOnce(true) // seed immediately so charts have a point
		t := time.NewTicker(5 * time.Minute)
		tick := 0
		for range t.C {
			tick++
			// Suspended/paused projects change slowly, and the retention DELETE
			// scans history - both run hourly (every 12th tick), not every 5 min.
			a.sampleOnce(tick%12 == 0)
		}
	}()
}

func ramUsedMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var tot, avail int
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) >= 2 {
			if f[0] == "MemTotal:" {
				tot, _ = strconv.Atoi(f[1])
			}
			if f[0] == "MemAvailable:" {
				avail, _ = strconv.Atoi(f[1])
			}
		}
	}
	return (tot - avail) / 1024
}

func cpuLoad() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func (a *app) sampleOnce(full bool) {
	defer func() { recover() }() // never let the sampler crash the process
	// host row
	a.db.Exec(`INSERT INTO metrics_samples(slug, ram_used, cpu_load) VALUES ($1,$2,$3)`,
		hostSlug, ramUsedMB(), cpuLoad())
	// Per-project rows in ONE statement (was 2 queries + 1 insert per project,
	// every 5 minutes, for every project ever created). Suspended/paused
	// projects are included only on the hourly full pass.
	filter := `WHERE p.status NOT IN ('suspended','paused')`
	if full {
		filter = ``
	}
	a.db.Exec(`INSERT INTO metrics_samples(slug, db_size, conns)
		SELECT p.slug, pg_database_size(d.oid), coalesce(s.n,0)
		FROM projects p
		JOIN pg_database d ON d.datname = p.slug
		LEFT JOIN (SELECT datname, count(*) n FROM pg_stat_activity GROUP BY 1) s
		       ON s.datname = p.slug ` + filter)
	if full {
		a.db.Exec(`DELETE FROM metrics_samples WHERE at < now() - interval '30 days'`)
		// Bounded control-plane tables (same idea as webhook_deliveries' 500-row
		// cap): age limit AND a hard row cap, hourly. Nothing here grows forever.
		a.db.Exec(`DELETE FROM audit_log WHERE at < now() - interval '90 days'`)
		a.db.Exec(`DELETE FROM audit_log WHERE id NOT IN (
			SELECT id FROM audit_log ORDER BY at DESC LIMIT 20000)`)
		a.db.Exec(`DELETE FROM edge_logs WHERE at < now() - interval '30 days'`)
		a.db.Exec(`DELETE FROM edge_logs e WHERE e.id NOT IN (
			SELECT id FROM edge_logs e2 WHERE e2.slug = e.slug ORDER BY at DESC LIMIT 200)`)
		a.maybeWeeklyDigest()
		a.pruneStorageCache()
	}
	a.syncInstanceStatus()
	a.expireBranches()
	a.shipLogs()

	a.autoSuspend()
	// "Keep awake" projects are also kept WARM: their API sidecar and realtime
	// listener are exempt from idle reaping, so they never pay a cold start.
	pinned := map[string]bool{}
	if rows, err := a.db.Query(`SELECT slug FROM projects WHERE keep_awake`); err == nil {
		for rows.Next() {
			var s string
			rows.Scan(&s)
			pinned[s] = true
		}
		rows.Close()
	}
	a.reapPostgREST(15*time.Minute, pinned)
	a.reapRealtimeHubs(15*time.Minute, pinned)
}

// clientActivitySubquery matches projects that have real client connections
// right now. The control plane's own connections (meta/editor pools, realtime
// LISTEN backends, PostgREST) and background workers are excluded - otherwise
// a project with a webhook hub or a live-sync subscription would count as
// "active" forever and never sleep.
const clientActivitySubquery = `SELECT DISTINCT datname FROM pg_stat_activity
	WHERE datname IS NOT NULL
	  AND backend_type = 'client backend'
	  AND application_name NOT IN ('pgforged','pgforge-rest')`

// autoSuspend puts projects with no client activity to sleep:
// sleep NEVER blocks logins and never touches data - it only releases what
// actually costs resources (API sidecar, realtime listener, cached pools).
// Waking is automatic: an HTTP request wakes instantly via touchAndResume, and
// a direct database connection (never refused) is noticed here within one
// sampler tick. Manual Pause remains the explicit hard lockout (NOLOGIN).
func (a *app) autoSuspend() {
	// 1) bump activity for projects with live client connections
	a.db.Exec(`UPDATE projects SET last_active=now()
		WHERE slug IN (` + clientActivitySubquery + `)`)

	// 2) wake sleeping projects that received direct connections
	if rows, err := a.db.Query(`UPDATE projects SET status='active', last_active=now()
		WHERE status='suspended' AND slug IN (` + clientActivitySubquery + `)
		RETURNING slug`); err == nil {
		var woke []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			woke = append(woke, s)
		}
		rows.Close()
		for _, s := range woke {
			a.auditRaw("system", "-", "auto-resume", s)
			if a.hasWebhooks(s) {
				a.rtGetHub(s) // restart webhook delivery
			}
		}
	}

	// 3) put idle projects to sleep (window configurable; 0 = never)
	hours := 168
	var v string
	if a.db.QueryRow(`SELECT value FROM settings WHERE key='suspend_hours'`).Scan(&v); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			hours = n
		}
	}
	if hours <= 0 {
		return
	}
	rows, err := a.db.Query(`SELECT slug FROM projects
		WHERE status='active' AND NOT keep_awake AND mode <> 'instance'
		  AND last_active < now() - make_interval(hours => $1)`, hours)
	if err != nil {
		return
	}
	var idle []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		idle = append(idle, s)
	}
	rows.Close()
	for _, s := range idle {
		a.stopPostgREST(s)
		a.stopRealtimeHub(s)
		closeConn(s)
		a.db.Exec(`UPDATE projects SET status='suspended' WHERE slug=$1`, s)
		a.auditRaw("system", "-", "auto-suspend", s)
	}
}

type pt struct {
	t float64
	v float64
}

// series returns up to `days` days of (epoch, value) points for a whitelisted column.
func (a *app) series(slug, col string, days int) []pt {
	switch col {
	case "db_size", "conns", "ram_used", "cpu_load":
	default:
		return nil
	}
	if days < 1 || days > 30 {
		days = 7
	}
	q := fmt.Sprintf(`SELECT extract(epoch from at), (%s)::float8 FROM metrics_samples
		WHERE slug=$1 AND at > now()-make_interval(days => %d) AND %s IS NOT NULL ORDER BY at`, col, days, col)
	rows, err := a.db.Query(q, slug)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []pt
	for rows.Next() {
		var p pt
		rows.Scan(&p.t, &p.v)
		out = append(out, p)
	}
	return out
}

// areaChart renders a compact 7-day area chart as inline SVG.
func areaChart(pts []pt, color string) template.HTML {
	if len(pts) == 0 {
		return template.HTML(`<div class="muted" style="font-size:12px;padding:1.2rem 0;text-align:center">Collecting data - check back soon.</div>`)
	}
	const W, H = 300.0, 64.0
	tmin, tmax := pts[0].t, pts[len(pts)-1].t
	if tmax == tmin {
		tmax = tmin + 1
	}
	var vmax float64
	for _, p := range pts {
		if p.v > vmax {
			vmax = p.v
		}
	}
	if vmax == 0 {
		vmax = 1
	}
	x := func(t float64) float64 { return (t - tmin) / (tmax - tmin) * W }
	y := func(v float64) float64 { return H - 4 - (v/vmax)*(H-8) }
	var line strings.Builder
	for i, p := range pts {
		if i == 0 {
			fmt.Fprintf(&line, "M%.1f %.1f", x(p.t), y(p.v))
		} else {
			fmt.Fprintf(&line, " L%.1f %.1f", x(p.t), y(p.v))
		}
	}
	area := line.String() + fmt.Sprintf(" L%.1f %.1f L%.1f %.1f Z", x(pts[len(pts)-1].t), H, x(pts[0].t), H)
	svg := fmt.Sprintf(`<svg viewBox="0 0 %g %g" preserveAspectRatio="none" style="width:100%%;height:64px;display:block">`+
		`<path d="%s" fill="hsl(var(--primary)/.12)"/>`+
		`<path d="%s" fill="none" stroke="hsl(var(--primary))" stroke-width="1.5"/></svg>`, W, H, area, line.String())

	// time axis labels: start, midpoint, end
	span := tmax - tmin
	fmtT := func(epoch float64) string {
		t := time.Unix(int64(epoch), 0)
		if span > 2*24*3600 {
			return t.Format("Jan 2, 15:04")
		}
		return t.Format("15:04")
	}
	axis := fmt.Sprintf(`<div style="display:flex;justify-content:space-between;font-size:9.5px;color:hsl(var(--muted-fg));margin-top:.25rem">`+
		`<span>%s</span><span>%s</span><span>%s</span></div>`,
		fmtT(tmin), fmtT(tmin+span/2), fmtT(tmax))
	return template.HTML(svg + axis)
}
