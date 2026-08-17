# Changelog

All notable changes to ForgeBase are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions before 1.0.0 were private-beta milestones; dates for those are the month
the work landed. 1.0.0 is the first public release.

## [Unreleased]

### Added
- Nothing yet. Open an issue or PR to propose the next change.

## [1.0.0] - 2026-08-17

First public release. ForgeBase is a lightweight, self-hosted Supabase and Neon
alternative that runs as a single Go binary against a shared Postgres cluster.

### Added
- In-app self-update: check GitHub for a newer build, read the changelog, and
  install it with one click. The updater rebuilds the binary, swaps it
  atomically, restarts, health-checks, and rolls back automatically on failure.
  Admin only, opt-in, audit-logged.
- "What's New" page in the panel: the full version history plus a catalog of
  every feature, rendered from the same source as this file.
- Public GitHub repository and one-command interactive installer that prompts for
  a domain, verifies DNS, and provisions Let's Encrypt TLS automatically.

### Changed
- Rebranded to ForgeBase across the panel, docs, and installer.
- Backups copy on the panel now describes exactly what the daily restore does,
  separate from continuous WAL and point-in-time recovery, so the UI never
  claims a capability it cannot perform.

### Security
- Removed the host environment from the edge-function runtime: functions now see
  only their own scoped secrets plus the project URL and keys, never the panel
  password or database DSN.

## [0.9.0] - 2026-08

### Added
- Realtime per-subscription filters: clients subscribe to a specific table and
  event instead of one global change firehose.
- Webhook HMAC signing (`X-ForgeBase-Signature`), automatic retries with
  backoff, and a per-endpoint delivery log.
- Edge-function scoped secrets and a per-function log viewer capturing stderr.

## [0.8.0] - 2026-08

### Added
- End-user auth refresh tokens with rotation and revocation: short-lived access
  tokens plus a `grant_type=refresh_token` flow on `/auth/v1/token`.
- Client-facing Storage API authenticated by the project JWT: upload, download,
  and delete objects, plus mint signed URLs, with per-object path authorization.

### Changed
- Backups now include the storage and edge-function file planes, not just the
  database.

## [0.7.0] - 2026-08

### Added
- Table editor: row pagination, in-panel schema editing (create and drop tables,
  add and drop columns), typed input widgets, and CSV export.
- SQL editor: saved queries and history, CSV and JSON export of results, and
  run-as-role (anon or authenticated) to test policies.

## [0.6.0] - 2026-08

### Added
- Secure-by-default Row Level Security story: one-click "Enable RLS" per table
  with starter policy templates (public read, authenticated read, authenticated
  write, owner read and write).
- SQL auth helpers installed with the Data API: `auth.uid()`, `auth.jwt()`,
  `auth.role()`, and `auth.email()`, reading the request JWT claims.

### Changed
- GraphQL now propagates the request JWT claims, so `auth.uid()` works through
  GraphQL, and enforces a query depth limit and a statement timeout.
- PostgREST reloads its schema cache after DDL, so new tables and columns appear
  on the REST API immediately.

## [0.5.0] - 2026-07

### Added
- Full mobile layout: the sidebar collapses into an off-canvas drawer behind a
  hamburger button, cards stack to a single column, and wide tables scroll inside
  their own container. No horizontal page scroll on a phone.

## [0.4.0] - 2026-07

### Security
- External database access is TLS-only.
- Server access moved to SSH key authentication.

### Added
- Per-project `CREATEDB` toggle (for Prisma shadow databases).
- Editable per-project connection limits from the panel.

## [0.3.0] - 2026-06

### Added
- End-user Auth: email and password sign-up and sign-in issuing project JWTs,
  plus OAuth providers and a users admin.
- Storage: public and private buckets with panel upload and download.
- Realtime: WebSocket change streams per project.
- Database Webhooks: fire an HTTP request on row changes.
- Edge Functions: Deno-based functions per project.
- Backups: nightly logical dumps, continuous WAL archiving, basebackups, a
  verified restore test, and off-box S3 storage.
- Monitoring: host and per-project resource stats.
- Branches: create a copy of a project database.
- Sync and Clone: import from an external Postgres and keep it in sync via
  logical replication.
- Team and Audit: owner, admin, and member roles with an actor and IP audit log.

## [0.2.0] - 2026-06

### Added
- Auto Data API (PostgREST) per project with anon and service JWT keys.
- GraphQL API (pg_graphql) per project.
- Per-project docs with connection strings and client code snippets.

## [0.1.0] - 2026-05

### Added
- Core platform: multi-tenant Postgres 17 cluster with a per-project database and
  role, PgBouncer pooling, and Caddy on-demand TLS.
- Panel authentication and project lifecycle: create, pause, resume, delete.
- Table editor: browse rows, insert, update, delete, and CSV import.
- SQL editor: run queries against a project database with a statement timeout.

[Unreleased]: https://github.com/FutureForge-Studios/forgebase/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/FutureForge-Studios/forgebase/releases/tag/v1.0.0
