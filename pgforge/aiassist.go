package main

// Optional AI assistant, bring-your-own-key: each panel user stores an
// endpoint + key + model in Account settings, and the SQL editor's Ask-AI
// button turns a natural-language prompt into SQL with the live schema as
// context. Supports Anthropic's Messages API and any OpenAI-compatible
// /chat/completions endpoint. Keys are encrypted at rest in the meta DB and
// only ever sent to the endpoint the user configured.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *app) aiConfigFor(user string) (base, key, model string) {
	a.db.QueryRow(`SELECT coalesce(ai_base,''), coalesce(pgp_sym_decrypt(ai_key_enc,$2),''), coalesce(ai_model,'')
		FROM users WHERE name=$1`, user, string(a.cfg.secret)).Scan(&base, &key, &model)
	return
}

func (a *app) saveAIConfig(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	base := strings.TrimRight(strings.TrimSpace(r.FormValue("ai_base")), "/")
	model := strings.TrimSpace(r.FormValue("ai_model"))
	key := strings.TrimSpace(r.FormValue("ai_key"))
	if base != "" && !strings.HasPrefix(base, "https://") {
		redirectErr(w, r, "/account", "Endpoint must be https.")
		return
	}
	if key == "" {
		a.db.Exec(`UPDATE users SET ai_base=$2, ai_model=$3 WHERE name=$1`, user, base, model)
	} else {
		a.db.Exec(`UPDATE users SET ai_base=$2, ai_model=$3, ai_key_enc=pgp_sym_encrypt($4,$5) WHERE name=$1`,
			user, base, model, key, string(a.cfg.secret))
	}
	a.audit(r, "ai-config", user)
	redirectMsg(w, r, "/account", "AI settings saved.")
}

// aiModels lists the models an endpoint offers, so the Account page can show
// a dropdown instead of a free-text model field. The key never reaches the
// browser's own requests: the panel proxies the one models call server-side,
// using the just-typed key or, when blank, the stored one.
func (a *app) aiModels(w http.ResponseWriter, r *http.Request) {
	var body struct{ Base, Key string }
	json.NewDecoder(r.Body).Decode(&body)
	base := strings.TrimRight(strings.TrimSpace(body.Base), "/")
	key := strings.TrimSpace(body.Key)
	if !strings.HasPrefix(base, "https://") {
		writeJSON(w, 400, map[string]string{"message": "set an https endpoint first"})
		return
	}
	if key == "" {
		_, key, _ = a.aiConfigFor(currentUser(r))
	}
	if key == "" {
		writeJSON(w, 400, map[string]string{"message": "enter an API key first"})
		return
	}
	var req *http.Request
	if strings.Contains(base, "anthropic") {
		req, _ = http.NewRequest(http.MethodGet, base+"/v1/models?limit=100", nil)
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req, _ = http.NewRequest(http.MethodGet, base+"/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, 502, map[string]string{"message": "endpoint unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
		Error struct{ Message string } `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Data) == 0 {
		msg := out.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		writeJSON(w, 502, map[string]string{"message": "could not list models: " + msg})
		return
	}
	type model struct{ ID, Name string }
	var models []model
	for _, m := range out.Data {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		models = append(models, model{m.ID, name})
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// callAI sends a chat to the user's configured endpoint (Anthropic Messages
// or any OpenAI-compatible /chat/completions) and returns the reply text.
func callAI(base, key, model, system string, messages []map[string]string) (string, error) {
	var req *http.Request
	if strings.Contains(base, "anthropic") {
		payload, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": 1500, "system": system, "messages": messages,
		})
		req, _ = http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader(payload))
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		all := append([]map[string]string{{"role": "system", "content": system}}, messages...)
		payload, _ := json.Marshal(map[string]any{"model": model, "messages": all})
		req, _ = http.NewRequest(http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("AI endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		// Anthropic returns a LIST of typed blocks - reasoning models (Opus,
		// extended thinking) put a "thinking" block FIRST, so taking block 0
		// blindly yields an empty reply. Collect every text block.
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Error struct{ Message string } `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" || (c.Type == "" && c.Text != "") {
			text.WriteString(c.Text)
		}
	}
	if text.Len() > 0 {
		return text.String(), nil
	}
	if len(out.Choices) > 0 && out.Choices[0].Message.Content != "" {
		return out.Choices[0].Message.Content, nil
	}
	msg := out.Error.Message
	if msg == "" {
		msg = resp.Status
	}
	return "", fmt.Errorf("AI returned nothing: %s", msg)
}

