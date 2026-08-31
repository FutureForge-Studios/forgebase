package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lib/pq"
)

// Realtime: WebSocket subscriptions to row changes. A trigger on each table
// NOTIFYs a Postgres channel on INSERT/UPDATE/DELETE; pgforged LISTENs (one
// pq.Listener per project) and fans changes out to connected WebSocket clients
// on <slug>.<domain>/realtime/v1.

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 8192,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// rtSub is a client's subscription filter: empty fields match everything, so a
// client can subscribe to one table, one event, and/or one column value instead
// of the firehose.
type rtSub struct {
	Table  string
	Event  string // INSERT / UPDATE / DELETE
	Column string // optional equality filter column (?filter=id=eq.5)
	Value  string
	// Role + Claims (raw JSON) captured at join, for per-subscriber RLS
	// checks on change events when the project enables them.
	Role   string
	Claims string
	// Channel subscribes this client to broadcast + presence messages on one
	// named channel (channels are independent of table change streams).
	Channel string
	// PresenceKey announces this client on the channel's presence list.
	PresenceKey string
}

type rtHub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]rtSub
	lis     *pq.Listener
	// app + slug let the change fan-out run per-subscriber RLS checks
	// against the project database when the project has that enabled
	app  *app
	slug string
	// emptySince is when the hub last had zero WebSocket clients (zero value =
	// it has clients). The reaper closes hubs that have been empty for a while
	// and serve no webhooks - each hub holds a dedicated Postgres LISTEN
	// backend that would otherwise live until project deletion.
	emptySince time.Time
	// closed marks a hub the reaper has torn down. A client that raced the
	// reaper sees it under h.mu and re-fetches a fresh hub instead of
	// registering on a dead one that would never deliver an event.
	closed bool
}

var (
	rtMu   sync.Mutex
	rtHubs = map[string]*rtHub{}
)

func (a *app) realtimeEnabled(slug string) bool {
	var enabled bool
	a.db.QueryRow(`SELECT enabled FROM realtime_config WHERE slug=$1`, slug).Scan(&enabled)
	return enabled
}

func (a *app) realtimeRequireAuth(slug string) bool {
	var v bool
	a.db.QueryRow(`SELECT require_auth FROM realtime_config WHERE slug=$1`, slug).Scan(&v)
	return v
}

func (a *app) setRealtimeAuth(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	on := r.FormValue("require_auth") == "on"
	a.db.Exec(`UPDATE realtime_config SET require_auth=$2 WHERE slug=$1`, slug, on)
	a.audit(r, "realtime-auth", fmt.Sprintf("%s require_auth=%v", slug, on))
	redirectMsg(w, r, "/p/"+slug+"/realtime", "Realtime access updated.")
}

// stopRealtimeHub tears down a project's LISTEN hub. Without this, deleting a
// project left a pq.Listener pointed at the dropped database retrying forever.
func (a *app) stopRealtimeHub(slug string) {
	rtMu.Lock()
	h, ok := rtHubs[slug]
	if ok {
		delete(rtHubs, slug)
	}
	rtMu.Unlock()
	if ok && h.lis != nil {
		h.lis.Close() // closes lis.Notify, ending the fan-out goroutine
	}
}

// reapHubIfUnused stops a project's hub when nothing needs it right now: no
// WebSocket clients and no webhooks. rtGetHub re-creates it lazily on the next
// subscribe or webhook, so this is always safe to call.
func (a *app) reapHubIfUnused(slug string) {
	if a.hasWebhooks(slug) {
		return
	}
	rtMu.Lock()
	h, ok := rtHubs[slug]
	rtMu.Unlock()
	if !ok {
		return
	}
	h.mu.Lock()
	if len(h.clients) == 0 {
		h.closed = true
		h.mu.Unlock()
		a.stopRealtimeHub(slug)
		return
	}
	h.mu.Unlock()
}

