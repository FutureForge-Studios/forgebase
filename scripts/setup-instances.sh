#!/usr/bin/env bash
#
# setup-instances.sh - opt-in: enable per-instance mode (copy-on-write branching
# and scale-to-zero). Sets up a btrfs store for per-project data directories,
# builds the cold-start proxy, and installs the proxy service + reaper timer.
#
# Enable at install time with: INSTANCES=1 bash install.sh
# (Idempotent; safe to re-run. The classic shared-cluster mode is unaffected.)
set -e
REPO_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
ROOT=/opt/pgforge/instances
# Default: 40% of current free space, capped 20G, floor 5G. A fixed large
# default once created a 50G sparse image on a 38G disk - an ENOSPC trap where
# btrfs reports space the host cannot deliver.
free_kb="$(df --output=avail /opt 2>/dev/null | tail -1 | tr -dc 0-9)"
def_g=$(( ${free_kb:-20000000} * 40 / 100 / 1024 / 1024 ))
[ "$def_g" -gt 20 ] && def_g=20
[ "$def_g" -lt 5 ] && def_g=5
SIZE="${INSTANCES_SIZE:-${def_g}G}"
want_g="$(printf '%s' "$SIZE" | tr -dc 0-9)"
have_g=$(( ${free_kb:-0} / 1024 / 1024 ))
if [ "${want_g:-0}" -gt "$have_g" ]; then
  echo "refusing: INSTANCES_SIZE=$SIZE exceeds free space (${have_g}G)"; exit 1
fi

echo "==> per-instance mode: btrfs store + cold-start proxy + reaper"
apt-get install -y -qq btrfs-progs >/dev/null 2>&1 || true
mkdir -p "$ROOT"

# A btrfs store gives copy-on-write snapshots. Use a real btrfs mount if the
# operator already provided one at $ROOT; otherwise back it with a loopback image.
if ! mountpoint -q "$ROOT"; then
  if [ ! -f /opt/pgforge/instances.img ]; then
    fallocate -l "$SIZE" /opt/pgforge/instances.img
    mkfs.btrfs -q -f /opt/pgforge/instances.img
  fi
  mount -o loop /opt/pgforge/instances.img "$ROOT"
  grep -q 'instances.img' /etc/fstab || \
    echo "/opt/pgforge/instances.img $ROOT btrfs loop 0 0" >> /etc/fstab
fi

install -m 0755 "$REPO_DIR/scripts/pg-instance.sh" /opt/pgforge/bin/pg-instance.sh
( cd "$REPO_DIR/tools/pgproxy" && go build -o /opt/pgforge/bin/pgproxy . )

for u in pgforge-pgproxy.service pgforge-reaper.service pgforge-reaper.timer; do
  install -m 0644 "$REPO_DIR/systemd/$u" "/etc/systemd/system/$u"
done
systemctl daemon-reload
systemctl enable --now pgforge-pgproxy.service pgforge-reaper.timer >/dev/null 2>&1

echo "    enabled: btrfs at $ROOT, cold-start proxy on :5433, reaper every 5 min"
