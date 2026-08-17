#!/bin/bash
#
# pg-instance.sh - per-project Postgres instances on a copy-on-write filesystem.
#
# Each project (and branch) runs as its own postgres container `pgi-<slug>` whose
# data directory is a btrfs subvolume under $INSTANCES_ROOT. This is the foundation
# for two things the shared cluster cannot do:
#   * instant copy-on-write branching (btrfs snapshot, O(1), no parent lock)
#   * true scale-to-zero (stop the idle container -> real CPU/RAM freed; the
#     cold-start proxy restarts it on the next connection)
#
# Opt-in: only used when $INSTANCES_ROOT is a btrfs mount. Subcommands:
#   init | create <slug> | start <slug> | stop <slug> | branch <parent> <child>
#   | delete <slug> | port <slug> | wait <slug> | list
set -e
ROOT="${INSTANCES_ROOT:-/opt/pgforge/instances}"
IMG="${INSTANCE_IMAGE:-postgres:17}"
PORTS="$ROOT/.ports"
BASEPORT=7001
cmd="$1"; slug="$2"
mkdir -p "$ROOT"; touch "$PORTS"

port_of() { # slug -> host port, allocating a new one if unseen
  local s="$1" p
  p=$(awk -v s="$s" '$1==s{print $2}' "$PORTS" | head -1)
  if [ -z "$p" ]; then
    local max; max=$(awk '{print $2}' "$PORTS" | sort -n | tail -1)
    p=$(( ${max:-$((BASEPORT-1))} + 1 )); [ "$p" -lt "$BASEPORT" ] && p=$BASEPORT
    echo "$s $p" >> "$PORTS"
  fi
  echo "$p"
}

run_instance() { # $1=slug $2=port  (start a fresh container)
  docker run -d --name "pgi-$1" -v "$ROOT/$1:/var/lib/postgresql/data" \
    -e POSTGRES_PASSWORD=x -e POSTGRES_HOST_AUTH_METHOD=trust -p "127.0.0.1:$2:5432" \
    "$IMG" postgres -c ssl=off -c archive_mode=off >/dev/null
  touch "$ROOT/.active-$1" # so the reaper gives a fresh instance its full idle window
}

wait_ready() { # $1=slug
  for _ in $(seq 1 60); do
    docker exec "pgi-$1" pg_isready -U postgres >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  return 1
}

case "$cmd" in
  init)
    if mountpoint -q "$ROOT" && btrfs filesystem df "$ROOT" >/dev/null 2>&1; then echo btrfs-ok; else echo not-btrfs; exit 1; fi ;;
  create)
    btrfs subvolume create "$ROOT/$slug" >/dev/null
    chown 999:999 "$ROOT/$slug"
    p=$(port_of "$slug"); run_instance "$slug" "$p"; wait_ready "$slug"
    # a database named after the slug, so the proxy can route by database name
    docker exec "pgi-$slug" psql -U postgres -tAc "CREATE DATABASE \"$slug\"" >/dev/null
    echo "created $slug port=$p" ;;
  start)
    [ -d "$ROOT/$slug" ] || { echo "no instance $slug"; exit 1; }
    p=$(port_of "$slug")
    if [ -n "$(docker ps -aq -f name=^pgi-$slug$)" ]; then docker start "pgi-$slug" >/dev/null; else run_instance "$slug" "$p"; fi
    wait_ready "$slug"; echo "started $slug port=$p" ;;
  stop)
    docker stop "pgi-$slug" >/dev/null; echo "stopped $slug (compute freed, data on disk)" ;;
  branch) # branch <parent> <child>: instant CoW snapshot + new instance, parent untouched
    parent="$2"; child="$3"
    [ -d "$ROOT/$parent" ] || { echo "no parent $parent"; exit 1; }
    btrfs subvolume snapshot "$ROOT/$parent" "$ROOT/$child" >/dev/null
    p=$(port_of "$child"); run_instance "$child" "$p"; wait_ready "$child"
    echo "branched $child from $parent port=$p" ;;
  delete)
    docker rm -f "pgi-$slug" >/dev/null 2>&1 || true
    btrfs subvolume delete "$ROOT/$slug" >/dev/null 2>&1 || true
    sed -i "/^$slug /d" "$PORTS" 2>/dev/null || true
    echo "deleted $slug" ;;
  port) port_of "$slug" ;;
  wait) wait_ready "$slug" && echo ready || { echo notready; exit 1; } ;;
  reap) # reap [idle_seconds]: stop instances with no client connections for a while
    idle="${2:-900}"; now=$(date +%s)
    for d in "$ROOT"/*/; do
      s=$(basename "$d"); [ "$s" = "*" ] && continue
      [ -n "$(docker ps -q -f name=^pgi-$s$)" ] || continue
      conns=$(docker exec "pgi-$s" psql -U postgres -tAc "SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()" 2>/dev/null | tr -dc 0-9)
      la="$ROOT/.active-$s"
      if [ "${conns:-0}" -gt 0 ]; then touch "$la"
      else
        last=$(stat -c %Y "$la" 2>/dev/null || echo 0)
        [ $((now - last)) -ge "$idle" ] && docker stop "pgi-$s" >/dev/null && echo "reaped $s (idle >= ${idle}s, compute freed)"
      fi
    done ;;
  list)
    for d in "$ROOT"/*/; do
      s=$(basename "$d"); [ "$s" = "*" ] && continue
      running=$([ -n "$(docker ps -q -f name=^pgi-$s$)" ] && echo up || echo stopped)
      echo "$s port=$(awk -v s="$s" '$1==s{print $2}' "$PORTS") $running"
    done ;;
  *) echo "usage: pg-instance.sh {init|create|start|stop|branch <p> <c>|delete|port|wait|list} <slug>"; exit 2 ;;
esac
