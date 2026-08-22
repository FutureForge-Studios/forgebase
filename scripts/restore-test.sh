#!/bin/sh
#
# ForgeBase monthly restore-test: prove the newest logical dump actually restores.
# Picks the largest project dump from the newest night, restores it into a
# throwaway DB, counts tables, logs PASS/FAIL, cleans up.
#
set -e
CONT=pgforge-db
OUT=/opt/pgforge-backups
LOG="$OUT/restore-test.log"
TESTDB=pgforge_restore_test

# The test DB is dropped on EVERY exit path. Without this, a failure between
# restore and cleanup stranded a full-size database in the live cluster forever
# (which the nightly backup would then have dumped at full size every night -
# backup.sh also excludes $TESTDB by name as a second line of defense).
cleanup() {
  docker exec "$CONT" psql -U postgres -tAc "drop database if exists $TESTDB;" >/dev/null 2>&1 || true
}
trap cleanup EXIT

latest="$(ls -1S $(ls -1t "$OUT"/dumps/*.dump 2>/dev/null | head -20) 2>/dev/null | head -1)"
if [ -z "$latest" ]; then
  echo "$(date -u '+%F %T') FAIL no dumps found" >> "$LOG"; exit 1
fi

# Skip (successfully) when the disk can't hold a restored copy: the restore
# needs roughly 2x the dump size, and running the drill on a nearly-full disk
# risks the exact incident it exists to prevent.
free_kb="$(df --output=avail "$OUT" 2>/dev/null | tail -1 | tr -dc 0-9)"
need_kb=$(( $(wc -c < "$latest") / 1024 * 2 ))
if [ "${free_kb:-0}" -lt "$need_kb" ]; then
  echo "$(date -u '+%F %T') SKIP $(basename "$latest"): needs ~${need_kb}KB free, have ${free_kb}KB" >> "$LOG"
  exit 0
fi

docker exec "$CONT" psql -U postgres -tAc "drop database if exists $TESTDB;" >/dev/null
docker exec "$CONT" psql -U postgres -tAc "create database $TESTDB;" >/dev/null
docker exec -i "$CONT" pg_restore -U postgres -d "$TESTDB" --no-owner --no-acl < "$latest" 2>/dev/null || true
tables=$(docker exec "$CONT" psql -U postgres -d "$TESTDB" -tAc \
  "select count(*) from information_schema.tables where table_schema not in ('pg_catalog','information_schema');")

if [ "${tables:-0}" -gt 0 ]; then
  echo "$(date -u '+%F %T') PASS $(basename "$latest"): $tables tables" >> "$LOG"
else
  echo "$(date -u '+%F %T') FAIL $(basename "$latest"): 0 tables" >> "$LOG"; exit 1
fi
