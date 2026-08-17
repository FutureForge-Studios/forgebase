# Self-hosting ForgeBase

ForgeBase is a lightweight, self-hosted Supabase/Neon alternative: one login, and
per project you get a table editor, SQL editor, instant REST + GraphQL API, auth,
storage, realtime, branches, backups and monitoring - all on one small VPS. Every
service runs once and is multi-tenant; a project is just a Postgres database +
role + a metadata row.

This guide takes you from a bare server to a working platform in one command.

---

## 1. What you need

- **A server**: Ubuntu 22.04 or 24.04, x86-64, root (or sudo) access. 2 GB RAM
  works; 4 GB is comfortable. The installer adds 4 GB of swap automatically.
- **A domain** you control, with the ability to add DNS records. You will point
  three records at the server:
  - `base.example.com` (the panel)
  - `db.base.example.com` (Postgres/pooler hostname)
  - `*.base.example.com` (wildcard - every project's API lives on a subdomain)
- **Open ports**: 22 (SSH), 80 + 443 (HTTPS), 5432 (Postgres, TLS) and 6543
  (pooler). The installer configures a firewall to exactly these.

On Cloudflare, set the records to **DNS only** (grey cloud), not proxied, so
Let's Encrypt can validate and issue certificates directly.

---

## 2. One-command install

SSH into the fresh server and run:

```sh
git clone <your-repo-url> forgebase && cd forgebase
sudo bash install.sh
```

The installer will:

1. Ask for your **domain** and an **email** (for Let's Encrypt).
2. Print the exact DNS records to create, then **wait and verify** they resolve
   to this server before continuing (type `skip` to bypass if you know better).
3. Install Docker, Go, PostgREST and Deno; add swap.
4. Bring up Postgres 17 (TLS, WAL archiving), PgBouncer, and Caddy.
5. Provision **TLS automatically** via Caddy + Let's Encrypt for the panel, the
   `db.` host, and on-demand for every project subdomain.
6. Build and start `pgforged` (the control plane) under systemd, plus the nightly
   backup + monthly restore-test timers, a firewall, and fail2ban.

When it finishes it prints the panel URL, the admin username, and a generated
password. **Save that password.**

Non-interactive / scripted installs:

```sh
DOMAIN=base.example.com ACME_EMAIL=you@example.com sudo -E bash install.sh
```

Useful env toggles: `PANEL_PASS` (set the admin password), `SKIP_FIREWALL=1`,
`SKIP_HARDENING=1`, `SKIP_DNS_CHECK=1`, `MAX_UPLOAD_MB` (default 100),
`SKIP_CADDY=1` + `LISTEN=0.0.0.0:8080` for a DNS-less test box.

---

## 3. First login

1. Open `https://base.example.com`.
2. The **first** account you register becomes the **owner** (registration then
   closes - further users are invite-only from the Team page). Or log in with the
   break-glass `admin` / generated password from the installer output.
3. Create a project. In under a second you get its database, connection strings,
   and every section (Tables, SQL, API, Auth, Storage, Realtime, Branches,
   Backups, Monitoring).

---

## 4. Backups & recovery

Backups run automatically every night at 03:30 UTC:

- **Logical dumps** of every database + globals -> `/opt/pgforge-backups/dumps`
  (30-day retention, editable per platform in any project's Backups page).
- **Physical basebackups** + a continuous, gzip-compressed **WAL archive** ->
  point-in-time recovery (7-day basebackup retention; WAL is pruned to exactly
  what the oldest kept basebackup needs, with an emergency prune at 85% disk).
- A **monthly restore-test** proves the newest dump actually restores.

Per-project, the **Backups** page offers "Back up now" (atomic, timestamped) and
one-click restore of any dump.

**Off-box copies (strongly recommended):**

```sh
apt install rclone && rclone config          # add a remote, e.g. S3
echo '<remote>:<path>' > /opt/pgforge/backup_remote
```

After each nightly run everything is synced to that remote.

**Point-in-time restore** (advanced): restore the newest basebackup from
`/opt/pgforge-backups/physical`, then replay the WAL archive with a
`restore_command` that gunzips segments, e.g.
`gunzip -c /wal-archive/%f.gz > %p`.

---

## 5. Operating it

```sh
systemctl status pgforged ; journalctl -u pgforged -n 50   # control plane
docker ps                                                   # db, pgbouncer, caddy
fail2ban-client status forgebase-panel
tail /var/log/pgforge-backup.log
```

**Upgrades**: pull the latest code and re-run the installer - it's idempotent and
preserves your secrets and data:

```sh
cd forgebase && git pull && sudo bash install.sh
```

(Contributors with the dev repo can use `deploy.sh` for the fast build-and-restart
loop.)

**Building the binary reproducibly** (CI / cross-compile) is available via the
`Dockerfile`; note it builds the binary only - the runtime is installed on the
host by `install.sh`, not run as a container (pgforged manages Docker, systemd,
and host paths directly).

---

## 6. Security checklist

The installer sets up TLS 1.3, Postgres TLS + scram-sha-256, a firewall,
fail2ban, HMAC sessions, invite-only registration, security headers, and
external DB connections that **require** TLS. After install:

- [ ] Save the panel password; register your real owner account.
- [ ] Switch SSH to key-only auth and disable password login once your key works.
- [ ] Confirm off-box backups are configured (section 4).
- [ ] `SESSION_SECRET` in `/opt/pgforge/pgforged.env` is generated strong - do not
      shorten it, and note it also encrypts stored DB passwords, so **rotating it
      is destructive** (existing connection strings become unrecoverable).
- [ ] Keep the OS patched (`unattended-upgrades` is enabled; reboot for kernel
      updates).

---

## 7. Ports at a glance

| Port | Purpose | Exposure |
|------|---------|----------|
| 22   | SSH | your admin access |
| 80   | HTTP -> HTTPS redirect + ACME | public |
| 443  | Panel + project APIs (TLS) | public |
| 5432 | Postgres direct (TLS, session use: Prisma, migrations) | public, TLS-only |
| 6543 | PgBouncer transaction pooler | public |

Project APIs are served on `https://<project>.base.example.com` (`/rest/v1`,
`/graphql/v1`, `/auth/v1`, `/storage/v1`, `/realtime/v1`, `/functions/v1`).

---

ForgeBase is designed, built and maintained by **FutureForge Studios Private
Limited** ([ffstudios.io](https://ffstudios.io)). Crafted with a lot of care in
India. ♥
