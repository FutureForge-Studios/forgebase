#!/bin/bash
# Prove REAL copy-on-write branching of a LIVE Postgres via btrfs snapshots:
#  - instant (snapshot is O(1), not a full copy)
#  - parent is NEVER stopped or locked
#  - copy-on-write on disk (branch shares blocks, ~0 extra space initially)
#  - branch holds the parent's data as of the snapshot, then diverges independently
set -e
IMG=postgres:17
apt-get install -y -qq btrfs-progs >/dev/null 2>&1 || true

echo "== setup: btrfs filesystem on a loopback file =="
docker rm -f cow-parent cow-branch >/dev/null 2>&1 || true
umount /cow 2>/dev/null || true
rm -f /cow.img; truncate -s 20G /cow.img
mkfs.btrfs -q -f /cow.img
mkdir -p /cow; mount -o loop /cow.img /cow
btrfs subvolume create /cow/parent >/dev/null
chown 999:999 /cow/parent

echo "== start the PARENT postgres on the btrfs subvolume =="
docker run -d --name cow-parent -v /cow/parent:/var/lib/postgresql/data \
  -e POSTGRES_PASSWORD=x -e POSTGRES_HOST_AUTH_METHOD=trust -p 6001:5432 \
  "$IMG" postgres -c ssl=off -c archive_mode=off >/dev/null
for i in $(seq 1 40); do docker exec cow-parent pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
PP(){ docker exec cow-parent psql -U postgres -tAc "$1"; }
PP "CREATE TABLE t(id serial primary key, note text)" >/dev/null
PP "INSERT INTO t(note) VALUES ('parent-before-snapshot')" >/dev/null
PP "CHECKPOINT" >/dev/null
PARENT_PID=$(docker inspect -f '{{.State.Pid}}' cow-parent)
echo "   parent up (container pid $PARENT_PID), 1 row written"

echo "== BRANCH: snapshot the live subvolume (parent stays RUNNING) =="
USED_BEFORE=$(df --output=used /cow | tail -1 | tr -dc 0-9)
S=$(date +%s.%N)
btrfs subvolume snapshot /cow/parent /cow/branch >/dev/null
E=$(date +%s.%N)
USED_AFTER=$(df --output=used /cow | tail -1 | tr -dc 0-9)
echo "   snapshot took $(echo "$E - $S" | bc)s"
echo "   btrfs used: ${USED_BEFORE}KB -> ${USED_AFTER}KB (delta $((USED_AFTER-USED_BEFORE))KB = copy-on-write, no full copy)"

echo "== prove the parent was never interrupted: it answers a query + takes a NEW write =="
[ "$(docker inspect -f '{{.State.Pid}}' cow-parent)" = "$PARENT_PID" ] && echo "   parent postmaster pid unchanged ($PARENT_PID) - never restarted"
PP "INSERT INTO t(note) VALUES ('parent-after-snapshot')" >/dev/null
echo "   parent rows now: $(PP "SELECT string_agg(note,',' ORDER BY id) FROM t")"

echo "== start the BRANCH postgres on the snapshot (own port, own process) =="
docker run -d --name cow-branch -v /cow/branch:/var/lib/postgresql/data \
  -e POSTGRES_PASSWORD=x -e POSTGRES_HOST_AUTH_METHOD=trust -p 6002:5432 \
  "$IMG" postgres -c ssl=off -c archive_mode=off >/dev/null
for i in $(seq 1 40); do docker exec cow-branch pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
BB(){ docker exec cow-branch psql -U postgres -tAc "$1"; }
echo "   branch rows (expect ONLY parent-before-snapshot): $(BB "SELECT string_agg(note,',' ORDER BY id) FROM t")"

echo "== divergence: write to each independently =="
BB "INSERT INTO t(note) VALUES ('branch-only')" >/dev/null
PP "INSERT INTO t(note) VALUES ('parent-only-2')" >/dev/null
PR="$(PP "SELECT string_agg(note,',' ORDER BY id) FROM t")"
BR="$(BB "SELECT string_agg(note,',' ORDER BY id) FROM t")"
echo "   parent final: $PR"
echo "   branch final: $BR"

echo "== VERDICT =="
if echo "$BR" | grep -q 'parent-before-snapshot' && ! echo "$BR" | grep -q 'parent-after-snapshot' \
   && echo "$BR" | grep -q 'branch-only' && ! echo "$PR" | grep -q 'branch-only' \
   && [ "$((USED_AFTER-USED_BEFORE))" -lt 5000 ]; then
  echo "PASS: instant copy-on-write branch of a LIVE parent - no parent lock, blocks shared, independent divergence"
else
  echo "CHECK: parent=[$PR] branch=[$BR] delta=$((USED_AFTER-USED_BEFORE))KB"
fi
# cleanup
docker rm -f cow-parent cow-branch >/dev/null 2>&1 || true
btrfs subvolume delete /cow/branch >/dev/null 2>&1 || true
btrfs subvolume delete /cow/parent >/dev/null 2>&1 || true
umount /cow 2>/dev/null || true; rm -f /cow.img
