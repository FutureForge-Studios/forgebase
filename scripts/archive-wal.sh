#!/bin/sh
#
# ForgeBase WAL archive_command. Runs INSIDE the pgforge-db container (bind
# mount /opt/pgforge/bin -> /pgforge-bin) as: archive-wal.sh %p %f
#
# Semantics:
#   target missing            -> gzip via .part temp + atomic rename (a disk-full
#                                mid-copy can never leave a partial final file)
#   target exists, IDENTICAL  -> success. This is the retry-after-crash case:
#                                Postgres re-archives a segment it already
#                                archived, and the old `test ! -f` approach
#                                failed forever here, wedging the archiver until
#                                pg_wal filled the data volume.
#   target exists, DIFFERENT  -> fail loudly (a real conflict must never be
#                                silently overwritten).
set -u
p="$1"  # path to the WAL segment
f="$2"  # file name
dst="/wal-archive/$f.gz"

if [ -f "$dst" ]; then
  if gzip -dc "$dst" 2>/dev/null | cmp -s - "$p"; then
    exit 0  # already archived, identical - retry succeeds
  fi
  echo "archive-wal: $dst exists with DIFFERENT content" >&2
  exit 1
fi

# Ring-buffer panic guard: if the archive somehow outruns every pruner (write
# churn faster than the 15-minute cadence), trim the OLDEST segments here, at
# the moment of writing, before adding the new one. Failing the archive would
# be worse - Postgres would back WAL up into the data volume and die of a full
# disk, which is the one outcome this platform must never allow.
PANIC_KB=$((${WAL_ARCHIVE_PANIC_GB:-12} * 1024 * 1024))
used_kb="$(du -sk /wal-archive 2>/dev/null | cut -f1)"
if [ "${used_kb:-0}" -gt "$PANIC_KB" ]; then
  for old in $(ls /wal-archive/0*.gz 2>/dev/null | sort | head -50); do
    rm -f "$old"
    used_kb="$(du -sk /wal-archive | cut -f1)"
    [ "$used_kb" -le "$PANIC_KB" ] && break
  done
  echo "archive-wal: archive exceeded panic ceiling - oldest segments dropped" >&2
fi

gzip -c "$p" > "/wal-archive/$f.part" && mv "/wal-archive/$f.part" "$dst"
