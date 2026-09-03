#!/bin/sh
#
# ForgeBase infra apply. The self-updater only swaps the pgforged binary; this
# script is how script/systemd/compose changes in the repo actually reach a
# running box. It is idempotent and safe to run repeatedly.
#
#   apply-infra.sh --safe          (default) install every script in scripts/
#                                  that belongs on the box + every systemd unit,
#                                  daemon-reload, enable timers. Never restarts
#                                  pgforged or any container.
#   apply-infra.sh --with-compose  additionally sync server/ -> the stack dir,
#                                  write docker log defaults, and run
#                                  "docker compose up -d" (recreates changed
#                                  containers; the DB restarts for ~15-30s when
#                                  its definition changed). Never run this
#                                  automatically - it is an operator action.
#
# Called from: install.sh (fresh installs), the pgforged startup reconciler
# (after a self-update, --safe only), and operators over SSH.
set -e
MODE="${1:---safe}"
REPO="$(cat /opt/pgforge/repo_dir 2>/dev/null || true)"
[ -z "$REPO" ] && REPO="$(cd "$(dirname "$0")/.." && pwd)"
LOG=/opt/pgforge/infra-apply.log
BIN=/opt/pgforge/bin
STACK=/opt/pgforge/stack

log() { echo "$(date -u '+%F %T') $*" | tee -a "$LOG"; }

[ -d "$REPO/scripts" ] || { echo "!! repo not found at $REPO"; exit 1; }
mkdir -p "$BIN"
log "== apply-infra $MODE from $REPO =="

