package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Operator alerts via a Discord webhook (settings key discord_webhook, set on
// the System page). Used for security events (login brute-force surges) and,
// later, watchdog alerts (disk filling, WAL archiver stuck). Best-effort:
// alerting must never break or slow the caller.

func (a *app) discordHook() string {
	var hook string
	a.db.QueryRow(`SELECT value FROM settings WHERE key='discord_webhook'`).Scan(&hook)
	return strings.TrimSpace(hook)
}

func (a *app) notifyDiscord(msg string) {
	hook := a.discordHook()
	if hook == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"content": msg})
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Post(hook, "application/json", bytes.NewReader(body)); err == nil {
		resp.Body.Close()
	}
}

// setDiscordWebhook saves (or clears) the webhook and sends a test message so
// the operator immediately knows it works.
func (a *app) setDiscordWebhook(w http.ResponseWriter, r *http.Request) {
	hook := strings.TrimSpace(r.FormValue("webhook"))
	if hook != "" && !strings.HasPrefix(hook, "https://discord.com/api/webhooks/") &&
		!strings.HasPrefix(hook, "https://discordapp.com/api/webhooks/") {
		redirectErr(w, r, "/system", "That does not look like a Discord webhook URL.")
		return
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('discord_webhook',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, hook)
	a.audit(r, "discord-webhook", boolStr(hook != "", "set", "cleared"))
	if hook == "" {
		redirectMsg(w, r, "/system", "Discord alerts disabled.")
		return
	}
	go a.notifyDiscord(fmt.Sprintf("✅ ForgeBase alerts connected (v%s). You'll hear from me when something needs attention.", appVersion))
	redirectMsg(w, r, "/system", "Discord alerts enabled - a test message was just sent.")
}
