#!/bin/sh
#
# ForgeBase nightly backups. Two layers every night:
#   1. logical:  pg_dump -Fc of every database + pg_dumpall globals
#                -> /opt/pgforge-backups/dumps/, 30-day retention (editable
#                via /opt/pgforge/retention_days)
#   2. physical: pg_basebackup (tar+gzip) -> /opt/pgforge-backups/physical/,
#                7-day retention. Combined with the continuous WAL archive
#                (/opt/pgforge-backups/wal, gzipped segments written by
#                archive_command in the compose file) this gives PITR.
# WAL is pruned to what the oldest kept basebackup needs (pg_archivecleanup),
# with an emergency prune at 85% disk so the archive can never fill the box.
# Optional off-box sync: put an rclone remote path in
# /opt/pgforge/backup_remote and install rclone.
#
set -e
CONT=pgforge-db
OUT=/opt/pgforge-backups
DATE="$(date -u +%F)"
RETENTION_DUMPS="${RETENTION_DUMPS:-$(cat /opt/pgforge/retention_days 2>/dev/null || echo 30)}"
RETENTION_BASE="${RETENTION_BASE:-7}"

echo "== pgforge backup $(date -u '+%F %T') UTC =="
mkdir -p "$OUT/dumps" "$OUT/physical"

# ---- layer 1: logical dumps
docker exec "$CONT" pg_dumpall -U postgres --globals-only > "$OUT/dumps/globals-$DATE.sql"
for db in $(docker exec "$CONT" psql -U postgres -tAc \
    "select datname from pg_database where not datistemplate"); do
  if docker exec "$CONT" pg_dump -U postgres -Fc -d "$db" > "$OUT/dumps/$db-$DATE.dump" 2>"$OUT/.err"; then
    echo "  ok dump $db ($(wc -c < "$OUT/dumps/$db-$DATE.dump") bytes)"
  else
    echo "  ! dump $db failed: $(head -1 "$OUT/.err")"
    rm -f "$OUT/dumps/$db-$DATE.dump"
  fi
done
rm -f "$OUT/.err"

# ---- layer 2: physical basebackup (for PITR together with the WAL archive)
if docker exec "$CONT" sh -c "rm -rf /physical/base-$DATE && pg_basebackup -U postgres -D /physical/base-$DATE -Ft -z -X none" 2>"$OUT/.err"; then
  echo "  ok basebackup base-$DATE"
else
  echo "  ! basebackup failed: $(head -1 "$OUT/.err")"; rm -f "$OUT/.err"
fi

# ---- layer 3: file plane (storage objects + edge functions) so a restore can
# rebuild the whole platform, not just the databases.
mkdir -p "$OUT/files"
[ -d /opt/pgforge-storage ] && tar -czf "$OUT/files/storage-$DATE.tgz" -C /opt pgforge-storage 2>/dev/null && echo "  ok storage archive"
[ -d /opt/pgforge-functions ] && tar -czf "$OUT/files/functions-$DATE.tgz" -C /opt pgforge-functions 2>/dev/null && echo "  ok functions archive"

# ---- retention
find "$OUT/dumps" -type f -mtime "+$RETENTION_DUMPS" -delete
find "$OUT/files" -type f -mtime "+$RETENTION_DUMPS" -delete 2>/dev/null || true
find "$OUT/physical" -maxdepth 1 -type d -name 'base-*' -mtime "+$RETENTION_BASE" \
  -exec rm -rf {} +

# ---- WAL archive hygiene
# Compress any stray uncompressed segments (pre-gzip era or crash leftovers)
# and drop stale .part temp files. Anything younger than 10 min may still be
# in flight from the archiver, so leave it alone.
find "$OUT/wal" -maxdepth 1 -type f -name '0*' ! -name '*.gz' ! -name '*.part' \
  -mmin +10 -exec gzip -f {} \; 2>/dev/null || true
find "$OUT/wal" -maxdepth 1 -type f -name '*.part' -mmin +10 -delete 2>/dev/null || true

# Prune WAL to exactly what PITR needs: everything logically older than the
# START segment of the oldest kept basebackup is useless without that backup.
# The .backup history files in the archive record each basebackup's start.
# (A blind mtime prune sized in days can exceed the disk - that is what filled
# the box on 2026-07-22 and took Postgres down.)
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
OLDEST_BASE="$(ls -d "$OUT"/physical/base-* 2>/dev/null | sort | head -1 | sed 's/.*base-//')" || true
CUTSEG="$([ -n "$OLDEST_BASE" ] && wal_cutoff "$OLDEST_BASE")" || true
if [ -n "$CUTSEG" ]; then
  docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$CUTSEG" \
    && echo "  ok wal pruned to $CUTSEG (oldest basebackup $OLDEST_BASE)"
else
  echo "  ! wal prune: no cutoff found, falling back to age-based prune"
  find "$OUT/wal" -type f -mtime "+$((RETENTION_BASE + 1))" -delete 2>/dev/null || true
fi

# Emergency guard: if the disk is still filling despite retention, keep only
# the WAL needed by the NEWEST basebackup. Older days stay restorable from
# their nightly dumps (and the off-box copy holds everything).
USED_PCT="$(df --output=pcent "$OUT" 2>/dev/null | tail -1 | tr -dc '0-9')"
if [ "${USED_PCT:-0}" -ge 85 ]; then
  NEWEST_BASE="$(ls -d "$OUT"/physical/base-* 2>/dev/null | sort | tail -1 | sed 's/.*base-//')" || true
  CUTSEG="$([ -n "$NEWEST_BASE" ] && wal_cutoff "$NEWEST_BASE")" || true
  [ -n "$CUTSEG" ] && docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$CUTSEG"
  echo "  ! disk at ${USED_PCT}% - emergency WAL prune to newest basebackup ($NEWEST_BASE)"
fi

# ---- housekeeping: keep the box clean
journalctl --vacuum-time=7d --vacuum-size=200M >/dev/null 2>&1 || true
apt-get clean >/dev/null 2>&1 || true
docker image prune -f >/dev/null 2>&1 || true
rm -f /root/pgforge-src.tar.gz 2>/dev/null || true

# ---- off-box (optional)
REMOTE="$(cat /opt/pgforge/backup_remote 2>/dev/null || true)"
if [ -n "$REMOTE" ] && command -v rclone >/dev/null 2>&1; then
  rclone sync "$OUT" "$REMOTE" --transfers 4 2>&1 | tail -2
  echo "  off-box: synced to $REMOTE"
else
  echo "  off-box: NOT CONFIGURED (echo '<rclone-remote>:<path>' > /opt/pgforge/backup_remote)"
fi
echo "== done; usage: $(du -sh "$OUT" | cut -f1) =="