// reapRealtimeHubs closes hubs that have had zero WebSocket clients for at
// least `idle` and serve no webhooks, releasing their dedicated LISTEN
// backends. Called from the metrics sampler. Events NOTIFYed while no hub is
// listening are dropped - which is already the behavior when nobody subscribes.
func (a *app) reapRealtimeHubs(idle time.Duration, pinned map[string]bool) {
	rtMu.Lock()
	candidates := make(map[string]*rtHub, len(rtHubs))
	for slug, h := range rtHubs {
		candidates[slug] = h
	}
	rtMu.Unlock()
	for slug, h := range candidates {
		if pinned[slug] {
			continue // kept warm
		}
		h.mu.Lock()
		emptyLong := len(h.clients) == 0 && !h.emptySince.IsZero() && time.Since(h.emptySince) > idle
		h.mu.Unlock()
		if emptyLong && !a.hasWebhooks(slug) {
			// re-check under the lock: a client may have joined since
			h.mu.Lock()
			if len(h.clients) == 0 {
				h.closed = true
				h.mu.Unlock()
				a.stopRealtimeHub(slug)
			} else {
				h.mu.Unlock()
			}
		}
	}
}

func (a *app) rtGetHub(slug string) *rtHub {
	rtMu.Lock()
	if h, ok := rtHubs[slug]; ok {
		rtMu.Unlock()
		return h
	}
	dsn := ""
	if a.projectMode(slug) == "instance" {
		_, pw := a.projectCred(slug)
		dsn = a.instanceDSN(slug, pw)
	} else {
		u := *a.baseURL
		u.Path = "/" + slug
		dsn = u.String()
	}
	h := &rtHub{clients: map[*websocket.Conn]rtSub{}, emptySince: time.Now(), app: a, slug: slug}
	lis := pq.NewListener(dsn, 2*time.Second, time.Minute, nil)
	h.lis = lis
	rtHubs[slug] = h
	rtMu.Unlock()
	// Listen BLOCKS until a database connection is established (and retries
	// forever on failure) - it must never run under the global rtMu, or one
	// unreachable project database wedges every realtime/webhook path and the
	// sampler with it.
	lis.Listen("forgebase_realtime")
	go func() {
		// A panic in this background goroutine would take down the whole binary
		// (net/http only recovers per-request handler panics).
		defer func() { recover() }()
		for n := range lis.Notify {
			if n != nil {
				h.broadcast([]byte(n.Extra))
				a.fireWebhooks(slug, []byte(n.Extra)) // also dispatch DB webhooks
			}
		}
	}()
	return h
}

