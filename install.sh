#!/usr/bin/env bash
#
# install.sh - stand up the entire ForgeBase platform on a fresh Ubuntu server
# (22.04/24.04). Idempotent: safe to re-run (also serves as the upgrade path).
#
# One command on a fresh box, then answer the prompts (domain, email) and it
# verifies DNS and provisions Let's Encrypt TLS automatically:
#
#   git clone <repo> forgebase && cd forgebase && sudo bash install.sh
#
# Non-interactive (CI / scripted):
#   DOMAIN=base.example.com ACME_EMAIL=you@example.com sudo -E bash install.sh
#
# Optional env: PANEL_USER / PANEL_PASS (else admin + random), SKIP_FIREWALL=1,
# SKIP_HARDENING=1, SKIP_CADDY=1 (DNS-less/test), SKIP_DNS_CHECK=1,
# MAX_UPLOAD_MB (default 100), LISTEN (default 127.0.0.1:8080).
# DNS: A records for DOMAIN, db.DOMAIN and a wildcard *.DOMAIN pointing here.
#
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then echo "Run as root (sudo -E bash install.sh)"; exit 1; fi
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"

DOMAIN="${DOMAIN:-$(cat /opt/pgforge/domain 2>/dev/null || true)}"
ACME_EMAIL="${ACME_EMAIL:-$(cat /opt/pgforge/acme_email 2>/dev/null || true)}"

# Detect this server's public IP up front (shown in the DNS instructions).
PUBIP="$(curl -fsSL --max-time 8 https://api.ipify.org 2>/dev/null || curl -fsSL --max-time 8 https://ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')"

# ----------------------------------------------------------------- interactive setup
# When DOMAIN/ACME_EMAIL aren't provided and we have a terminal, walk the operator
# through it step by step and verify DNS is pointed before provisioning TLS.
if [ -t 0 ]; then
  echo "=================================================================="
  echo " ForgeBase installer"
  echo "   This server's public IP: ${PUBIP}"
  echo "=================================================================="
  if [ -z "$DOMAIN" ]; then
    read -rp " Domain to host ForgeBase on (e.g. base.example.com): " DOMAIN
    DOMAIN="$(echo "$DOMAIN" | tr 'A-Z' 'a-z' | tr -d ' ')"
  fi
  if [ -z "$ACME_EMAIL" ]; then
    read -rp " Email for Let's Encrypt TLS certificates: " ACME_EMAIL
  fi
fi
[ -z "$DOMAIN" ] && { echo "Set DOMAIN=<your.domain> (with sudo -E)"; exit 1; }
[ -z "$ACME_EMAIL" ] && { echo "Set ACME_EMAIL=<you@example.com> (with sudo -E)"; exit 1; }

# DNS verification loop: the panel, the db host and every project subdomain need
# to resolve here, and Let's Encrypt will fail to issue certs until they do.
resolve_ip() { # $1 = hostname -> first A record (tries a public resolver, then the system)
  { command -v dig >/dev/null 2>&1 && dig +short A "$1" @1.1.1.1 2>/dev/null | grep -Eo '^[0-9.]+' | head -1; } \
    || getent hosts "$1" 2>/dev/null | awk '{print $1}' | head -1
}
if [ -t 0 ] && [ -z "${SKIP_DNS_CHECK:-}" ]; then
  echo
  echo " Point these DNS records (type A) at ${PUBIP}:"
  echo "     ${DOMAIN}"
  echo "     db.${DOMAIN}"
  echo "     *.${DOMAIN}        (wildcard - one record covers every project)"
  echo " Use 'DNS only' (grey cloud) on Cloudflare so TLS is issued directly."
  echo
  while true; do
    read -rp " Press Enter once the records are set (or type 'skip' to continue anyway): " ans || ans=skip
    [ "$ans" = "skip" ] && { echo "  Skipping DNS check; TLS will fail until DNS resolves."; break; }
    apex_ip="$(resolve_ip "$DOMAIN")"
    wild_ip="$(resolve_ip "forgebase-dns-check.${DOMAIN}")" # exercises the wildcard
    if [ "$apex_ip" = "$PUBIP" ] && { [ "$wild_ip" = "$PUBIP" ] || [ -z "$wild_ip" ]; }; then
      echo "  DNS OK: ${DOMAIN} -> ${apex_ip}${wild_ip:+, wildcard -> ${wild_ip}}"
      break
    fi
    echo "  Not there yet: ${DOMAIN} -> ${apex_ip:-(no answer)} (want ${PUBIP}). DNS can take a few minutes."
  done
