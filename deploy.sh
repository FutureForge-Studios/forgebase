#!/usr/bin/env bash
#
# deploy.sh - push the ForgeBase source to the server and (re)install.
# install.sh is idempotent, so deploy = upload + re-run installer.
# Needs .env with SERVER_IP / SERVER_USER / SERVER_PASS and pip install paramiko.
#
set -euo pipefail
cd "$(dirname "$0")"
export MSYS_NO_PATHCONV=1
set -a; . ./.env; set +a

echo ">> packing source"
tar -czf ./pgforge-src.tar.gz pgforge server scripts systemd install.sh

echo ">> uploading"
python tools/put.py ./pgforge-src.tar.gz /root/pgforge-src.tar.gz

# Tag the build with the current git commit + UTC time so the panel footer shows
# exactly which version is running (used for rollback).
VER="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
BT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ">> installing on server (version $VER)"
python tools/rr.py "rm -rf /root/pgforge-src && mkdir -p /root/pgforge-src \
  && tar -xzf /root/pgforge-src.tar.gz -C /root/pgforge-src \
  && cd /root/pgforge-src && PGFORGE_VERSION=$VER PGFORGE_BUILDTIME=$BT bash install.sh"

echo ">> done"