// ensureChangeTriggers installs the NOTIFY trigger function + per-table triggers
// used by both Realtime and Webhooks, plus an event trigger that auto-attaches the
// same trigger to any table created later (via the editor, SQL, or an external
// client) so new tables are covered without a manual "re-scan".
func (a *app) ensureChangeTriggers(slug string) error {
	db, err := a.dbFor(slug)
	if err != nil {
		return err
	}
	if _, err := db.Exec(rtTriggerFn); err != nil {
		return err
	}
	pubs := a.rtPubConfig(slug)

	// What is already installed? Re-creating a trigger that is already
	// correct costs an ACCESS EXCLUSIVE lock per table, and on a busy
	// database (constant writes) each of those waits its turn - which is
	// what made "Enable realtime" take minutes on a 113-table project.
	// One catalog query lets us touch ONLY what actually differs.
	type ev struct{ ins, upd, del bool }
	have := map[string]ev{}
	if rows, err := db.Query(`SELECT c.relname,
			(t.tgtype & 4) > 0, (t.tgtype & 16) > 0, (t.tgtype & 8) > 0
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname='public'
		WHERE t.tgname='forgebase_rt' AND NOT t.tgisinternal`); err == nil {
		for rows.Next() {
			var name string
			var e ev
			rows.Scan(&name, &e.ins, &e.upd, &e.del)
			have[name] = e
		}
		rows.Close()
	}
	// A table locked by a long-running query must not stall the whole run:
	// fail that one fast and carry on. Applies to this pooled connection.
	db.Exec(`SET lock_timeout = '5s'`)
	defer db.Exec(`SET lock_timeout = 0`)

	for _, t := range a.listTables(db) {
		want := ev{true, true, true}
		if p, ok := pubs[t]; ok {
			want = ev{p[0], p[1], p[2]}
		}
		cur, installed := have[t]
		if installed && cur == want {
			continue // already exactly right - no lock, no DDL
		}
		q := pq.QuoteIdentifier(t)
		if installed {
			db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS forgebase_rt ON public.%s`, q))
		}
		var evs []string
		if want.ins {
			evs = append(evs, "INSERT")
		}
		if want.upd {
			evs = append(evs, "UPDATE")
		}
		if want.del {
			evs = append(evs, "DELETE")
		}
		if len(evs) == 0 {
			continue // publication fully off for this table
		}
		db.Exec(fmt.Sprintf(`CREATE TRIGGER forgebase_rt AFTER %s ON public.%s FOR EACH ROW EXECUTE FUNCTION forgebase_notify()`,
			strings.Join(evs, " OR "), q))
	}
	db.Exec(`CREATE SCHEMA IF NOT EXISTS forgebase`)
	db.Exec(`CREATE OR REPLACE FUNCTION forgebase.broadcast(channel text, payload jsonb DEFAULT 'null')
		RETURNS void AS $fb$
		BEGIN
			PERFORM pg_notify('forgebase_realtime',
				json_build_object('type','broadcast','channel',channel,'payload',payload)::text);
		END
		$fb$ LANGUAGE plpgsql`)
	db.Exec(rtEventTriggerFn)
	db.Exec(`DROP EVENT TRIGGER IF EXISTS forgebase_rt_ddl`)
	db.Exec(`CREATE EVENT TRIGGER forgebase_rt_ddl ON ddl_command_end WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION forgebase_rt_autoattach()`)
	return nil
}

// dropChangeTriggers removes the change triggers + the auto-attach event trigger.
func (a *app) dropChangeTriggers(slug string) {
	db, err := a.dbFor(slug)
	if err != nil {
		return
	}
	db.Exec(`DROP EVENT TRIGGER IF EXISTS forgebase_rt_ddl`)
	for _, t := range a.listTables(db) {
		db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS forgebase_rt ON public.%s`, pq.QuoteIdentifier(t)))
	}
}

// rtPubConfig returns per-table event toggles: [insert, update, delete].
// Tables with no row keep the default (everything on).
func (a *app) rtPubConfig(slug string) map[string][3]bool {
	out := map[string][3]bool{}
	rows, err := a.db.Query(`SELECT tablename, ins, upd, del FROM rt_publications WHERE slug=$1`, slug)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var i, u, d bool
		rows.Scan(&t, &i, &u, &d)
		out[t] = [3]bool{i, u, d}
	}
	return out
}

// rtPub is one table's publication row for the settings UI.
type rtPub struct {
	Table         string
	Ins, Upd, Del bool
}

func (a *app) rtPublications(slug string) []rtPub {
	db, err := a.dbFor(slug)
	if err != nil {
		return nil
	}
	cfg := a.rtPubConfig(slug)
	var out []rtPub
	for _, t := range a.listTables(db) {
		p := rtPub{Table: t, Ins: true, Upd: true, Del: true}
		if c, ok := cfg[t]; ok {
			p.Ins, p.Upd, p.Del = c[0], c[1], c[2]
		}
		out = append(out, p)
	}
	return out
}

// setRtPublication stores a table's event toggles and reinstalls triggers.
func (a *app) setRtPublication(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	table := r.FormValue("table")
	back := "/p/" + slug + "/realtime"
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if !tableIn(db, "public", table) {
		redirectErr(w, r, back, "Unknown table.")
		return
	}
	ins, upd, del := r.FormValue("ins") == "on", r.FormValue("upd") == "on", r.FormValue("del") == "on"
	if _, err := a.db.Exec(`INSERT INTO rt_publications (slug, tablename, ins, upd, del)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (slug, tablename) DO UPDATE SET ins=$3, upd=$4, del=$5`,
		slug, table, ins, upd, del); err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if a.realtimeEnabled(slug) || a.hasWebhooks(slug) {
		a.ensureChangeTriggers(slug)
	}
	a.audit(r, "realtime-pub", fmt.Sprintf("%s/%s ins=%t upd=%t del=%t", slug, table, ins, upd, del))
	redirectMsg(w, r, back, "Captured events for "+table+" updated.")
}

