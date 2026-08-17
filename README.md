<div align="center">

# ForgeBase

**Your own Postgres backend platform - the Supabase/Neon experience, self-hosted, on one small server.**

Create isolated Postgres databases in under a second and get, per project, a
table editor, SQL editor, instant REST + GraphQL API, auth, storage, realtime,
webhooks, edge functions, branches, backups and monitoring - behind a single
login, with automatic HTTPS.

</div>

---

## Why ForgeBase

Most self-hosted Supabase stacks spin up a full set of containers **per project**,
so a handful of projects eats a whole server. ForgeBase inverts that: **every
service runs once and is multi-tenant.** A project is just a database + role +
a metadata row, so it costs ~0 MB idle and is created in under a second. Base
footprint is ~800 MB; a dozen projects still fit on a 4 GB VPS.

- **One binary, one server.** No Kubernetes, no per-project containers.
- **Real engines.** Genuine PostgREST for REST, `pg_graphql` for GraphQL, real
  Postgres 17 - not reimplementations.
- **Batteries included.** TLS, nightly backups with point-in-time WAL archiving,
  a verified monthly restore drill, firewall and fail2ban are set up for you.
- **Self-contained.** No SMTP, no external object store, no extra services
  required to get started.

## Features

| Area | What you get |
|------|--------------|
| **Projects** | Create / pause / resume / delete in ~1s; overview with copy-paste connection strings (direct TLS + transaction pooler); scale-to-zero for idle projects. |
| **Table editor** | Browse, insert, inline-edit and delete rows; blob-safe grid; CSV import with type inference and all-or-nothing loading. |
| **SQL editor** | Full Postgres - DDL, DML, functions - with a schema browser, saved queries, safety timeouts and result caps. |
| **Data API** | Auto REST (PostgREST) + GraphQL (`pg_graphql`) per project on `https://<project>.<domain>`, with anon/service JWT keys. |
| **Auth** | End-user email+password and Google/GitHub OAuth issuing JWTs your Data API trusts; roles, invite-only team management. |
| **Storage** | Public and private file buckets with signed URLs. |
| **Realtime & Webhooks** | Live row-change streams over WebSockets and outbound webhooks on insert/update/delete. |
| **Edge Functions** | Per-project Deno functions on `/functions/v1/<name>`. |
| **Branches** | Instant database copies with their own credentials for staging/testing. |
| **Clone & Sync** | Import any external Postgres from a connection string and optionally keep it live-synced via logical replication. |
| **Database admin** | Rotate credentials, enable extensions (~50-item catalog), tune connection limits. |
| **Backups & recovery** | Nightly logical dumps + basebackups + continuous WAL archive; per-project "back up now" and one-click restore; off-box S3 sync. |
| **Monitoring, Logs & Audit** | Per-project size/connections/cache-hit and 7-day charts; live session view; a platform-wide audit trail with actor + source IP. |

## Install

One command on a fresh Ubuntu 22.04/24.04 server - it prompts for your domain
and email, shows the DNS records to add, waits until they resolve, then
provisions Let's Encrypt TLS automatically:

```sh
git clone https://github.com/FutureForge-Studios/forgebase.git forgebase
cd forgebase
sudo bash install.sh
```

Point `yourdomain`, `db.yourdomain` and `*.yourdomain` at the server. When it
finishes it prints the panel URL and admin password. Re-run any time to upgrade
- it's idempotent and preserves your data and secrets.

Full walkthrough, backup/restore runbook and security checklist:
**[docs/SELF-HOSTING.md](docs/SELF-HOSTING.md)**.

## Use

- **Panel**: `https://yourdomain`
- **Direct Postgres** (TLS, Prisma-safe): `yourdomain:5432`, `sslmode=require`
- **Pooled** (transaction mode): `yourdomain:6543`
- **Project API**: `https://<project>.yourdomain/rest/v1` (and `/graphql/v1`,
  `/auth/v1`, `/storage/v1`, `/realtime/v1`, `/functions/v1`)

---

<div align="center">

**ForgeBase** is designed, built and maintained by
**[FutureForge Studios Private Limited](https://ffstudios.io)**.

Crafted with a lot of care in India. ♥

</div>
