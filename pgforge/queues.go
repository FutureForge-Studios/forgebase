package main

// Message queues: durable, transactional queues living inside the project
// database (schema forgebase), driven by plain SQL functions so any client -
// app code, edge functions, cron jobs - can produce and consume without a
// broker:
//
//	SELECT forgebase.queue_send('emails', '{"to":"a@b.c"}');
//	SELECT * FROM forgebase.queue_read('emails', 30, 10);  -- lock 10 msgs 30s
//	SELECT forgebase.queue_delete_msg('emails', msg_id);   -- ack
//	SELECT forgebase.queue_archive('emails', msg_id);      -- keep a copy
//
// Visibility timeouts make delivery at-least-once: a crashed consumer's
// messages simply reappear when the lock lapses.

import (
	"fmt"
	"net/http"

	"github.com/lib/pq"
)

const queueFns = `
CREATE SCHEMA IF NOT EXISTS forgebase;
CREATE TABLE IF NOT EXISTS forgebase.queues (
	name text PRIMARY KEY,
	created_at timestamptz NOT NULL DEFAULT now()
);
CREATE OR REPLACE FUNCTION forgebase.queue_send(qname text, msg jsonb) RETURNS bigint AS $fb$
DECLARE mid bigint;
BEGIN
	EXECUTE format('INSERT INTO forgebase.%I (message) VALUES ($1) RETURNING msg_id', 'q_'||qname)
		INTO mid USING msg;
	RETURN mid;
END $fb$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION forgebase.queue_read(qname text, vt_seconds int DEFAULT 30, qty int DEFAULT 1)
RETURNS TABLE(msg_id bigint, read_ct int, enqueued_at timestamptz, message jsonb) AS $fb$
BEGIN
	RETURN QUERY EXECUTE format(
		'UPDATE forgebase.%I q SET vt = now() + make_interval(secs => $1), read_ct = q.read_ct + 1
		 WHERE q.msg_id IN (
			SELECT i.msg_id FROM forgebase.%I i
			WHERE i.vt <= now() ORDER BY i.msg_id LIMIT $2
			FOR UPDATE SKIP LOCKED)
		 RETURNING q.msg_id, q.read_ct, q.enqueued_at, q.message',
		'q_'||qname, 'q_'||qname) USING vt_seconds, qty;
END $fb$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION forgebase.queue_delete_msg(qname text, mid bigint) RETURNS boolean AS $fb$
DECLARE n int;
BEGIN
	EXECUTE format('DELETE FROM forgebase.%I WHERE msg_id = $1', 'q_'||qname) USING mid;
	GET DIAGNOSTICS n = ROW_COUNT;
	RETURN n > 0;
END $fb$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION forgebase.queue_archive(qname text, mid bigint) RETURNS boolean AS $fb$
DECLARE n int;
BEGIN
	EXECUTE format(
		'WITH del AS (DELETE FROM forgebase.%I WHERE msg_id = $1 RETURNING *)
		 INSERT INTO forgebase.%I (msg_id, read_ct, enqueued_at, message)
		 SELECT msg_id, read_ct, enqueued_at, message FROM del',
		'q_'||qname, 'q_'||qname||'_archive') USING mid;
	GET DIAGNOSTICS n = ROW_COUNT;
	RETURN n > 0;
END $fb$ LANGUAGE plpgsql;
`

func (a *app) ensureQueueFns(slug string) error {
	db, err := a.dbFor(slug)
	if err != nil {
		return err
	}
	// one multi-statement exec: no bind parameters, so the driver's simple
	// query protocol runs the whole idempotent DDL block in one round trip
	_, err = db.Exec(queueFns)
	return err
}

type queueRow struct {
	Name, Created  string
	Depth, InVT    int64
	Archived       int64
	OldestSeconds  int64
}

