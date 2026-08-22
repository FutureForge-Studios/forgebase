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

# A basebackup's exact start segment comes from its own backup_manifest - the
# only authoritative source. Keying by START TIME date proved WRONG on
# 2026-08-22: two basebackups landed on one date, the date-grep matched the
# OLDER one's history file, and the prune anchored a whole day too early while
# 15GB of dead WAL piled up.
manifest_cutseg() { # $1 = base-<date> dir path; echoes the start SEGMENT name
  python3 - "$1/backup_manifest" <<'PYEOF' 2>/dev/null
import json, sys
m = json.load(open(sys.argv[1]))
r = m["WAL-Ranges"][0]
hi, lo = r["Start-LSN"].split("/")
print("%08X%08X%08X" % (int(r["Timeline"]), int(hi, 16), int(lo, 16) >> 24))
PYEOF
}

# 1) routine prune: drop WAL older than the oldest kept basebackup needs.
OLDEST_DIR="$(ls -d "$OUT"/physical/base-* 2>/dev/null | sort | head -1)" || true
CUTSEG="$([ -n "$OLDEST_DIR" ] && manifest_cutseg "$OLDEST_DIR")" || true
if [ -n "$CUTSEG" ]; then
  docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$CUTSEG" 2>/dev/null || true
fi

# 2) burst guard: if the disk is at/above 80%, tighten to the NEWEST basebackup.
#    Older days stay restorable from their nightly logical dumps.
USED_PCT="$(df --output=pcent "$OUT" 2>/dev/null | tail -1 | tr -dc '0-9')"
if [ "${USED_PCT:-0}" -ge 80 ]; then
  NEWEST_DIR="$(ls -d "$OUT"/physical/base-* 2>/dev/null | sort | tail -1)" || true
  CUTSEG="$([ -n "$NEWEST_DIR" ] && manifest_cutseg "$NEWEST_DIR")" || true
  if [ -n "$CUTSEG" ]; then
    docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$CUTSEG" 2>/dev/null || true
    echo "wal-prune: disk was ${USED_PCT}%, pruned WAL to newest basebackup ($(basename "$NEWEST_DIR"))"
  fi
  # drop stale .part temp files from interrupted archiving
  find "$OUT/wal" -maxdepth 1 -type f -name '*.part' -mmin +10 -delete 2>/dev/null || true
fi

# 2b) hard size cap: a write-churn-heavy tenant can produce dead WAL faster
# than any anchor logic reclaims it. Above WAL_ARCHIVE_MAX_GB (default 8) the
# oldest segments go regardless - shrinking the PITR window, loudly, beats
# the box dying of a full disk (the 2026-08-22 lesson, twice).
CAP_GB="${WAL_ARCHIVE_MAX_GB:-8}"
WAL_KB="$(du -sk "$OUT/wal" 2>/dev/null | cut -f1)"
CAP_KB=$((CAP_GB * 1024 * 1024))
if [ "${WAL_KB:-0}" -gt "$CAP_KB" ]; then
  for f in $(ls "$OUT"/wal/0*.gz 2>/dev/null | sort); do
    rm -f "$f"
    WAL_KB="$(du -sk "$OUT/wal" | cut -f1)"
    [ "$WAL_KB" -le "$CAP_KB" ] && break
  done
  echo "wal-prune: archive exceeded ${CAP_GB}GB cap - oldest segments dropped (PITR window shortened)"
  mkdir -p /opt/pgforge/alerts
  if [ ! -f /opt/pgforge/alerts/wal_cap ]; then
    echo "WAL archive exceeded ${CAP_GB}GB and was cut to the cap - point-in-time recovery reaches less far back today. A heavy-write tenant is churning; see Logs > Slow statements." > /opt/pgforge/alerts/wal_cap
    sh /opt/pgforge/bin/alert-notify.sh "WARNING ForgeBase: WAL archive hit the ${CAP_GB}GB cap and was trimmed. A tenant is writing heavily." || true
  fi
else
  rm -f /opt/pgforge/alerts/wal_cap 2>/dev/null || true
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
