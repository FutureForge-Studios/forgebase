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
# The full ForgeBase image (pgvector, pg_cron, pg_graphql) so instance projects
# have feature parity with the shared cluster; stock postgres:17 as fallback.
IMG="${INSTANCE_IMAGE:-pgforge-postgres:17}"
docker image inspect "$IMG" >/dev/null 2>&1 || IMG=postgres:17
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

# superuser name inside an instance (the project role); recorded per instance
user_of() { cat "$ROOT/.user-$1" 2>/dev/null || echo postgres; }

run_instance() { # $1=slug $2=port [extra docker env args...]  (fresh container)
  local s="$1" p="$2"; shift 2
  # Small per-instance footprint: 64MB shared_buffers + 30 connections lets
  # many instances coexist; scram host auth - NEVER trust (the cold-start
  # proxy exposes these to the network once published).
  docker run -d --name "pgi-$s" -v "$ROOT/$s:/var/lib/postgresql/data" \
    --shm-size=128m "$@" -p "127.0.0.1:$p:5432" \
    "$IMG" postgres -c ssl=off -c archive_mode=off \
    -c shared_buffers=64MB -c max_connections=30 \
    -c password_encryption=scram-sha-256 >/dev/null
  touch "$ROOT/.active-$s" # so the reaper gives a fresh instance its full idle window
}

wait_ready() { # $1=slug [$2=user]
  local u="${2:-$(user_of "$1")}"
  for _ in $(seq 1 120); do
    docker exec "pgi-$1" pg_isready -U "$u" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  return 1
}

case "$cmd" in
  init)
    if mountpoint -q "$ROOT" && btrfs filesystem df "$ROOT" >/dev/null 2>&1; then echo btrfs-ok; else echo not-btrfs; exit 1; fi ;;
  create) # create <slug> <password>: the project role IS the instance superuser,
          # and the default database carries the slug (the proxy routes by it)
    pw="$3"; [ -z "$pw" ] && { echo "create needs a password"; exit 2; }
    btrfs subvolume create "$ROOT/$slug" >/dev/null
    chown 999:999 "$ROOT/$slug"
    printf '%s' "$slug" > "$ROOT/.user-$slug"
    p=$(port_of "$slug")
    run_instance "$slug" "$p"       -e "POSTGRES_USER=$slug" -e "POSTGRES_PASSWORD=$pw"       -e "POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256"
    wait_ready "$slug" "$slug" || { echo "instance did not become ready"; exit 1; }
    echo "created $slug port=$p" ;;
  start)
    [ -d "$ROOT/$slug" ] || { echo "no instance $slug"; exit 1; }
    p=$(port_of "$slug")
    if [ -n "$(docker ps -aq -f name=^pgi-$slug$)" ]; then docker start "pgi-$slug" >/dev/null; else run_instance "$slug" "$p"; fi
    wait_ready "$slug"; echo "started $slug port=$p" ;;
  stop)
    docker stop "pgi-$slug" >/dev/null; echo "stopped $slug (compute freed, data on disk)" ;;
  branch) # branch <parent> <child> <childpw>: instant CoW snapshot + new
          # instance; the child gets its own role + its database renamed so the
          # proxy can route to it. Parent untouched throughout.
    parent="$2"; child="$3"; childpw="$4"
    [ -d "$ROOT/$parent" ] || { echo "no parent $parent"; exit 1; }
    [ -z "$childpw" ] && { echo "branch needs a child password"; exit 2; }
    btrfs subvolume snapshot "$ROOT/$parent" "$ROOT/$child" >/dev/null
    puser="$(user_of "$parent")"
    printf '%s' "$child" > "$ROOT/.user-$child"
    p=$(port_of "$child"); run_instance "$child" "$p"; wait_ready "$child" "$puser"
    # local socket inside the container is trust for the initdb superuser
    docker exec "pgi-$child" psql -U "$puser" -d postgres -v ON_ERROR_STOP=1       -c "ALTER DATABASE \"$parent\" RENAME TO \"$child\""       -c "CREATE ROLE \"$child\" LOGIN SUPERUSER PASSWORD '$childpw'"       -c "ALTER DATABASE \"$child\" OWNER TO \"$child\"" >/dev/null
    echo "branched $child from $parent port=$p" ;;
  delete)
    docker rm -f "pgi-$slug" >/dev/null 2>&1 || true
    btrfs subvolume delete "$ROOT/$slug" >/dev/null 2>&1 || true
    sed -i "/^$slug /d" "$PORTS" 2>/dev/null || true
    rm -f "$ROOT/.user-$slug" "$ROOT/.active-$slug"
    echo "deleted $slug" ;;
  port) port_of "$slug" ;;
  wait) wait_ready "$slug" && echo ready || { echo notready; exit 1; } ;;
  reap) # reap [idle_seconds]: stop instances with no client connections for a while
    idle="${2:-900}"; now=$(date +%s)
    for d in "$ROOT"/*/; do
      s=$(basename "$d"); [ "$s" = "*" ] && continue
      [ -n "$(docker ps -q -f name=^pgi-$s$)" ] || continue
      conns=$(docker exec "pgi-$s" psql -U "$(user_of "$s")" -tAc "SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()" 2>/dev/null | tr -dc 0-9)
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
