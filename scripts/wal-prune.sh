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

# 2b) hard size cap -> COMPACTION, not amputation. Above WAL_ARCHIVE_MAX_GB
# (default 8) the archive is no longer blindly trimmed: we take a FRESH
# basebackup and re-anchor the WAL archive to it, so point-in-time recovery
# stays CONTINUOUS (recent base + small tail) and the archive collapses.
# A busy-but-honest tenant writing hundreds of MB of WAL per hour then costs
# one automatic basebackup every day or two instead of a shrinking PITR
# window and an alert. Guards: at most one auto-basebackup per 12h, and only
# with 3GB+ free disk. Only if compaction cannot run - or the archive is
# STILL over the cap afterwards (true runaway) - does the old trim-oldest
# path fire, loudly.
CAP_GB="${WAL_ARCHIVE_MAX_GB:-8}"
WAL_KB="$(du -sk "$OUT/wal" 2>/dev/null | cut -f1)"
CAP_KB=$((CAP_GB * 1024 * 1024))
# compact proactively from 75% of the cap - waiting for a hard overrun just
# leaves the archive hovering at the cap with hourly trims and alerts
if [ "${WAL_KB:-0}" -gt $((CAP_KB * 3 / 4)) ]; then
  STAMP=/opt/pgforge/last_auto_basebackup
  NOW="$(date +%s)"
  LAST="$(cat "$STAMP" 2>/dev/null || echo 0)"
  FREE_KB="$(df --output=avail "$OUT" 2>/dev/null | tail -1 | tr -dc '0-9')"
  if [ $((NOW - LAST)) -ge 43200 ] && [ "${FREE_KB:-0}" -gt $((3 * 1024 * 1024)) ]; then
    BB="base-$(date -u +%F-%H%M%S)"
    echo "wal-prune: archive over ${CAP_GB}GB - compacting (fresh basebackup $BB + re-anchor)"
    if docker exec "$CONT" sh -c "rm -rf /physical/$BB && pg_basebackup -U postgres -D /physical/$BB -Ft -z -X none" 2>/dev/null; then
      echo "$NOW" > "$STAMP"
      # keep the newest 2 basebackups (same rule as the nightly retention)
      ls -d "$OUT"/physical/base-* 2>/dev/null | sort | head -n -2 | while read -r d; do rm -rf "$d"; done
      NEWSEG="$(manifest_cutseg "$OUT/physical/$BB")" || true
      [ -n "$NEWSEG" ] && docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$NEWSEG" 2>/dev/null || true
      WAL_KB="$(du -sk "$OUT/wal" | cut -f1)"
      echo "wal-prune: compacted - archive now $((WAL_KB / 1024))MB, PITR continuous from $BB"
      sh /opt/pgforge/bin/alert-notify.sh "ForgeBase: WAL archive auto-compacted (fresh basebackup, PITR stays continuous). A tenant is writing heavily - see per-project Usage pages." || true
    else
      echo "wal-prune: auto-basebackup FAILED - falling back to trim"
    fi
  fi
fi
if [ "${WAL_KB:-0}" -gt "$CAP_KB" ]; then
  for f in $(ls "$OUT"/wal/0*.gz 2>/dev/null | sort); do
    rm -f "$f"
    WAL_KB="$(du -sk "$OUT/wal" | cut -f1)"
    [ "$WAL_KB" -le "$CAP_KB" ] && break
  done
  echo "wal-prune: archive exceeded ${CAP_GB}GB cap - oldest segments dropped (PITR window shortened)"
  mkdir -p /opt/pgforge/alerts
  if [ ! -f /opt/pgforge/alerts/wal_cap ]; then
    # NAME the churner: sample per-database write counters 15s apart and report
    # the database generating the most rows right now (the 2026-08-23 lesson:
    # a generic "a tenant is churning" alert costs a manual investigation).
    # Two separate sessions on purpose - inside ONE transaction pg_stat_*
    # views are snapshot-frozen and every delta reads as zero.
    W1="$(docker exec pgforge-db psql -U postgres -Atc "SELECT datname||'|'||(tup_inserted+tup_updated+tup_deleted) FROM pg_stat_database WHERE datname IS NOT NULL" 2>/dev/null)"
    sleep 15
    W2="$(docker exec pgforge-db psql -U postgres -Atc "SELECT datname||'|'||(tup_inserted+tup_updated+tup_deleted) FROM pg_stat_database WHERE datname IS NOT NULL" 2>/dev/null)"
    CHURNER="$(printf '%s\n%s\n' "$W1" "$W2" | awk -F'|' '
      $1=="" { next }
      !($1 in seen) { seen[$1]=$2; next }
      { d=$2-seen[$1]; if (d>best) { best=d; name=$1 } }
      END { if (name!="") printf "%s (+%d rows/15s)", name, best }')"
    [ -n "$CHURNER" ] || CHURNER="unknown - see the Usage page per project"
    echo "WAL archive exceeded ${CAP_GB}GB and was cut to the cap - point-in-time recovery reaches less far back today. Top writer right now: ${CHURNER}. Check that project's Usage page and Advisors." > /opt/pgforge/alerts/wal_cap
    sh /opt/pgforge/bin/alert-notify.sh "WARNING ForgeBase: WAL archive hit the ${CAP_GB}GB cap and was trimmed. Top writer right now: ${CHURNER}." || true
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

# stale backups: the nightly run failed silently from 2026-08-27 to 2026-09-03
# because one corrupted comment line in backup.sh exited 127 before any dump ran.
# Nothing noticed, because the only evidence was a systemd unit failure nobody
# reads. This watchdog does not care WHY dumping stopped - it only asks whether
# a dump landed recently, which is the property that actually matters. 48h, so
# a single skipped night (reboot, long restore drill) is not an alert.
NEWEST_DUMP="$(find "$OUT/dumps" -maxdepth 1 -name '*.dump' -newermt '-48 hours' 2>/dev/null | head -1)"
if [ -z "$NEWEST_DUMP" ]; then
  if [ ! -f "$ALERTS/backup_stale" ]; then
    { AGE="$(find "$OUT/dumps" -maxdepth 1 -name '*.dump' -printf '%TY-%Tm-%Td %TH:%TM %p\n' 2>/dev/null | sort | tail -1)"
      echo "No database dump has completed in the last 48 hours - backups are not running."
      echo "Newest dump on disk: ${AGE:-none at all}"
      echo "Last run: $(systemctl show -p Result --value pgforge-backup.service 2>/dev/null || echo unknown). Run 'sh /opt/pgforge/bin/backup.sh' to see the error."
    } > "$ALERTS/backup_stale"
    sh "$NOTIFY" "WARNING ForgeBase: no database dump in 48h - backups are not running. See the System page." || true
  fi
else
  [ -f "$ALERTS/backup_stale" ] && rm -f "$ALERTS/backup_stale" \
    && sh "$NOTIFY" "RESOLVED ForgeBase: nightly dumps are landing again." || true
fi
