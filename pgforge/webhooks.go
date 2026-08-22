package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Database Webhooks: POST a JSON payload to a URL whenever a row changes. Reuses
// the Realtime change-capture triggers + per-project listener; fireWebhooks is
// called for every change and dispatches to matching webhook configs.

func (a *app) fireWebhooks(slug string, payload []byte) {
	var ev struct {
		Type  string `json:"type"`
		Table string `json:"table"`
	}
	if json.Unmarshal(payload, &ev) != nil {
		return
	}
	rows, err := a.db.Query(`SELECT id, url, table_name, events, coalesce(secret,''), method, headers FROM webhooks WHERE slug=$1`, slug)
	if err != nil {
		return
	}
	type target struct{ id, url, secret, method, headers string }
	var targets []target
	for rows.Next() {
		var t target
		var table, events string
		rows.Scan(&t.id, &t.url, &table, &events, &t.secret, &t.method, &t.headers)
		if table != "" && table != ev.Table {
			continue
		}
		if !strings.Contains(events, ev.Type) {
			continue
		}
		targets = append(targets, t)
	}
	rows.Close()
	for _, t := range targets {
		go a.deliverWebhook(t.id, slug, t.url, t.secret, t.method, t.headers, payload)
	}
}

// deliverWebhook POSTs the payload with an HMAC signature and retries with
// backoff, recording every attempt so failures are visible instead of silently
// dropped.
func (a *app) deliverWebhook(id, slug, url, secret, method, headers string, payload []byte) {
	defer func() { recover() }() // a panic here must not kill the binary
	if method == "" {
		method = "POST"
	}
	var sig string
	if secret != "" {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(payload)
		sig = "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	// Retry with a longer horizon so a target that is down for a few minutes still
	// gets the event (was ~13s total; now ~7.5 min over 5 attempts).
	backoff := []time.Duration{0, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 5 * time.Minute}
	for attempt := 1; attempt <= len(backoff); attempt++ {
		if backoff[attempt-1] > 0 {
			time.Sleep(backoff[attempt-1])
		}
		req, err := http.NewRequest(method, url, bytes.NewReader(payload))
		if err != nil {
			a.logDelivery(id, slug, 0, false, attempt, err.Error(), payload)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "ForgeBase-Webhook/1")
		if sig != "" {
			req.Header.Set("X-ForgeBase-Signature", sig)
		}
		// custom headers: one "Key: Value" per line (e.g. an Authorization token)
		for _, line := range strings.Split(headers, "\n") {
			if i := strings.Index(line, ":"); i > 0 {
				req.Header.Set(strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]))
			}
		}
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			a.logDelivery(id, slug, 0, false, attempt, err.Error(), payload)
			continue
		}
		code := resp.StatusCode
		resp.Body.Close()
		okc := code >= 200 && code < 300
		a.logDelivery(id, slug, code, okc, attempt, "", payload)
		if okc {
			return
		}
	}
}

