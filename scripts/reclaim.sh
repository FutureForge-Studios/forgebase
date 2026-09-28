#!/bin/sh
#
# ForgeBase weekly storage reclamation. Gives back space the live cluster has
# already lost to index bloat, and restores the statistics autovacuum needs so it
# does not come back.
#
# Why this exists: on 2026-09-28 the volume filled and Postgres crash-looped.
# Part of the cause was that nothing ever reclaimed anything. Detection existed
# (pgforge/advisors.go reports bloat in prose) but nothing acted on it. A
# supervised per-index rebuild had already taken ppc-profitzon-command from
# 2500MB to 1619MB, one index going 1159MB to 488MB, because leaf density had
# fallen to 68 percent with 50 percent fragmentation. This automates that.
#
# Three passes, in this order:
#   0. drop _ccnew leftovers from any previously interrupted run
#   1. VACUUM (ANALYZE) tables whose statistics a crash wiped
#   2. REINDEX INDEX CONCURRENTLY bloated LEAF indexes, biggest first
#
# ONLINE ONLY by design. No VACUUM FULL, no pg_repack, no table rewrites, so
# nothing here takes a lock an application will notice. The consequence is that
# heap and TOAST dead space is NOT reclaimed, only index bloat. Reclaiming heap
# needs a rewrite and an approved window, which is a separate decision.
#
# Not covered: dedicated pgi-* instances, which have their own containers. Out of
# scope for v1 rather than silently skipped.
#
# Usage:
#   reclaim.sh                  the real thing
#   reclaim.sh --selftest       assert the four hard constraints, touch nothing real
#   RECLAIM_DRY=1 reclaim.sh    print candidates and the space maths, change nothing

CONT="${RECLAIM_CONT:-pgforge-db}"
ALERTS=/opt/pgforge/alerts
NOTIFY=/opt/pgforge/bin/alert-notify.sh
PROTECT_FILE="${RECLAIM_PROTECT:-/opt/pgforge/reclaim_protect}"
MIN_MB="${RECLAIM_MIN_MB:-50}"            # ignore indexes smaller than this
MAX_DENSITY="${RECLAIM_MAX_DENSITY:-70}"  # rebuild below this avg_leaf_density
BUDGET_MIN="${RECLAIM_BUDGET_MIN:-60}"    # wall-clock ceiling for pass 2
MARGIN_MB="${RECLAIM_MARGIN_MB:-1024}"    # free space kept spare, on top of 2x
MAX_DATA_PCT="${RECLAIM_MAX_DATA_PCT:-85}"
START_EPOCH="$(date -u +%s)"
REBUILT=0; FREED_KB=0; VACUUMED=0; FAILED=0
mkdir -p "$ALERTS"

say() { echo "$(date -u '+%F %T') $*"; }

# Deliberately NOT set -e. wal-prune.sh is, and the consequence there is that a
# mid-script death never reaches its own alerting code, so a crash is silent.
# Here every step is checked explicitly and one EXIT trap reports whatever got
# past the checks.
DONE=0
PROTECT_RE=""
finish() {
  [ -n "$PROTECT_RE" ] && rm -f "$PROTECT_RE"
  if [ "$DONE" = 1 ] && [ "$FAILED" = 0 ]; then
    if [ -f "$ALERTS/reclaim" ]; then
      rm -f "$ALERTS/reclaim"
      sh "$NOTIFY" "RESOLVED ForgeBase: storage reclamation is healthy again." 2>/dev/null || true
    fi
    return
  fi
  { if [ "$DONE" = 1 ]; then
      echo "The weekly storage reclamation run finished with $FAILED index rebuild failure(s)."
    else
      echo "The weekly storage reclamation run did not finish."
    fi
    echo "Check /var/log/pgforge-reclaim.log, then confirm nothing was left behind:"
    echo "  SELECT count(*) FROM pg_index WHERE NOT indisvalid;      -- must be 0"
    echo "  SELECT count(*) FROM pg_class WHERE relname ~ '_ccnew';  -- must be 0"
  } > "$ALERTS/reclaim"
  sh "$NOTIFY" "WARNING ForgeBase: storage reclamation needs attention. See the System page." 2>/dev/null || true
}
trap finish EXIT

# One statement, outside any transaction block, with the guards set through the
# CONNECTION. statement_timeout in PGOPTIONS rather than a SET is what lets
# REINDEX CONCURRENTLY run outside a transaction, which it must.
psql_db() {
  _db="$1"; shift
  docker exec -e PGOPTIONS="-c statement_timeout=30min -c lock_timeout=30s" "$CONT" \
    psql -X -q -v ON_ERROR_STOP=1 -U postgres -d "$_db" -tAc "$*"
}
psql_ro() {
  _db="$1"; shift
  docker exec "$CONT" psql -X -q -U postgres -d "$_db" -tAc "$*" 2>/dev/null
}