func (a *app) listQueues(slug string) []queueRow {
	db, err := a.dbFor(slug)
	if err != nil {
		return nil
	}
	rows, err := db.Query(`SELECT name, to_char(created_at,'Mon DD, YYYY') FROM forgebase.queues ORDER BY name`)
	if err != nil {
		return nil
	}
	var out []queueRow
	for rows.Next() {
		var q queueRow
		rows.Scan(&q.Name, &q.Created)
		out = append(out, q)
	}
	rows.Close()
	for i := range out {
		qt := pq.QuoteIdentifier("q_" + out[i].Name)
		at := pq.QuoteIdentifier("q_" + out[i].Name + "_archive")
		db.QueryRow(fmt.Sprintf(`SELECT count(*), count(*) FILTER (WHERE vt > now()),
				coalesce(extract(epoch FROM now() - min(enqueued_at))::bigint, 0)
			FROM forgebase.%s`, qt)).Scan(&out[i].Depth, &out[i].InVT, &out[i].OldestSeconds)
		db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM forgebase.%s`, at)).Scan(&out[i].Archived)
	}
	return out
}

func (a *app) queuesPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	content := renderContent(queuesBody, map[string]any{
		"Slug": slug, "Queues": a.listQueues(slug),
	})
	a.renderShell(w, r, shellData{Title: slug + " · Queues", Nav: "queues", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Queues"}}}, content)
}

func (a *app) queueCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/queues"
	name := sanitizeIdent(r.FormValue("name"))
	if name == "" || len(name) > 40 {
		redirectErr(w, r, back, "Queue name: letters, digits, underscores (max 40).")
		return
	}
	if err := a.ensureQueueFns(slug); err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	db, _ := a.dbFor(slug)
	qt := pq.QuoteIdentifier("q_" + name)
	at := pq.QuoteIdentifier("q_" + name + "_archive")
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS forgebase.%s (
			msg_id bigserial PRIMARY KEY,
			read_ct int NOT NULL DEFAULT 0,
			vt timestamptz NOT NULL DEFAULT now(),
			enqueued_at timestamptz NOT NULL DEFAULT now(),
			message jsonb NOT NULL
		)`, qt),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON forgebase.%s (vt, msg_id)`,
			pq.QuoteIdentifier("q_"+name+"_vt"), qt),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS forgebase.%s (
			msg_id bigint PRIMARY KEY,
			read_ct int NOT NULL,
			enqueued_at timestamptz NOT NULL,
			archived_at timestamptz NOT NULL DEFAULT now(),
			message jsonb NOT NULL
		)`, at),
		`INSERT INTO forgebase.queues(name) VALUES (` + pq.QuoteLiteral(name) + `) ON CONFLICT DO NOTHING`,
	}
	for _, st := range stmts {
		if _, err := db.Exec(st); err != nil {
			redirectErr(w, r, back, "Create failed: "+err.Error())
			return
		}
	}
	a.chownRel(db, slug, "TABLE", "forgebase", "q_"+name)
	a.chownRel(db, slug, "TABLE", "forgebase", "q_"+name+"_archive")
	a.audit(r, "queue-create", slug+"/"+name)
	redirectMsg(w, r, back, "Queue "+name+" created. Send with SELECT forgebase.queue_send('"+name+"', '{...}').")
}

func (a *app) queueExists(slug, name string) bool {
	db, err := a.dbFor(slug)
	if err != nil {
		return false
	}
	var ok bool
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM forgebase.queues WHERE name=$1)`, name).Scan(&ok)
	return ok
}

func (a *app) queuePurge(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/queues"
	name := r.FormValue("name")
	if !a.queueExists(slug, name) {
		redirectErr(w, r, back, "Unknown queue.")
		return
	}
	db, _ := a.dbFor(slug)
	if _, err := db.Exec(fmt.Sprintf(`TRUNCATE forgebase.%s`, pq.QuoteIdentifier("q_"+name))); err != nil {
		redirectErr(w, r, back, "Purge failed: "+err.Error())
		return
	}
	a.audit(r, "queue-purge", slug+"/"+name)
	redirectMsg(w, r, back, "Queue "+name+" purged (archive kept).")
}

func (a *app) queueDelete(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/queues"
	name := r.FormValue("name")
	if !a.queueExists(slug, name) {
		redirectErr(w, r, back, "Unknown queue.")
		return
	}
	db, _ := a.dbFor(slug)
	db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS forgebase.%s`, pq.QuoteIdentifier("q_"+name)))
	db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS forgebase.%s`, pq.QuoteIdentifier("q_"+name+"_archive")))
	db.Exec(`DELETE FROM forgebase.queues WHERE name=$1`, name)
	a.audit(r, "queue-delete", slug+"/"+name)
	redirectMsg(w, r, back, "Queue "+name+" deleted.")
}

// queueSendTest drops a test message in from the panel.
func (a *app) queueSendTest(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/queues"
	name := r.FormValue("name")
	if !a.queueExists(slug, name) {
		redirectErr(w, r, back, "Unknown queue.")
		return
	}
	db, _ := a.dbFor(slug)
	if _, err := db.Exec(`SELECT forgebase.queue_send($1, '{"test": true, "from": "panel"}'::jsonb)`, name); err != nil {
		redirectErr(w, r, back, "Send failed: "+err.Error())
		return
	}
	redirectMsg(w, r, back, "Test message queued on "+name+".")
}
