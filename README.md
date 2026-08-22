<div align="center">

# ForgeBase

<img width="3181" height="1639" alt="image" src="https://github.com/user-attachments/assets/4c044286-9dca-4993-b69f-a5ec647f1f79" />


**Your own Postgres backend platform - projects, auth, APIs, storage, realtime and instant branching, self-hosted on one small server.**

Create isolated Postgres databases in under a second and get, per project, a
table editor, SQL editor, instant REST + GraphQL API, auth, storage, realtime,
webhooks, edge functions, branches, backups and monitoring - behind a single
login, with automatic HTTPS.

</div>

---

## Why ForgeBase

Typical self-hosted backend stacks spin up a full set of containers **per project**,
so a handful of projects eats a whole server. ForgeBase inverts that: **every
service runs once and is multi-tenant.** A project is just a database + role +
a metadata row, so it costs ~0 MB idle and is created in under a second. Base
footprint is ~800 MB; a dozen projects still fit on a 4 GB VPS.

- **One binary, one server.** No Kubernetes, no per-project containers.
- **Real engines.** Genuine PostgREST for REST, `pg_graphql` for GraphQL, real
  Postgres 17 - not reimplementations.
- **Batteries included.** TLS, nightly backups with continuous WAL archiving and real point-in-time recovery (restore to any second into a new project),
  a verified monthly restore drill, firewall and fail2ban are set up for you.
- **Self-contained.** No SMTP, no external object store, no extra services
  required to get started.

## Features

| Area | What you get |
|------|--------------|
| **Projects** | Create / pause / resume / delete in ~1s; overview with copy-paste connection strings (direct TLS + transaction pooler); idle projects auto-suspend (login blocked, API sidecar stopped) and resume instantly on the next request. |
| **Table editor** | Browse, insert, inline-edit and delete rows; blob-safe grid; CSV import with type inference and all-or-nothing loading. |
| **SQL editor** | Full Postgres - DDL, DML, functions - with a schema browser, saved queries, safety timeouts and result caps. |
| **Data API** | Auto REST (PostgREST) + GraphQL (`pg_graphql`) per project on `https://<project>.<domain>`, with anon/service JWT keys. |
| **Auth** | End-user email+password and Google/GitHub OAuth issuing JWTs your Data API trusts; roles, invite-only team management. |
| **Storage** | Public and private file buckets with signed URLs. |
| **Realtime & Webhooks** | Live row-change streams over WebSockets and outbound webhooks on insert/update/delete. |
| **Edge Functions** | Per-project Deno functions on `/functions/v1/<name>`. |
| **Branches** | Dedicated-instance projects branch instantly (~2s copy-on-write snapshots that share storage with the parent); shared-cluster projects get full copies with their own credentials. |
| **Clone & Sync** | Import any external Postgres from a connection string and optionally keep it live-synced via logical replication. |
| **Database admin** | Rotate credentials, enable extensions (~50-item catalog), tune connection limits. |
| **Backups & recovery** | Nightly logical dumps + basebackups + continuous WAL archive; per-project "back up now" and one-click restore; real point-in-time recovery to any second into a new project; off-box S3 sync. |
| **Monitoring, Logs & Audit** | Per-project size/connections/cache-hit and 7-day charts; live session view; a platform-wide audit trail with actor + source IP. |

We would rather tell you exactly what each feature does than oversell it - the
docs describe real behavior, and the build plan lives in
[docs/ROADMAP.md](docs/ROADMAP.md).

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

## License

ForgeBase is licensed under the [GNU AGPL v3.0](LICENSE).

In practice: self-host it, run it for your own projects, run it for your
clients, modify it - no obligations. The copyleft only bites if you offer a
modified ForgeBase to third parties as a network service, in which case you
must publish your modifications under the same license.

For a commercial license without the AGPL terms, contact
[FutureForge Studios](https://ffstudios.io).

---

<div align="center">

**ForgeBase** is designed, built and maintained by
**[FutureForge Studios Private Limited](https://ffstudios.io)**.

Crafted with a lot of care in India. ♥

</div>
