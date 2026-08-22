package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/lib/pq"
	"time"
)

var branchNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// ----------------------------------------------------------------- monitoring

type barRow struct {
	Label, Disp string
	Pct         int
}

type topQuery struct {
	Query   string
	Calls   int64
	MeanMs  string
	TotalMs string
}

func (a *app) monitoringPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	// chart window: 24h / 7d (default) / 30d
	rangeName := r.URL.Query().Get("range")
	rangeDays := 7
	switch rangeName {
	case "24h":
		rangeDays = 1
	case "30d":
		rangeDays = 30
	default:
		rangeName = "7d"
	}
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var sizeBytes int64
	var sizePretty string
	a.db.QueryRow(`SELECT pg_database_size($1), pg_size_pretty(pg_database_size($1))`, slug).Scan(&sizeBytes, &sizePretty)
	var conns, maxConns int
	a.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=$1`, slug).Scan(&conns)
	a.db.QueryRow(`SELECT setting::int FROM pg_settings WHERE name='max_connections'`).Scan(&maxConns)

	// database-level counters (cumulative since stats reset)
	var deadlocks, commits, rollbacks, tupIns, tupUpd, tupDel, tempFiles int64
	a.db.QueryRow(`SELECT deadlocks, xact_commit, xact_rollback,
		tup_inserted, tup_updated, tup_deleted, temp_files
		FROM pg_stat_database WHERE datname=$1`, slug).
		Scan(&deadlocks, &commits, &rollbacks, &tupIns, &tupUpd, &tupDel, &tempFiles)

	// host RAM + CPU load
	hs := a.hostStats()
	load := "-"
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			load = f[0]
		}
	}
	cores := runtime.NumCPU()

	// cache hit ratio (this database)
	var hit float64
	db.QueryRow(`SELECT coalesce(sum(heap_blks_hit)::float/nullif(sum(heap_blks_hit+heap_blks_read),0),1)
		FROM pg_statio_user_tables`).Scan(&hit)

	// table sizes (top 10)
	var tbars []barRow
	var maxSize int64
	rows, _ := db.Query(`SELECT relname, pg_total_relation_size(c.oid)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind='r' ORDER BY 2 DESC LIMIT 10`)
	type ts struct {
		name string
		sz   int64
	}
	var tss []ts
	if rows != nil {
		for rows.Next() {
			var t ts
			rows.Scan(&t.name, &t.sz)
			if t.sz > maxSize {
				maxSize = t.sz
			}
			tss = append(tss, t)
		}
		rows.Close()
	}
	for _, t := range tss {
		pct := 4
		if maxSize > 0 {
			pct = int(t.sz * 100 / maxSize)
			if pct < 4 {
				pct = 4
			}
		}
		tbars = append(tbars, barRow{Label: t.name, Disp: humanBytes(t.sz), Pct: pct})
	}

	// top queries (pg_stat_statements, if available in this DB)
	var tqs []topQuery
	pssOK := false
	if rows, err := db.Query(`SELECT query, calls,
			to_char(mean_exec_time,'FM999990.00'), to_char(total_exec_time,'FM999990.0')
		FROM pg_stat_statements s JOIN pg_database d ON d.oid=s.dbid
		WHERE d.datname=current_database()
		ORDER BY total_exec_time DESC LIMIT 8`); err == nil {
		pssOK = true
		for rows.Next() {
			var q topQuery
			rows.Scan(&q.Query, &q.Calls, &q.MeanMs, &q.TotalMs)
			if len(q.Query) > 90 {
				q.Query = q.Query[:90] + "…"
			}
			tqs = append(tqs, q)
		}
		rows.Close()
	}

	// storage meter vs disk (rough): show DB size against total disk
	// per-database activity attribution: what THIS project costs the cluster
	type attrRow struct {
		Xacts, TupWrites, TempMB, Deadlocks int64
		CacheHitPct                         int
		Backends                            int
	}
	var attr attrRow
	if db, err := a.dbFor(slug); err == nil {
		db.QueryRow(`SELECT xact_commit + xact_rollback,
				tup_inserted + tup_updated + tup_deleted,
				temp_bytes/1024/1024, deadlocks,
				CASE WHEN blks_hit + blks_read = 0 THEN 100
					ELSE (blks_hit * 100 / (blks_hit + blks_read)) END,
				numbackends
			FROM pg_stat_database WHERE datname = current_database()`).
			Scan(&attr.Xacts, &attr.TupWrites, &attr.TempMB, &attr.Deadlocks, &attr.CacheHitPct, &attr.Backends)
	}
	content := renderContent(monitoringBody, map[string]any{
		"Attr": attr,
		"Slug": slug, "Size": sizePretty, "Conns": conns, "MaxConns": maxConns,
		"ConnPct": pctInt(conns, maxConns), "Hit": fmt.Sprintf("%.1f", hit*100),
		"HitPct": int(hit * 100), "Tables": tbars, "Top": tqs, "PSS": pssOK,
		"Deadlocks": deadlocks, "Commits": commits, "Rollbacks": rollbacks,
		"TupIns": tupIns, "TupUpd": tupUpd, "TupDel": tupDel, "TempFiles": tempFiles,
		"RAMUsed": hs.RAMUsed, "RAMTotal": hs.RAMTotal, "Load": load, "Cores": cores,
		"ChartSize": areaChart(a.series(slug, "db_size", rangeDays), ""),
		"ChartConn": areaChart(a.series(slug, "conns", rangeDays), ""),
		"ChartRAM":  areaChart(a.series(hostSlug, "ram_used", rangeDays), ""),
		"ChartCPU":  areaChart(a.series(hostSlug, "cpu_load", rangeDays), ""),
		"Range":     rangeName,
	})
	a.renderShell(w, r, shellData{Title: slug + " · Monitoring", Nav: "monitoring", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Monitoring"}}}, content)
}

// ----------------------------------------------------------------- logs

func (a *app) logsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	// audit trail for this project, filterable by time range, action and text
	rng := r.URL.Query().Get("rng")
	ivals := map[string]string{"1h": "1 hour", "24h": "24 hours", "7d": "7 days", "30d": "30 days"}
	if _, ok := ivals[rng]; !ok {
		rng = "7d"
	}
	act := strings.TrimSpace(r.URL.Query().Get("act"))
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	type logRow struct{ At, Actor, IP, Action, Target string }
	var events []logRow
	rows, _ := a.db.Query(`SELECT to_char(at,'Mon DD, HH24:MI:SS'), actor, coalesce(ip,'-'),
			action, coalesce(detail->>'target','')
		FROM audit_log
		WHERE (detail->>'target' = $1
		   OR detail->>'target' LIKE $1 || '/%'
		   OR detail->>'target' LIKE $1 || ' %')
		  AND at > now() - ($2)::interval
		  AND ($3 = '' OR action ILIKE '%'||$3||'%')
		  AND ($4 = '' OR coalesce(detail->>'target','') ILIKE '%'||$4||'%')
		ORDER BY at DESC LIMIT 200`, slug, ivals[rng], act, search)
	if rows != nil {
		for rows.Next() {
			var l logRow
			rows.Scan(&l.At, &l.Actor, &l.IP, &l.Action, &l.Target)
			events = append(events, l)
		}
		rows.Close()
	}
	// live activity on this project - full statement + client address + state
	type actRow struct{ PID, State, Client, Query, Started, Dur string }
	var acts []actRow
	rows2, _ := a.db.Query(`SELECT pid, coalesce(state,''), coalesce(host(client_addr),'local'),
			coalesce(left(regexp_replace(query,'\s+',' ','g'),120),''),
			coalesce(to_char(query_start,'HH24:MI:SS'),''),
			coalesce(to_char(now()-query_start,'MI:SS'),'')
		FROM pg_stat_activity WHERE datname=$1 AND state IS NOT NULL
		ORDER BY query_start DESC NULLS LAST LIMIT 20`, slug)
	if rows2 != nil {
		for rows2.Next() {
			var ar actRow
			rows2.Scan(&ar.PID, &ar.State, &ar.Client, &ar.Query, &ar.Started, &ar.Dur)
			acts = append(acts, ar)
		}
		rows2.Close()
	}
	// slow statements for this database (pg_stat_statements is preloaded
	// cluster-wide; the view lives in the control-plane db)
	a.db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_stat_statements`)
	type slowRow struct {
		Query       string
		Calls, Rows int64
		Mean, Total string
	}
	var slow []slowRow
	if srows, err := a.db.Query(`SELECT left(regexp_replace(s.query,'[[:space:]]+',' ','g'),160),
			s.calls, s.rows, round(s.mean_exec_time::numeric,2)::text,
			round((s.total_exec_time/1000)::numeric,2)::text
		FROM pg_stat_statements s JOIN pg_database d ON d.oid = s.dbid
		WHERE d.datname = $1 AND s.query NOT ILIKE '%pg_stat_statements%'
		ORDER BY s.mean_exec_time DESC LIMIT 15`, slug); err == nil {
		for srows.Next() {
			var sr slowRow
			srows.Scan(&sr.Query, &sr.Calls, &sr.Rows, &sr.Mean, &sr.Total)
			slow = append(slow, sr)
		}
		srows.Close()
	}
	type logView struct{ ID, Name, Rng, Act, Q string }
	var views []logView
	if vrows, err := a.db.Query(`SELECT id, name, rng, act, q FROM log_views WHERE slug=$1 ORDER BY name`, slug); err == nil {
		for vrows.Next() {
			var v logView
			vrows.Scan(&v.ID, &v.Name, &v.Rng, &v.Act, &v.Q)
			views = append(views, v)
		}
		vrows.Close()
	}
	var shipURL string
	a.db.QueryRow(`SELECT coalesce(log_ship_url,'') FROM projects WHERE slug=$1`, slug).Scan(&shipURL)
	content := renderContent(logsBody, map[string]any{"Slug": slug, "Events": events, "Acts": acts,
		"Rng": rng, "Act": act, "Query": search, "Slow": slow, "Views": views, "ShipURL": shipURL})
	a.renderShell(w, r, shellData{Title: slug + " · Logs", Nav: "logs", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Logs"}}}, content)
}

// globalAuditPage is the platform-wide audit log: every login, registration,
// and privileged action across all projects.
func (a *app) globalAuditPage(w http.ResponseWriter, r *http.Request) {
	type logRow struct{ At, Actor, IP, Action, Target string }
	var events []logRow
	rows, _ := a.db.Query(`SELECT to_char(at,'Mon DD, HH24:MI:SS'), actor, coalesce(ip,'-'),
		action, coalesce(detail->>'target','') FROM audit_log ORDER BY at DESC LIMIT 300`)
	if rows != nil {
		for rows.Next() {
			var l logRow
			rows.Scan(&l.At, &l.Actor, &l.IP, &l.Action, &l.Target)
			events = append(events, l)
		}
		rows.Close()
	}
	var logins, failed int
	a.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action IN ('login','user-login') AND at > now()-interval '7 days'`).Scan(&logins)
	a.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='login-failed' AND at > now()-interval '7 days'`).Scan(&failed)
	content := renderContent(auditBody, map[string]any{"Events": events, "Logins": logins, "Failed": failed})
	a.renderShell(w, r, shellData{Title: "Audit log", Nav: "audit",
		Crumbs: []crumb{{Label: "Audit log"}}}, content)
}