# ---- scripts: every operational script the box runs lives in scripts/ and is
# installed wholesale, so the list can never drift from the repo again (the
# 2026-08 walprune bug was exactly a hand-maintained list going stale).
for s in "$REPO"/scripts/*.sh; do
  base="$(basename "$s")"
  case "$base" in
    cow-branch-demo.sh) continue ;; # demo, not operational
  esac
  install -m 0755 "$s" "$BIN/$base"
done
log "scripts installed: $(ls "$REPO"/scripts/*.sh | wc -l) files -> $BIN"

# ---- edge runners: the per-invoke runner and the warm-serve runner
for rn in edge-runner.ts edge-server.ts; do
  [ -f "$REPO/server/$rn" ] && install -m 0644 "$REPO/server/$rn" "/opt/pgforge/$rn"
done
log "edge runners installed"

# ---- PgBouncer client TLS: the pooler container runs unprivileged (uid 70),
# so it gets its own copies of the server cert/key it can actually read.
# Without this, sslmode=require on port 6543 fails and drivers refuse.
mkdir -p /opt/pgforge/pgbouncer/tls
cp -f /opt/pgforge/certs/server.crt /opt/pgforge/certs/server.key /opt/pgforge/pgbouncer/tls/ 2>/dev/null || true
chown -R 70:70 /opt/pgforge/pgbouncer/tls 2>/dev/null || true
chmod 700 /opt/pgforge/pgbouncer/tls 2>/dev/null || true
chmod 600 /opt/pgforge/pgbouncer/tls/server.key 2>/dev/null || true
chmod 644 /opt/pgforge/pgbouncer/tls/server.crt 2>/dev/null || true
log "pgbouncer tls copies refreshed"

# ---- systemd units: install ALL of them, then enable the timers that exist.
for u in "$REPO"/systemd/*; do
  install -m 0644 "$u" "/etc/systemd/system/$(basename "$u")"
done
systemctl daemon-reload
for t in "$REPO"/systemd/*.timer; do
  systemctl enable --now "$(basename "$t")" >/dev/null 2>&1 || true
done
log "systemd units installed: $(ls "$REPO"/systemd | wc -l); timers enabled"

# ---- fail2ban jail for panel login brute-force (only where fail2ban exists)
if command -v fail2ban-client >/dev/null 2>&1 && [ -d /etc/fail2ban ]; then
  changed=0
  if [ -f "$REPO/server/fail2ban/filter-forgebase.conf" ]; then
    cmp -s "$REPO/server/fail2ban/filter-forgebase.conf" /etc/fail2ban/filter.d/forgebase.conf 2>/dev/null || {
      install -m 0644 "$REPO/server/fail2ban/filter-forgebase.conf" /etc/fail2ban/filter.d/forgebase.conf; changed=1; }
  fi
  if [ -f "$REPO/server/fail2ban/jail-forgebase.conf" ]; then
    cmp -s "$REPO/server/fail2ban/jail-forgebase.conf" /etc/fail2ban/jail.d/forgebase.conf 2>/dev/null || {
      install -m 0644 "$REPO/server/fail2ban/jail-forgebase.conf" /etc/fail2ban/jail.d/forgebase.conf; changed=1; }
  fi
  [ "$changed" = 1 ] && { fail2ban-client reload >/dev/null 2>&1 || systemctl reload fail2ban >/dev/null 2>&1 || true; log "fail2ban jail updated + reloaded"; }
fi

if [ "$MODE" = "--with-compose" ]; then
  # docker log rotation defaults for FUTURE containers (existing ones keep
  # their config until recreated below). Only write if absent - never clobber
  # an operator's daemon.json.
  if [ ! -f /etc/docker/daemon.json ]; then
    printf '{\n  "log-driver": "json-file",\n  "log-opts": { "max-size": "10m", "max-file": "3" }\n}\n' > /etc/docker/daemon.json
    log "wrote /etc/docker/daemon.json log defaults (takes effect on docker restart)"
  fi

  # ---- RAM tier -> stack .env (compose command reads PG_* vars). Idempotent:
  # replaces existing PG_* lines, appends missing ones.
  memmb=$(( $(grep MemTotal /proc/meminfo | tr -dc 0-9) / 1024 ))
  if [ "$memmb" -le 2200 ]; then SB=256; WM=4
  elif [ "$memmb" -le 4200 ]; then SB=512; WM=8
  else SB=$((memmb / 4)); WM=8; fi
  EC=$((memmb - SB)); ML=$((SB + 1024))
  env_set() {
    grep -q "^$1=" "$STACK/.env" 2>/dev/null \
      && sed -i "s|^$1=.*|$1=$2|" "$STACK/.env" \
      || echo "$1=$2" >> "$STACK/.env"
  }
  env_set PG_SHARED_BUFFERS "${SB}MB"
  env_set PG_WORK_MEM "${WM}MB"
  env_set PG_EFFECTIVE_CACHE "${EC}MB"
  env_set PG_MAINT_WORK_MEM "64MB"
  # 4 workers, not 2: a single busy partitioned table can monopolise both slots
  # for hours, and every other table then goes unvacuumed while its bloat grows.
  # Peak cost is workers x maintenance_work_mem = 256MB, affordable at any tier.
  env_set PG_AV_WORKERS "4"
  env_set PG_AV_NAPTIME "180"
  env_set PG_MEM_LIMIT "${ML}m"
  log "RAM tier: host ${memmb}MB -> shared_buffers=${SB}MB work_mem=${WM}MB effective_cache=${EC}MB mem_limit=${ML}m"

  # ---- cluster GUCs via ALTER SYSTEM while the old container is still up
  # (compose recreate below picks them up). max_connections is raised ONLY when
  # it is still the untouched default - never clobber an operator's value.
  PSQL="docker exec pgforge-db psql -U postgres"
  cur=$($PSQL -tAc "SELECT setting FROM pg_settings WHERE name='max_connections'" 2>/dev/null || true)
  src=$($PSQL -tAc "SELECT source FROM pg_settings WHERE name='max_connections'" 2>/dev/null || true)
  if [ "$cur" = "100" ] && [ "$src" = "default" ]; then
    $PSQL -c "ALTER SYSTEM SET max_connections = 200" >/dev/null 2>&1 \
      && $PSQL -c "ALTER SYSTEM SET superuser_reserved_connections = 5" >/dev/null 2>&1 \
      && log "max_connections 100(default) -> 200 (+5 reserved), applies on restart"
  fi
  # reloadable: abandoned transactions can't hold locks/bloat forever
  $PSQL -c "ALTER SYSTEM SET idle_in_transaction_session_timeout = '10min'" >/dev/null 2>&1 || true

  # ---- per-role idle-connection timeout: idle DIRECT connections release
  # their backend after 30 min; client pools reconnect transparently. Per-role
  # (not cluster-wide) so the superuser control plane and its LISTEN backends
  # are never affected.
  if [ -n "$($PSQL -tAc 'SELECT 1' 2>/dev/null)" ]; then
    for role in $($PSQL -d pgforge -tAc "SELECT slug FROM projects" 2>/dev/null); do
      $PSQL -c "ALTER ROLE \"$role\" SET idle_session_timeout = '30min'" >/dev/null 2>&1 || true
    done
    log "per-role idle_session_timeout=30min applied to all project roles"
  fi

  # sync compose stack (compose files, pgbouncer config, Dockerfile) - .env is
  # box-local and never touched.
  if [ -d "$REPO/server" ] && [ -d "$STACK" ]; then
    (cd "$REPO/server" && find . -type f ! -name '.env' | while read -r f; do
      mkdir -p "$STACK/$(dirname "$f")"
      cp -f "$f" "$STACK/$f"
    done)
    log "stack synced from server/"
    # up -d recreates only services whose definition changed (the db recreate is
    # the ~15-30s maintenance moment; Postgres data is untouched). A failed
    # apply must FAIL the script - success must never be recorded over it.
    if ! (cd "$STACK" && docker compose up -d >> "$LOG" 2>&1); then
      log "!! docker compose up FAILED - infra NOT applied, see $LOG"
      exit 1
    fi
    # HUP pgbouncer so an ini-only change (no recreate) still reloads
    docker kill -s HUP pgforge-pgbouncer >/dev/null 2>&1 || true
  fi
fi

# record the applied revision so the startup reconciler knows we are current
REV="$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)"
echo "$REV" > /opt/pgforge/infra_rev
log "== done, infra_rev=$REV =="
