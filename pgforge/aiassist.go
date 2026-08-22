package main

// Optional AI assistant, bring-your-own-key: each panel user stores an
// endpoint + key + model in Account settings, and the SQL editor's Ask-AI
// button turns a natural-language prompt into SQL with the live schema as
// context. Supports Anthropic's Messages API and any OpenAI-compatible
// /chat/completions endpoint. Keys are encrypted at rest in the meta DB and
// only ever sent to the endpoint the user configured.

import (
	"bytes"
	"encoding/json"
	"fmt"
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

	var req *http.Request
	if strings.Contains(base, "anthropic") {
		payload, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": 1024, "system": system,
			"messages": []map[string]string{{"role": "user", "content": body.Prompt}},
		})
		req, _ = http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader(payload))
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		payload, _ := json.Marshal(map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "system", "content": system},
				{"role": "user", "content": body.Prompt},
			},
		})
		req, _ = http.NewRequest(http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, 502, map[string]string{"message": "AI endpoint unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	var out struct {
		Content []struct{ Text string } `json:"content"` // anthropic
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"` // openai
		Error struct{ Message string } `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	text := ""
	if len(out.Content) > 0 {
		text = out.Content[0].Text
	} else if len(out.Choices) > 0 {
		text = out.Choices[0].Message.Content
	}
	if text == "" {
		msg := out.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		writeJSON(w, 502, map[string]string{"message": "AI returned nothing: " + msg})
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