func (a *app) hasWebhooks(slug string) bool {
	var n int
	a.db.QueryRow(`SELECT count(*) FROM webhooks WHERE slug=$1`, slug).Scan(&n)
	return n > 0
}

// reconcileChangeTriggers keeps the shared change triggers installed as long as
// either Realtime or Webhooks needs them, and removes them only when neither does.
// This fixes the coupling where disabling Realtime silently killed webhooks.
func (a *app) reconcileChangeTriggers(slug string) {
	if a.realtimeEnabled(slug) || a.hasWebhooks(slug) {
		a.ensureChangeTriggers(slug)
	} else {
		a.dropChangeTriggers(slug)
	}
}

func (h *rtHub) broadcast(msg []byte) {
	var ev struct {
		Type, Table, Channel string
		Record               map[string]any
	}
	json.Unmarshal(msg, &ev)
	isChange := ev.Type != "broadcast" && ev.Type != "presence"
	// Per-subscriber RLS: when the project enables it, a change event is
	// only delivered to a subscriber whose role/claims can SELECT that row.
	// The visibility queries must run OUTSIDE the hub mutex, so delivery is
	// three phases: match under the lock, check unlocked, write under the lock.
	rlsGate := isChange && h.app != nil && h.app.realtimeRLSOn(h.slug)

	type target struct {
		c   *websocket.Conn
		sub rtSub
	}
	var targets []target
	h.mu.Lock()
	for c, sub := range h.clients {
		if !isChange {
			if sub.Channel == "" || sub.Channel != ev.Channel {
				continue
			}
			targets = append(targets, target{c, sub})
			continue
		}
		if sub.Channel != "" && sub.Table == "" {
			continue // channel-only subscriber: no change events
		}
		if sub.Table != "" && sub.Table != ev.Table {
			continue
		}
		if sub.Event != "" && sub.Event != ev.Type {
			continue
		}
		if sub.Column != "" {
			// column-equality filter; if the row is absent (large-payload case) or
			// the value doesn't match, this subscriber doesn't get it.
			rv, present := ev.Record[sub.Column]
			if !present || fmt.Sprintf("%v", rv) != sub.Value {
				continue
			}
		}
		targets = append(targets, target{c, sub})
	}
	h.mu.Unlock()

	if rlsGate && len(targets) > 0 {
		kept := targets[:0]
		// one visibility answer per distinct claims set - identical
		// subscribers (same user, several tabs) cost one query
		memo := map[string]bool{}
		for _, t := range targets {
			// service_role sees everything; DELETEs carry a gone row we
			// cannot evaluate policies against, so they pass through
			if t.sub.Role == "service_role" || ev.Type == "DELETE" || ev.Record == nil {
				kept = append(kept, t)
				continue
			}
			vis, seen := memo[t.sub.Claims]
			if !seen {
				vis = h.app.rtRowVisible(h.slug, ev.Table, ev.Record, t.sub.Claims, t.sub.Role)
				memo[t.sub.Claims] = vis
			}
			if vis {
				kept = append(kept, t)
			}
		}
		targets = kept
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range targets {
		if _, still := h.clients[t.c]; !still {
			continue
		}
		t.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := t.c.WriteMessage(websocket.TextMessage, msg); err != nil {
			t.c.Close()
			delete(h.clients, t.c)
			if len(h.clients) == 0 {
				h.emptySince = time.Now()
			}
		}
	}
}

// realtimeRLSOn reports whether change events are RLS-filtered per subscriber.
func (a *app) realtimeRLSOn(slug string) bool {
	var v bool
	a.db.QueryRow(`SELECT coalesce(rls_changes,false) FROM realtime_config WHERE slug=$1`, slug).Scan(&v)
	return v
}

func (a *app) setRealtimeRLS(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	on := r.FormValue("rls_changes") == "on"
	a.db.Exec(`UPDATE realtime_config SET rls_changes=$2 WHERE slug=$1`, slug, on)
	a.audit(r, "realtime-rls", fmt.Sprintf("%s rls_changes=%v", slug, on))
	redirectMsg(w, r, "/p/"+slug+"/realtime", "Change-stream RLS filtering updated.")
}

// rtRowVisible asks the project database whether this subscriber's
// role+claims can SELECT the changed row, exactly as PostgREST would:
// request.jwt.claims set, role assumed, everything rolled back. Errors
// (no grant, no table, bad claims) suppress delivery - safe by default.
// No primary key or missing pk values in the payload = deliver (nothing
// row-level to check against).
func (a *app) rtRowVisible(slug, table string, record map[string]any, claims, role string) bool {
	if role != "anon" && role != "authenticated" {
		return false
	}
	db, err := a.dbFor(slug)
	if err != nil {
		return false
	}
	pks := a.tablePK(db, "public", table)
	if len(pks) == 0 {
		return true
	}
	var conds []string
	var vals []any
	for i, col := range pks {
		v, present := record[col]
		if !present {
			return true
		}
		conds = append(conds, fmt.Sprintf("%s::text = $%d", pq.QuoteIdentifier(col), i+1))
		vals = append(vals, fmt.Sprintf("%v", v))
	}
	tx, err := db.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT set_config('request.jwt.claims', $1, true)`, claims); err != nil {
		return false
	}
	if _, err := tx.Exec(`SET LOCAL ROLE ` + pq.QuoteIdentifier(role)); err != nil {
		return false
	}
	var vis bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM public.`+pq.QuoteIdentifier(table)+
		` WHERE `+strings.Join(conds, " AND ")+`)`, vals...).Scan(&vis); err != nil {
		return false
	}
	return vis
}

