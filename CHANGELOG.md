# Changelog

All notable changes to ForgeBase are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions before 1.0.0 were private-beta milestones; dates for those are the month
the work landed. 1.0.0 is the first public release.

## [Unreleased]

### Added
- Nothing yet. Open an issue or PR to propose the next change.

## [1.1.9] - 2026-08-18

### Added
- Admin user-management API using the `service_role` key at
  `/auth/v1/admin/users`: list, create, get, update, and delete users, plus ban
  and unban (`ban_duration`). Banned users cannot sign in.

### Fixed
- Auth schema additions (metadata, session families, bans) now apply to
  already-enabled projects automatically on startup, so updating never leaves an
  existing auth project on an old schema.

## [1.1.8] - 2026-08-18

### Added
- GitLab and Discord are now available as end-user OAuth sign-in providers,
  alongside Google and GitHub. Configure the client id and secret on the Auth
  page.

## [1.1.7] - 2026-08-18

### Security
- Refresh-token reuse detection. Tokens rotate on use; if an already-used token
  is replayed (a strong sign it was stolen), the entire session lineage is
  revoked, not just that one token.
- Global sign-out: `POST /auth/v1/logout?scope=global` revokes every active
  session for the user.

## [1.1.6] - 2026-08-18

### Added
- End-user accounts now carry `user_metadata` and `app_metadata`. Pass
  `user_metadata` as `data` at sign-up, read/update it via GET and PUT
  `/auth/v1/user`, and it is embedded in the access token so your app and RLS
  policies (via `auth.jwt()`) can read it. `app_metadata` is admin-controlled.
- Access tokens now include the standard `aud` ("authenticated") claim.

## [1.1.5] - 2026-08-18

### Added
- Webhook payloads now include `old_record` (the row's previous values on update
  and delete), so consumers can diff changes.
- Webhooks support a custom HTTP method (POST/PUT/PATCH) and a custom header (for
  example an Authorization token to the target).

### Changed
- Webhook delivery retries over a longer window (up to 5 attempts across about 7
  minutes) so a briefly-down target still receives the event.

## [1.1.4] - 2026-08-18

### Fixed
- Disabling Realtime no longer silently stops Webhooks. The two share a
  change-capture trigger, which is now kept as long as either feature needs it.
- Realtime and Webhooks now cover tables created after they were enabled: an
  event trigger auto-attaches change capture to new tables.

## [1.1.3] - 2026-08-18

### Security
- Realtime now requires an authenticated (or service) key by default. The stream
  is not per-row RLS filtered, so the public anon key could otherwise read every
  change. A toggle on the Realtime page allows the anon key per project.

### Added
- Realtime column-equality subscription filter, e.g. `?filter=id=eq.5`.

## [1.1.2] - 2026-08-18

### Added
- Storage buckets can set a maximum file size (MB) and an allowed MIME-type list
  when created. Both the panel upload and the client upload API reject files that
  are too large or of a disallowed type.

## [1.1.1] - 2026-08-18

### Security
- Edge Functions can now require a valid JWT to invoke. New functions default to
  JWT-required (toggle it per function on the Functions page); already-deployed
  functions keep their current public setting. This closes a footgun where a
  public function using the injected service-role key was reachable by anonymous
  callers.

## [1.1.0] - 2026-08-18

### Changed
- The in-app update check now shows the real release notes from the changelog
  instead of raw developer commit messages, and tracks updates by version.

### Added
- Experimental, opt-in per-instance mode (enable with `INSTANCES=1` at install):
  each project or branch runs as its own Postgres instance on a copy-on-write
  filesystem, which makes branching instant (no parent downtime) and lets idle
  projects scale to zero and wake on the next connection. It ships as a proven
  engine, a cold-start proxy, and a reaper; it is not yet wired into the panel.

## [1.0.3] - 2026-08-17

### Added
- Real point-in-time recovery. From the Backup page, restore a project to any
  instant down to the second, into a new project (non-destructive). It stands up
  a throwaway instance from the newest basebackup before the target and replays
  the continuously archived WAL forward to exactly that moment
  (`recovery_target_time`), then loads the result into a fresh project.

### Fixed
- RLS write policies now work end to end. Adding an "authenticated write" or
  "owner" policy also grants the `authenticated` role the table's write
  privileges and sequence usage, so signed-in users can write their own rows
  through the Data API. Previously the write was denied with "permission denied
  for table" before the policy was ever evaluated.

### Changed
- Corrected over-claims in the README and UI: branching is a full copy that
  briefly locks the source (not instant copy-on-write), pause is auto-suspend
  (not scale-to-zero of compute). Added `docs/COMPARISON.md`, an honest
  feature-by-feature comparison with Supabase and Neon.

## [1.0.2] - 2026-08-17

### Fixed
- The one-click self-update now builds correctly. The updater runs in an
  isolated environment and could not locate the Go module cache, so the rebuild
  failed and the update was skipped (the running version was left untouched,
  with no downtime). It now sets the Go toolchain environment explicitly.

## [1.0.1] - 2026-08-17

### Added
- Continuous integration that builds, vets, and format-checks the control plane
  on every push and pull request.
- Contributor guide, security policy, code of conduct, and issue and
  pull-request templates.

### Changed
- Formatted the whole control plane with gofmt and pinned Go sources to LF line
  endings.

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

[Unreleased]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.9...HEAD
[1.1.9]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.8...v1.1.9
[1.1.8]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.7...v1.1.8
[1.1.7]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.6...v1.1.7
[1.1.6]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.5...v1.1.6
[1.1.5]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.4...v1.1.5
[1.1.4]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.3...v1.1.4
[1.1.3]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.2...v1.1.3
[1.1.2]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.1...v1.1.2
[1.1.1]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/FutureForge-Studios/forgebase/compare/v1.0.3...v1.1.0
[1.0.3]: https://github.com/FutureForge-Studios/forgebase/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/FutureForge-Studios/forgebase/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/FutureForge-Studios/forgebase/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/FutureForge-Studios/forgebase/releases/tag/v1.0.0
