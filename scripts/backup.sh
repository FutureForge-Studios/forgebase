#!/bin/sh
#
# ForgeBase nightly backups. Three layers every night:
#   1. logical:  pg_dump -Fc of every database + pg_dumpall globals
#                -> /opt/pgforge-backups/dumps/, TIERED retention: the newest
#                dump_keep_daily (default 7) dumps per database plus the newest
#                dump per ISO week for dump_keep_weekly (default 4) weeks, with
#                retention_days as a hard age ceiling.
#   2. physical: pg_basebackup (tar+gzip) -> /opt/pgforge-backups/physical/,
#                newest basebackup_keep (default 2) kept. Combined with the
#                continuous WAL archive this gives PITR over that window;
#                anything older restores from the logical dumps.
#   3. files:    storage + edge functions archives, same tiered policy.
# WAL is pruned to what the oldest kept basebackup needs (pg_archivecleanup),
# with an emergency prune at 85% disk so the archive can never fill the box.
# Retention runs BOTH before and after the backup work, so a failed dump or a
# full disk can never prevent pruning (a full disk once crash-looped Postgres
# here - pruning must always get its chance first).
# Optional off-box sync: put an rclone remote path in
# /opt/pgforge/backup_remote and install rclone.
#
set -e
CONT=pgforge-db
OUT=/opt/pgforge-backups
DATE="$(date -u +%F)"

# num FILE DEFAULT - read a positive integer from FILE, else DEFAULT. An empty
# or garbage file must never produce an empty value: `find -mtime "+"` errors
# and (under set -e) used to abort the whole script before any pruning ran.
num() {
  v="$(tr -dc 0-9 < "$1" 2>/dev/null || true)"
  [ -n "$v" ] && echo "$v" || echo "$2"
}
RETENTION_DUMPS="${RETENTION_DUMPS:-$(num /opt/pgforge/retention_days 30)}"
KEEP_DAILY="$(num /opt/pgforge/dump_keep_daily 7)"
KEEP_WEEKLY="$(num /opt/pgforge/dump_keep_weekly 4)"
KEEP_BASE="${RETENTION_BASE:-$(num /opt/pgforge/basebackup_keep 2)}"
[ "$KEEP_BASE" -lt 1 ] && KEEP_BASE=1
# The age ceiling must always reach past the weekly tier, or it would silently
# delete the weekly keepers the tier just decided to keep.
MIN_CEIL=$(( KEEP_DAILY + KEEP_WEEKLY * 7 + 7 ))
[ "$RETENTION_DUMPS" -lt "$MIN_CEIL" ] && RETENTION_DUMPS=$MIN_CEIL

echo "== pgforge backup $(date -u '+%F %T') UTC =="
mkdir -p "$OUT/dumps" "$OUT/dumps/.state" "$OUT/physical" "$OUT/files"
FORCE_DAYS="$(num /opt/pgforge/force_dump_days 7)"

# tier_prune DIR GLOB - keep the newest $KEEP_DAILY files matching GLOB, plus
# the newest file per ISO week for up to $KEEP_WEEKLY older weeks; delete the
# rest. retention_days stays as a hard age ceiling applied separately.
tier_prune() {
  dir="$1"; glob="$2"
  # shellcheck disable=SC2012
  ls -1t "$dir"/$glob 2>/dev/null | {
    i=0; weeks=""; nweeks=0
    while IFS= read -r f; do
      i=$((i+1))
      if [ "$i" -le "$KEEP_DAILY" ]; then continue; fi
      wk="$(date -u -r "$f" +%G-%V 2>/dev/null || echo x)"
      case " $weeks " in
        *" $wk "*) rm -f "$f" ;;              # this week already has its keeper
        *) if [ "$nweeks" -lt "$KEEP_WEEKLY" ]; then
             weeks="$weeks $wk"; nweeks=$((nweeks+1))   # newest of a new week
           else
             rm -f "$f"                        # past the weekly window
           fi ;;
      esac
    done
  }
}