fi

PANEL_USER="${PANEL_USER:-admin}"

echo "==> Installing ForgeBase for https://${DOMAIN}"

# ----------------------------------------------------------------- packages
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq git curl openssl unzip ca-certificates golang-go fail2ban >/dev/null
systemctl enable --now fail2ban >/dev/null 2>&1 || true

if ! command -v docker >/dev/null 2>&1; then
  echo "==> Installing Docker"
  curl -fsSL https://get.docker.com | sh >/dev/null
fi
docker compose version >/dev/null 2>&1 || { echo "docker compose plugin missing"; exit 1; }

# PostgREST powers the per-project Data API (auto REST). Static binary.
if [ ! -x /usr/local/bin/postgrest ]; then
  echo "==> Installing PostgREST"
  PGRST_URL="$(curl -fsSL https://api.github.com/repos/PostgREST/postgrest/releases/latest \
    | grep -oE 'https://[^"]*linux-static-x86-64.tar.xz' | head -1)"
  if [ -n "$PGRST_URL" ]; then
    ( cd /tmp && curl -fsSL -o pgrst.tar.xz "$PGRST_URL" && tar -xf pgrst.tar.xz \
      && install -m 0755 postgrest /usr/local/bin/postgrest && rm -f pgrst.tar.xz postgrest )
  fi
fi

# Deno powers Edge Functions.
if [ ! -x /usr/local/bin/deno ]; then
  echo "==> Installing Deno"
  curl -fsSL https://deno.land/install.sh | DENO_INSTALL=/usr/local sh >/dev/null 2>&1 || true
fi

# ----------------------------------------------------------------- swap
if ! swapon --show | grep -q '/swapfile'; then
  echo "==> Adding 4G swap"
  fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile
  grep -q '/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi
sysctl -w vm.swappiness=10 >/dev/null

# ----------------------------------------------------------------- layout
mkdir -p /opt/pgforge/{bin,certs,pgbouncer,caddy,stack} \
         /opt/pgforge-backups/{wal,physical,dumps} \
         /opt/pgforge-storage /opt/pgforge-functions
install -m 0644 "$REPO_DIR/server/edge-runner.ts" /opt/pgforge/edge-runner.ts 2>/dev/null || true
printf '%s\n' "$DOMAIN" > /opt/pgforge/domain
printf '%s\n' "$ACME_EMAIL" > /opt/pgforge/acme_email

# ----------------------------------------------------------------- secrets
# On upgrade, reuse the existing secrets so credentials + the stored-password
# encryption key (SESSION_SECRET also encrypts every project's DB password) stay
# stable. Read each from the file that owns it, not by parsing the DSN.
gen() { openssl rand -hex "$1"; }
# Panel login password: exactly 12 characters, letters + digits only (no
# symbols - easy to read out / type). `|| true` swallows the SIGPIPE that head
# raises when it closes the stream early (harmless; the 12 chars are captured).
gen_pass() { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c 12 || true; }
ENVF=/opt/pgforge/pgforged.env
STACK_ENV=/opt/pgforge/stack/.env
readval() { [ -f "$1" ] && sed -n "s/^$2=//p" "$1" | head -1; }
if [ -f "$ENVF" ]; then
  PANEL_PASS="$(readval "$ENVF" PANEL_PASS)"
  SESSION_SECRET="$(readval "$ENVF" SESSION_SECRET)"
  PANEL_USER="$(readval "$ENVF" PANEL_USER)"
  POSTGRES_PASSWORD="$(readval "$STACK_ENV" POSTGRES_PASSWORD)"
