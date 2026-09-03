#!/bin/sh
#
# Give Postgres (and the pooler) the real Let's Encrypt certificate Caddy
# already holds for the hostname the panel hands out in connection strings.
#
# Out of the box install.sh generates a self-signed cert, which means every
# client is stuck on sslmode=require: encrypted, but verifying nothing, and a
# driver that verifies by default (node-postgres among them) fails outright
# until someone sets rejectUnauthorized:false. Caddy is already solving the
# ACME problem for the same hostname, so borrow its answer and verify-full
# starts working with no client configuration at all.
#
# Safe to run at any time: it does nothing unless a valid, different cert is
# available, and it never removes a working one. Daily via pgforge-certsync.timer,
# because Let's Encrypt renews at 30 days remaining and Caddy does it silently.
set -e

CERTS=/opt/pgforge/certs
CADDY=/var/lib/docker/volumes/pgforge-caddy_caddy_data/_data/caddy/certificates
say() { echo "sync-db-cert: $*"; }

DOMAIN="$(sed -n 's/^DOMAIN=//p' /opt/pgforge/pgforged.env 2>/dev/null | head -1)"
[ -n "$DOMAIN" ] || { say "no DOMAIN in pgforged.env, nothing to do"; exit 0; }

# The advertised database host gains a db. prefix once a secondary domain
# exists - same rule as dbHostForDisplay() in the control plane. Read it from
# the same settings row rather than guessing, so the two cannot disagree.
SEC="$(docker exec pgforge-db psql -U postgres -d pgforge -tAc \
  "SELECT value FROM settings WHERE key='domain_secondary'" 2>/dev/null | tr -d '[:space:]')"
if [ -n "$SEC" ]; then HOST="db.$DOMAIN"; else HOST="$DOMAIN"; fi

SRC="$(find "$CADDY" -type d -name "$HOST" 2>/dev/null | head -1)"
if [ -z "$SRC" ] || [ ! -f "$SRC/$HOST.crt" ] || [ ! -f "$SRC/$HOST.key" ]; then
  say "Caddy has no certificate for $HOST yet - keeping the current one"
  exit 0
fi

# Never install something expired or unparseable over a cert that works.
if ! openssl x509 -in "$SRC/$HOST.crt" -noout -checkend 0 >/dev/null 2>&1; then
  say "Caddy's certificate for $HOST is expired or unreadable - keeping the current one"
  exit 0
fi

NEW="$(openssl x509 -in "$SRC/$HOST.crt" -noout -fingerprint -sha256 2>/dev/null)"
CUR="$(openssl x509 -in "$CERTS/server.crt" -noout -fingerprint -sha256 2>/dev/null || true)"
[ "$NEW" = "$CUR" ] && exit 0

# uid 999 is postgres inside the container; the key must stay unreadable to
# anyone else or Postgres refuses to start with it.
install -m 0600 -o 999 -g 999 "$SRC/$HOST.key" "$CERTS/server.key"
install -m 0644 -o 999 -g 999 "$SRC/$HOST.crt" "$CERTS/server.crt"

# Postgres re-reads ssl_cert_file on SIGHUP, so live sessions are undisturbed.
docker exec pgforge-db psql -U postgres -qc "SELECT pg_reload_conf()" >/dev/null 2>&1 || true

# PgBouncer runs unprivileged (uid 70) and needs its own readable copies.
mkdir -p /opt/pgforge/pgbouncer/tls
cp -f "$CERTS/server.crt" "$CERTS/server.key" /opt/pgforge/pgbouncer/tls/ 2>/dev/null || true
chown -R 70:70 /opt/pgforge/pgbouncer/tls 2>/dev/null || true
chmod 700 /opt/pgforge/pgbouncer/tls 2>/dev/null || true
chmod 600 /opt/pgforge/pgbouncer/tls/server.key 2>/dev/null || true
chmod 644 /opt/pgforge/pgbouncer/tls/server.crt 2>/dev/null || true
docker kill -s HUP pgforge-pgbouncer >/dev/null 2>&1 || true

say "installed the Let's Encrypt certificate for $HOST (expires $(openssl x509 -in "$CERTS/server.crt" -noout -enddate | cut -d= -f2))"
