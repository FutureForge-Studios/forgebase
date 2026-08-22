package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// hostRe validates a bare hostname (no scheme/path) for the custom status domain.
var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)+$`)

// Public status page, served UNAUTHENTICATED at status.<domain> (and an
// optional custom domain via the status_custom_domain setting). Shows overall
// platform health plus per-project health for projects that OPTED IN
// (projects.public_status) - project names never leak by default. Uptime bars
// come from the metrics sampler's host heartbeat: one sample every 5 minutes,
// so missing samples in a day = downtime that day.

type statusDay struct {
	Label string  // "Aug 22"
	Pct   float64 // 0..100
	Class string  // ok / warn / down / nodata
}

type statusProj struct {
	Slug  string
	State string // operational / on-demand / maintenance / down
	Class string
}

func (a *app) statusPage(w http.ResponseWriter, r *http.Request) {
	// overall health = can the control plane reach Postgres right now
	ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
	defer cancel()
	dbOK := a.db.PingContext(ctx) == nil

	// last-30-day uptime bars from the host heartbeat (288 samples/day)
	days := make([]statusDay, 0, 30)
	counts := map[string]int{}
	if rows, err := a.db.Query(`SELECT to_char(date_trunc('day', at), 'YYYY-MM-DD'), count(*)
		FROM metrics_samples WHERE slug=$1 AND at > now() - interval '30 days'
		GROUP BY 1`, hostSlug); err == nil {
		for rows.Next() {
			var d string
			var n int
			rows.Scan(&d, &n)
			counts[d] = n
		}
		rows.Close()
	}
	now := time.Now().UTC()
	uptimeSum, uptimeDays := 0.0, 0
	for i := 29; i >= 0; i-- {
		day := now.AddDate(0, 0, -i)
		key := day.Format("2006-01-02")
		expected := 288.0
		if i == 0 { // today is partial
			expected = float64(day.Hour()*12 + day.Minute()/5)
			if expected < 1 {
				expected = 1
			}
		}
		n := counts[key]
		pct := float64(n) / expected * 100
		if pct > 100 {
			pct = 100
		}
		cls := "ok"
		switch {
		case n == 0 && i > 0:
			cls = "nodata"
		case pct < 95:
			cls = "down"
		case pct < 99.5:
			cls = "warn"
		}
		if n > 0 || i == 0 {
			uptimeSum += pct
			uptimeDays++
		}
		days = append(days, statusDay{Label: day.Format("Jan 2"), Pct: pct, Class: cls})
	}
	uptime := 100.0
	if uptimeDays > 0 {
		uptime = uptimeSum / float64(uptimeDays)
	}

	// opted-in projects with a live health probe each
	var projs []statusProj
	if rows, err := a.db.Query(`SELECT slug, status FROM projects WHERE public_status ORDER BY slug`); err == nil {
		type pr struct{ slug, status string }
		var list []pr
		for rows.Next() {
			var p pr
			rows.Scan(&p.slug, &p.status)
			list = append(list, p)
		}
		rows.Close()
		for _, p := range list {
			sp := statusProj{Slug: p.slug, State: "operational", Class: "ok"}
			switch p.status {
			case "paused":
				sp.State, sp.Class = "maintenance", "warn"
			case "suspended":
				// sleeping is HEALTHY: it wakes on the next request
				sp.State, sp.Class = "operational (on-demand)", "ok"
			default:
				pctx, pcancel := context.WithTimeout(r.Context(), 900*time.Millisecond)
				if db, err := a.dbFor(p.slug); err != nil || db.PingContext(pctx) != nil {
					sp.State, sp.Class = "down", "down"
				}
				pcancel()
			}
			projs = append(projs, sp)
		}
	}

	banner, bannerClass := "All systems operational", "ok"
	if !dbOK {
		banner, bannerClass = "Major outage - database unreachable", "down"
	} else {
		for _, p := range projs {
			if p.Class == "down" {
				banner, bannerClass = "Partial outage", "warn"
				break
			}
		}
	}

	// manual incident notes: an unresolved incident overrides the banner, and
	// the last few resolved ones form a short history
	type incident struct{ Title, Note, Started, Resolved string }
	var active *incident
	var history []incident
	if rows, err := a.db.Query(`SELECT title, note, to_char(started_at,'Mon DD, HH24:MI'),
		coalesce(to_char(resolved_at,'Mon DD, HH24:MI'),'') FROM incidents
		ORDER BY started_at DESC LIMIT 6`); err == nil {
		for rows.Next() {
			var in incident
			rows.Scan(&in.Title, &in.Note, &in.Started, &in.Resolved)
			if in.Resolved == "" && active == nil {
				v := in
				active = &v
			} else if in.Resolved != "" {
				history = append(history, in)
			}
		}
		rows.Close()
	}
	if active != nil && bannerClass == "ok" {
		banner, bannerClass = active.Title, "warn"
	}

	title := "ForgeBase Status"
	if v := ""; true {
		if a.db.QueryRow(`SELECT value FROM settings WHERE key='status_title'`).Scan(&v); strings.TrimSpace(v) != "" {
			title = strings.TrimSpace(v)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=30")
	statusTmpl.Execute(w, map[string]any{
		"Title": title, "Banner": banner, "BannerClass": bannerClass, "Days": days,
		"Incident": active, "History": history,
		"Uptime": fmt.Sprintf("%.2f", uptime), "Projs": projs,
		"Domain": a.cfg.domain, "At": time.Now().UTC().Format("Jan 2, 2006 15:04 UTC"),
	})
}

// ----------------------------------------------------------------- incidents

// saveIncident opens a new incident or updates the note of the active one.
// Shown as a banner on the public status page until resolved.
func (a *app) saveIncident(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	note := strings.TrimSpace(r.FormValue("note"))
	if title == "" {
		redirectErr(w, r, "/system", "Incident needs a title.")
		return
	}
	var id int64
	a.db.QueryRow(`SELECT id FROM incidents WHERE resolved_at IS NULL ORDER BY started_at DESC LIMIT 1`).Scan(&id)
	if id > 0 {
		a.db.Exec(`UPDATE incidents SET title=$2, note=$3 WHERE id=$1`, id, title, note)
		a.audit(r, "incident-update", title)
		redirectMsg(w, r, "/system", "Incident updated on the status page.")
		return
	}
	a.db.Exec(`INSERT INTO incidents(title, note) VALUES ($1,$2)`, title, note)
	a.audit(r, "incident-open", title)
	go a.notifyDiscord("🟠 Incident opened: " + title)
	redirectMsg(w, r, "/system", "Incident is now showing on the status page.")
}

func (a *app) resolveIncident(w http.ResponseWriter, r *http.Request) {
	var title string
	a.db.QueryRow(`UPDATE incidents SET resolved_at=now() WHERE resolved_at IS NULL RETURNING title`).Scan(&title)
	if title == "" {
		redirectMsg(w, r, "/system", "No active incident.")
		return
	}
	a.audit(r, "incident-resolve", title)
	go a.notifyDiscord("🟢 Incident resolved: " + title)
	redirectMsg(w, r, "/system", "Incident marked resolved.")
}

// setPublicStatus toggles a project's presence on the public status page.
func (a *app) setPublicStatus(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	on := r.FormValue("public_status") == "on"
	a.db.Exec(`UPDATE projects SET public_status=$2 WHERE slug=$1`, slug, on)
	a.audit(r, "public-status", fmt.Sprintf("%s=%v", slug, on))
	msg := slug + " removed from the public status page."
	if on {
		msg = slug + " now appears on the public status page (status." + a.cfg.domain + ")."
	}
	redirectMsg(w, r, "/p/"+slug+"/settings", msg)
}

var statusDomainCache atomic.Value // string

// statusCustomDomain returns the optional extra hostname that serves the page.
// Cached: rootHandler consults it on EVERY inbound request, and a per-request
// settings query would be a hot-path amplifier.
func (a *app) statusCustomDomain() string {
	if v := statusDomainCache.Load(); v != nil {
		return v.(string)
	}
	var v string
	err := a.db.QueryRow(`SELECT value FROM settings WHERE key='status_custom_domain'`).Scan(&v)
	v = strings.TrimSpace(v)
	if err != nil && err != sql.ErrNoRows {
		return v // transient error: do not cache
	}
	statusDomainCache.Store(v)
	return v
}

func (a *app) setStatusDomain(w http.ResponseWriter, r *http.Request) {
	// the same form carries the optional page title
	if t := strings.TrimSpace(r.FormValue("title")); len(t) <= 60 {
		a.db.Exec(`INSERT INTO settings(key,value) VALUES ('status_title',$1)
			ON CONFLICT (key) DO UPDATE SET value=$1`, t)
	}
	d := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))
	if d != "" && !hostRe.MatchString(d) {
		redirectErr(w, r, "/system", "That does not look like a valid hostname.")
		return
	}
	// The custom status domain is matched FIRST in rootHandler - accepting one
	// of our own hostnames would shadow the panel or a project API and lock
	// the operator out of that hostname entirely.
	if d != "" {
		for _, own := range []string{a.cfg.domain, a.secondaryDomain()} {
			if own != "" && (d == own || strings.HasSuffix(d, "."+own)) {
				redirectErr(w, r, "/system", "Use an outside hostname (like status.yourcompany.com) - "+own+" and its subdomains already belong to the platform (status."+own+" works out of the box).")
				return
			}
		}
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('status_custom_domain',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, d)
	statusDomainCache.Store(d)
	a.audit(r, "status-domain", d)
	if d == "" {
		redirectMsg(w, r, "/system", "Custom status domain cleared (status."+a.cfg.domain+" keeps working).")
		return
	}
	redirectMsg(w, r, "/system", "Status page also serves at https://"+d+" - point its DNS at this server and TLS is automatic.")
}

var statusTmpl = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<meta http-equiv="refresh" content="60">
<style>` + cssDesign + `
.swrap{max-width:760px;margin:0 auto;padding:3rem 1.2rem}
.sbanner{border-radius:1rem;padding:1.1rem 1.4rem;font-weight:600;font-size:16px;display:flex;align-items:center;gap:.7rem}
.sbanner.ok{background:hsl(var(--primary)/.12);color:hsl(var(--primary));border:1px solid hsl(var(--primary)/.3)}
.sbanner.warn{background:hsl(var(--warn)/.12);color:hsl(var(--warn));border:1px solid hsl(var(--warn)/.3)}
.sbanner.down{background:hsl(var(--destructive)/.1);color:hsl(var(--destructive));border:1px solid hsl(var(--destructive)/.3)}
.dot{width:10px;height:10px;border-radius:50%;background:currentColor;animation:pulse 2s ease-in-out infinite}
.bars{display:flex;gap:2px;margin:.6rem 0 .2rem}
.bars .b{flex:1;height:34px;border-radius:3px;background:hsl(var(--primary)/.75)}
.bars .b.warn{background:hsl(var(--warn)/.8)}
.bars .b.down{background:hsl(var(--destructive)/.8)}
.bars .b.nodata{background:hsl(var(--muted))}
.bars .b:hover{outline:2px solid hsl(var(--fg)/.25)}
.srow{display:flex;align-items:center;gap:.8rem;padding:.85rem .2rem;border-bottom:1px solid hsl(var(--border))}
.srow:last-child{border-bottom:none}
.sstate.ok{color:hsl(var(--primary))}.sstate.warn{color:hsl(var(--warn))}.sstate.down{color:hsl(var(--destructive))}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.4}}
</style></head><body><div class="aurora"></div>
<div class="swrap">
  <div style="display:flex;align-items:center;gap:.7rem;margin-bottom:1.6rem">
    <div style="font-family:var(--serif);font-size:22px;font-weight:600">{{.Title}}</div>
    <div class="spacer" style="flex:1"></div>
    <div class="muted" style="font-size:12px">{{.At}}</div>
  </div>
  <div class="sbanner {{.BannerClass}}"><span class="dot"></span> {{.Banner}}</div>
  {{if .Incident}}<div class="card" style="margin-top:.8rem;border-color:hsl(var(--warn)/.4)">
    <div style="display:flex;gap:.6rem;align-items:baseline"><h2 style="font-size:15px">{{.Incident.Title}}</h2>
    <div class="spacer" style="flex:1"></div><span class="muted" style="font-size:11.5px">since {{.Incident.Started}} UTC</span></div>
    {{if .Incident.Note}}<p class="muted" style="font-size:13px;margin:.4rem 0 0">{{.Incident.Note}}</p>{{end}}
  </div>{{end}}
  <div class="card" style="margin-top:1.2rem">
    <div style="display:flex;align-items:baseline;gap:.7rem">
      <h2 style="font-size:15px">Platform</h2>
      <div class="spacer" style="flex:1"></div>
      <div class="muted" style="font-size:12.5px">{{.Uptime}}% uptime · last 30 days</div>
    </div>
    <div class="bars">{{range .Days}}<div class="b {{.Class}}" title="{{.Label}}: {{printf "%.1f" .Pct}}%"></div>{{end}}</div>
    <div style="display:flex;justify-content:space-between;font-size:10.5px;color:hsl(var(--muted-fg))"><span>30 days ago</span><span>today</span></div>
  </div>
  {{if .Projs}}
  <div class="card" style="margin-top:1rem">
    <h2 style="font-size:15px;margin-bottom:.4rem">Services</h2>
    {{range .Projs}}
    <div class="srow">
      <span style="font-weight:600;font-size:14px">{{.Slug}}</span>
      <div class="spacer" style="flex:1"></div>
      <span class="sstate {{.Class}}" style="font-size:13px;font-weight:600">{{.State}}</span>
    </div>
    {{end}}
  </div>
  {{end}}
  {{if .History}}
  <div class="card" style="margin-top:1rem">
    <h2 style="font-size:15px;margin-bottom:.4rem">Past incidents</h2>
    {{range .History}}
    <div class="srow"><div><div style="font-weight:600;font-size:13.5px">{{.Title}}</div>
      {{if .Note}}<div class="muted" style="font-size:12px">{{.Note}}</div>{{end}}</div>
      <div class="spacer" style="flex:1"></div>
      <span class="muted" style="font-size:11.5px;white-space:nowrap">{{.Started}} &rarr; {{.Resolved}} UTC</span></div>
    {{end}}
  </div>
  {{end}}
  <p class="muted" style="text-align:center;font-size:11.5px;margin-top:2rem">Powered by ForgeBase · auto-refreshes every minute</p>
</div></body></html>`))