func (a *app) logDelivery(id, slug string, code int, ok bool, attempt int, errmsg string, payload []byte) {
	// store the payload once (first attempt) so any delivery can be replayed;
	// capped so a giant row change cannot bloat the log
	if attempt != 1 || len(payload) > 64*1024 {
		payload = nil
	}
	a.db.Exec(`INSERT INTO webhook_deliveries(webhook_id, slug, status_code, ok, attempt, error, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, slug, code, ok, attempt, errmsg, payload)
	// keep only the most recent ~500 deliveries per project
	a.db.Exec(`DELETE FROM webhook_deliveries WHERE slug=$1 AND id NOT IN (
		SELECT id FROM webhook_deliveries WHERE slug=$1 ORDER BY at DESC LIMIT 500)`, slug)
}

// startWebhookPumps starts a per-project listener for every project that has
// webhooks configured, so events fire even with no WebSocket clients connected.
func (a *app) startWebhookPumps() {
	// Suspended/paused projects don't get a hub resurrected at boot - waking
	// the project recreates it (rtGetHub is lazy).
	rows, err := a.db.Query(`SELECT DISTINCT w.slug FROM webhooks w
		JOIN projects p ON p.slug = w.slug WHERE p.status = 'active'`)
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
		a.rtGetHub(s)
	}
}

// ----------------------------------------------------------------- page + CRUD

func (a *app) webhooksPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	type hook struct{ ID, Name, URL, Table, Events, Created, Secret string }
	var hooks []hook
	rows, _ := a.db.Query(`SELECT id, name, url, coalesce(nullif(table_name,''),'all tables'),
		events, to_char(created_at,'Mon DD, YYYY'), coalesce(secret,'') FROM webhooks WHERE slug=$1 ORDER BY created_at DESC`, slug)
	if rows != nil {
		for rows.Next() {
			var h hook
			rows.Scan(&h.ID, &h.Name, &h.URL, &h.Table, &h.Events, &h.Created, &h.Secret)
			hooks = append(hooks, h)
		}
		rows.Close()
	}
	type delivery struct {
		ID, Name, At, Status string
		OK, CanReplay        bool
	}
	var deliveries []delivery
	if drows, _ := a.db.Query(`SELECT d.id, coalesce(w.name,'(deleted)'), to_char(d.at,'Mon DD HH24:MI:SS'),
		coalesce(d.status_code::text, coalesce(d.error,'-')), d.ok,
		d.payload IS NOT NULL AND w.id IS NOT NULL
		FROM webhook_deliveries d LEFT JOIN webhooks w ON w.id=d.webhook_id
		WHERE d.slug=$1 ORDER BY d.at DESC LIMIT 25`, slug); drows != nil {
		for drows.Next() {
			var d delivery
			drows.Scan(&d.ID, &d.Name, &d.At, &d.Status, &d.OK, &d.CanReplay)
			deliveries = append(deliveries, d)
		}
		drows.Close()
	}
	var tables []string
	if db, err := a.dbFor(slug); err == nil {
		tables = a.listTables(db)
	}
	content := renderContent(webhooksBody, map[string]any{"Slug": slug, "Hooks": hooks, "Tables": tables, "Deliveries": deliveries})
	a.renderShell(w, r, shellData{Title: slug + " · Webhooks", Nav: "webhooks", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Webhooks"}}}, content)
}

// replayDelivery re-sends a stored payload through the normal delivery path
// (same signature, headers and retry ladder).
func (a *app) replayDelivery(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/webhooks"
	var payload []byte
	var hid, url, secret, method, headers string
	err := a.db.QueryRow(`SELECT d.payload, w.id, w.url, coalesce(w.secret,''), w.method, w.headers
		FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id
		WHERE d.id = $1::bigint AND d.slug = $2 AND d.payload IS NOT NULL`,
		r.FormValue("id"), slug).Scan(&payload, &hid, &url, &secret, &method, &headers)
	if err != nil {
		redirectErr(w, r, back, "That delivery has no stored payload to replay.")
		return
	}
	go a.deliverWebhook(hid, slug, url, secret, method, headers, payload)
	a.audit(r, "webhook-replay", slug+"/"+hid)
	redirectMsg(w, r, back, "Replay queued - watch the deliveries list.")
}

// testWebhook fires a synthetic event at one webhook.
func (a *app) testWebhook(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/webhooks"
	var url, secret, method, headers string
	err := a.db.QueryRow(`SELECT url, coalesce(secret,''), method, headers FROM webhooks
		WHERE id = $1 AND slug = $2`, r.FormValue("id"), slug).Scan(&url, &secret, &method, &headers)
	if err != nil {
		redirectErr(w, r, back, "Unknown webhook.")
		return
	}
	payload := []byte(`{"type":"TEST","table":"_forgebase_test","record":{"message":"This is a test event from the ForgeBase panel."},"old_record":null}`)
	go a.deliverWebhook(r.FormValue("id"), slug, url, secret, method, headers, payload)
	a.audit(r, "webhook-test", slug+"/"+r.FormValue("id"))
	redirectMsg(w, r, back, "Test event queued - watch the deliveries list.")
}

func (a *app) createWebhook(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := strings.TrimSpace(r.FormValue("name"))
	url := strings.TrimSpace(r.FormValue("url"))
	table := strings.TrimSpace(r.FormValue("table"))
	var events []string
	for _, e := range []string{"INSERT", "UPDATE", "DELETE"} {
		if r.FormValue("ev_"+e) == "on" {
			events = append(events, e)
		}
	}
	method := strings.ToUpper(strings.TrimSpace(r.FormValue("method")))
	if method != "PUT" && method != "PATCH" {
		method = "POST"
	}
	headers := strings.TrimSpace(r.FormValue("headers"))
	if name == "" || !strings.HasPrefix(url, "http") || len(events) == 0 {
		redirectErr(w, r, "/p/"+slug+"/webhooks", "Enter a name, an http(s) URL, and at least one event.")
		return
	}
	if err := a.ensureChangeTriggers(slug); err != nil {
		redirectErr(w, r, "/p/"+slug+"/webhooks", "Setup failed: "+err.Error())
		return
	}
	a.db.Exec(`INSERT INTO webhooks(slug,name,url,table_name,events,secret,method,headers) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		slug, name, url, table, strings.Join(events, ","), randHex(16), method, headers)
	a.rtGetHub(slug) // ensure the listener is running
	a.audit(r, "webhook-create", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/webhooks", "Webhook \""+name+"\" created.")
}

func (a *app) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a.db.Exec(`DELETE FROM webhooks WHERE id=$1 AND slug=$2`, r.FormValue("id"), slug)
	// Drop the shared change triggers only if Realtime isn't using them either.
	a.reconcileChangeTriggers(slug)
	// If that was the last webhook and no WebSocket clients are connected, the
	// hub (and its dedicated LISTEN backend) has nothing left to do.
	a.reapHubIfUnused(slug)
	a.audit(r, "webhook-delete", slug)
	redirectMsg(w, r, "/p/"+slug+"/webhooks", "Webhook deleted.")
}
