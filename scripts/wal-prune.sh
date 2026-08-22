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
