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
| **Projects** | Create / pause / resume / delete in ~1s; connection strings for direct TLS (5432), transaction pooler (6543, TLS) and the optional read-only replica (5434); idle projects sleep and wake on any request; per-project member scoping for teams. |
| **Table editor** | Type-aware cell editors, JSON editor, row panel, stacked filters, multi-sort, bulk actions, CSV import/export, full schema editing with FK management, saved layouts. |
| **SQL editor** | Tabs, schema-aware autocomplete, visual EXPLAIN, run-as-role for policy testing, history, formatter, safety guards - plus a built-in AI assistant (bring your own key) on every project page. |
| **Data API** | Auto REST (PostgREST) + GraphQL (`pg_graphql`) per project, anon/service JWT keys, optional RS256 signing with a public JWKS endpoint and one-click key rotation, per-project row caps, exposed schemas, IP allowlists, TypeScript type generation, OpenAPI, in-panel API explorer. |
| **Auth** | Email+password, magic links, email OTP, phone OTP (bring-your-own SMS webhook), anonymous sign-ins, 12 OAuth providers + generic OIDC + SAML 2.0 SSO, TOTP MFA with recovery codes, captcha, configurable rate limits, leaked-password screening, single-session mode, custom-claims + before-create SQL hooks, email templates, per-user session control, admin impersonation. |
| **Storage** | Buckets with folders, drag-drop, quotas and usage meters, signed URLs and signed uploads, resumable tus uploads, S3-compatible protocol access with scoped keys, path-level access rules (public/authenticated/owner/private), image transformations with cached renditions, ETag caching. |
| **Realtime & Webhooks** | Row-change streams over WebSockets with optional per-subscriber RLS filtering, named channels with broadcast + presence (also from SQL), private channels, logical-replication publications UI, outbound webhooks with replay. |
| **Edge Functions** | Deno handlers with warm processes (no cold starts), streaming responses, WebSocket support, background work, per-function secrets, timeouts, memory caps and cron schedules, full invocation logs. |
| **Queues & Cron** | Durable at-least-once message queues in SQL (`forgebase.queue_*`) with a panel UI; `pg_cron` job scheduler. |
| **Branches** | Instant copy-on-write branches (dedicated instances) or full copies; branch from any point in time via WAL replay; anonymized branches (PII scrub rules); schema diff; reset from parent; auto-expiry. |
| **Platform depth** | One-click migration from shared cluster to a dedicated instance; live memory/CPU limits with adaptive sizing; one-click streaming read replica; encrypted secrets vault callable from SQL; foreign-data wrappers UI; `forgebase` CLI with personal API keys. |
| **Backups & recovery** | Nightly logical dumps + basebackups + continuous WAL archive; point-in-time recovery to any second into a new project; off-box S3 sync of everything including an encrypted disaster-recovery kit; verified restore drills. |
| **Monitoring, Logs & Audit** | Per-project charts, per-database activity attribution, usage reports, 11 live advisor rules, logs with saved views and daily log shipping, platform-wide audit trail. |

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
**[docs/SELF-HOSTING.md](docs/SELF-HOSTING.md)** - and the
box-is-gone rebuild procedure: **[docs/DISASTER-RECOVERY.md](docs/DISASTER-RECOVERY.md)**.

## Use

- **Panel**: `https://yourdomain`
- **Direct Postgres** (TLS, Prisma-safe): `yourdomain:5432`, `sslmode=require`
- **Pooled** (transaction mode): `yourdomain:6543`
- **Read replica** (optional, one click): `yourdomain:5434`, read-only
- **Project API**: `https://<project>.yourdomain/rest/v1` (and `/graphql/v1`,
  `/auth/v1`, `/storage/v1`, `/realtime/v1`, `/functions/v1`, `/s3/`-style
  object access, `/.well-known/jwks.json`)

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
