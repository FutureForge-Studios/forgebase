package main

import (
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
	content := renderContent(monitoringBody, map[string]any{
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
	// audit trail for this project (events whose target references the project)
	// plus platform-wide auth/login events, newest first.
	type logRow struct{ At, Actor, IP, Action, Target string }
	var events []logRow
	rows, _ := a.db.Query(`SELECT to_char(at,'Mon DD, HH24:MI:SS'), actor, coalesce(ip,'-'),
			action, coalesce(detail->>'target','')
		FROM audit_log
		WHERE detail->>'target' = $1
		   OR detail->>'target' LIKE $1 || '/%'
		   OR detail->>'target' LIKE $1 || ' %'
		ORDER BY at DESC LIMIT 100`, slug)
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
	content := renderContent(logsBody, map[string]any{"Slug": slug, "Events": events, "Acts": acts})
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
		Slug, Created, Size, Conn string
	}
	var branches []br
	rows, _ := a.db.Query(`SELECT slug, to_char(created_at,'Mon DD, YYYY'),
		coalesce(pg_size_pretty(pg_database_size(slug)),'-'),
		pgp_sym_decrypt(password_enc,$2)
		FROM projects WHERE parent=$1 ORDER BY created_at`, slug, string(a.cfg.secret))
	if rows != nil {
		for rows.Next() {
			var b br
			var pw string
			rows.Scan(&b.Slug, &b.Created, &b.Size, &pw)
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
		if _, err := a.db.Exec(`INSERT INTO projects(slug, role_name, password_enc, parent, mode)
			VALUES ($1,$1,pgp_sym_encrypt($2,$3),$4,'instance')`,
			branch, bpw, string(a.cfg.secret), slug); err != nil {
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
	a.db.Exec(`INSERT INTO projects(slug, role_name, password_enc, parent) VALUES ($1,$1,pgp_sym_encrypt($2,$3),$4)`,
		branch, pw, string(a.cfg.secret), slug)
	a.rewriteUserlist()
	a.audit(r, "branch", branch)
	redirectMsg(w, r, "/p/"+branch, "Branch "+branch+" created from "+slug+".")
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