// streamAI is callAI's streaming twin: it requests server-sent events from
// the provider and hands each text delta to emit as it arrives, so the
// panel can show the reply while it is being written.
func streamAI(base, key, model, system string, messages []map[string]string, emit func(string)) error {
	var req *http.Request
	if strings.Contains(base, "anthropic") {
		payload, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": 1500, "system": system,
			"messages": messages, "stream": true,
		})
		req, _ = http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader(payload))
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		all := append([]map[string]string{{"role": "system", "content": system}}, messages...)
		payload, _ := json.Marshal(map[string]any{"model": model, "messages": all, "stream": true})
		req, _ = http.NewRequest(http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("AI endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		json.Unmarshal(raw, &e)
		if e.Error.Message != "" {
			return fmt.Errorf("%s", e.Error.Message)
		}
		return fmt.Errorf("AI endpoint answered %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	got := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev struct {
			// anthropic content_block_delta
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			// openai chat.completion.chunk
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
			emit(ev.Delta.Text)
			got = true
		}
		for _, c := range ev.Choices {
			if c.Delta.Content != "" {
				emit(c.Delta.Content)
				got = true
			}
		}
	}
	if !got {
		return fmt.Errorf("AI returned nothing")
	}
	return nil
}

// forgebaseCheatSheet is what the assistant knows about the PLATFORM, so it
// can answer "how do I..." questions, not just write SQL.
const forgebaseCheatSheet = `ForgeBase panel map (every path is under the project):
- Table Editor (/tables): browse/edit rows, type-aware editors, filters, sort, CSV export, schema editing, FK management.
- SQL Editor (/sql): tabs, autocomplete, Explain plans, run-as-role, history, format; the Ask AI button generates SQL.
- Objects (/objects): functions, triggers, enum types, indexes, constraints. Policies (/policies): full RLS editor + column grants.
- Schema diagram (/erd). Database (/database): extensions, roles, publications (logical replication), foreign servers (postgres_fdw), timeouts, connection info (direct 5432, pooled 6543, read replica 5434 when enabled).
- Data API (/api): auto REST (PostgREST) at https://<project>.<domain>/rest/v1 with anon/service keys, GraphQL at /graphql/v1, OpenAPI, TypeScript types, API explorer, IP allowlist, RS256/JWKS signing.
- Auth (/auth): email+password, magic links, email OTP, phone OTP via SMS webhook, anonymous sign-ins, 12 OAuth providers + generic OIDC + SAML SSO, TOTP MFA with recovery codes, captcha, rate limits, leaked-password check, email templates, user management + impersonation. Endpoints under /auth/v1.
- Storage (/storage): buckets, folders, drag-drop, quotas, signed URLs, resumable tus uploads at /storage/v1/tus, S3-protocol access with scoped keys, path access rules, image transforms (?width=&height=).
- Realtime (/realtime): change streams over WebSocket at /realtime/v1, channels/broadcast/presence, per-subscriber RLS filtering. Webhooks (/webhooks): table-change HTTP calls with replay.
- Edge Functions (/functions): Deno handlers at /functions/v1/<name>, warm processes, streaming, WebSockets, secrets, cron schedules.
- Queues (/queues): forgebase.queue_send/read/delete_msg/archive. Cron (/cron): pg_cron jobs. Vault (Settings): forgebase.secret_set/get/list/delete.
- Branches (/branches): copy-on-write or full-copy branches, time-travel (from any past instant), anonymized branches, schema diff, reset, expiry.
- Monitoring, Usage, Advisors, Logs (+saved views, log shipping), Migrations, Backups (PITR restore to a new project), Sync/Clone, Settings (compute limits, vault, migrate to dedicated instance).
Connection strings live on the project home page. The panel Account page holds personal API keys and AI settings.`

