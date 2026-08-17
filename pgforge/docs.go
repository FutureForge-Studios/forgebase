package main

import (
	"fmt"
	"net/http"
)

// Per-project API documentation, populated with the project's real base URLs
// and (if enabled) its anon key, so examples are copy-paste ready.
func (a *app) docsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	_, pw := a.projectCred(slug)
	anon, _, _, apiOn := a.apiKeys(slug)
	_, authOn := a.authConfig(slug)
	rtOn := a.realtimeEnabled(slug)
	base := "https://" + slug + "." + a.cfg.domain
	// list edge functions for this project
	var fns []string
	frows, _ := a.db.Query(`SELECT name FROM edge_functions WHERE slug=$1 ORDER BY name`, slug)
	if frows != nil {
		for frows.Next() {
			var n string
			frows.Scan(&n)
			fns = append(fns, n)
		}
		frows.Close()
	}
	d := map[string]any{
		"Slug":   slug,
		"Domain": a.cfg.domain,
		"Direct": fmt.Sprintf("postgresql://%s:%s@%s:5432/%s?sslmode=require", slug, pw, a.cfg.domain, slug),
		"Pooled": fmt.Sprintf("postgresql://%s:%s@%s:6543/%s", slug, pw, a.cfg.domain, slug),
		"APIOn":  apiOn, "AuthOn": authOn, "RTOn": rtOn,
		"Rest":     base + "/rest/v1",
		"GraphQL":  base + "/graphql/v1",
		"AuthURL":  base + "/auth/v1",
		"StoreURL": base + "/storage/v1/object",
		"Realtime": "wss://" + slug + "." + a.cfg.domain + "/realtime/v1",
		"FuncBase": base + "/functions/v1",
		"Fns":      fns,
		"Anon":     anon,
	}
	content := renderContent(docsBody, d)
	a.renderShell(w, r, shellData{Title: slug + " · Docs", Nav: "docs", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Docs"}}}, content)
}

// globalDocsPage is the platform guide: creating and managing projects, account
// and team - the account-management docs.
func (a *app) globalDocsPage(w http.ResponseWriter, r *http.Request) {
	content := renderContent(guideBody, map[string]any{"Domain": a.cfg.domain})
	a.renderShell(w, r, shellData{Title: "Guide", Nav: "guide",
		Crumbs: []crumb{{Label: "Guide"}}}, content)
}
