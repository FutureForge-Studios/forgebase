package main

// Advisors: automated security and performance review of a project database.
// Every rule reads live catalogs/statistics - nothing is cached - and comes
// with a concrete, copy-pasteable fix. Levels: ERROR (fix now), WARN (should
// fix), INFO (worth knowing).

import (
	"fmt"
	"net/http"
	"strings"
)

type advisory struct {
	Level, Area, Title, Detail, Fix string
}

func (a *app) runAdvisors(slug string) []advisory {
	db, err := a.dbFor(slug)
	if err != nil {
		return nil
	}
	var out []advisory
	add := func(level, area, title, detail, fix string) {
		out = append(out, advisory{level, area, title, detail, fix})
	}

	// ---- security: RLS off on API-visible tables
	if rows, err := db.Query(`SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind='r' AND NOT c.relrowsecurity
		ORDER BY 1`); err == nil {
		var tables []string
		for rows.Next() {
			var t string
			rows.Scan(&t)
			tables = append(tables, t)
		}
		rows.Close()
		if len(tables) > 0 {
			add("ERROR", "security", "Row Level Security is off on public tables",
				fmt.Sprintf("%d table(s) in the API-exposed public schema have no RLS: %s. Any role holding a table grant sees every row.",
					len(tables), summarize(tables, 6)),
				"Enable RLS and add policies on the Policies page. Tables without policies become inaccessible to API roles, which is the safe default.")
		}
	}

	// ---- security: blanket write policies
	if rows, err := db.Query(`SELECT tablename, policyname FROM pg_policies
		WHERE schemaname='public' AND cmd IN ('ALL','INSERT','UPDATE','DELETE')
			AND coalesce(with_check, qual) = 'true'
			AND (roles::text LIKE '%anon%' OR roles::text LIKE '%authenticated%')
		ORDER BY 1,2`); err == nil {
		var hits []string
		for rows.Next() {
			var t, p string
			rows.Scan(&t, &p)
			hits = append(hits, t+"."+p)
		}
		rows.Close()
		if len(hits) > 0 {
			add("WARN", "security", "Write policies that allow everything",
				fmt.Sprintf("%d write polic(ies) use a blanket true expression for anon/authenticated: %s. Any signed-in (or anonymous) client can modify every row.",
					len(hits), summarize(hits, 5)),
				"Tighten the policy expression, e.g. WITH CHECK (auth.uid() = user_id), on the Policies page.")
		}
	}

	// ---- security: SECURITY DEFINER functions
	if rows, err := db.Query(`SELECT p.proname FROM pg_proc p
		JOIN pg_namespace n ON n.oid=p.pronamespace
		WHERE n.nspname='public' AND p.prosecdef
			AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid=p.oid AND d.deptype='e')
		ORDER BY 1`); err == nil {
		var fns []string
		for rows.Next() {
			var f string
			rows.Scan(&f)
			fns = append(fns, f)
		}
		rows.Close()
		if len(fns) > 0 {
			add("WARN", "security", "SECURITY DEFINER functions in public",
				fmt.Sprintf("%s run with their owner's privileges, bypassing the caller's RLS. That is sometimes intended - but each one deserves a review.", summarize(fns, 5)),
				"Confirm each function must bypass RLS; otherwise recreate it without SECURITY DEFINER (Objects page shows every definition).")
		}
	}

	// ---- security: sensitive-looking columns readable by anon
	if rows, err := db.Query(`SELECT DISTINCT c.relname||'.'||a.attname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname='public'
		JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
		WHERE c.relkind='r'
			AND (a.attname ~* '(password|secret|token|api_key|private_key)')
			AND (has_column_privilege('anon', c.oid, a.attnum, 'SELECT'))
		ORDER BY 1`); err == nil {
		var cols []string
		for rows.Next() {
			var cn string
			rows.Scan(&cn)
			cols = append(cols, cn)
		}
		rows.Close()
		if len(cols) > 0 {
			add("WARN", "security", "Sensitive-looking columns readable by anon",
				fmt.Sprintf("Columns named like credentials are SELECTable by the anonymous role: %s.", summarize(cols, 6)),
				"Revoke anon's SELECT on those columns (Policies page, Column privileges) or move secrets out of API-exposed tables.")
		}
	}

	// ---- security: RLS on but zero policies
	if rows, err := db.Query(`SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind='r' AND c.relrowsecurity
			AND NOT EXISTS (SELECT 1 FROM pg_policies p WHERE p.schemaname='public' AND p.tablename=c.relname)
		ORDER BY 1`); err == nil {
		var tables []string
		for rows.Next() {
			var t string
			rows.Scan(&t)
			tables = append(tables, t)
		}
		rows.Close()
		if len(tables) > 0 {
			add("INFO", "security", "RLS enabled with no policies",
				fmt.Sprintf("%s deny ALL access to API roles (owner connections bypass RLS). Fine if intended - surprising if not.", summarize(tables, 6)),
				"Add a policy on the Policies page if API clients should reach these tables.")
		}
	}

	// ---- performance: tables without a primary key
	if rows, err := db.Query(`SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind='r'
			AND NOT EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid=c.oid AND i.indisprimary)
		ORDER BY 1`); err == nil {
		var tables []string
		for rows.Next() {
			var t string
			rows.Scan(&t)
			tables = append(tables, t)
		}
		rows.Close()
		if len(tables) > 0 {
			add("WARN", "performance", "Tables without a primary key",
				fmt.Sprintf("%s have no primary key - row edits in the Table Editor are disabled, replication needs one, and updates scan more than they should.", summarize(tables, 6)),
				"ALTER TABLE ... ADD PRIMARY KEY (...) or add an id bigserial primary key column.")
		}
	}

	// ---- performance: FK columns without an index
	if rows, err := db.Query(`SELECT c.relname||'.'||a.attname
		FROM pg_constraint co
		JOIN pg_class c ON c.oid = co.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname='public'
		JOIN unnest(co.conkey) AS k(attnum) ON true
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		WHERE co.contype='f'
			AND NOT EXISTS (SELECT 1 FROM pg_index i
				WHERE i.indrelid = c.oid AND i.indkey[0] = a.attnum)
		ORDER BY 1`); err == nil {
		var cols []string
		for rows.Next() {
			var cn string
			rows.Scan(&cn)
			cols = append(cols, cn)
		}
		rows.Close()
		if len(cols) > 0 {
			add("WARN", "performance", "Foreign keys without an index",
				fmt.Sprintf("%s reference other tables but have no index leading on that column - joins and cascading deletes go slow.", summarize(cols, 6)),
				"CREATE INDEX on each referencing column (Objects page, Indexes tab).")
		}
	}

	// ---- performance: sizable indexes that are never used
	if rows, err := db.Query(`SELECT ic.relname, pg_size_pretty(pg_relation_size(i.indexrelid))
		FROM pg_index i
		JOIN pg_class ic ON ic.oid=i.indexrelid
		JOIN pg_namespace n ON n.oid=ic.relnamespace AND n.nspname='public'
		JOIN pg_stat_user_indexes s ON s.indexrelid=i.indexrelid
		WHERE NOT i.indisprimary AND NOT i.indisunique
			AND s.idx_scan = 0 AND pg_relation_size(i.indexrelid) > 10*1024*1024
		ORDER BY pg_relation_size(i.indexrelid) DESC LIMIT 10`); err == nil {
		var idx []string
		for rows.Next() {
			var name, size string
			rows.Scan(&name, &size)
			idx = append(idx, name+" ("+size+")")
		}
		rows.Close()
		if len(idx) > 0 {
			add("INFO", "performance", "Large indexes with zero scans",
				fmt.Sprintf("%s have never been used since statistics were last reset. They still cost disk and slow every write.", summarize(idx, 5)),
				"If they stay unused over a real traffic period, drop them (Objects page, Indexes tab).")
		}
	}

	// ---- performance: sequential-scan heavy tables
	if rows, err := db.Query(`SELECT relname, seq_scan, coalesce(idx_scan,0), n_live_tup
		FROM pg_stat_user_tables
		WHERE schemaname='public' AND n_live_tup > 10000 AND seq_scan > 50
			AND seq_scan > coalesce(idx_scan,0) * 5
		ORDER BY seq_scan DESC LIMIT 8`); err == nil {
		var hits []string
		for rows.Next() {
			var t string
			var sq, ix, live int64
			rows.Scan(&t, &sq, &ix, &live)
			hits = append(hits, fmt.Sprintf("%s (%d seq / %d idx scans, %d rows)", t, sq, ix, live))
		}
		rows.Close()
		if len(hits) > 0 {
			add("INFO", "performance", "Tables read mostly by sequential scan",
				fmt.Sprintf("%s - queries on these sizable tables rarely use an index.", summarize(hits, 4)),
				"EXPLAIN the frequent queries (SQL editor, Explain button) and add an index on their filter columns.")
		}
	}

	// ---- performance: bloated tables
	if rows, err := db.Query(`SELECT relname, n_dead_tup, n_live_tup FROM pg_stat_user_tables
		WHERE schemaname='public' AND n_live_tup > 1000 AND n_dead_tup > n_live_tup/5
		ORDER BY n_dead_tup DESC LIMIT 8`); err == nil {
		var hits []string
		for rows.Next() {
			var t string
			var dead, live int64
			rows.Scan(&t, &dead, &live)
			hits = append(hits, fmt.Sprintf("%s (%d dead / %d live)", t, dead, live))
		}
		rows.Close()
		if len(hits) > 0 {
			add("INFO", "performance", "Tables carrying many dead rows",
				fmt.Sprintf("%s hold noticeable bloat, which slows scans until vacuum reclaims it.", summarize(hits, 4)),
				"Usually autovacuum catches up on its own; a manual VACUUM (ANALYZE) tablename helps after bulk deletes.")
		}
	}

	// ---- performance: full-refresh sync pattern (delete-all + reinsert-all).
	// Matched insert and delete counts on a table mean a job wipes and
	// reloads it repeatedly - every run writes WAL for every row even when
	// nothing changed (the 2026-08-23 profitzon pattern).
	if rows, err := db.Query(`SELECT relname, n_tup_ins, n_tup_del FROM pg_stat_user_tables
		WHERE schemaname='public' AND n_tup_ins > 10000
			AND n_tup_del > n_tup_ins*8/10 AND n_tup_del < n_tup_ins*12/10
		ORDER BY n_tup_ins DESC LIMIT 5`); err == nil {
		var hits []string
		for rows.Next() {
			var t string
			var ins, del int64
			rows.Scan(&t, &ins, &del)
			hits = append(hits, fmt.Sprintf("%s (%d inserted, %d deleted)", t, ins, del))
		}
		rows.Close()
		if len(hits) > 0 {
			add("INFO", "performance", "Tables refreshed by delete-and-reinsert",
				fmt.Sprintf("%s - matching insert and delete counts suggest a sync job wipes and reloads these tables on every run, generating write-ahead log for every row even when nothing changed.", summarize(hits, 3)),
				"Upsert with change detection instead: INSERT ... ON CONFLICT (key) DO UPDATE SET ... WHERE (t.*) IS DISTINCT FROM (excluded.*) - only changed rows get written, and the WAL volume drops to what actually changed.")
		}
	}

	// ---- performance: TOAST write churn (large values rewritten constantly).
	// The signature is a toast table with huge, roughly-matched insert and
	// delete counts: some column holds a big value (raw API payloads, blobs
	// of JSON) that the app rewrites on every sync even when unchanged. This
	// is the class of problem that filled the WAL archive on 2026-08-23.
	if rows, err := db.Query(`SELECT c.relname, st.n_tup_ins, st.n_tup_del,
			pg_size_pretty(pg_total_relation_size(c.reltoastrelid))
		FROM pg_stat_sys_tables st
		JOIN pg_class tc ON tc.relname = st.relname AND st.schemaname = 'pg_toast'
		JOIN pg_class c ON c.reltoastrelid = tc.oid
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE st.n_tup_ins > 500000 AND st.n_tup_del > st.n_tup_ins/2
		ORDER BY st.n_tup_ins DESC LIMIT 5`); err == nil {
		var hits []string
		for rows.Next() {
			var t, sz string
			var ins, del int64
			rows.Scan(&t, &ins, &del, &sz)
			hits = append(hits, fmt.Sprintf("%s (%d large-value writes, %d deletes, %s of TOAST)", t, ins, del, sz))
		}
		rows.Close()
		if len(hits) > 0 {
			add("WARN", "performance", "Large values rewritten constantly (TOAST churn)",
				fmt.Sprintf("%s - an oversized column on these tables is rewritten over and over, generating write-ahead log far faster than the data actually changes. This shortens the point-in-time recovery window for the whole server.", summarize(hits, 3)),
				"In the app, skip writes when the content is unchanged (compare before updating), and avoid re-storing volatile raw payloads on every sync - store them once keyed by content hash, or strip timestamps/etags before saving.")
		}
	}

	// ---- performance: duplicate indexes (same table, same leading definition)
	if rows, err := db.Query(`SELECT array_agg(ic.relname ORDER BY ic.relname)
		FROM pg_index i
		JOIN pg_class ic ON ic.oid=i.indexrelid
		JOIN pg_namespace n ON n.oid=ic.relnamespace AND n.nspname='public'
		GROUP BY i.indrelid, i.indkey::text, i.indisunique
		HAVING count(*) > 1`); err == nil {
		var groups []string
		for rows.Next() {
			var arr string
			rows.Scan(&arr)
			groups = append(groups, strings.Trim(arr, "{}"))
		}
		rows.Close()
		if len(groups) > 0 {
			add("WARN", "performance", "Duplicate indexes",
				fmt.Sprintf("These index groups cover identical columns: %s. Each duplicate doubles write cost for no read gain.", summarize(groups, 4)),
				"Keep one per group and drop the rest (Objects page, Indexes tab).")
		}
	}

	return out
}

// summarize joins up to n items and appends how many more there are.
func summarize(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(" and %d more", len(items)-n)
}

func (a *app) advisorsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	items := a.runAdvisors(slug)
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Level]++
	}
	content := renderContent(advisorsBody, map[string]any{
		"Slug": slug, "Items": items,
		"Errors": counts["ERROR"], "Warns": counts["WARN"], "Infos": counts["INFO"],
	})
	a.renderShell(w, r, shellData{Title: slug + " · Advisors", Nav: "advisors", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Advisors"}}}, content)
}
