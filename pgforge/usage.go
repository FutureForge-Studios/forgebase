package main

// Usage reports: one page that answers "what is this project actually
// consuming?" over the last 30 days - database growth, storage against
// quota, edge invocations, auth activity, webhook deliveries and live
// realtime connections - all from data the platform already collects.

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// rtClientCount reports the live WebSocket connections on a project's hub.
func rtClientCount(slug string) int {
	rtMu.Lock()
	h, ok := rtHubs[slug]
	rtMu.Unlock()
	if !ok {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (a *app) usagePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	var sizeNow, sizeThen int64
	a.db.QueryRow(`SELECT coalesce((SELECT db_size FROM metrics_samples
		WHERE slug=$1 AND db_size IS NOT NULL ORDER BY at DESC LIMIT 1),0)`, slug).Scan(&sizeNow)
	a.db.QueryRow(`SELECT coalesce((SELECT db_size FROM metrics_samples
		WHERE slug=$1 AND db_size IS NOT NULL ORDER BY at ASC LIMIT 1),0)`, slug).Scan(&sizeThen)
	if sizeNow == 0 {
		a.db.QueryRow(`SELECT pg_database_size($1)`, slug).Scan(&sizeNow)
	}

	var edgeCalls, edgeErrs, edgeAvgMs int
	a.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE NOT ok), coalesce(avg(ms),0)::int
		FROM edge_logs WHERE slug=$1 AND at > now() - interval '30 days'`, slug).
		Scan(&edgeCalls, &edgeErrs, &edgeAvgMs)

	var hookCalls, hookErrs int
	a.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE NOT coalesce(ok,false))
		FROM webhook_deliveries WHERE slug=$1 AND at > now() - interval '30 days'`, slug).
		Scan(&hookCalls, &hookErrs)

	// auth activity comes from the project's own database, when enabled
	var users, usersNew, usersActive int
	authOn := false
	if _, on := a.authConfig(slug); on {
		authOn = true
		if db, err := a.dbFor(slug); err == nil {
			db.QueryRow(`SELECT count(*),
					count(*) FILTER (WHERE created_at > now() - interval '30 days'),
					count(*) FILTER (WHERE last_sign_in_at > now() - interval '30 days')
				FROM auth.users`).Scan(&users, &usersNew, &usersActive)
		}
	}

	// database-size sparkline over the sample window
	type pt struct {
		At time.Time
		V  int64
	}
	var pts []pt
	if rows, err := a.db.Query(`SELECT at, db_size FROM metrics_samples
			WHERE slug=$1 AND db_size IS NOT NULL ORDER BY at`, slug); err == nil {
		for rows.Next() {
			var p pt
			if rows.Scan(&p.At, &p.V) == nil {
				pts = append(pts, p)
			}
		}
		rows.Close()
	}
	poly, maxLabel, days := "", "", 0
	if len(pts) >= 2 {
		var maxV int64 = 1
		for _, p := range pts {
			if p.V > maxV {
				maxV = p.V
			}
		}
		span := pts[len(pts)-1].At.Sub(pts[0].At)
		days = int(span.Hours()/24) + 1
		var b strings.Builder
		for _, p := range pts {
			x := 0.0
			if span > 0 {
				x = p.At.Sub(pts[0].At).Seconds() / span.Seconds() * 600
			}
			y := 110 - float64(p.V)/float64(maxV)*100
			fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
		}
		poly = strings.TrimSpace(b.String())
		maxLabel = humanBytes(maxV)
	}

	storB := a.storageUsageBytes(slug)
	quotaB := a.storageQuotaBytes(slug)
	quota := "unlimited"
	if quotaB > 0 {
		quota = humanBytes(quotaB)
	}
	content := renderContent(usageBody, map[string]any{
		"Slug": slug, "SizeNow": humanBytes(sizeNow), "SizeThen": humanBytes(sizeThen),
		"Grew": sizeNow > sizeThen && sizeThen > 0, "Delta": humanBytes(sizeNow - sizeThen),
		"Storage": humanBytes(storB), "Quota": quota,
		"EdgeCalls": edgeCalls, "EdgeErrs": edgeErrs, "EdgeAvgMs": edgeAvgMs,
		"HookCalls": hookCalls, "HookErrs": hookErrs,
		"AuthOn": authOn, "Users": users, "UsersNew": usersNew, "UsersActive": usersActive,
		"RTClients": rtClientCount(slug),
		"Poly":      poly, "MaxLabel": maxLabel, "Days": days,
	})
	a.renderShell(w, r, shellData{Title: slug + " · Usage", Nav: "usage", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Usage"}}}, content)
}

const usageBody = `
<div class="pagehead"><h1>Usage</h1><p>What <b>{{.Slug}}</b> consumed over the last 30 days.</p></div>
<div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(170px,1fr));gap:.8rem;margin-bottom:1rem">
  <div class="card stat"><div class="k">Database size</div><div class="v" style="font-size:18px">{{.SizeNow}}</div>
    {{if .Grew}}<div class="muted" style="font-size:11px">+{{.Delta}} from {{.SizeThen}}</div>{{end}}</div>
  <div class="card stat"><div class="k">Storage</div><div class="v" style="font-size:18px">{{.Storage}}</div>
    <div class="muted" style="font-size:11px">quota {{.Quota}}</div></div>
  <div class="card stat"><div class="k">Function calls</div><div class="v" style="font-size:18px">{{.EdgeCalls}}</div>
    <div class="muted" style="font-size:11px">{{.EdgeErrs}} errors · avg {{.EdgeAvgMs}} ms</div></div>
  <div class="card stat"><div class="k">Webhook deliveries</div><div class="v" style="font-size:18px">{{.HookCalls}}</div>
    <div class="muted" style="font-size:11px">{{.HookErrs}} failed</div></div>
  {{if .AuthOn}}<div class="card stat"><div class="k">Auth users</div><div class="v" style="font-size:18px">{{.Users}}</div>
    <div class="muted" style="font-size:11px">+{{.UsersNew}} new · {{.UsersActive}} active</div></div>{{end}}
  <div class="card stat"><div class="k">Realtime connections</div><div class="v" style="font-size:18px">{{.RTClients}}</div>
    <div class="muted" style="font-size:11px">live right now</div></div>
</div>
{{if .Poly}}
<div class="card">
  <div style="display:flex;align-items:baseline;gap:.6rem"><h2>Database size over {{.Days}} days</h2>
    <span class="muted" style="font-size:11px">peak {{.MaxLabel}}</span></div>
  <svg viewBox="0 0 600 120" preserveAspectRatio="none" style="width:100%;height:120px;display:block;margin-top:.6rem">
    <polyline points="{{.Poly}}" style="fill:none;stroke:hsl(160 84% 30%);stroke-width:1.6"/>
  </svg>
</div>
{{else}}
<div class="card"><p class="muted" style="margin:0">Not enough samples yet for a growth chart - it fills in as the platform collects metrics (every 5 minutes).</p></div>
{{end}}
`
