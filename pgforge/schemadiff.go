package main

// Schema diff between a project and its branches: pg_dump -s both databases,
// normalize away noise (SET lines, comments, the randomized \restrict token),
// and render a unified diff so a branch's schema drift is readable at a glance.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// schemaDump returns the normalized schema-only dump of one shared-cluster db.
func (a *app) schemaDump(ctx context.Context, dbname string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", "pgforge-db",
		"pg_dump", "-U", "postgres", "-s", dbname).Output()
	if err != nil {
		return "", fmt.Errorf("pg_dump %s: %v", dbname, err)
	}
	var keep []string
	for _, ln := range strings.Split(string(out), "\n") {
		t := strings.TrimRight(ln, " \t\r")
		switch {
		case t == "", strings.HasPrefix(t, "--"), strings.HasPrefix(t, "\\"),
			strings.HasPrefix(t, "SET "), strings.HasPrefix(t, "SELECT pg_catalog.set_config"):
			continue
		}
		keep = append(keep, t)
	}
	return strings.Join(keep, "\n") + "\n", nil
}

// diffFamily reports whether two db names belong to the same branch family.
func (a *app) diffFamily(slug, other string) bool {
	if other == slug {
		return true
	}
	var parent string
	a.db.QueryRow(`SELECT coalesce(parent,'') FROM projects WHERE slug=$1`, other).Scan(&parent)
	return parent == slug
}

type diffLine struct {
	Text  string
	Class string // add | del | hunk | ctx
}

func (a *app) branchDiff(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = slug
	}
	back := "/p/" + slug + "/branches"
	if !a.diffFamily(slug, from) || !a.diffFamily(slug, to) || to == "" || from == to {
		redirectErr(w, r, back, "Pick two different databases from this project's family.")
		return
	}
	if a.projectMode(from) == "instance" || a.projectMode(to) == "instance" {
		redirectErr(w, r, back, "Schema diff currently covers shared-cluster databases; dedicated instances are compared via their own dumps.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	dumpA, errA := a.schemaDump(ctx, from)
	dumpB, errB := a.schemaDump(ctx, to)
	if errA != nil || errB != nil {
		redirectErr(w, r, back, "Could not dump schemas (is a side sleeping?).")
		return
	}
	dir, err := os.MkdirTemp("", "fbdiff")
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	defer os.RemoveAll(dir)
	fa, fb := filepath.Join(dir, "a.sql"), filepath.Join(dir, "b.sql")
	os.WriteFile(fa, []byte(dumpA), 0o600)
	os.WriteFile(fb, []byte(dumpB), 0o600)
	// diff exits 1 when files differ - that is the interesting case, not an error
	out, _ := exec.CommandContext(ctx, "diff", "-u",
		"--label", from, "--label", to, fa, fb).Output()

	var lines []diffLine
	same := len(out) == 0
	for _, ln := range strings.Split(string(out), "\n") {
		cl := "ctx"
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"), strings.HasPrefix(ln, "@@"):
			cl = "hunk"
		case strings.HasPrefix(ln, "+"):
			cl = "add"
		case strings.HasPrefix(ln, "-"):
			cl = "del"
		}
		lines = append(lines, diffLine{Text: ln, Class: cl})
		if len(lines) > 4000 {
			lines = append(lines, diffLine{Text: "... diff truncated at 4000 lines", Class: "hunk"})
			break
		}
	}
	content := renderContent(diffBody, map[string]any{
		"Slug": slug, "From": from, "To": to, "Lines": lines, "Same": same,
	})
	a.renderShell(w, r, shellData{Title: slug + " · Schema diff", Nav: "branches", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug},
			{Label: "Branches", Href: back}, {Label: "Diff"}}}, content)
}

const diffBody = `
<div class="pagehead"><h1>Schema Diff</h1><p><code>{{.From}}</code> compared to <code>{{.To}}</code> - structure only, data is not compared.</p></div>
<div style="margin-bottom:1rem"><a class="btn btn-ghost btn-sm" href="/p/{{.Slug}}/branches">{{icon "back"}} Branches</a></div>
{{if .Same}}
<div class="card" style="text-align:center;padding:2.5rem"><p style="margin:0"><span class="badge active">identical</span> <span class="muted">The two schemas match exactly.</span></p></div>
{{else}}
<div class="card" style="padding:.6rem;overflow-x:auto">
<pre style="font-family:var(--mono);font-size:12px;line-height:1.55;margin:0">{{range .Lines}}<span class="d-{{.Class}}">{{.Text}}
</span>{{end}}</pre>
</div>
{{end}}
`
