package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
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

// maybeWeeklyDigest sends the Sunday-morning summary (~10:00 IST = 04:xx UTC).
// Called from the sampler's hourly full pass; the digest_last date guard makes
// it fire once per Sunday, and it stays silent without a webhook.
func (a *app) maybeWeeklyDigest() {
	now := time.Now().UTC()
	if now.Weekday() != time.Sunday || now.Hour() != 4 {
		return
	}
	today := now.Format("2006-01-02")
	var last string
	a.db.QueryRow(`SELECT value FROM settings WHERE key='digest_last'`).Scan(&last)
	if last == today || a.discordHook() == "" {
		return
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('digest_last',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, today)

	hs := a.hostStats()
	var total, sleeping, pinned int
	a.db.QueryRow(`SELECT count(*),
		count(*) FILTER (WHERE status='suspended'),
		count(*) FILTER (WHERE keep_awake) FROM projects`).Scan(&total, &sleeping, &pinned)
	// 7-day uptime from the 5-minute host heartbeat (2016 expected samples)
	var beats int
	a.db.QueryRow(`SELECT count(*) FROM metrics_samples
		WHERE slug=$1 AND at > now() - interval '7 days'`, hostSlug).Scan(&beats)
	uptime := float64(beats) / 2016 * 100
	if uptime > 100 {
		uptime = 100
	}
	var updates int
	a.db.QueryRow(`SELECT count(*) FROM audit_log
		WHERE action IN ('self-update','auto-update') AND at > now() - interval '7 days'`).Scan(&updates)
	tier := func(p string) string {
		if out, err := exec.Command("du", "-sh", p).Output(); err == nil {
			if f := strings.Fields(string(out)); len(f) > 0 {
				return f[0]
			}
		}
		return "-"
	}
	msg := fmt.Sprintf(
		"📊 ForgeBase weekly digest (v%s)\n"+
			"Uptime (7d): %.2f%% · RAM %s / %s · Disk free %s\n"+
			"Projects: %d total · %d sleeping · %d pinned awake\n"+
			"Backups on disk: dumps %s · snapshots %s · WAL %s · files %s\n"+
			"Updates installed this week: %d",
		appVersion, uptime, hs.RAMUsed, hs.RAMTotal, hs.DiskFree,
		total, sleeping, pinned,
		tier("/opt/pgforge-backups/dumps"), tier("/opt/pgforge-backups/physical"),
		tier("/opt/pgforge-backups/wal"), tier("/opt/pgforge-backups/files"),
		updates)
	go a.notifyDiscord(msg)
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
