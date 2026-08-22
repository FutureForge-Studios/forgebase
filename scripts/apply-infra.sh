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
#                                  write docker log defaults, and
#                                  `docker compose up -d` (recreates changed
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

# ---- systemd units: install ALL of them, then enable the timers that exist.
for u in "$REPO"/systemd/*; do
  install -m 0644 "$u" "/etc/systemd/system/$(basename "$u")"
done
systemctl daemon-reload
for t in "$REPO"/systemd/*.timer; do
  systemctl enable --now "$(basename "$t")" >/dev/null 2>&1 || true
done
log "systemd units installed: $(ls "$REPO"/systemd | wc -l); timers enabled"

if [ "$MODE" = "--with-compose" ]; then
  # docker log rotation defaults for FUTURE containers (existing ones keep
  # their config until recreated below). Only write if absent - never clobber
  # an operator's daemon.json.
  if [ ! -f /etc/docker/daemon.json ]; then
    printf '{\n  "log-driver": "json-file",\n  "log-opts": { "max-size": "10m", "max-file": "3" }\n}\n' > /etc/docker/daemon.json
    log "wrote /etc/docker/daemon.json log defaults (takes effect on docker restart)"
  fi
  # sync compose stack (compose files, pgbouncer config, Dockerfile) - .env is
  # box-local and never touched.
  if [ -d "$REPO/server" ] && [ -d "$STACK" ]; then
    (cd "$REPO/server" && find . -type f ! -name '.env' | while read -r f; do
      mkdir -p "$STACK/$(dirname "$f")"
      cp -f "$f" "$STACK/$f"
    done)
    log "stack synced from server/"
    (cd "$STACK" && docker compose up -d 2>&1 | tail -3 | tee -a "$LOG")
  fi
fi

# record the applied revision so the startup reconciler knows we are current
REV="$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)"
echo "$REV" > /opt/pgforge/infra_rev
log "== done, infra_rev=$REV =="