prune_all() {
  # dumps: tiered per database prefix, then the age ceiling
  for pre in $(ls -1 "$OUT/dumps"/*.dump 2>/dev/null | sed -E 's|.*/||; s/-[0-9]{4}-[0-9]{2}-[0-9]{2}[^/]*\.dump$//' | sort -u); do
    tier_prune "$OUT/dumps" "$pre-[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]*.dump"
  done
  tier_prune "$OUT/dumps" "globals-*.sql"
  find "$OUT/dumps" -maxdepth 1 -type f -mtime "+$RETENTION_DUMPS" -delete 2>/dev/null || true
  # deleted-project dumps land in .trash for a 7-day grace period
  find "$OUT/dumps/.trash" -type f -mtime +7 -delete 2>/dev/null || true
  # signature files for databases that no longer produce dumps age out too
  find "$OUT/dumps/.state" -type f -name '*.sig' -mtime +60 -delete 2>/dev/null || true

  # files: same tiered policy per archive kind
  tier_prune "$OUT/files" "storage-*.tgz"
  tier_prune "$OUT/files" "functions-*.tgz"
  find "$OUT/files" -maxdepth 1 -type f -mtime "+$RETENTION_DUMPS" -delete 2>/dev/null || true

  # physical: count-based - keep the newest $KEEP_BASE VALID basebackups. A
  # dir without base.tar.gz is a crashed partial: it must never count toward
  # the keep quota (that could evict every good backup) and gets removed once
  # it is clearly not in progress anymore.
  ls -1dt "$OUT"/physical/base-* 2>/dev/null | {
    kept=0
    while IFS= read -r d; do
      if [ -f "$d/base.tar.gz" ]; then
        kept=$((kept + 1))
        [ "$kept" -gt "$KEEP_BASE" ] && rm -rf "$d" && echo "  pruned basebackup $(basename "$d")"
      else
        find "$d" -maxdepth 0 -mmin +120 -exec rm -rf {} + 2>/dev/null           && echo "  removed partial basebackup $(basename "$d")"
      fi
    done
  }

  # PITR working files: restored dumps older than 2 days, stale scratch dirs
  find "$OUT/pitr" -maxdepth 1 -type f -name '*.dump' -mtime +2 -delete 2>/dev/null || true
  find /opt/pgforge/pitr -maxdepth 1 -type d -name 'scratch-*' -mtime +1 -exec rm -rf {} + 2>/dev/null || true

  # WAL archive hygiene: compress strays, drop stale temp files
  find "$OUT/wal" -maxdepth 1 -type f -name '0*' ! -name '*.gz' ! -name '*.part' \
    -mmin +10 -exec gzip -f {} \; 2>/dev/null || true
  find "$OUT/wal" -maxdepth 1 -type f -name '*.part' -mmin +10 -delete 2>/dev/null || true

  # Prune WAL to exactly what PITR needs: everything logically older than the
  # START segment of the oldest kept basebackup is useless without that backup.
  OLDEST_BASE="$(ls -d "$OUT"/physical/base-* 2>/dev/null | sort | head -1 | sed 's/.*base-//')" || true
  CUTSEG="$([ -n "$OLDEST_BASE" ] && wal_cutoff "$OLDEST_BASE")" || true
  if [ -n "$CUTSEG" ]; then
    docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$CUTSEG" 2>/dev/null \
      && echo "  ok wal pruned to $CUTSEG (oldest basebackup $OLDEST_BASE)"
  fi

  # Emergency guard: at >=85% disk keep only the WAL the NEWEST basebackup needs.
  USED_PCT="$(df --output=pcent "$OUT" 2>/dev/null | tail -1 | tr -dc '0-9')"
  if [ "${USED_PCT:-0}" -ge 85 ]; then
    NEWEST_BASE="$(ls -d "$OUT"/physical/base-* 2>/dev/null | sort | tail -1 | sed 's/.*base-//')" || true
    CUTSEG="$([ -n "$NEWEST_BASE" ] && wal_cutoff "$NEWEST_BASE")" || true
    [ -n "$CUTSEG" ] && docker exec "$CONT" pg_archivecleanup -x .gz /wal-archive "$CUTSEG" 2>/dev/null
    echo "  ! disk at ${USED_PCT}% - emergency WAL prune to newest basebackup ($NEWEST_BASE)"
  fi
}

# wal_cutoff BASE_DATE - echoes the basebackup's START WAL segment name. The
# .backup history files in the archive record each basebackup's start.
wal_cutoff() {
  for f in "$OUT"/wal/*.backup "$OUT"/wal/*.backup.gz; do
    [ -f "$f" ] || continue
    case "$f" in *.gz) reader=zcat;; *) reader=cat;; esac
    if $reader "$f" 2>/dev/null | grep -q "START TIME: $1"; then
      $reader "$f" 2>/dev/null | sed -n 's/.*(file \([0-9A-F]*\)).*/\1/p' | head -1
      return
    fi
  done
}

# ---- retention FIRST: free space before writing anything, and guarantee that
# a failure later in the script can never mean "no pruning happened today".
prune_all || echo "  ! pre-prune had errors (continuing)"

# ---- layer 1: logical dumps (pgforge_restore_test is the monthly restore
# drill's scratch database - never worth backing up, and a stranded one would
# otherwise be dumped at full size nightly)
docker exec "$CONT" pg_dumpall -U postgres --globals-only > "$OUT/dumps/globals-$DATE.sql"
for db in $(docker exec "$CONT" psql -U postgres -tAc \
    "select datname from pg_database where not datistemplate and datname <> 'pgforge_restore_test'"); do
  # Skip-unchanged: a database whose data counters AND schema are identical to
  # its last successful dump is not re-dumped - sleeping/idle projects stop
  # costing a full dump every night. The signature is captured BEFORE dumping
  # (writes racing the dump bump the counters and force a fresh dump tomorrow -
  # the safe direction). stats_reset changing (crash, failover) also forces a
  # dump. Safety valve: always dump when the newest dump is older than
  # FORCE_DAYS, so a broken detector can never leave only stale backups.
  sig="$(docker exec "$CONT" psql -U postgres -tAc "select coalesce(extract(epoch from stats_reset)::bigint,0)||'|'||tup_inserted||'|'||tup_updated||'|'||tup_deleted from pg_stat_database where datname='$db'" 2>/dev/null)"
  # awk drops psql meta-command lines (leading backslash): modern pg_dump
  # embeds a RANDOMIZED 
estrict token in every run, which would make the
  # schema hash different each night and defeat skip-unchanged entirely.
  schemasum="$(docker exec "$CONT" pg_dump -U postgres -s -d "$db" 2>/dev/null | awk 'substr($0,1,1)!="\\"' | sha256sum | cut -d' ' -f1)"
  sig="$sig|$schemasum"
  newest="$(ls -1t "$OUT/dumps/$db"-[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]*.dump 2>/dev/null | head -1)"
  if [ -n "$newest" ] && [ "$(cat "$OUT/dumps/.state/$db.sig" 2>/dev/null)" = "$sig" ] \
     && [ -n "$(find "$newest" -mtime -"$FORCE_DAYS" 2>/dev/null)" ]; then
    echo "  == $db unchanged, skipped"
    continue
  fi
  if docker exec "$CONT" pg_dump -U postgres -Fc -d "$db" > "$OUT/dumps/$db-$DATE.dump" 2>"$OUT/.err"; then
    echo "  ok dump $db ($(wc -c < "$OUT/dumps/$db-$DATE.dump") bytes)"
    printf '%s' "$sig" > "$OUT/dumps/.state/$db.sig"
  else
    echo "  ! dump $db failed: $(head -1 "$OUT/.err")"
    rm -f "$OUT/dumps/$db-$DATE.dump"
    HAD_FAIL=1
  fi
done
rm -f "$OUT/.err"

# ---- layer 1b: dedicated-instance projects (own containers, not in the shared
# cluster). Only RUNNING instances are dumped - a stopped instance has had no
# writes since it stopped, so its last dump is by definition still current
# (the skip-unchanged logic would skip it anyway).
IROOT=/opt/pgforge/instances
if [ -d "$IROOT" ]; then
  for d in "$IROOT"/*/; do
    s2=$(basename "$d" 2>/dev/null); [ "$s2" = "*" ] && continue
    [ -n "$(docker ps -q -f name=^pgi-$s2$ 2>/dev/null)" ] || continue
    iuser="$(cat "$IROOT/.user-$s2" 2>/dev/null || echo postgres)"
    isig="$(docker exec "pgi-$s2" psql -U "$iuser" -tAc "select coalesce(extract(epoch from stats_reset)::bigint,0)||'|'||tup_inserted||'|'||tup_updated||'|'||tup_deleted from pg_stat_database where datname='$s2'" 2>/dev/null)"
    ischema="$(docker exec "pgi-$s2" pg_dump -U "$iuser" -s -d "$s2" 2>/dev/null | awk 'substr($0,1,1)!="\\"' | sha256sum | cut -d' ' -f1)"
    isig="$isig|$ischema"
    inewest="$(ls -1t "$OUT/dumps/$s2"-[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]*.dump 2>/dev/null | head -1)"
    if [ -n "$inewest" ] && [ "$(cat "$OUT/dumps/.state/$s2.sig" 2>/dev/null)" = "$isig" ]        && [ -n "$(find "$inewest" -mtime -"$FORCE_DAYS" 2>/dev/null)" ]; then
      echo "  == $s2 (instance) unchanged, skipped"
      continue
    fi
    if docker exec "pgi-$s2" pg_dump -U "$iuser" -Fc -d "$s2" > "$OUT/dumps/$s2-$DATE.dump" 2>"$OUT/.err"; then
      echo "  ok dump $s2 (instance, $(wc -c < "$OUT/dumps/$s2-$DATE.dump") bytes)"
      printf '%s' "$isig" > "$OUT/dumps/.state/$s2.sig"
    else
      echo "  ! dump $s2 (instance) failed: $(head -1 "$OUT/.err")"
      rm -f "$OUT/dumps/$s2-$DATE.dump"
      HAD_FAIL=1
    fi
  done
fi
rm -f "$OUT/.err"

# ---- layer 2: physical basebackup (for PITR together with the WAL archive)
if docker exec "$CONT" sh -c "rm -rf /physical/base-$DATE && pg_basebackup -U postgres -D /physical/base-$DATE -Ft -z -X none" 2>"$OUT/.err"; then
  echo "  ok basebackup base-$DATE"
else
  echo "  ! basebackup failed: $(head -1 "$OUT/.err")"; rm -f "$OUT/.err"
  HAD_FAIL=1
fi

# ---- layer 3: file plane (storage objects + edge functions) so a restore can
# rebuild the whole platform, not just the databases.
[ -d /opt/pgforge-storage ] && tar -czf "$OUT/files/storage-$DATE.tgz" -C /opt pgforge-storage 2>/dev/null && echo "  ok storage archive"
[ -d /opt/pgforge-functions ] && tar -czf "$OUT/files/functions-$DATE.tgz" -C /opt pgforge-functions 2>/dev/null && echo "  ok functions archive"

# ---- retention AGAIN now that today's artifacts exist (prunes yesterday's
# out-of-tier files and trims WAL to the fresh basebackup's cutoff)
prune_all || echo "  ! post-prune had errors (continuing)"

# ---- housekeeping: keep the box clean
journalctl --vacuum-time=7d --vacuum-size=200M >/dev/null 2>&1 || true
apt-get clean >/dev/null 2>&1 || true
docker image prune -f >/dev/null 2>&1 || true
docker builder prune -f --filter until=168h >/dev/null 2>&1 || true
rm -f /root/pgforge-src.tar.gz /tmp/pgforged.upd 2>/dev/null || true
# Deno module cache (edge functions with remote imports) - wipe past 500MB,
# Deno re-fetches on demand
for dc in /opt/pgforge/deno-cache /root/.cache/deno; do
  if [ -d "$dc" ] && [ "$(du -sm "$dc" 2>/dev/null | cut -f1)" -gt 500 ] 2>/dev/null; then
    rm -rf "$dc"/* 2>/dev/null && echo "  pruned deno cache $dc"
  fi
done

# ---- off-box (optional). pitr/ holds transient restore products and .trash
# holds deleted projects' grace-period dumps - neither belongs off-box.
REMOTE="$(cat /opt/pgforge/backup_remote 2>/dev/null || true)"
if [ -n "$REMOTE" ] && command -v rclone >/dev/null 2>&1; then
  rclone sync "$OUT" "$REMOTE" --transfers 4 \
    --exclude 'pitr/**' --exclude 'dumps/.trash/**' 2>&1 | tail -2
  echo "  off-box: synced to $REMOTE"
else
  echo "  off-box: NOT CONFIGURED (echo '<rclone-remote>:<path>' > /opt/pgforge/backup_remote)"
fi
if [ "${HAD_FAIL:-0}" = "1" ]; then
  sh /opt/pgforge/bin/alert-notify.sh "WARNING ForgeBase: last night's backup completed WITH ERRORS - check the backup log on the server." || true
fi
echo "== done; usage: $(du -sh "$OUT" | cut -f1) =="
