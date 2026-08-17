#!/bin/sh
#
# set-db-allowlist.sh - restrict who can reach Postgres (5432) and the pooler
# (6543).
#
# Usage:
#   set-db-allowlist.sh 1.2.3.4 5.6.7.0/24 ...   restrict to these sources
#   set-db-allowlist.sh --public                 back to open-to-internet
#   set-db-allowlist.sh --show                   print current policy
#
# HTTPS (443) and SSH (22) are never affected. Existing DB connections survive
# a tightening (conntrack) - only NEW connections are filtered.
#
set -e

ALLOWLIST=/opt/pgforge/db_allowlist

case "${1:-}" in
  --show)
    if [ -s "$ALLOWLIST" ]; then echo "DB ports restricted to:"; cat "$ALLOWLIST"
    else echo "DB ports: open to the internet (no allowlist)"; fi
    exit 0 ;;
  --public)
    rm -f "$ALLOWLIST"
    ufw allow 5432/tcp >/dev/null
    ufw allow 6543/tcp >/dev/null
    ;;
  "")
    echo "usage: set-db-allowlist.sh <ip|cidr>... | --public | --show" >&2; exit 1 ;;
  *)
    : > "$ALLOWLIST.tmp"
    for src in "$@"; do
      echo "$src" | grep -qE '^[0-9]{1,3}(\.[0-9]{1,3}){3}(/[0-9]{1,2})?$' \
        || { echo "ERROR: '$src' is not an IPv4 or CIDR" >&2; rm -f "$ALLOWLIST.tmp"; exit 1; }
      echo "$src" >> "$ALLOWLIST.tmp"
    done
    mv "$ALLOWLIST.tmp" "$ALLOWLIST"
    # ufw side (docker-proxy/IPv6 path): drop broad allows, add per-source
    ufw delete allow 5432/tcp >/dev/null 2>&1 || true
    ufw delete allow 6543/tcp >/dev/null 2>&1 || true
    ufw status numbered | grep -E ' (5432|6543)(/tcp)? ' | grep -oE '^\[ *[0-9]+\]' | \
      grep -oE '[0-9]+' | sort -rn | while read -r n; do yes | ufw delete "$n" >/dev/null; done
    while read -r src; do
      ufw allow from "$src" to any port 5432 proto tcp >/dev/null
      ufw allow from "$src" to any port 6543 proto tcp >/dev/null
    done < "$ALLOWLIST"
    ;;
esac

systemctl restart pgforge-firewall
echo ">> applied. Current policy:"
sh "$0" --show
