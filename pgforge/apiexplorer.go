package main

// Interactive API explorer: build a REST request against this project's Data
// API in the panel, run it through the local PostgREST sidecar with the role
// of your choice, and inspect status, headers and body - no curl needed.

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *app) apiExplorerPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	secret, enabled := a.apiConfig(slug)
	data := map[string]any{
		"Slug": slug, "Enabled": enabled,
		"Method": "GET", "Path": "/rest/v1/", "Role": "anon", "Body": "", "Prefer": "",
	}
	if enabled && r.Method == http.MethodPost {
		method := strings.ToUpper(r.FormValue("method"))
		switch method {
		case "GET", "POST", "PATCH", "DELETE", "HEAD":
		default:
			method = "GET"
		}
		role := r.FormValue("role")
		if role != "service_role" && role != "authenticated" {
			role = "anon"
		}
		path := r.FormValue("path")
		if !strings.HasPrefix(path, "/rest/v1") {
			path = "/rest/v1/" + strings.TrimPrefix(path, "/")
		}
		rel := strings.TrimPrefix(path, "/rest/v1")
		if rel == "" {
			rel = "/"
		}
		bodyText := r.FormValue("body")
		prefer := strings.TrimSpace(r.FormValue("prefer"))
		data["Method"], data["Path"], data["Role"], data["Body"], data["Prefer"] =
			method, path, role, bodyText, prefer

		p, err := a.ensurePostgREST(slug)
		if err != nil {
			data["RespStatus"] = "unavailable"
			data["RespBody"] = err.Error()
		} else {
			var rb io.Reader
			if bodyText != "" {
				rb = strings.NewReader(bodyText)
			}
			req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", p.port, rel), rb)
			key := signJWT([]byte(secret), role)
			req.Header.Set("apikey", key)
			req.Header.Set("Authorization", "Bearer "+key)
			if bodyText != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if prefer != "" {
				req.Header.Set("Prefer", prefer)
			}
			resp, rerr := (&http.Client{Timeout: 20 * time.Second}).Do(req)
			if rerr != nil {
				data["RespStatus"] = "error"
				data["RespBody"] = rerr.Error()
			} else {
				raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				data["RespStatus"] = resp.Status
				data["RespOK"] = resp.StatusCode < 400
				data["RespBody"] = string(raw)
				var hdr []string
				for _, h := range []string{"Content-Range", "Content-Type", "Content-Profile", "Location", "Preference-Applied"} {
					if v := resp.Header.Get(h); v != "" {
						hdr = append(hdr, h+": "+v)
					}
				}
				data["RespHeaders"] = hdr
				data["Curl"] = fmt.Sprintf("curl -X %s 'https://%s.%s%s' -H 'apikey: <%s key>' -H 'Authorization: Bearer <%s key>'",
					method, slug, a.cfg.domain, path, role, role)
			}
		}
	}
	content := renderContent(apiExplorerBody, data)
	a.renderShell(w, r, shellData{Title: slug + " · API Explorer", Nav: "api", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug},
			{Label: "Data API", Href: "/p/" + slug + "/api"}, {Label: "Explorer"}}}, content)
}

// cliProjects lists projects as JSON for the CLI.
func (a *app) cliProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT slug, status, coalesce(parent,''), coalesce(mode,'shared'),
			pg_size_pretty(pg_database_size(slug))
		FROM projects ORDER BY slug`)
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": err.Error()})
		return
	}
	defer rows.Close()
	type row struct {
		Slug, Status, Parent, Mode, Size string
	}
	var out []row
	for rows.Next() {
		var p row
		rows.Scan(&p.Slug, &p.Status, &p.Parent, &p.Mode, &p.Size)
		out = append(out, p)
	}
	writeJSON(w, 200, map[string]any{"projects": out})
}

const apiExplorerBody = `
<div class="pagehead"><h1>API Explorer</h1><p>Build and run requests against <b>{{.Slug}}</b>'s REST API - the exact responses your clients get, with the role you pick.</p></div>
{{if not .Enabled}}
<div class="card" style="text-align:center;padding:2.5rem"><p class="muted" style="margin:0">Enable the Data API first.</p></div>
{{else}}
<form method="post" action="/p/{{.Slug}}/api-explorer" class="card" style="margin-bottom:1rem">
  <div style="display:flex;gap:.5rem;flex-wrap:wrap;align-items:flex-end">
    <label class="fld" style="margin:0"><span class="lt">Method</span><select name="method" style="width:auto">
      <option {{if eq .Method "GET"}}selected{{end}}>GET</option>
      <option {{if eq .Method "POST"}}selected{{end}}>POST</option>
      <option {{if eq .Method "PATCH"}}selected{{end}}>PATCH</option>
      <option {{if eq .Method "DELETE"}}selected{{end}}>DELETE</option>
      <option {{if eq .Method "HEAD"}}selected{{end}}>HEAD</option>
    </select></label>
    <label class="fld" style="margin:0;flex:1;min-width:280px"><span class="lt">Path (table + filters)</span>
      <input type="text" name="path" value="{{.Path}}" placeholder="/rest/v1/orders?select=*&status=eq.paid&limit=5" style="font-family:var(--mono);font-size:12.5px"></label>
    <label class="fld" style="margin:0"><span class="lt">Run as</span><select name="role" style="width:auto">
      <option {{if eq .Role "anon"}}selected{{end}}>anon</option>
      <option {{if eq .Role "authenticated"}}selected{{end}}>authenticated</option>
      <option {{if eq .Role "service_role"}}selected{{end}}>service_role</option>
    </select></label>
    <button class="btn btn-primary btn-sm" type="submit">{{icon "play"}} Send</button>
  </div>
  <div style="display:flex;gap:.5rem;flex-wrap:wrap;align-items:flex-end;margin-top:.6rem">
    <label class="fld" style="margin:0;flex:1;min-width:280px"><span class="lt">JSON body (POST/PATCH)</span>
      <textarea name="body" rows="3" placeholder='{"status":"paid"}' style="font-family:var(--mono);font-size:12.5px">{{.Body}}</textarea></label>
    <label class="fld" style="margin:0"><span class="lt">Prefer header (optional)</span>
      <input type="text" name="prefer" value="{{.Prefer}}" placeholder="return=representation" style="width:200px;font-family:var(--mono);font-size:12px"></label>
  </div>
</form>
{{if .RespStatus}}
<div class="card">
  <div style="display:flex;align-items:center;gap:.6rem;flex-wrap:wrap">
    <h2 style="font-size:15px">Response</h2>
    {{if .RespOK}}<span class="badge active">{{.RespStatus}}</span>{{else}}<span class="badge paused">{{.RespStatus}}</span>{{end}}
    {{range .RespHeaders}}<code class="muted" style="font-size:10.5px">{{.}}</code>{{end}}
  </div>
  <pre style="font-family:var(--mono);font-size:12px;line-height:1.6;overflow-x:auto;margin-top:.6rem;max-height:420px;overflow-y:auto;white-space:pre-wrap">{{.RespBody}}</pre>
  {{if .Curl}}<div class="cs" style="margin-top:.5rem"><span class="tag">curl</span><code id="xcurl" style="font-size:10.5px">{{.Curl}}</code><button class="copy" onclick="cp('xcurl')">{{icon "copy"}}</button></div>{{end}}
</div>
{{end}}
{{end}}
` + copyJS