fi
# Fill any missing value (fresh install, or a partial prior run).
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-$(gen 24)}"
PANEL_PASS="${PANEL_PASS:-$(gen_pass)}"
SESSION_SECRET="${SESSION_SECRET:-$(gen 32)}"

# ----------------------------------------------------------------- postgres TLS
if [ ! -f /opt/pgforge/certs/server.crt ]; then
  echo "==> Generating Postgres TLS cert (10y self-signed; sslmode=require)"
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -keyout /opt/pgforge/certs/server.key -out /opt/pgforge/certs/server.crt \
    -subj "/CN=${DOMAIN}" 2>/dev/null
fi
chown 999:999 /opt/pgforge/certs/server.key /opt/pgforge/certs/server.crt
chmod 600 /opt/pgforge/certs/server.key
chown -R 999:999 /opt/pgforge-backups/wal /opt/pgforge-backups/physical

# ----------------------------------------------------------------- db stack
echo "==> Postgres 17 + PgBouncer"
cp -r "$REPO_DIR/server/." /opt/pgforge/stack/
rm -rf /opt/pgforge/stack/caddy
printf 'POSTGRES_PASSWORD=%s\n' "$POSTGRES_PASSWORD" > /opt/pgforge/stack/.env
chmod 600 /opt/pgforge/stack/.env
# uid 70 = pgbouncer user inside the edoburu image; it must be able to read the
# auth file. pgforged's rewrites keep the inode, so ownership persists.
touch /opt/pgforge/pgbouncer/userlist.txt
chown 70 /opt/pgforge/pgbouncer/userlist.txt && chmod 600 /opt/pgforge/pgbouncer/userlist.txt
( cd /opt/pgforge/stack
  docker compose build -q
  docker compose up -d db
  echo "    waiting for postgres to be healthy..."
  for i in $(seq 1 60); do
    [ "$(docker inspect -f '{{.State.Health.Status}}' pgforge-db 2>/dev/null)" = healthy ] && break
    sleep 2
  done
  docker compose up -d pgbouncer )

# ----------------------------------------------------------------- caddy
# SKIP_CADDY=1 is for DNS-less test boxes (talk to pgforged directly on :8080).
if [ -z "${SKIP_CADDY:-}" ]; then
  echo "==> Caddy (HTTPS)"
  sed -e "s/__DOMAIN__/${DOMAIN}/g" -e "s/__ACME_EMAIL__/${ACME_EMAIL}/g" \
    "$REPO_DIR/server/caddy/Caddyfile" > /opt/pgforge/caddy/Caddyfile
  cp "$REPO_DIR/server/caddy/docker-compose.yml" /opt/pgforge/caddy/docker-compose.yml
  ( cd /opt/pgforge/caddy && docker compose up -d )
  docker exec pgforge-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile 2>/dev/null || true
else
  echo "==> Skipping Caddy (SKIP_CADDY set); pgforged serves plain HTTP on ${LISTEN:-127.0.0.1:8080}"
fi

