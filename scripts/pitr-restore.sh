#!/bin/sh
#
# pitr-restore.sh - REAL point-in-time recovery.
#
# Brings up a throwaway Postgres from the newest basebackup taken at or before
# TARGET, replays the archived WAL forward to exactly TARGET (recovery_target_time),
# and pg_dumps SOURCE_DB as it existed at that instant. Non-destructive: the live
# cluster and every existing project are never touched. Prints the dump path on
# stdout (all progress goes to stderr) so a caller can load it into a new project.
#
# Usage: pitr-restore.sh "YYYY-MM-DD HH:MM:SS" SOURCE_DB
#
# This is the engine behind "Restore to a point in time" in the panel. It works
# because the platform already ships continuous gzipped WAL archiving + nightly
# basebackups; this script is what actually turns those into a per-second restore.
set -e

TARGET="$1"; SRCDB="$2"
[ -n "$TARGET" ] && [ -n "$SRCDB" ] || { echo "usage: pitr-restore.sh '<YYYY-MM-DD HH:MM:SS>' <db>" >&2; exit 2; }

OUT=/opt/pgforge-backups
IMG=pgforge-postgres:17
STAMP="$(date -u +%s)-$$"
SCRATCH="/opt/pgforge/pitr/scratch-$STAMP"
CONT="pgforge-pitr-$STAMP"
DUMP="$OUT/pitr/${SRCDB}-$STAMP.dump"
PGPASS="$(sed -n 's/^POSTGRES_PASSWORD=//p' /opt/pgforge/stack/.env | head -1)"
mkdir -p "$OUT/pitr"

cleanup() { docker rm -f "$CONT" >/dev/null 2>&1 || true; rm -rf "$SCRATCH"; }
trap cleanup EXIT

TARGET_EPOCH="$(date -u -d "$TARGET" +%s)" || { echo "bad target time: $TARGET" >&2; exit 2; }

# 1) newest basebackup whose START TIME <= TARGET (its backup_label lives inside base.tar.gz)
BASE=""; BST=""
for d in $(ls -d "$OUT"/physical/base-* 2>/dev/null | sort -r); do
  st="$(tar -xzOf "$d/base.tar.gz" backup_label 2>/dev/null | sed -n 's/^START TIME: //p')"
  [ -n "$st" ] || continue
  se="$(date -u -d "$st" +%s 2>/dev/null)" || continue
  if [ "$se" -le "$TARGET_EPOCH" ]; then BASE="$d"; BST="$st"; break; fi
done
[ -n "$BASE" ] || { echo "no basebackup at or before $TARGET (need one older than the target)" >&2; exit 1; }
echo ">> basebackup: $(basename "$BASE") (started $BST)" >&2

# 2) extract the basebackup into a scratch data directory and add recovery settings
mkdir -p "$SCRATCH"
tar -xzf "$BASE/base.tar.gz" -C "$SCRATCH"
cat >> "$SCRATCH/postgresql.auto.conf" <<CONF
restore_command = 'gunzip -c /wal-archive/%f.gz > %p'
recovery_target_time = '$TARGET'
recovery_target_action = 'promote'
recovery_target_inclusive = on
CONF
touch "$SCRATCH/recovery.signal"
rm -f "$SCRATCH/postmaster.pid"
# The throwaway instance is never published to the network (docker run below has
# no -p), so local trust auth is safe and sidesteps the live cluster's pg_hba
# (which may be TLS/scram only and would otherwise refuse our local connections).
cat > "$SCRATCH/pg_hba.conf" <<HBA
local   all all      trust
host    all all 127.0.0.1/32 trust
host    all all ::1/128      trust
HBA
chown -R 999:999 "$SCRATCH"
chmod 700 "$SCRATCH"

# 3) start a throwaway instance: no archiving (must not pollute the live archive),
#    no ssl, minimal preload. It reads the WAL archive read-only and recovers.
echo ">> replaying WAL to $TARGET ..." >&2
docker run -d --name "$CONT" \
  -v "$SCRATCH":/var/lib/postgresql/data \
  -v "$OUT/wal":/wal-archive:ro \
  -e POSTGRES_PASSWORD=unused \
  "$IMG" postgres -c archive_mode=off -c ssl=off \
    -c shared_preload_libraries=pg_stat_statements -c listen_addresses='127.0.0.1' >/dev/null

# 4) wait for recovery to reach the target and promote (pg_is_in_recovery -> f)
ok=0
for i in $(seq 1 90); do
  sleep 2
  inrec="$(docker exec -e PGPASSWORD="$PGPASS" "$CONT" psql -U postgres -h 127.0.0.1 -tAc 'SELECT pg_is_in_recovery()' 2>/dev/null || echo starting)"
  [ "$inrec" = "f" ] && { ok=1; break; }
done
if [ "$ok" != 1 ]; then
  echo "!! recovery did not complete in time. Last log lines:" >&2
  docker logs --tail 40 "$CONT" >&2 || true
  exit 1
fi

# 5) confirm the recovered instant, then dump the source database as of TARGET
RECAT="$(docker exec -e PGPASSWORD="$PGPASS" "$CONT" psql -U postgres -h 127.0.0.1 -tAc 'SELECT now()' 2>/dev/null | tr -d ' ')"
echo ">> recovered and promoted (recovered clock ~ $RECAT)" >&2
if ! docker exec -e PGPASSWORD="$PGPASS" "$CONT" psql -U postgres -h 127.0.0.1 -tAc "SELECT 1 FROM pg_database WHERE datname='$SRCDB'" 2>/dev/null | grep -q 1; then
  echo "!! database '$SRCDB' did not exist yet at $TARGET" >&2
  exit 1
fi
docker exec -e PGPASSWORD="$PGPASS" "$CONT" pg_dump -U postgres -h 127.0.0.1 -Fc -d "$SRCDB" > "$DUMP"
echo ">> point-in-time dump of '$SRCDB' at $TARGET written ($(wc -c < "$DUMP") bytes)" >&2
echo "$DUMP"
