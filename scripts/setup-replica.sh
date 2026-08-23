#!/bin/sh
#
# setup-replica.sh - stand up (or tear down) the streaming read replica.
#
#   setup-replica.sh enable    create role+slot+hba, basebackup, start container
#   setup-replica.sh disable   stop container, drop slot, remove data
#   setup-replica.sh status    one line: state + replication lag
#
# The replica is a hot standby of the whole shared cluster in its own
# container (pgforge-replica), streaming over the compose network through a
# physical replication slot, serving READ-ONLY connections on port 5434 with
# the same TLS certs and the same per-project credentials as the primary
# (roles replicate). Idempotent; enable refuses to run without 2x the
# cluster's disk free.
set -eu

IMG=pgforge-postgres:17
NET=pgforge_default
DATA=/opt/pgforge/replica-data
PWFILE=/opt/pgforge/replica.pw
SLOT=fb_replica
PORT=5434

status() {
  if ! docker ps --format '{{.Names}}' | grep -q '^pgforge-replica$'; then
    echo "off"
    return 0
  fi
  LAG="$(docker exec pgforge-db psql -U postgres -Atc \
    "SELECT coalesce(round(extract(epoch from replay_lag)::numeric,1)::text,'0') FROM pg_stat_replication WHERE application_name='pgforge-replica' LIMIT 1" 2>/dev/null || true)"
  REC="$(docker exec pgforge-replica psql -U postgres -Atc 'SELECT pg_is_in_recovery()' 2>/dev/null || echo error)"
  if [ "$REC" = "t" ]; then
    echo "streaming lag=${LAG:-?}s"
  else
    echo "unhealthy ($REC)"
  fi
}

case "${1:-status}" in
status)
  status
  ;;

enable)
  if docker ps -a --format '{{.Names}}' | grep -q '^pgforge-replica$'; then
    docker start pgforge-replica >/dev/null 2>&1 || true
    status
    exit 0
  fi
  # disk guard: need room for a full copy of the cluster, twice over
  CLUSTER_KB="$(docker exec pgforge-db du -sk /var/lib/postgresql/data | cut -f1)"
  FREE_KB="$(df -k / | tail -1 | awk '{print $4}')"
  [ "$FREE_KB" -gt $((CLUSTER_KB * 2)) ] || {
    echo "refusing: need $((CLUSTER_KB*2/1024))MB free for a safe replica, have $((FREE_KB/1024))MB" >&2
    exit 1
  }

  # replication role (cluster-wide) + password kept root-only on the host
  if [ ! -f "$PWFILE" ]; then
    head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$PWFILE"
    chmod 600 "$PWFILE"
  fi
  RPW="$(cat "$PWFILE")"
  docker exec pgforge-db psql -U postgres -c \
    "DO \$\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='fb_repl') THEN CREATE ROLE fb_repl REPLICATION LOGIN; END IF; END \$\$" >/dev/null
  docker exec pgforge-db psql -U postgres -c "ALTER ROLE fb_repl PASSWORD '$RPW'" >/dev/null

  # hba: allow replication from the compose network (persists in the datadir)
  if ! docker exec pgforge-db grep -q 'replication fb_repl 172.16' /var/lib/postgresql/data/pg_hba.conf; then
    docker exec pgforge-db sh -c "echo 'host replication fb_repl 172.16.0.0/12 scram-sha-256' >> /var/lib/postgresql/data/pg_hba.conf"
    docker exec pgforge-db psql -U postgres -c 'SELECT pg_reload_conf()' >/dev/null
  fi

  docker exec pgforge-db psql -U postgres -c \
    "SELECT CASE WHEN NOT EXISTS (SELECT FROM pg_replication_slots WHERE slot_name='$SLOT') THEN pg_create_physical_replication_slot('$SLOT')::text END" >/dev/null

  echo ">> basebackup (cluster copy, may take a few minutes)..." >&2
  rm -rf "$DATA"
  mkdir -p "$DATA"
  docker run --rm --network "$NET" -e PGPASSWORD="$RPW" -v "$DATA":/out "$IMG" \
    pg_basebackup -h pgforge-db -U fb_repl -D /out -R -X stream -S "$SLOT" -c fast >&2
  chown -R 999:999 "$DATA"

  docker run -d --name pgforge-replica --restart unless-stopped \
    --network "$NET" -p ${PORT}:5432 \
    -v "$DATA":/var/lib/postgresql/data \
    -v /opt/pgforge/certs:/certs:ro \
    --memory=768m --memory-swap=768m \
    "$IMG" postgres \
      -c hot_standby=on \
      -c shared_buffers=256MB \
      -c ssl=on -c ssl_cert_file=/certs/server.crt -c ssl_key_file=/certs/server.key \
      -c primary_slot_name="$SLOT" \
      -c cluster_name=pgforge-replica \
      -c max_connections=200 >/dev/null
      # max_connections must be >= the primary's (200) or recovery refuses to start
  command -v ufw >/dev/null 2>&1 && ufw allow ${PORT}/tcp >/dev/null 2>&1 || true

  # wait for recovery mode
  for i in $(seq 1 60); do
    REC="$(docker exec pgforge-replica psql -U postgres -Atc 'SELECT pg_is_in_recovery()' 2>/dev/null || true)"
    [ "$REC" = "t" ] && break
    sleep 2
  done
  status
  ;;

disable)
  docker rm -f pgforge-replica >/dev/null 2>&1 || true
  docker exec pgforge-db psql -U postgres -c \
    "SELECT pg_drop_replication_slot('$SLOT') WHERE EXISTS (SELECT FROM pg_replication_slots WHERE slot_name='$SLOT')" >/dev/null 2>&1 || true
  rm -rf "$DATA"
  command -v ufw >/dev/null 2>&1 && ufw delete allow ${PORT}/tcp >/dev/null 2>&1 || true
  echo "off"
  ;;

*)
  echo "usage: setup-replica.sh enable|disable|status" >&2
  exit 2
  ;;
esac