// ----------------------------------------------------------------- branches
//
// Shared-cluster branching: a branch is a plain full
// copy - a database created with CREATE DATABASE <branch> TEMPLATE <source>. It
// gets its own role + credentials + connection string, tracked in projects.

func (a *app) branchesPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	// branches are projects whose name starts with "<slug>-"
	type br struct {
		Slug, Created, Size, Conn, Expires string
	}
	var branches []br
	rows, _ := a.db.Query(`SELECT slug, to_char(created_at,'Mon DD, YYYY'),
		coalesce(pg_size_pretty(pg_database_size(slug)),'-'),
		pgp_sym_decrypt(password_enc,$2),
		coalesce(to_char(expires_at,'Mon DD, HH24:MI'),'')
		FROM projects WHERE parent=$1 ORDER BY created_at`, slug, string(a.cfg.secret))
	if rows != nil {
		for rows.Next() {
			var b br
			var pw string
			rows.Scan(&b.Slug, &b.Created, &b.Size, &pw, &b.Expires)
			b.Conn = fmt.Sprintf("postgresql://%s:%s@%s:5432/%s?sslmode=require", b.Slug, pw, a.cfg.domain, b.Slug)
			branches = append(branches, b)
		}
		rows.Close()
	}
	content := renderContent(branchesBody, map[string]any{"Slug": slug, "Branches": branches})
	a.renderShell(w, r, shellData{Title: slug + " · Branches", Nav: "branches", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Branches"}}}, content)
}

func (a *app) branchCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := strings.TrimSpace(strings.ToLower(r.FormValue("name")))
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	if !branchNameRe.MatchString(name) {
		redirectErr(w, r, "/p/"+slug+"/branches", "Branch name must be 1-20 chars: a-z, 0-9, _.")
		return
	}
	// Only one level of branching, so project delete's single-level cascade
	// (WHERE parent=$1) can never orphan a branch-of-a-branch.
	var parent string
	a.db.QueryRow(`SELECT coalesce(parent,'') FROM projects WHERE slug=$1`, slug).Scan(&parent)
	if parent != "" {
		redirectErr(w, r, "/p/"+slug+"/branches", "Cannot branch a branch; branch the parent project instead.")
		return
	}
	branch := slug + "-" + name
	var expires any // NULL = never
	switch r.FormValue("expires") {
	case "1d":
		expires = time.Now().Add(24 * time.Hour)
	case "7d":
		expires = time.Now().Add(7 * 24 * time.Hour)
	case "30d":
		expires = time.Now().Add(30 * 24 * time.Hour)
	}
	if len(branch) > 63 { // Postgres identifier limit; silently truncated names become undeletable
		redirectErr(w, r, "/p/"+slug+"/branches", "Combined branch name is too long; use a shorter branch name.")
		return
	}
	if a.projectExists(branch) {
		redirectErr(w, r, "/p/"+slug+"/branches", "A branch named "+branch+" already exists.")
		return
	}
	// Dedicated-instance parents get INSTANT copy-on-write branches: a btrfs
	// snapshot + fresh container (~2s), no lock on the parent, and the branch
	// shares unchanged disk blocks with it.
	if a.projectMode(slug) == "instance" {
		bpw := randHex(18)
		if _, err := pgInstance(3*time.Minute, "branch", slug, branch, bpw); err != nil {
			redirectErr(w, r, "/p/"+slug+"/branches", "Instant branch failed: "+err.Error())
			return
		}
		if _, err := a.db.Exec(`INSERT INTO projects(slug, role_name, password_enc, parent, mode, expires_at)
			VALUES ($1,$1,pgp_sym_encrypt($2,$3),$4,'instance',$5)`,
			branch, bpw, string(a.cfg.secret), slug, expires); err != nil {
			pgInstance(time.Minute, "delete", branch)
			redirectErr(w, r, "/p/"+slug+"/branches", "Branch record failed: "+err.Error())
			return
		}
		a.audit(r, "branch-create-instant", branch)
		redirectMsg(w, r, "/p/"+slug+"/branches", "Instant branch "+branch+" is ready (copy-on-write - it shares storage with "+slug+").")
		return
	}
	pw := randHex(18)
	bq := pq.QuoteIdentifier(branch)
	src := pq.QuoteIdentifier(slug)

	// CREATE DATABASE ... TEMPLATE needs zero sessions on the source. Block new
	// connections (this also stops the pooler reconnecting), terminate the rest,
	// copy, then always re-open connections to the source - via defer so even a
	// panic mid-copy can't leave the source permanently refusing connections.
	closeConn(slug)
	a.db.Exec(fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS false`, src))
	defer a.db.Exec(fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS true`, src))
	a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, slug)

	if _, err := a.db.Exec(fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD %s CONNECTION LIMIT 20`, bq, pq.QuoteLiteral(pw))); err != nil {
		redirectErr(w, r, "/p/"+slug+"/branches", "Branch failed: "+err.Error())
		return
	}
	_, cerr := a.db.Exec(fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s OWNER %s`, bq, src, bq))
	if cerr != nil {
		a.db.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, bq))
		a.db.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS %s`, bq))
		redirectErr(w, r, "/p/"+slug+"/branches", "Branch failed: "+cerr.Error())
		return
	}
	a.db.Exec(fmt.Sprintf(`REVOKE CONNECT ON DATABASE %s FROM PUBLIC`, bq))
	a.db.Exec(`INSERT INTO projects(slug, role_name, password_enc, parent, expires_at) VALUES ($1,$1,pgp_sym_encrypt($2,$3),$4,$5)`,
		branch, pw, string(a.cfg.secret), slug, expires)
	if warn := a.scrubBranch(branch, r.FormValue("anonymize")); warn != "" {
		a.rewriteUserlist()
		a.audit(r, "branch", branch+" (anonymized, with warnings)")
		redirectMsg(w, r, "/p/"+branch, "Branch "+branch+" created; anonymization notes: "+warn)
		return
	}
	a.rewriteUserlist()
	a.audit(r, "branch", branch)
	redirectMsg(w, r, "/p/"+branch, "Branch "+branch+" created from "+slug+".")
}

// saveLogView stores the current log filters as a named quick view.
func (a *app) saveLogView(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		redirectErr(w, r, "/p/"+slug+"/logs", "Give the view a name.")
		return
	}
	a.db.Exec(`INSERT INTO log_views(slug, name, rng, act, q) VALUES ($1,$2,$3,$4,$5)`,
		slug, name, r.FormValue("rng"), r.FormValue("act"), r.FormValue("q"))
	redirectMsg(w, r, "/p/"+slug+"/logs?rng="+r.FormValue("rng")+"&act="+r.FormValue("act")+"&q="+r.FormValue("q"), "View saved.")
}

func (a *app) deleteLogView(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a.db.Exec(`DELETE FROM log_views WHERE id=$1 AND slug=$2`, r.FormValue("id"), slug)
	redirectMsg(w, r, "/p/"+slug+"/logs", "View removed.")
}

// setLogShip stores a webhook URL that receives this project's last-day logs
// nightly as JSON (empty disables shipping).
func (a *app) setLogShip(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	u := strings.TrimSpace(r.FormValue("url"))
	if u != "" && !strings.HasPrefix(u, "https://") {
		redirectErr(w, r, "/p/"+slug+"/logs", "Ship URL must be https.")
		return
	}
	a.db.Exec(`UPDATE projects SET log_ship_url=$2 WHERE slug=$1`, slug, u)
	a.audit(r, "log-ship", slug)
	redirectMsg(w, r, "/p/"+slug+"/logs", "Log shipping saved - a JSON bundle posts nightly after the backup run.")
}

// shipLogs posts each opted-in project's last-24h audit + edge logs to its
// webhook. Called once per day from the sampler.
func (a *app) shipLogs() {
	rows, err := a.db.Query(`SELECT slug, log_ship_url FROM projects
		WHERE coalesce(log_ship_url,'') <> ''
		  AND (log_shipped_at IS NULL OR log_shipped_at < now() - interval '23 hours')`)
	if err != nil {
		return
	}
	type tgt struct{ slug, url string }
	var tgts []tgt
	for rows.Next() {
		var t tgt
		rows.Scan(&t.slug, &t.url)
		tgts = append(tgts, t)
	}
	rows.Close()
	for _, t := range tgts {
		payload := map[string]any{"project": t.slug, "audit": []map[string]any{}, "edge": []map[string]any{}}
		if ar, err := a.db.Query(`SELECT to_char(at,'YYYY-MM-DD"T"HH24:MI:SS"Z"'), actor, action, coalesce(detail->>'target','')
			FROM audit_log WHERE at > now() - interval '24 hours'
			  AND (detail->>'target' = $1 OR detail->>'target' LIKE $1 || '/%') ORDER BY at`, t.slug); err == nil {
			var list []map[string]any
			for ar.Next() {
				var at, actor, action, target string
				ar.Scan(&at, &actor, &action, &target)
				list = append(list, map[string]any{"at": at, "actor": actor, "action": action, "target": target})
			}
			ar.Close()
			payload["audit"] = list
		}
		if er, err := a.db.Query(`SELECT to_char(at,'YYYY-MM-DD"T"HH24:MI:SS"Z"'), name, coalesce(status,0), coalesce(ms,0), coalesce(ok,false), coalesce(error,'')
			FROM edge_logs WHERE slug=$1 AND at > now() - interval '24 hours' ORDER BY at`, t.slug); err == nil {
			var list []map[string]any
			for er.Next() {
				var at, name, errmsg string
				var status, ms int
				var ok bool
				er.Scan(&at, &name, &status, &ms, &ok, &errmsg)
				list = append(list, map[string]any{"at": at, "fn": name, "status": status, "ms": ms, "ok": ok, "error": errmsg})
			}
			er.Close()
			payload["edge"] = list
		}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, t.url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "ForgeBase-LogShip/1")
		if resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req); err == nil {
			resp.Body.Close()
		}
		a.db.Exec(`UPDATE projects SET log_shipped_at=now() WHERE slug=$1`, t.slug)
	}
}

// scrubBranch anonymizes columns on a fresh branch: one "table.column" per
// line or comma. Text-ish columns become deterministic anon_<md5> tokens
// (joins keep working); anything else is nulled. Returns warnings, "" = clean.
func (a *app) scrubBranch(branch, rules string) string {
	rules = strings.TrimSpace(rules)
	if rules == "" {
		return ""
	}
	db, err := a.dbFor(branch)
	if err != nil {
		return "could not connect: " + err.Error()
	}
	var warns []string
	for _, raw := range strings.FieldsFunc(rules, func(r rune) bool { return r == ',' || r == '\n' }) {
		spec := strings.TrimSpace(raw)
		if spec == "" {
			continue
		}
		i := strings.IndexByte(spec, '.')
		if i <= 0 {
			warns = append(warns, spec+" (use table.column)")
			continue
		}
		table, col := spec[:i], spec[i+1:]
		colType := ""
		for _, c := range a.tableCols(db, "public", table) {
			if c.Name == col {
				colType = c.Type
			}
		}
		if colType == "" {
			warns = append(warns, spec+" (not found)")
			continue
		}
		qt, qc := qrel("public", table), pq.QuoteIdentifier(col)
		var stmt string
		switch colType {
		case "text", "character varying", "citext":
			stmt = fmt.Sprintf(`UPDATE %s SET %s = 'anon_' || md5(%s) WHERE %s IS NOT NULL`, qt, qc, qc, qc)
		default:
			stmt = fmt.Sprintf(`UPDATE %s SET %s = NULL`, qt, qc)
		}
		if _, err := db.Exec(stmt); err != nil {
			warns = append(warns, spec+" ("+err.Error()+")")
		}
	}
	return strings.Join(warns, "; ")
}

// branchReset throws away a branch's current state and recreates it from its
// parent - same name, same role, same password, same connection string.
func (a *app) branchReset(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	branch := r.FormValue("branch")
	back := "/p/" + slug + "/branches"
	var parent string
	a.db.QueryRow(`SELECT coalesce(parent,'') FROM projects WHERE slug=$1`, branch).Scan(&parent)
	if parent != slug {
		redirectErr(w, r, back, "That is not a branch of this project.")
		return
	}
	if a.projectMode(branch) == "instance" {
		_, pw := a.projectCred(branch)
		if _, err := pgInstance(time.Minute, "delete", branch); err != nil {
			redirectErr(w, r, back, "Reset failed: "+err.Error())
			return
		}
		if _, err := pgInstance(3*time.Minute, "branch", slug, branch, pw); err != nil {
			redirectErr(w, r, back, "Reset failed mid-way (branch removed): "+err.Error())
			return
		}
		a.audit(r, "branch-reset", branch)
		redirectMsg(w, r, back, branch+" reset from "+slug+" (copy-on-write).")
		return
	}
	bq := pq.QuoteIdentifier(branch)
	src := pq.QuoteIdentifier(slug)
	// stop everything holding the branch, drop its database (role + record stay)
	a.stopPostgREST(branch)
	a.stopRealtimeHub(branch)
	closeConn(branch)
	a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, branch)
	if _, err := a.db.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, bq)); err != nil {
		redirectErr(w, r, back, "Reset failed: "+err.Error())
		return
	}
	// quiesce the parent exactly like branch creation does
	closeConn(slug)
	a.db.Exec(fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS false`, src))
	defer a.db.Exec(fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS true`, src))
	a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, slug)
	if _, err := a.db.Exec(fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s OWNER %s`, bq, src, bq)); err != nil {
		redirectErr(w, r, back, "Reset failed after drop - recreate the branch manually: "+err.Error())
		return
	}
	a.db.Exec(fmt.Sprintf(`REVOKE CONNECT ON DATABASE %s FROM PUBLIC`, bq))
	a.db.Exec(`UPDATE projects SET status='active' WHERE slug=$1`, branch)
	a.audit(r, "branch-reset", branch)
	redirectMsg(w, r, back, branch+" reset to match "+slug+".")
}