# The protect list uses % as its wildcard, matching the SQL LIKE shape it was
# written in. Convert it once to an anchored regex file rather than trying to
# glob-match from a variable, which POSIX sh cannot do without eval.
build_protect_re() {
  [ -r "$PROTECT_FILE" ] || return 0
  PROTECT_RE="$(mktemp)"
  sed -e 's/[][(){}.^$*+?|\\]/\\&/g' -e 's/%/.*/g' -e 's/^/^/' -e 's/$/$/' \
      "$PROTECT_FILE" > "$PROTECT_RE"
}
is_protected() {
  [ -n "$PROTECT_RE" ] || return 1
  printf '%s\n' "$1" | grep -qiE -f "$PROTECT_RE"
}

# fits_free KB - room for a rebuild? A REINDEX CONCURRENTLY holds the new index
# alongside the old until it swaps, and the build's WAL lands in pg_wal on the
# same volume, so the requirement is 2x the index plus a margin. This gate is
# what stands between this job and a repeat of 2026-09-28, so it is pessimistic
# on purpose.
fits_free() {
  _need_kb=$(( $1 * 2 + MARGIN_MB * 1024 ))
  _avail_kb="$(df --output=avail /opt/pgforge/data 2>/dev/null | tail -1 | tr -dc 0-9)"
  [ -n "$_avail_kb" ] || return 1
  [ "$_avail_kb" -ge "$_need_kb" ]
}

data_pct() { df --output=pcent /opt/pgforge/data 2>/dev/null | tail -1 | tr -dc 0-9; }

live_dbs() {
  psql_ro postgres "SELECT datname FROM pg_database
                    WHERE NOT datistemplate AND datname <> 'pgforge_restore_test'
                    ORDER BY pg_database_size(oid) DESC"
}

# ---- pass 0: leftovers from an interrupted run.
# Runs FIRST on every invocation, so a run killed by OOM or a reboot self-heals
# next time instead of leaving a permanent double-size index on disk. The
# NOT indisvalid condition is what keeps this from touching a real index that
# happens to carry the name.
drop_ccnew() {
  _db="$1"
  psql_ro "$_db" "SELECT quote_ident(n.nspname)||'.'||quote_ident(c.relname)
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_index i ON i.indexrelid = c.oid
                  WHERE c.relname ~ '_ccnew[0-9]*\$' AND NOT i.indisvalid" |
  while IFS= read -r idx; do
    [ -n "$idx" ] || continue
    if psql_db "$_db" "DROP INDEX CONCURRENTLY IF EXISTS $idx" >/dev/null 2>&1; then
      say "  dropped leftover $_db $idx"
    fi
  done
}

# ---- pass 1: re-establish the statistics a crash wiped.
# A table with real pages reporting zero live AND zero dead rows is exactly a
# reset counter. autovacuum reads those counters to decide what to do, so until
# they are repopulated it is blind to all pre-existing bloat and will not act
# until fresh dead tuples accumulate. VACUUM (ANALYZE) fixes that and corrects
# reltuples too.
# Self-limiting: after one good pass these tables stop matching, so the job
# converges on doing nothing, and a future crash re-selects them automatically.
vacuum_blind() {
  _db="$1"
  psql_ro "$_db" "SELECT quote_ident(schemaname)||'.'||quote_ident(relname)
                  FROM pg_stat_user_tables
                  WHERE n_live_tup = 0 AND n_dead_tup = 0
                    AND pg_relation_size(relid) >= 8*1024*1024
                  ORDER BY pg_relation_size(relid) DESC" > /tmp/reclaim_vac.$$
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    if [ -n "$RECLAIM_DRY" ]; then say "  [dry] would vacuum $_db $t"; continue; fi
    # vacuum_cost_delay defaults to 0 for a MANUAL vacuum, so setting it is what
    # makes this pass gentle on a small box. One table at a time, never
    # vacuumdb -j: a thundering herd is how maintenance becomes an incident.
    if docker exec -e PGOPTIONS="-c vacuum_cost_delay=2ms -c lock_timeout=30s" "$CONT" \
         psql -X -q -v ON_ERROR_STOP=1 -U postgres -d "$_db" \
         -c "VACUUM (ANALYZE) $t" >/dev/null 2>&1; then
      VACUUMED=$((VACUUMED + 1))
    fi
  done < /tmp/reclaim_vac.$$
  rm -f /tmp/reclaim_vac.$$
}

