package main

import (
	"fmt"
	"html/template"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Background metrics sampler: every few minutes it records per-project and host
// metrics into metrics_samples, so Monitoring can show 7-day usage graphs.

const hostSlug = "__host__"

func (a *app) startSampler() {
	go func() {
		a.sampleOnce() // seed immediately so charts have a point
		t := time.NewTicker(5 * time.Minute)
		for range t.C {
			a.sampleOnce()
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

func (a *app) sampleOnce() {
	defer func() { recover() }() // never let the sampler crash the process
	// host row
	a.db.Exec(`INSERT INTO metrics_samples(slug, ram_used, cpu_load) VALUES ($1,$2,$3)`,
		hostSlug, ramUsedMB(), cpuLoad())
	// per-project rows
	rows, err := a.db.Query(`SELECT slug FROM projects`)
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
	for _, s := range slugs {
		var size int64
		var conns int
		a.db.QueryRow(`SELECT pg_database_size($1)`, s).Scan(&size)
		a.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=$1`, s).Scan(&conns)
		a.db.Exec(`INSERT INTO metrics_samples(slug, db_size, conns) VALUES ($1,$2,$3)`, s, size, conns)
	}
	a.db.Exec(`DELETE FROM metrics_samples WHERE at < now() - interval '30 days'`)

	a.autoSuspend()
	a.reapPostgREST(15 * time.Minute)
}

// autoSuspend marks projects with no activity for 14 days as suspended (role
// NOLOGIN), freeing pooler/API resources. Any request auto-resumes them.
func (a *app) autoSuspend() {
	// projects with live connections are active right now
	a.db.Exec(`UPDATE projects SET last_active=now()
		WHERE slug IN (SELECT DISTINCT datname FROM pg_stat_activity WHERE datname IS NOT NULL)`)
	rows, err := a.db.Query(`SELECT slug FROM projects
		WHERE status='active' AND last_active < now()-interval '14 days'`)
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
		a.db.Exec(fmt.Sprintf(`ALTER ROLE %s NOLOGIN`, pq.QuoteIdentifier(s)))
		a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, s)
		a.stopPostgREST(s)
		a.db.Exec(`UPDATE projects SET status='suspended' WHERE slug=$1`, s)
		a.auditRaw("system", "-", "auto-suspend", s)
	}
}

type pt struct {
	t float64
	v float64
}

// series returns up to 7 days of (epoch, value) points for a whitelisted column.
func (a *app) series(slug, col string) []pt {
	switch col {
	case "db_size", "conns", "ram_used", "cpu_load":
	default:
		return nil
	}
	q := fmt.Sprintf(`SELECT extract(epoch from at), (%s)::float8 FROM metrics_samples
		WHERE slug=$1 AND at > now()-interval '7 days' AND %s IS NOT NULL ORDER BY at`, col, col)
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
