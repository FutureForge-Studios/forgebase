package main

// Scheduled edge functions: a function with a cron schedule is invoked by the
// control plane every matching minute (UTC), through the exact same code path
// as an HTTP call - so slots, timeouts, logging and metrics all apply, and the
// run history IS the invocation log.

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"
)

// cronMatch reports whether a 5-field cron expression matches a time (UTC).
// Supports *, */step, lists (a,b,c), ranges (a-b), and range/step (a-b/n).
// Day-of-month and day-of-week combine with OR when both are restricted,
// matching standard cron semantics.
func cronMatch(expr string, t time.Time) bool {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return false
	}
	min := cronFieldMatch(f[0], t.Minute(), 0, 59)
	hr := cronFieldMatch(f[1], t.Hour(), 0, 23)
	dom := cronFieldMatch(f[2], t.Day(), 1, 31)
	mon := cronFieldMatch(f[3], int(t.Month()), 1, 12)
	dow := cronFieldMatch(f[4], int(t.Weekday()), 0, 6)
	day := dom && dow
	if f[2] != "*" && f[4] != "*" {
		day = dom || dow
	}
	return min && hr && day && mon
}

func cronFieldMatch(field string, val, lo, hi int) bool {
	for _, part := range strings.Split(field, ",") {
		step := 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s <= 0 {
				return false
			}
			step = s
			part = part[:i]
		}
		start, end := lo, hi
		switch {
		case part == "*" || part == "":
		case strings.Contains(part, "-"):
			bits := strings.SplitN(part, "-", 2)
			a, e1 := strconv.Atoi(bits[0])
			b, e2 := strconv.Atoi(bits[1])
			if e1 != nil || e2 != nil {
				return false
			}
			start, end = a, b
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return false
			}
			// a bare value with a step (e.g. 5/10) means "starting at 5"
			if step == 1 {
				if n == val {
					return true
				}
				continue
			}
			start = n
		}
		if val >= start && val <= end && (val-start)%step == 0 {
			return true
		}
	}
	return false
}

// cronExprValid is the save-time check: five fields, each parseable.
func cronExprValid(expr string) bool {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return false
	}
	// probe a fixed time purely to exercise the parser on every field
	probe := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lo := []int{0, 0, 1, 1, 0}
	hi := []int{59, 23, 31, 12, 6}
	vals := []int{probe.Minute(), probe.Hour(), probe.Day(), int(probe.Month()), int(probe.Weekday())}
	for i, fld := range f {
		for _, part := range strings.Split(fld, ",") {
			p := part
			if j := strings.IndexByte(p, '/'); j >= 0 {
				if _, err := strconv.Atoi(p[j+1:]); err != nil {
					return false
				}
				p = p[:j]
			}
			if p == "*" || p == "" {
				continue
			}
			if strings.Contains(p, "-") {
				bits := strings.SplitN(p, "-", 2)
				a, e1 := strconv.Atoi(bits[0])
				b, e2 := strconv.Atoi(bits[1])
				if e1 != nil || e2 != nil || a < lo[i] || b > hi[i] || a > b {
					return false
				}
			} else if n, err := strconv.Atoi(p); err != nil || n < lo[i] || n > hi[i] {
				return false
			}
		}
		_ = vals[i]
	}
	return true
}

// startEdgeCron fires scheduled functions once per matching UTC minute.
func (a *app) startEdgeCron() {
	go func() {
		defer func() { recover() }()
		// align to the top of the minute so matches are exact
		time.Sleep(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			a.runEdgeCronPass(time.Now().UTC())
			<-tick.C
		}
	}()
}

func (a *app) runEdgeCronPass(now time.Time) {
	defer func() { recover() }()
	minute := now.Truncate(time.Minute)
	rows, err := a.db.Query(`SELECT slug, name, schedule FROM edge_functions
		WHERE coalesce(schedule,'') <> ''
		  AND (last_cron IS NULL OR last_cron < $1)`, minute)
	if err != nil {
		return
	}
	type job struct{ slug, name, sched string }
	var jobs []job
	for rows.Next() {
		var j job
		rows.Scan(&j.slug, &j.name, &j.sched)
		jobs = append(jobs, j)
	}
	rows.Close()
	for _, j := range jobs {
		if !cronMatch(j.sched, minute) {
			continue
		}
		// claim this minute first so a crash mid-run cannot double-fire
		res, err := a.db.Exec(`UPDATE edge_functions SET last_cron=$3
			WHERE slug=$1 AND name=$2 AND (last_cron IS NULL OR last_cron < $3)`,
			j.slug, j.name, minute)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		go a.invokeScheduled(j.slug, j.name)
	}
}

// invokeScheduled calls a function through serveFunction with the project's
// service key, exactly like an external HTTPS call would land.
func (a *app) invokeScheduled(slug, name string) {
	defer func() { recover() }()
	_, service, _, _ := a.apiKeys(slug)
	req := httptest.NewRequest("POST", fmt.Sprintf("/functions/v1/%s", name),
		strings.NewReader(`{"trigger":"schedule"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+service)
	req.Header.Set("X-Forgebase-Trigger", "schedule")
	rec := httptest.NewRecorder()
	a.serveFunction(rec, req, slug)
}