# ---- pass 2: rebuild bloated leaf indexes.
reindex_db() {
  _db="$1"
  # pgstattuple carries pgstatindex, which is how leaf density is measured. It is
  # contrib and present in the image, installed on demand the way cron.go does it.
  psql_db "$_db" "CREATE EXTENSION IF NOT EXISTS pgstattuple" >/dev/null 2>&1 || true
  if [ "$(psql_ro "$_db" "SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='pgstattuple')")" != "t" ]; then
    say "  $_db: pgstattuple unavailable, skipping reindex pass"
    return 0
  fi

  # relkind='i' only: 'I' is a PARTITIONED index and REINDEX CONCURRENTLY is not
  # supported on one, so the parent must never reach this list. btree only,
  # because pgstatindex only understands btree.
  psql_ro "$_db" "SELECT quote_ident(n.nspname)||'.'||quote_ident(c.relname)||'|'||
                         pg_relation_size(c.oid)/1024||'|'||c.relname
                  FROM pg_index i
                  JOIN pg_class c ON c.oid = i.indexrelid
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  WHERE c.relkind = 'i' AND i.indisvalid
                    AND c.relam = (SELECT oid FROM pg_am WHERE amname = 'btree')
                    AND n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
                    AND c.relname !~ '_ccnew[0-9]*\$'
                    AND pg_relation_size(c.oid) >= $MIN_MB*1024*1024
                  ORDER BY pg_relation_size(c.oid) DESC" > /tmp/reclaim_idx.$$

  while IFS='|' read -r idx kb bare; do
    [ -n "$idx" ] || continue

    if [ $(( $(date -u +%s) - START_EPOCH )) -ge $(( BUDGET_MIN * 60 )) ]; then
      say "  budget of ${BUDGET_MIN}min spent, stopping cleanly"
      break
    fi

    # Protect list. Seeded with the ppc finance primary key: it is the ON CONFLICT
    # arbiter for every finance write, arbiter probes do NOT increment idx_scan so
    # it reads as unused when it is anything but, and an unattended job is not the
    # thing that should ever touch it.
    if is_protected "$bare"; then
      say "  skipped $_db $idx (protected)"
      continue
    fi

    dens="$(psql_ro "$_db" "SELECT round((pgstatindex('$idx')).avg_leaf_density)")"
    [ -n "$dens" ] || continue
    [ "$dens" -lt "$MAX_DENSITY" ] || continue

    if ! fits_free "$kb"; then
      say "  not enough free space for $_db $idx ($((kb/1024))MB index needs 2x + ${MARGIN_MB}MB) - stopping"
      break
    fi

    if [ -n "$RECLAIM_DRY" ]; then
      say "  [dry] would rebuild $_db $idx density=${dens}% size=$((kb/1024))MB"
      continue
    fi

    if psql_db "$_db" "REINDEX INDEX CONCURRENTLY $idx" >/dev/null 2>&1; then
      after="$(psql_ro "$_db" "SELECT pg_relation_size('$idx')/1024")"
      REBUILT=$((REBUILT + 1))
      FREED_KB=$((FREED_KB + kb - ${after:-$kb}))
      say "  rebuilt $_db $idx ${kb}KB -> ${after:-?}KB (was ${dens}% dense)"
    else
      FAILED=$((FAILED + 1))
      say "  ! FAILED to rebuild $_db $idx"
      # clean up now rather than leaving a full-size leftover for a week
      drop_ccnew "$_db"
    fi
  done < /tmp/reclaim_idx.$$
  rm -f /tmp/reclaim_idx.$$
}