// expireBranches pauses branches past their expiry - data is kept, connections
// stop, and the owner decides deletion. Called from the sampler.
func (a *app) expireBranches() {
	rows, err := a.db.Query(`SELECT slug, mode FROM projects
		WHERE parent IS NOT NULL AND expires_at IS NOT NULL AND expires_at < now()
		  AND status IN ('active','suspended')`)
	if err != nil {
		return
	}
	type b struct{ slug, mode string }
	var list []b
	for rows.Next() {
		var x b
		rows.Scan(&x.slug, &x.mode)
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		if x.mode == "instance" {
			os.WriteFile(instancesRoot+"/.paused-"+x.slug, []byte("expired"), 0o644)
			pgInstance(time.Minute, "stop", x.slug)
		} else {
			a.db.Exec(fmt.Sprintf(`ALTER ROLE %s NOLOGIN`, pq.QuoteIdentifier(x.slug)))
			a.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, x.slug)
		}
		a.stopPostgREST(x.slug)
		a.stopRealtimeHub(x.slug)
		closeConn(x.slug)
		a.db.Exec(`UPDATE projects SET status='paused' WHERE slug=$1`, x.slug)
		a.auditRaw("system", "-", "branch-expired", x.slug)
		a.notifyDiscord("Branch " + x.slug + " reached its expiry and was paused (data kept). Delete or resume it from the panel.")
	}
}

func pctInt(n, d int) int {
	if d <= 0 {
		return 0
	}
	p := n * 100 / d
	if p > 100 {
		return 100
	}
	return p
}
