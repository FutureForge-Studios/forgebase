#!/bin/sh
#
# setup-firewall.sh - lock the box down to HTTPS + SSH + Postgres. Idempotent.
#
# Public after this runs:
#   22        SSH
#   80, 443   Caddy (panel, SQL editor, project APIs over HTTPS)
#   5432      Postgres direct (TLS, session semantics)
#   6543      PgBouncer (transaction pooling)
# Everything else is blocked, including any future container port.
#
# IMPORTANT: docker-published ports BYPASS ufw (docker's iptables NAT runs
# before ufw's INPUT rules). ufw below only covers host processes. Container
# ports are enforced in the DOCKER-USER chain, applied on every boot by the
# pgforge-firewall systemd oneshot (ordered after docker.service).
#
# DB access policy comes from /opt/pgforge/db_allowlist (see set-db-allowlist.sh):
#   absent/empty -> DB ports open to the internet
#   one IPv4/CIDR per line -> DB ports reachable only from those sources
#
set -e

WAN_IF="${WAN_IF:-$(ip route get 1.1.1.1 2>/dev/null | grep -o 'dev [^ ]*' | cut -d' ' -f2)}"
[ -z "$WAN_IF" ] && WAN_IF=eth0
echo "==> Firewall for public interface: $WAN_IF"

# ----------------------------------------------------------------- ufw (host)
command -v ufw >/dev/null 2>&1 || { apt-get update -qq && apt-get install -y -qq ufw >/dev/null; }
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
ufw allow 22/tcp >/dev/null       # ssh - allowed BEFORE enable, never lock ourselves out
ufw allow 80/tcp >/dev/null
ufw allow 443/tcp >/dev/null
ufw allow 5432/tcp >/dev/null     # covers the docker-proxy (IPv6) path too
ufw allow 6543/tcp >/dev/null
ufw --force enable >/dev/null
echo "==> ufw active (host ports: 22, 80, 443, 5432, 6543)"

# ------------------------------------------------- DOCKER-USER (container ports)
mkdir -p /opt/pgforge/bin
cat > /opt/pgforge/bin/firewall-docker.sh <<EOF
#!/bin/sh
# Applied on boot by pgforge-firewall.service. Filters traffic FORWARDED to
# containers (which bypasses ufw). Host-originated traffic (Caddy/pgforged to
# 127.0.0.1) never traverses this chain, so local proxying keeps working.
set -e
ALLOWLIST=/opt/pgforge/db_allowlist
iptables -N DOCKER-USER 2>/dev/null || true
iptables -F DOCKER-USER
iptables -A DOCKER-USER -m conntrack --ctstate RELATED,ESTABLISHED -j RETURN
if [ -s "\$ALLOWLIST" ]; then
  grep -vE '^\s*(#|\$)' "\$ALLOWLIST" | while read -r src; do
    iptables -A DOCKER-USER -i ${WAN_IF} -s "\$src" -p tcp --dport 5432 -j RETURN
    iptables -A DOCKER-USER -i ${WAN_IF} -s "\$src" -p tcp --dport 6543 -j RETURN
  done
else
  iptables -A DOCKER-USER -i ${WAN_IF} -p tcp --dport 5432 -j RETURN
  iptables -A DOCKER-USER -i ${WAN_IF} -p tcp --dport 6543 -j RETURN
fi
iptables -A DOCKER-USER -i ${WAN_IF} -j DROP
iptables -A DOCKER-USER -j RETURN
EOF
chmod 700 /opt/pgforge/bin/firewall-docker.sh

cat > /etc/systemd/system/pgforge-firewall.service <<'EOF'
[Unit]
Description=ForgeBase DOCKER-USER firewall rules
After=docker.service network-online.target
Requires=docker.service

[Service]
Type=oneshot
ExecStart=/opt/pgforge/bin/firewall-docker.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now pgforge-firewall >/dev/null 2>&1
systemctl restart pgforge-firewall

echo "==> DOCKER-USER rules applied (service: pgforge-firewall)"
iptables -L DOCKER-USER -n --line-numbers | sed 's/^/   /'
