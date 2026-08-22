#!/bin/sh
#
# ForgeBase operator alert -> Discord. Usage: alert-notify.sh "message".
# Reads the webhook from the settings table (set on the System page). Silent
# no-op when unset or unreachable - alerting must never break the caller.
MSG="$1"
[ -z "$MSG" ] && exit 0
HOOK="$(docker exec pgforge-db psql -U postgres -d pgforge -tAc \
  "SELECT value FROM settings WHERE key='discord_webhook'" 2>/dev/null | tr -d '[:space:]')"
[ -z "$HOOK" ] && exit 0
# JSON-escape the bare minimum (quotes + backslashes + newlines)
ESC="$(printf '%s' "$MSG" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')"
curl -fsS -m 8 -H 'Content-Type: application/json' \
  -d "{\"content\":\"$ESC\"}" "$HOOK" >/dev/null 2>&1 || true
