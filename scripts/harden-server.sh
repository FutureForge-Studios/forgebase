#!/bin/sh
#
# harden-server.sh - server-level security hardening for ForgeBase. Idempotent.
#   - fail2ban jail for SSH and for the ForgeBase panel (bans IPs that brute
#     force the login; pgforged logs "FAILED LOGIN ip=..." to the journal)
#   - automatic unattended security updates
# The firewall itself is scripts/setup-firewall.sh (ufw + DOCKER-USER).
#
set -e

echo "==> Installing fail2ban + unattended-upgrades"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq fail2ban unattended-upgrades >/dev/null

# ---- fail2ban filter: match pgforged's failed-login log line
cat > /etc/fail2ban/filter.d/forgebase.conf <<'EOF'
[Definition]
failregex = FAILED LOGIN ip=<HOST>
ignoreregex =
EOF

# ---- jails: sshd (journal) + the panel (journal, pgforged.service)
cat > /etc/fail2ban/jail.d/forgebase.conf <<'EOF'
[DEFAULT]
bantime  = 1h
findtime = 10m
maxretry = 6
banaction = ufw

[sshd]
enabled = true
backend = systemd

[forgebase-panel]
enabled = true
backend = systemd
filter = forgebase
journalmatch = _SYSTEMD_UNIT=pgforged.service
maxretry = 6
findtime = 10m
bantime = 1h
EOF

systemctl enable --now fail2ban >/dev/null 2>&1
systemctl restart fail2ban
sleep 1
fail2ban-client status forgebase-panel 2>/dev/null | sed 's/^/   /' || true

# ---- automatic security updates
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF
systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true

echo "==> Hardening applied: fail2ban (sshd + panel), auto security updates."
echo "    Banned IPs:  fail2ban-client status forgebase-panel"