// aiChat is the panel-wide assistant: multi-turn, schema-aware, and briefed
// on the platform itself, so it can answer feature questions AND data
// questions for this project.
func (a *app) aiChat(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	base, key, model := a.aiConfigFor(currentUser(r))
	if base == "" || key == "" {
		writeJSON(w, 400, map[string]string{"message": "no AI key configured - add one on the Account page"})
		return
	}
	var body struct {
		Messages []map[string]string `json:"messages"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&body)
	if len(body.Messages) == 0 {
		writeJSON(w, 400, map[string]string{"message": "say something first"})
		return
	}
	if len(body.Messages) > 12 {
		body.Messages = body.Messages[len(body.Messages)-12:]
	}
	// drop empty messages (a failed earlier exchange leaves them in the
	// client's history, and providers reject empty content blocks)
	kept := body.Messages[:0]
	for _, m := range body.Messages {
		if m["role"] != "user" && m["role"] != "assistant" {
			writeJSON(w, 400, map[string]string{"message": "bad message role"})
			return
		}
		if strings.TrimSpace(m["content"]) == "" {
			continue
		}
		if len(m["content"]) > 8000 {
			m["content"] = m["content"][:8000]
		}
		kept = append(kept, m)
	}
	body.Messages = kept
	if len(body.Messages) == 0 {
		writeJSON(w, 400, map[string]string{"message": "say something first"})
		return
	}
	var schema strings.Builder
	for _, t := range a.schemaTree(slug) {
		fmt.Fprintf(&schema, "%s(", t.Name)
		for i, c := range t.Cols {
			if i > 0 {
				schema.WriteString(", ")
			}
			schema.WriteString(c.Name + " " + c.Type)
		}
		schema.WriteString(")\n")
		if schema.Len() > 10000 {
			schema.WriteString("... (more tables omitted)\n")
			break
		}
	}
	system := "You are the built-in assistant of ForgeBase, a self-hosted Postgres platform. " +
		"You are helping with the project '" + slug + "'. Be concise and practical. " +
		"When you produce SQL, put it in a ```sql fence so the panel can offer to run it.\n\n" +
		forgebaseCheatSheet + "\n\nThis project's live schema (public):\n" + schema.String()
	// Stream the reply as plain text chunks so the panel shows it while the
	// model is still writing. Errors BEFORE the first chunk go out as JSON
	// (the client branches on Content-Type); after the first chunk the text
	// path is committed and the client shows whatever arrived.
	fl, _ := w.(http.Flusher)
	first := true
	err := streamAI(base, key, model, system, body.Messages, func(chunk string) {
		if first {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			first = false
		}
		io.WriteString(w, chunk)
		if fl != nil {
			fl.Flush()
		}
	})
	if err != nil && first {
		writeJSON(w, 502, map[string]string{"message": err.Error()})
		return
	}
	a.audit(r, "ai-chat", slug)
}

// aiSQL answers a natural-language prompt with SQL for this project.
func (a *app) aiSQL(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	base, key, model := a.aiConfigFor(currentUser(r))
	if base == "" || key == "" {
		writeJSON(w, 400, map[string]string{"message": "configure an AI endpoint and key in Account settings first"})
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	body.Prompt = strings.TrimSpace(body.Prompt)
	if body.Prompt == "" || len(body.Prompt) > 4000 {
		writeJSON(w, 400, map[string]string{"message": "prompt required (max 4000 chars)"})
		return
	}
	var schema strings.Builder
	for _, t := range a.schemaTree(slug) {
		fmt.Fprintf(&schema, "%s(", t.Name)
		for i, c := range t.Cols {
			if i > 0 {
				schema.WriteString(", ")
			}
			schema.WriteString(c.Name + " " + c.Type)
		}
		schema.WriteString(")\n")
		if schema.Len() > 12000 {
			schema.WriteString("... (more tables omitted)\n")
			break
		}
	}
	system := "You write PostgreSQL for this schema:\n" + schema.String() +
		"\nAnswer with ONLY the SQL statement - no prose, no code fences."

	text, err := callAI(base, key, model, system,
		[]map[string]string{{"role": "user", "content": body.Prompt}})
	if err != nil {
		writeJSON(w, 502, map[string]string{"message": err.Error()})
		return
	}
	// strip stray code fences the model may add despite instructions
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```sql")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	a.audit(r, "ai-sql", slug)
	writeJSON(w, 200, map[string]string{"sql": strings.TrimSpace(text)})
}