func (a *app) serveRealtime(w http.ResponseWriter, r *http.Request, slug string) {
	if !a.realtimeEnabled(slug) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"message":"realtime not enabled"}`)
		return
	}
	// Authenticate before upgrading: the stream carries full row data, so an
	// unauthenticated subscriber would be a data-exfiltration firehose. Require a
	// valid project apikey/JWT (anon/authenticated/service_role), passed as
	// ?apikey= (browsers can't set headers on a WebSocket) or Authorization.
	secret, _ := a.apiConfig(slug)
	key := r.URL.Query().Get("apikey")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "data api not enabled"})
		return
	}
	claims, ok := verifyUserJWT([]byte(secret), key)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "invalid apikey"})
		return
	}
	// Without per-row RLS on the stream, the public anon key would let anyone read
	// every change. By default we require an authenticated/service_role key; an
	// operator can allow anon per project from the Realtime page.
	if role, _ := claims["role"].(string); role == "anon" && a.realtimeRequireAuth(slug) {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "realtime requires an authenticated key (the anon key is blocked for this project)"})
		return
	}
	// Optional subscription filter: ?table=<name>&event=<INSERT|UPDATE|DELETE>
	// and a column-equality filter ?filter=<column>=eq.<value>. Channels:
	// ?channel=<name> joins broadcast/presence traffic on that name;
	// ?presence_key=<id> announces this client on the channel.
	sub := rtSub{
		Table:       r.URL.Query().Get("table"),
		Event:       strings.ToUpper(r.URL.Query().Get("event")),
		Channel:     r.URL.Query().Get("channel"),
		PresenceKey: r.URL.Query().Get("presence_key"),
	}
	sub.Role, _ = claims["role"].(string)
	if cj, err := json.Marshal(claims); err == nil {
		sub.Claims = string(cj)
	}
	// private channels: names starting with "private-" refuse the anon role
	if strings.HasPrefix(sub.Channel, "private-") {
		if role, _ := claims["role"].(string); role == "anon" {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "private channels need an authenticated key"})
			return
		}
	}
	if f := r.URL.Query().Get("filter"); f != "" {
		if i := strings.Index(f, "=eq."); i > 0 {
			sub.Column = f[:i]
			sub.Value = f[i+4:]
		}
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	var h *rtHub
	for i := 0; i < 5; i++ {
		h = a.rtGetHub(slug)
		h.mu.Lock()
		if !h.closed {
			break
		}
		// raced the reaper onto a dead hub - it is (being) removed from the
		// map, so fetch a fresh one
		h.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		h = nil
	}
	if h == nil {
		conn.Close()
		return
	}
	h.clients[conn] = sub
	h.emptySince = time.Time{} // has clients again
	h.mu.Unlock()
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"connected","project":"`+slug+`"}`))
	if sub.Channel != "" && sub.PresenceKey != "" {
		h.channelSend(sub.Channel, map[string]any{"type": "presence", "event": "join",
			"channel": sub.Channel, "key": sub.PresenceKey})
	}
	// read loop: channel subscribers may send broadcast and presence-state
	// requests; everything else just detects disconnect
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			h.mu.Lock()
			delete(h.clients, conn)
			if len(h.clients) == 0 {
				h.emptySince = time.Now()
			}
			h.mu.Unlock()
			if sub.Channel != "" && sub.PresenceKey != "" {
				h.channelSend(sub.Channel, map[string]any{"type": "presence", "event": "leave",
					"channel": sub.Channel, "key": sub.PresenceKey})
			}
			conn.Close()
			return
		}
		if sub.Channel == "" || len(raw) > 16*1024 {
			continue
		}
		var m struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch m.Type {
		case "broadcast":
			pl := "null"
			if len(m.Payload) > 0 && json.Valid(m.Payload) {
				pl = string(m.Payload)
			}
			h.channelSend(sub.Channel, map[string]any{"type": "broadcast",
				"channel": sub.Channel, "payload": json.RawMessage(pl)})
		case "presence_state":
			h.mu.Lock()
			var keys []string
			for _, cs := range h.clients {
				if cs.Channel == sub.Channel && cs.PresenceKey != "" {
					keys = append(keys, cs.PresenceKey)
				}
			}
			h.mu.Unlock()
			resp, _ := json.Marshal(map[string]any{"type": "presence_state",
				"channel": sub.Channel, "keys": keys})
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			conn.WriteMessage(websocket.TextMessage, resp)
		}
	}
}

