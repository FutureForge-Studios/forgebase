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
gzip -c "$p" > "/wal-archive/$f.part" && mv "/wal-archive/$f.part" "$dst"