# ----------------------------------------------------------------- pgforged
echo "==> Building pgforged (Go control plane)"
# Version/build time are passed in from deploy.sh (git SHA on the dev machine) so
# the running commit is visible in the panel footer for rollback tracking.
PGFORGE_VERSION="${PGFORGE_VERSION:-dev}"
PGFORGE_BUILDTIME="${PGFORGE_BUILDTIME:-unknown}"
LDFLAGS="-X main.version=${PGFORGE_VERSION} -X main.buildTime=${PGFORGE_BUILDTIME}"
( cd "$REPO_DIR/pgforge" && go mod tidy && go build -ldflags "$LDFLAGS" -o /opt/pgforge/bin/pgforged . )
# statement_timeout is a generous backstop on the control-plane connection so a
# runaway meta query can't wedge it forever; interactive editor queries get a
# tighter per-request context timeout in pgforged. options= is how lib/pq sets a
# server GUC at connect (%20 = space, %3D = '=').
cat > "$ENVF" <<ENV
PGFORGE_DSN=postgres://postgres:${POSTGRES_PASSWORD}@127.0.0.1:5432/pgforge?sslmode=require&options=-c%20statement_timeout%3D120000
PANEL_USER=${PANEL_USER}
PANEL_PASS=${PANEL_PASS}
SESSION_SECRET=${SESSION_SECRET}
DOMAIN=${DOMAIN}
LISTEN=${LISTEN:-127.0.0.1:8080}
USERLIST_PATH=/opt/pgforge/pgbouncer/userlist.txt
ENV
chmod 600 "$ENVF"

# ----------------------------------------------------------------- services
# The SQL editor is built into pgforged now; the old interim pgweb is retired.
echo "==> systemd services + timers"
install -m 0755 "$REPO_DIR/scripts/backup.sh"       /opt/pgforge/bin/backup.sh
install -m 0755 "$REPO_DIR/scripts/restore-test.sh" /opt/pgforge/bin/restore-test.sh
install -m 0755 "$REPO_DIR/scripts/set-db-allowlist.sh" /opt/pgforge/bin/set-db-allowlist.sh
# Retire a pgweb unit left by an older install, if present.
if [ -f /etc/systemd/system/pgweb.service ]; then
  systemctl disable --now pgweb 2>/dev/null || true
  rm -f /etc/systemd/system/pgweb.service /opt/pgforge/pgweb.env
fi
for u in pgforged.service pgforge-backup.service pgforge-backup.timer \
         pgforge-restore-test.service pgforge-restore-test.timer; do
  install -m 0644 "$REPO_DIR/systemd/$u" "/etc/systemd/system/$u"
done
systemctl daemon-reload
systemctl enable --now pgforged pgforge-backup.timer pgforge-restore-test.timer >/dev/null 2>&1
systemctl restart pgforged

# ----------------------------------------------------------------- firewall
if [ -z "${SKIP_FIREWALL:-}" ]; then
  echo "==> Firewall"
  sh "$REPO_DIR/scripts/setup-firewall.sh"
fi

# ----------------------------------------------------------------- hardening
install -m 0755 "$REPO_DIR/scripts/harden-server.sh" /opt/pgforge/bin/harden-server.sh
if [ -z "${SKIP_HARDENING:-}" ]; then
  echo "==> Server hardening (fail2ban + auto security updates)"
  sh "$REPO_DIR/scripts/harden-server.sh" || true
fi

sleep 1
echo
echo "=================================================================="
echo " ForgeBase installed."
if [ -z "${SKIP_CADDY:-}" ]; then
  echo "   Panel      : https://${DOMAIN}"
else
  echo "   Panel      : http://<this-ip>:${LISTEN:-127.0.0.1:8080}  (SKIP_CADDY: no TLS)"
fi
echo "     user     : ${PANEL_USER}"
echo "     password : ${PANEL_PASS}"
echo "   Postgres   : ${DOMAIN}:5432 (TLS)   Pooler: ${DOMAIN}:6543"
echo "   pgforged   : $(systemctl is-active pgforged)"
echo
echo " Save PANEL_PASS into your local .env. Off-box backups:"
echo "   apt install rclone && rclone config"
echo "   echo '<remote>:<path>' > /opt/pgforge/backup_remote"
echo "=================================================================="
echo " ForgeBase by FutureForge Studios Private Limited (ffstudios.io)"
echo " Made with care in India."
echo "=================================================================="