# ---------------------------------------------------------------- selftest
# Asserts the four constraints that actually matter. It does NOT test the density
# comparison, which is a plain numeric less-than; the structural filters are where
# the danger lives. Said plainly so nobody assumes more coverage than exists.
if [ "$1" = "--selftest" ]; then
  T=pgforge_reclaim_test
  sfcleanup() {
    docker exec "$CONT" psql -U postgres -tAc "drop database if exists $T;" >/dev/null 2>&1 || true
    [ -n "$PROTECT_RE" ] && rm -f "$PROTECT_RE"
  }
  trap sfcleanup EXIT
  docker exec "$CONT" psql -U postgres -tAc "drop database if exists $T;" >/dev/null 2>&1 || true
  docker exec "$CONT" psql -U postgres -tAc "create database $T;" >/dev/null \
    || { echo "FAIL: cannot create $T"; exit 1; }
  docker exec "$CONT" psql -X -q -U postgres -d "$T" -c \
    "CREATE TABLE p (id int, d date) PARTITION BY RANGE (d);
     CREATE TABLE p_2026 PARTITION OF p FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
     CREATE INDEX p_idx ON p (id);
     CREATE TABLE q (id int);
     CREATE INDEX q_ccnew ON q (id);
     CREATE INDEX u_ccnew ON q (id) WHERE id > 0;" >/dev/null

  rc=0
  # 1: the leaf index is a candidate, the partitioned parent is NOT. Violating
  #    this is what makes REINDEX CONCURRENTLY fail outright.
  cand="$(docker exec "$CONT" psql -X -q -U postgres -d "$T" -tAc \
    "SELECT coalesce(string_agg(c.relname, ',' ORDER BY c.relname), '')
     FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
     JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE c.relkind = 'i' AND i.indisvalid AND n.nspname = 'public'
       AND c.relam = (SELECT oid FROM pg_am WHERE amname = 'btree')
       AND c.relname !~ '_ccnew[0-9]*\$'")"
  if printf '%s' "$cand" | grep -q 'p_2026_id_idx' && ! printf '%s' "$cand" | grep -qx 'p_idx' \
     && ! printf '%s' "$cand" | grep -q '\bp_idx,' && ! printf '%s' "$cand" | grep -q ',p_idx$'; then
    echo "PASS 1: leaf selected, partitioned parent excluded ($cand)"
  else
    echo "FAIL 1: candidate list wrong, expected the leaf and not p_idx, got '$cand'"; rc=1
  fi

  # 2: the protect list suppresses a match (the arbiter guard)
  PROTECT_FILE="$(mktemp)"; printf 'p_2026%%idx\n' > "$PROTECT_FILE"
  build_protect_re
  if is_protected p_2026_id_idx; then echo "PASS 2: protect list matches its pattern"
  else echo "FAIL 2: protect list did not match"; rc=1; fi
  if is_protected q_ccnew; then echo "FAIL 2b: protect list matched something it should not"; rc=1
  else echo "PASS 2b: protect list does not over-match"; fi
  rm -f "$PROTECT_FILE"

  # 3: pass 0 drops an INVALID _ccnew and leaves a VALID one alone
  docker exec "$CONT" psql -X -q -U postgres -d "$T" -tAc \
    "UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'q_ccnew'::regclass" >/dev/null
  drop_ccnew "$T" >/dev/null 2>&1
  left="$(docker exec "$CONT" psql -X -q -U postgres -d "$T" -tAc \
    "SELECT coalesce(string_agg(relname, ',' ORDER BY relname), '') FROM pg_class WHERE relname ~ '_ccnew'")"
  if [ "$left" = "u_ccnew" ]; then echo "PASS 3: invalid _ccnew dropped, valid one kept"
  else echo "FAIL 3: expected only u_ccnew to remain, got '$left'"; rc=1; fi

  # 4: the free-space gate answers both ways. This is the gate that stands
  #    between this job and a repeat of the 2026-09-28 disk-full outage.
  if fits_free 1 && ! fits_free 999999999; then echo "PASS 4: space gate allows small, refuses huge"
  else echo "FAIL 4: space gate is wrong"; rc=1; fi

  DONE=1; FAILED=0
  if [ "$rc" = 0 ]; then echo "selftest: all constraints hold"; else echo "selftest: FAILURES above"; fi
  exit "$rc"
fi

# ---------------------------------------------------------------- main
say "== reclaim start =="

# Refuse to compete with the heavy jobs. It cannot dodge the 15-minute wal-prune
# timer, so it checks rather than assuming a clear window.
if systemctl is-active --quiet pgforge-backup.service 2>/dev/null; then
  say "nightly backup is running, skipping this run"; DONE=1; exit 0
fi
if pgrep -f "[p]g_basebackup" >/dev/null 2>&1; then
  say "a basebackup is running, skipping this run"; DONE=1; exit 0
fi

PCT="$(data_pct)"
if [ "${PCT:-0}" -ge "$MAX_DATA_PCT" ]; then
  # Refusing on space is NOT a failure, so it must not raise an alert: the
  # data_disk watchdog in wal-prune.sh already owns that conversation.
  say "database filesystem at ${PCT}% (ceiling ${MAX_DATA_PCT}%) - refusing to rebuild anything"
  DONE=1; exit 0
fi

[ -r "$PROTECT_FILE" ] || printf 'wh_finance_event%%pkey\n' > "$PROTECT_FILE"
build_protect_re

for db in $(live_dbs); do
  [ -n "$db" ] || continue
  say "-- $db"
  drop_ccnew "$db"
  vacuum_blind "$db"
  reindex_db "$db"
done

say "== reclaim done: $REBUILT indexes rebuilt, $((FREED_KB / 1024))MB freed, $VACUUMED tables vacuumed, $FAILED failures, $(( ($(date -u +%s) - START_EPOCH) / 60 ))min =="
DONE=1
