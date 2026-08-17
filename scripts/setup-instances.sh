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
SIZE="${INSTANCES_SIZE:-50G}"

echo "==> per-instance mode: btrfs store + cold-start proxy + reaper"
apt-get install -y -qq btrfs-progs >/dev/null 2>&1 || true
mkdir -p "$ROOT"

# A btrfs store gives copy-on-write snapshots. Use a real btrfs mount if the
# operator already provided one at $ROOT; otherwise back it with a loopback image.
if ! mountpoint -q "$ROOT"; then
  if [ ! -f /opt/pgforge/instances.img ]; then
    truncate -s "$SIZE" /opt/pgforge/instances.img
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