// channelSend fans a message out to every subscriber of one channel.
func (h *rtHub) channelSend(channel string, msg map[string]any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.broadcast(b)
}

// ----------------------------------------------------------------- enable + page

const rtTriggerFn = `
CREATE OR REPLACE FUNCTION forgebase_notify() RETURNS trigger AS $$
DECLARE rec json; oldrec json; msg text; msgslim text;
BEGIN
  -- record = the row's new state (the deleted row on DELETE, for back-compat);
  -- old_record = the previous state on UPDATE/DELETE (null on INSERT).
  rec := row_to_json(CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END);
  oldrec := CASE WHEN TG_OP='INSERT' THEN NULL ELSE row_to_json(OLD) END;
  msg := json_build_object('type',TG_OP,'table',TG_TABLE_NAME,'record',rec,'old_record',oldrec)::text;
  IF length(msg) < 7900 THEN
    PERFORM pg_notify('forgebase_realtime', msg);
  ELSE
    -- too large with both; keep record alone if it fits (no regression), else drop both
    msgslim := json_build_object('type',TG_OP,'table',TG_TABLE_NAME,'record',rec,'old_record',null)::text;
    IF length(msgslim) < 7900 THEN
      PERFORM pg_notify('forgebase_realtime', msgslim);
    ELSE
      PERFORM pg_notify('forgebase_realtime', json_build_object('type',TG_OP,'table',TG_TABLE_NAME,'record',null,'old_record',null)::text);
    END IF;
  END IF;
  RETURN NULL;
END; $$ LANGUAGE plpgsql;`

