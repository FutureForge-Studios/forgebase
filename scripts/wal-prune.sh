#!/bin/sh
#
# ForgeBase hourly WAL hygiene. The nightly backup.sh already prunes the WAL
# archive with pg_archivecleanup, but only once a day at 03:30. A single heavy
# operation during the day (a project clone, a big import) can write many GB of
# WAL in minutes; on a small disk that can reach 100% long before the nightly
# prune runs, and a full disk crash-loops Postgres ("could not write lock file
# postmaster.pid: No space left on device" - this took the box down on
# 2026-08-22). This job runs hourly and applies the same safe pruning logic, so
# a burst is cleaned up within the hour instead of within the day.
#
# Safe by construction: WAL is only removed up to the START segment of the
# oldest kept basebackup - exactly what pg_archivecleanup guarantees - so every
# kept basebackup always retains the WAL it needs for PITR.
set -e
CONT=pgforge-db
OUT=/opt/pgforge-backups

# The .backup history files in the archive record each basebackup's start.
wal_cutoff() { # $1 = base-<date> date; echoes the backup's START segment name
  for f in "$OUT"/wal/*.backup "$OUT"/wal/*.backup.gz; do
    [ -f "$f" ] || continue
    case "$f" in *.gz) reader=zcat;; *) reader=cat;; esac
    if $reader "$f" 2>/dev/null | grep -q "START TIME: $1"; then
      $reader "$f" 2>/dev/null | sed -n 's/.*(file \([0-9A-F]*\)).*/\1/p' | head -1
      return
    fi
  done
}

# 1) routine prune: drop WAL older than the oldest kept basebackup needs.
OLDEST_BASE="$(ls -d "$OUT"/physical/base-* 2>/dev/null | sort | head -1 | sed 's/.*base-//')" || true
CUTSEG="$([ -n "$OLDEST_BASE" ] && wal_cutoff "$OLDEST_BASE")" || true
if [ -n "$CUTSEG" ]; then
  docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$CUTSEG" 2>/dev/null || true
fi

# 2) burst guard: if the disk is at/above 80%, tighten to the NEWEST basebackup.
#    Older days stay restorable from their nightly logical dumps.
USED_PCT="$(df --output=pcent "$OUT" 2>/dev/null | tail -1 | tr -dc '0-9')"
if [ "${USED_PCT:-0}" -ge 80 ]; then
  NEWEST_BASE="$(ls -d "$OUT"/physical/base-* 2>/dev/null | sort | tail -1 | sed 's/.*base-//')" || true
  CUTSEG="$([ -n "$NEWEST_BASE" ] && wal_cutoff "$NEWEST_BASE")" || true
  if [ -n "$CUTSEG" ]; then
    docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$CUTSEG" 2>/dev/null || true
    echo "wal-prune: disk was ${USED_PCT}%, pruned WAL to newest basebackup ($NEWEST_BASE)"
  fi
  # drop stale .part temp files from interrupted archiving
  find "$OUT/wal" -maxdepth 1 -type f -name '*.part' -mmin +10 -delete 2>/dev/null || true
fi

# 3) watchdogs. Each writes an alert file (System page shows a red banner while
# one exists) and pings Discord ONCE per episode - on the healthy->unhealthy
# transition, not every hour. Cleared automatically when healthy again.
ALERTS=/opt/pgforge/alerts
NOTIFY=/opt/pgforge/bin/alert-notify.sh
mkdir -p "$ALERTS"

# pg_wal inside the data dir: if it exceeds 2GB (double max_wal_size) the
# archiver is lagging or wedged - the exact chain that once filled the disk.
WALDIR_MB="$(docker exec "$CONT" psql -U postgres -tAc \
  "SELECT coalesce(sum(size),0)/1024/1024 FROM pg_ls_waldir()" 2>/dev/null | cut -d. -f1)"
if [ "${WALDIR_MB:-0}" -ge 2048 ]; then
  if [ ! -f "$ALERTS/pg_wal" ]; then
    { echo "pg_wal is ${WALDIR_MB}MB (limit 2048MB) - WAL archiving is lagging or stuck."
      docker exec "$CONT" psql -U postgres -tAc \
        "SELECT 'last_archived='||coalesce(last_archived_wal,'-')||' failed_count='||failed_count FROM pg_stat_archiver" 2>/dev/null
    } > "$ALERTS/pg_wal"
    sh "$NOTIFY" "WARNING ForgeBase: pg_wal is ${WALDIR_MB}MB - WAL archiving looks stuck. The System page has details." || true
  else
    # refresh contents so the banner shows current numbers, no re-notify
    sed -i "1s/.*/pg_wal is ${WALDIR_MB}MB (limit 2048MB) - WAL archiving is lagging or stuck./" "$ALERTS/pg_wal" 2>/dev/null || true
  fi
else
  [ -f "$ALERTS/pg_wal" ] && rm -f "$ALERTS/pg_wal" \
    && sh "$NOTIFY" "RESOLVED ForgeBase: pg_wal back to ${WALDIR_MB}MB - archiving healthy." || true
fi

# disk usage: warn at 85% (the emergency pruning kicks in at the same line)
if [ "${USED_PCT:-0}" -ge 85 ]; then
  if [ ! -f "$ALERTS/disk" ]; then
    df -h "$OUT" | tail -1 > "$ALERTS/disk"
    sh "$NOTIFY" "WARNING ForgeBase: disk at ${USED_PCT}% - emergency backup pruning is active. Check the System page." || true
  fi
else
  [ -f "$ALERTS/disk" ] && rm -f "$ALERTS/disk" \
    && sh "$NOTIFY" "RESOLVED ForgeBase: disk back to ${USED_PCT}%." || true
fi
