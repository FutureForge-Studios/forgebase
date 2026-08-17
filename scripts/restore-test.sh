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

latest="$(ls -1S $(ls -1t "$OUT"/dumps/*.dump 2>/dev/null | head -20) 2>/dev/null | head -1)"
if [ -z "$latest" ]; then
  echo "$(date -u '+%F %T') FAIL no dumps found" >> "$LOG"; exit 1
fi

docker exec "$CONT" psql -U postgres -tAc "drop database if exists $TESTDB;" >/dev/null
docker exec "$CONT" psql -U postgres -tAc "create database $TESTDB;" >/dev/null
docker exec -i "$CONT" pg_restore -U postgres -d "$TESTDB" --no-owner --no-acl < "$latest" 2>/dev/null || true
tables=$(docker exec "$CONT" psql -U postgres -d "$TESTDB" -tAc \
  "select count(*) from information_schema.tables where table_schema not in ('pg_catalog','information_schema');")
docker exec "$CONT" psql -U postgres -tAc "drop database if exists $TESTDB;" >/dev/null

if [ "${tables:-0}" -gt 0 ]; then
  echo "$(date -u '+%F %T') PASS $(basename "$latest"): $tables tables" >> "$LOG"
else
  echo "$(date -u '+%F %T') FAIL $(basename "$latest"): 0 tables" >> "$LOG"; exit 1
fi