// rtEventTriggerFn auto-attaches forgebase_rt to any table created after the
// feature was enabled, so Realtime and Webhooks cover new tables automatically.
const rtEventTriggerFn = `
CREATE OR REPLACE FUNCTION forgebase_rt_autoattach() RETURNS event_trigger AS $$
DECLARE obj record;
BEGIN
  FOR obj IN SELECT * FROM pg_event_trigger_ddl_commands() WHERE command_tag='CREATE TABLE'
  LOOP
    IF obj.schema_name = 'public' THEN
      EXECUTE format('DROP TRIGGER IF EXISTS forgebase_rt ON %s', obj.object_identity);
      EXECUTE format('CREATE TRIGGER forgebase_rt AFTER INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION forgebase_notify()', obj.object_identity);
    END IF;
  END LOOP;
END; $$ LANGUAGE plpgsql;`

func (a *app) enableRealtime(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/realtime", err.Error())
		return
	}
	_ = db
	if err := a.ensureChangeTriggers(slug); err != nil {
		redirectErr(w, r, "/p/"+slug+"/realtime", "Setup failed: "+err.Error())
		return
	}
	a.db.Exec(`INSERT INTO realtime_config(slug,enabled) VALUES ($1,true)
		ON CONFLICT (slug) DO UPDATE SET enabled=true`, slug)
	a.audit(r, "realtime-enable", slug)
	redirectMsg(w, r, "/p/"+slug+"/realtime", "Realtime enabled on all current tables.")
}

func (a *app) disableRealtime(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a.db.Exec(`UPDATE realtime_config SET enabled=false WHERE slug=$1`, slug)
	// Keep the shared change triggers if webhooks still need them; only drop when
	// neither Realtime nor Webhooks is active.
	a.reconcileChangeTriggers(slug)
	// Release the LISTEN backend too if nothing needs the hub anymore (with
	// live clients it lingers until they disconnect; the reaper then gets it).
	a.reapHubIfUnused(slug)
	a.audit(r, "realtime-disable", slug)
	redirectMsg(w, r, "/p/"+slug+"/realtime", "Realtime disabled.")
}

func (a *app) realtimePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	enabled := a.realtimeEnabled(slug)
	var tables int
	var clients int
	if db, err := a.dbFor(slug); err == nil {
		// count TABLES, not trigger-events: information_schema.triggers emits
		// one row per event, so a trigger on INSERT+UPDATE+DELETE counted
		// three times (107 tables showed as "321 tables watched").
		db.QueryRow(`SELECT count(*) FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname='public'
			WHERE t.tgname='forgebase_rt' AND NOT t.tgisinternal`).Scan(&tables)
	}
	rtMu.Lock()
	if h, ok := rtHubs[slug]; ok {
		h.mu.Lock()
		clients = len(h.clients)
		h.mu.Unlock()
	}
	rtMu.Unlock()
	// The in-panel live viewer subscribes with a real project key. Only
	// admins get one rendered into the page (it is a credential), and only
	// while realtime is on.
	// Sign directly from the project's JWT secret rather than going through
	// apiKeys: realtime authenticates on the secret alone, so the viewer must
	// work even when the Data API itself is switched off.
	liveKey := ""
	if enabled && a.atLeast(r, "admin") {
		if secret, _ := a.apiConfig(slug); secret != "" {
			liveKey = signJWT([]byte(secret), "service_role")
		}
	}
	content := renderContent(realtimeBody, map[string]any{
		"Slug": slug, "Enabled": enabled, "Tables": tables, "Clients": clients,
		"RequireAuth": a.realtimeRequireAuth(slug),
		"RLSChanges":  a.realtimeRLSOn(slug),
		"Pubs":        a.rtPublications(slug),
		"WS":          "wss://" + slug + "." + a.cfg.domain + "/realtime/v1",
		"LiveKey":     liveKey,
	})
	a.renderShell(w, r, shellData{Title: slug + " · Realtime", Nav: "realtime", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Realtime"}}}, content)
}
