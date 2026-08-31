# Changelog

All notable changes to ForgeBase are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions before 1.0.0 were private-beta milestones; dates for those are the month
the work landed. 1.0.0 is the first public release.

## [Unreleased]

### Added
- Nothing yet. Open an issue or PR to propose the next change.

## [1.4.23] - 2026-08-25

### Added
- Live events feed on the Realtime page (colour-coded insert/update/
  delete, newest first, pause + clear), admin-only.

### Fixed
- Enabling realtime / re-scanning tables no longer re-creates every
  trigger: only differing tables are touched (re-scan with no changes is
  instant), and lock_timeout stops one busy table stalling the run.

## [1.4.22] - 2026-08-25

### Fixed
- Outgoing mail had no Message-ID or Date header; Gmail (and other strict
  providers) reject such messages outright with 550-5.7.1
  RfcMessageNonCompliant. All mail now carries a unique Message-ID, a
  Date header and a Q-encoded Subject.

## [1.4.21] - 2026-08-23

### Added
- Platform-wide SMTP (System page): one sender for every project's auth
  emails, with per-project SMTP as an optional override that always wins.
  Includes a platform-level send-test button; Auth pages show which
  sender is active.

## [1.4.20] - 2026-08-23

### Added
- Designed default auth emails (confirm / magic link / reset / sign-in
  code): branded card, action button with plain-link fallback, spaced
  code digits; email-safe tables + inline styles.
- Per-template Preview buttons and a Send-test-email button on the Auth
  page (test proves the SMTP pipeline end to end).

## [1.4.19] - 2026-08-23

### Fixed
- The AI assistant declines off-topic questions instead of answering them.
- Sending during a streaming reply no longer merges two questions into
  one answer - the drawer serializes sends.
- The assistant's platform briefing corrected on instance-migration
  mechanics (same server, no DNS change) and the 5433/5434 ports.

## [1.4.18] - 2026-08-23

### Changed
- The WAL-archive cap compacts instead of trimming: past 75% of the cap a
  fresh basebackup is taken and the archive re-anchors to it - PITR stays
  continuous, the alert stops recurring. At most one auto-basebackup per
  12h, disk-guarded; trim-oldest remains only as the runaway fallback.

### Added
- Advisor rule for the delete-all-reinsert-all sync pattern, with the
  upsert-with-change-detection fix spelled out.

## [1.4.17] - 2026-08-23

### Changed
- Dual-domain model inverted to primary + legacy alias: the alias keeps
  old links and connection strings alive and redirects its panel to the
  primary; connection strings standardize on db.<primary>. install.sh
  gained an optional legacy-alias prompt and a disaster-recovery summary.

## [1.4.16] - 2026-08-23

### Added
- AI replies stream live and render as real formatting (headings, bold,
  bullets, inline code) instead of raw markdown markers.

### Fixed
- Pre-tracking panel sessions upgrade themselves on their next page load,
  so every signed-in device appears under Devices & sessions and can be
  revoked individually.
- Empty leftovers from earlier failed AI exchanges are dropped from the
  conversation history on both ends.

## [1.4.15] - 2026-08-23

### Added
- Encrypted disaster kit synced off-box nightly (stack secrets, certs,
  proxy config; AES-256). Rebuild procedure: docs/DISASTER-RECOVERY.md.
- Documentation caught up everywhere: README, in-panel Guide, project
  Docs pages, self-hosting runbook.

### Fixed
- WAL-cap alert named the wrong database (frozen stats snapshot); the
  sampler now uses two sessions.
- The updater cleans stray local edits in the deploy checkout, so the
  Update button cannot be wedged by an on-box hotfix.

## [1.4.14] - 2026-08-23

### Fixed
- AI replies were empty with extended-thinking models (Claude Opus etc.):
  the panel read only the first content block, which is the reasoning
  block on those models. All text blocks are collected now, and the
  server skips empty history entries so broken conversations self-heal.
- apply-infra.sh had a corrupted header since v1.4.1 that silently broke
  the startup reconciler; repaired, and the missed installs (warm edge
  runner, WAL-alert attribution) are now applied.

## [1.4.13] - 2026-08-23

### Added
- A floating AI assistant on every project page: multi-turn chat that
  knows the project's live schema and the whole platform - data questions
  and how-do-I questions alike. SQL replies carry Copy and
  Open-in-SQL-editor buttons. Same bring-your-own-key model.

## [1.4.12] - 2026-08-23

### Added
- Read replica: one-click hot standby of the whole cluster in its own
  container, read-only on port 5434 with the same credentials and TLS;
  live lag on the System page; disk-guarded setup, one-click removal.
- SAML 2.0 SSO per project via crewjam/saml (never hand-rolled XML-DSig):
  any IdP metadata URL or pasted XML; SP metadata + login URLs on the
  Auth page; assertions land as normal user tokens.
- The SQL editor's Ask AI became a visible inline panel with progress,
  clear errors, and a pointer to Account settings when no key is set.

### Fixed
- The WAL-cap alert names the top-writing database at alert time.
- New Advisor rule detects TOAST write churn (the WAL-filling pattern).

## [1.4.11] - 2026-08-23

### Added
- S3-compatible protocol access with scoped keys: rclone / AWS CLI / any
  S3 SDK against the project domain, path-style. Core object operations
  plus bucket listing; real SigV4 verification (passes the official AWS
  test vectors). Multipart and presigned URLs return an explicit 501 for
  now. Keys are minted and revoked on the Storage page; secrets shown once.

## [1.4.10] - 2026-08-23

### Added
- Time-travel branches: an optional past timestamp on the branch form
  rebuilds the branch from the WAL archive as the project was at that
  second; it then diffs/resets/expires like any branch. Non-destructive.
- Adaptive instance sizing: owner-set memory bounds; the platform grows
  the limit under sustained pressure and shrinks it when idle, hourly.

## [1.4.9] - 2026-08-23

### Added
- Warm process pool for edge functions: persistent per-function servers,
  no per-request boot, module state survives between calls; idle processes
  reaped after 5 minutes, redeploys/secret changes swap them automatically.
- Streaming responses (ReadableStream/SSE) pass chunks through live.
- WebSocket upgrades work through the functions endpoint.
- Post-response background work (setTimeout past the reply) now runs.

### Changed
- The one-process-per-request runner stays as an automatic fallback.

## [1.4.8] - 2026-08-23

### Added
- Per-subscriber RLS on realtime change streams (opt-in): INSERT/UPDATE
  events reach only subscribers whose token could SELECT the row under
  your policies; visibility checks are memoized per distinct token.
- Phone OTP sign-in via a bring-your-own SMS webhook: ForgeBase POSTs the
  HMAC-signed {phone, code, project} payload to your endpoint; verified
  codes create phone-only accounts with standard tokens.

## [1.4.7] - 2026-08-23

### Added
- Asymmetric JWT signing (opt-in, per project): user tokens become RS256
  with a kid header, verifiable by anyone at /.well-known/jwks.json.
  One-click key rotation keeps the previous public key valid. Anon and
  service keys stay HS256, and the REST API verifies both at once (tested
  against the production PostgREST build before shipping).

## [1.4.6] - 2026-08-23

### Added
- Path-level storage access rules: bucket prefixes scoped to public /
  authenticated / owner / private, longest prefix wins, enforced on reads
  and uploads alike. Bucket flags remain the fallback.
- Resumable uploads: a tus 1.0.0 endpoint (/storage/v1/tus/<bucket>)
  compatible with tus-js-client and Uppy; resumes across interruptions and
  daemon restarts, respects limits/quotas/rules, prunes abandoned uploads.

## [1.4.5] - 2026-08-23

### Added
- Per-project member scoping: members with a project list see only those
  projects (and their branches); everything else 404s. Owners see all.
- Panel session management: sign-ins are listed devices on the Account
  page; revoke one or all-but-current instantly. Server-side session rows
  mean revocation works even against a stolen cookie.
- Owners can sign any member out of every device from the Team page.

## [1.4.4] - 2026-08-23

### Added
- Usage page per project: 30-day database growth chart, storage vs quota,
  function calls with error rate and latency, webhook deliveries, auth
  signups/actives, live realtime connections.
- Foreign databases (postgres_fdw): connect an external Postgres, import
  schemas, query its tables locally; drop removes the imported tables.
- Before-create auth hook: auth.before_create(email) RETURNS text gates
  every signup from SQL.
- aal claim in access tokens (aal2 after a second factor) for step-up RLS.
- Per-instance compute controls: live memory/CPU limits for dedicated
  instances, preserved across sleep/wake.

## [1.4.3] - 2026-08-23

### Added
- Generic OpenID Connect provider: any issuer URL, endpoints discovered
  automatically via .well-known/openid-configuration.
- Identity linking: provider sign-ins are recorded in auth.identities; the
  same verified email across providers stays one account.
- Configurable auth rate limits (requests/min/IP, 0 = off).
- Single-session mode: a new sign-in revokes every other session.
- Leaked-password protection via the HIBP k-anonymity API (only 5 hash
  characters ever leave the server; fails open on outages).
- User impersonation: a one-hour access token as any app user, audited.
- AI settings: provider picker (Claude / OpenAI / custom) plus a live model
  dropdown loaded from the endpoint's own model list.

## [1.4.2] - 2026-08-23

### Added
- Secrets vault: encrypted-at-rest secrets readable from SQL, functions and
  cron via forgebase.secret_set / secret_get / secret_list / secret_delete,
  guarded by SECURITY DEFINER functions. Enable from Settings.
- End-user two-factor auth: TOTP enrollment with otpauth URI, verify-to-
  activate, ten single-use recovery codes, and MFA-gated password logins
  (/auth/v1/factors/enroll|verify|disable).
- Bot protection: optional Cloudflare Turnstile verification on signup/login.
- API Explorer: run REST requests as any role from the panel and inspect
  status, headers, body and the equivalent curl.
- AI SQL assistant (bring your own key): any Anthropic- or OpenAI-compatible
  endpoint; the SQL editor's Ask AI button writes queries against your live
  schema. Keys encrypted at rest.
- Image transformations (?width= / ?height= on storage URLs) with cached
  renditions, plus ETag/304 caching on all storage serving.
- Logical replication: create/drop publications from the Database page.
- Saved log views and daily log shipping to any HTTPS endpoint.
- Anonymized branches: table.column rules turn text into deterministic anon_
  tokens and null everything else.
- Per-database activity card on Monitoring (transactions, writes, cache hit,
  temp spill, backends, deadlocks).
- One-button migration from the shared cluster to a dedicated instance; the
  shared copy stays parked until deleted by hand.
- Eight more OAuth providers: Microsoft, Facebook, Twitch, Slack, Spotify,
  LinkedIn, Bitbucket, Notion.
- A POSIX CLI (scripts/forgebase) using personal API keys, now accepted as
  Bearer tokens by the panel API.

## [1.4.1] - 2026-08-23

### Fixed
- Pooled port 6543 now serves TLS (sslmode=require works; plaintext still
  accepted for backward compatibility).

## [1.4.0] - 2026-08-23

### Added
- Realtime channels: broadcast (client + SQL), presence, private channels.
- In-database message queues (send/read-with-lock/ack/archive + panel).
- Auth custom-claims SQL hook merged into tokens at mint time.
- Email template editor with {{link}}/{{code}} placeholders.
- Per-project data-plane IP allowlists.

## [1.3.39] - 2026-08-23

### Added
- Per-project storage quotas with a usage meter; enforced on every upload
  path.

## [1.3.38] - 2026-08-23

### Added
- Auth admin depth: user search, live session counts, anonymous markers,
  sign-out-everywhere per user.

## [1.3.37] - 2026-08-23

### Added
- Per-project auth policies: token lifetime, password minimum, redirect
  allowlist (applies to magic links and OAuth).

## [1.3.36] - 2026-08-23

### Added
- Per-function timeout (5-120s) and memory (64-256MB) configuration.

## [1.3.35] - 2026-08-23

### Added
- Email OTP sign-in (signInWithOtp/verifyOtp compatible): hashed codes,
  10-minute expiry, attempt caps, per-email send limits.

## [1.3.34] - 2026-08-23

### Added
- Signed upload URLs (path-bound, time-limited, keyless PUT; limits and S3
  sync enforced).

## [1.3.33] - 2026-08-23

### Added
- Branch reset-from-parent (shared + instance modes) and branch expiry
  (pause-not-delete, with notification).

## [1.3.32] - 2026-08-23

### Added
- Schema diff between a project and its branches (colored unified diff,
  structure only).

## [1.3.31] - 2026-08-23

### Added
- Anonymous sign-ins (opt-in): credential-less users with upgrade-to-permanent
  via PUT /user; is_anonymous claim in token metadata.

### Fixed
- 2FA authenticator label uses the account email.

## [1.3.30] - 2026-08-23

### Fixed
- WAL prune could anchor on the wrong same-date basebackup and keep a day of
  dead WAL; the cut point now comes from each backup's own manifest.

### Added
- Disk-safety defense in depth: 15-minute WAL hygiene cadence, hard 8 GB
  archive cap with alert, and a 12 GB ring-buffer panic ceiling inside the
  archiver itself.
- QR code for two-factor enrollment.
- Private/shared saved SQL snippets with rename.

## [1.3.29] - 2026-08-22

### Added
- Constraints tab (Objects): full listing with definitions, guided UNIQUE and
  CHECK creation, PK-protected drop.

## [1.3.28] - 2026-08-22

### Added
- Opt-in TOTP two-factor authentication for panel accounts (confirm-to-enable,
  lockout-aware, code required to disable).

## [1.3.27] - 2026-08-22

### Added
- Versioned migrations: atomic apply + in-database history + downloadable
  replay script.

## [1.3.26] - 2026-08-22

### Added
- Scheduled (cron) edge functions - invoked through the normal path each
  matching UTC minute; run history is the invocation log.

## [1.3.25] - 2026-08-22

### Added
- Edge functions: every invocation logged with status + duration; 24h
  per-function metrics (calls, errors, avg ms) in the sidebar.

## [1.3.24] - 2026-08-22

### Added
- OpenAPI spec view/download for the Data API (Postman/codegen importable).

## [1.3.23] - 2026-08-22

### Added
- Per-project statement and idle-session timeouts (role-level, instant for
  new connections; 0 = off).

## [1.3.22] - 2026-08-22

### Added
- Logs filters (time range, action, target search) and a slow-statements
  dashboard ranked by mean execution time (pg_stat_statements).

## [1.3.21] - 2026-08-22

### Added
- Per-project API settings: max-rows response cap and extra exposed schemas
  (auto-granted), applied by recycling the PostgREST sidecar lazily.

## [1.3.20] - 2026-08-22

### Added
- Webhook replay (stored payloads, one-click re-send through the normal
  delivery path) and a send-test-event button per webhook.

## [1.3.19] - 2026-08-22

### Added
- Storage list API endpoint (.list() compatible with standard JS clients):
  prefix, limit, offset, search; folder + file entries; bucket-visibility aware auth.

## [1.3.18] - 2026-08-22

### Added
- Storage explorer: folder navigation, bucket-wide search, move/rename,
  copy, and bulk delete - disk, metadata and off-box storage kept in sync.

## [1.3.17] - 2026-08-22

### Added
- Realtime publications: per-table insert/update/delete capture toggles
  (also govern webhooks); defaults stay everything-on.

## [1.3.16] - 2026-08-22

### Added
- Per-table code snippets (JS client, fetch, cURL, Python) on the Data API
  page, pre-filled with project URL and keys.

## [1.3.15] - 2026-08-22

### Added
- TypeScript type generation (view/download database.types.ts from the Data
  API page): Row/Insert/Update per table, views, enums, relationships.

## [1.3.14] - 2026-08-22

### Added
- Advisors page: live security review (RLS gaps, blanket write policies,
  SECURITY DEFINER, exposed credential columns) and performance review
  (missing PKs, unindexed FKs, unused/duplicate indexes, seq-scan-heavy
  tables, bloat), each finding with a concrete fix.

## [1.3.13] - 2026-08-22

### Added
- Cron Jobs page: schedule SQL per project (pg_cron), pause/resume/delete,
  run-once-now, and a 14-day run history.

### Fixed
- Table Editor grid rendered blank in owner view on 1.3.12 (inverted
  impersonation branch).

## [1.3.12] - 2026-08-22

### Added
- View-as-role grid: browse any table as anon/authenticated/service_role with
  RLS and grants applied (read-only, clearly bannered).
- RLS status badge with policy count in the Table Editor header, linking to
  the Policies page.

## [1.3.11] - 2026-08-22

### Added
- Policies page: RLS state per table, full policy details, enable/disable,
  FORCE mode.
- Custom policy builder (command, roles, USING/WITH CHECK, permissive or
  restrictive) with automatic matching grants; in-place policy editing.
- Column-level privileges with listing and revoke.
- Zoomable schema diagram (zoom, reset, fit-to-view default).

## [1.3.10] - 2026-08-22

### Added
- SQL editor tabs (persisted per project in the browser; double-click renames).
- Table definition card: generated CREATE TABLE SQL incl. keys and indexes.

### Fixed
- Schema diagram rendered collapsed and colorless; now full-size with theme
  colors.
- Objects page hides extension-owned functions/enums/indexes.

## [1.3.9] - 2026-08-22

### Added
- Auto-generated schema diagram (tables, primary keys, FK arrows; click
  through to the editor); one per schema.
- Create and drop (empty) schemas from the Table Editor.
- Enum types offered in the add-column and change-type pickers.

### Fixed
- Panel-created tables, schemas and enum types are owned by the project role,
  not the internal superuser.

## [1.3.8] - 2026-08-22

### Added
- Objects page: visual management of database functions, triggers, enum types
  and indexes, schema-aware.
- Functions: definitions with metadata, guided create, safe drop.
- Triggers: guided builder with trigger-function picker, enable/disable, drop.
- Enums: create, add value at a position, rename value, drop.
- Indexes: size + usage counts, guided creation, drop (primary keys protected).

## [1.3.7] - 2026-08-22

### Added
- Schema switcher: browse and edit any schema, not just public; all editor
  actions carry the selected schema.
- Views, materialized views and foreign tables listed with badges; read-only
  browsing with filters/sort/export and a collapsible SQL definition card.
- One-click Refresh for materialized views.
- New tables and CSV imports land in the selected schema.

### Fixed
- Dropping uses the correct statement per object kind.

## [1.3.6] - 2026-08-22

### Added
- Type-aware cell editing: boolean/enum dropdowns, date and timestamp pickers,
  numeric inputs, and a one-click set-to-NULL button on nullable cells.
- JSON editor dialog for json/jsonb cells with pretty-print and validation.
- Row side panel: view and edit every field of a row at full length with
  explicit null checkboxes and one transactional save.
- Foreign keys marked in the grid and column list; the row panel suggests
  live values from the referenced table.
- Column management: rename, change type (casting existing data, rolling back
  cleanly on failure), edit defaults, toggle not-null, per-column comments.
- Table rename and table comments.
- Type-aware insert form (enum/boolean dropdowns, date and number inputs).

## [1.3.5] - 2026-08-22

### Fixed
- Text inserted via the SQL editor's snippet buttons or schema sidebar clicks
  shows up immediately (the highlighter now repaints on programmatic inserts,
  not only on keystrokes).

## [1.3.4] - 2026-08-22

### Fixed
- The SQL editor's text stays visible unless the syntax highlighter has
  successfully painted; a highlighter failure can no longer make typing
  invisible.

## [1.3.3] - 2026-08-22

### Fixed
- Schema sidebar clicks and snippet buttons in the SQL Editor work again - a
  malformed script literal was preventing the editor's JavaScript from loading.
- SQL keyword highlighting matches word boundaries correctly again.

## [1.3.2] - 2026-08-22

### Added
- Stacked filters on any table: combine conditions (=, not equals,
  greater/less, contains, in-list, is null) as removable chips - values always
  travel as bound parameters.
- Click any column header to sort (again for descending, again to clear),
  with multiple sort columns combining.
- Bulk row selection with select-all and one-click transactional delete.
- Rows-per-page selector (25/100/500); filters, sorts, and page size are
  preserved while paging.
- Export any table as SQL INSERT statements, alongside CSV.
- Duplicate a table (structure with all indexes and constraints, optionally
  including data) in one click.

## [1.3.1] - 2026-08-22

### Added
- Syntax highlighting and schema-aware autocomplete in the SQL editor (Tab to
  accept, arrows to choose) - tables, columns, and keywords, generated from
  your live schema.
- Visual query plans: the new Explain button renders the plan as an indented
  tree with costs, row estimates, and filters - without executing writes.
- Persistent, team-visible query history (last 200 runs with status and
  timing) with one-click reload.
- Run only the selected text, choose a result row limit, export results as
  CSV, JSON, or Markdown, Ctrl+click any cell to copy it, and a one-click SQL
  formatter.
- A destructive-query guard: DROP/TRUNCATE, or DELETE/UPDATE without WHERE,
  now ask before running.

### Changed
- Documentation and interface copy cleanup.

## [1.3.0] - 2026-08-22

### Added
- Dedicated instances: choose "Dedicated instance" when creating a project and
  it runs as its OWN Postgres on copy-on-write storage - full isolation (its
  own engine, its own crash domain), with the project role as its superuser.
- INSTANT branching: for dedicated projects, a branch is a copy-on-write
  snapshot - it appears in about 2 seconds regardless of database size, never
  locks the parent, and shares unchanged storage with it (a branch of a 40MB
  database costs ~8MB).
- True scale-to-zero: an idle dedicated instance is STOPPED after 15 minutes -
  zero RAM, zero CPU. The next connection (app, API call, or panel visit)
  cold-starts it in about 1.3 seconds through the always-on proxy on port 5433.
- Everything else follows the mode automatically: connection strings, the Data
  API, Realtime, backups (dedicated instances are dumped with the same
  skip-unchanged smarts), Sleep/Wake buttons, and Pause (which locks the
  instance so connections can NOT wake it until you Resume).

### Changed
- Every dedicated instance uses password-only authentication (scram) from
  birth and runs the full ForgeBase image - pgvector and friends included.

## [1.2.21] - 2026-08-22

### Added
- S3-compatible object storage for uploaded files (System page): files become
  durable in your object storage bucket while local disk acts as a fast ~1.5GB
  cache. Existing files migrate automatically in the background; without a
  remote configured, storage stays local exactly as before.

### Security
- Login lockout now keys on the account itself, so alternating between an
  account's email and username no longer doubles the attempts an attacker
  gets - and the emergency admin login can no longer be locked out by junk
  attempts against it.
- Waking a project can no longer silently unlock a deliberately paused one.

### Fixed
- CRITICAL: with numbered project names (like app and app-2), the backup
  pruner's pattern could match the sibling's backups and delete them, and the
  skip-unchanged check could trust the sibling's dump. Both patterns are now
  date-anchored and cannot cross projects.
- Skip-unchanged backups actually skip now: modern pg_dump embeds a randomized
  security token in every schema dump, which made the change-detection hash
  different on every run. The token is excluded from hashing.
- Auto-install could silently never run: the update check's timing could
  permanently miss the 03:00-05:00 installation window. Checks are hourly now.
- A fresh install's default backup age ceiling would have deleted the weekly
  backup tier; the ceiling now always reaches past it.
- Realtime could briefly strand a subscriber that connected exactly as the
  idle reaper closed the listener; one unreachable project database could also
  stall realtime for every project. Both races fixed.
- One project's busy or hung edge functions can no longer consume the whole
  platform's function capacity - each project is capped individually inside
  the global limit.
- Assorted hardening: concurrent update launches, partial cluster snapshots
  counting toward retention, infra apply failures being masked, unbounded
  lockout bookkeeping, settings caches on transient errors, and
  hostname-validation gaps in the new domain settings.

## [1.2.20] - 2026-08-22

### Added
- Off-box archive browser: the Backups page can list your project's dumps
  stored in off-box storage (where older backups live after local pruning) and
  restore any of them into a NEW project - the original is never touched. Runs
  in the background; the restored project appears as soon as it is ready, and
  Discord is pinged on completion or failure.

### Fixed
- When an update was detected seconds after release, the "What's new" notes
  could be missing (GitHub serves the release notes file a couple of minutes
  later than the release itself). The panel now refetches the notes
  automatically and, if they are genuinely still syncing, says so instead of
  showing nothing.

## [1.2.19] - 2026-08-22

### Added
- Watchdogs: an hourly check alerts if the database's write-ahead log is
  growing abnormally (the early symptom of the class of problem that once
  filled the disk) or if the disk passes 85%. You get a red banner on the
  System page AND a Discord ping - once per episode, with an all-clear when it
  recovers.
- Incident notes: post "Investigating X..." to your public status page from
  the System page while you work on something; resolving moves it into a
  visible history. Discord is pinged on open and resolve.
- Weekly Discord digest (Sundays ~10:00 IST): uptime, RAM and disk, project
  counts (sleeping/pinned), backup sizes per tier, and updates installed that
  week.
- Self-updates now ping Discord on success and, importantly, on an automatic
  rollback. Failed nightly backups alert too.

### Fixed
- Instance-mode setup can no longer create a storage image larger than the
  disk (it sizes to 40% of free space and refuses oversize requests).

## [1.2.18] - 2026-08-22

### Added
- Secondary domain support: add a domain on the System page and the panel,
  every project's API (project.yourdomain), and the status page all serve on
  it - HTTPS certificates issue automatically on first visit. The original
  domain keeps working forever, so nothing connected to it can break.
- Connection strings on the new domain use db.<domain> for Postgres,
  deliberately separate from the web hostnames - that split lets you put the
  web side behind a proxy/CDN later while database traffic stays direct.
- Optional redirect: once you trust the new domain, one checkbox makes
  browsers visiting the old panel land on the new one (APIs and database
  connections are never redirected).
- Project cards show the new-domain connection strings with the legacy ones
  one click away.

## [1.2.17] - 2026-08-22

### Changed
- The audit log and edge-function logs are now bounded (90 days / 20,000
  entries and 30 days / 200 per project respectively). Previously both grew
  forever - a crash-looping function could write unlimited log rows.
- Deleting a project now also moves its backup dumps to a trash area (kept 7
  days, then purged) and a one-time sweep does the same for dumps of projects
  deleted in the past. Backups of deleted projects previously lingered
  invisibly.
- Edge-function dependency downloads now cache in a managed location that is
  pruned automatically past 500MB.

## [1.2.16] - 2026-08-22

### Changed
- Postgres memory settings now auto-tune to the server's RAM (a 4GB box runs
  512MB shared buffers instead of a hardcoded 768MB, freeing about 250MB),
  autovacuum is calmed for many-database hosts, and the database container gets
  a memory backstop.
- The connection pooler now caps per-project server connections (20) with
  smaller per-user pools, so many busy projects queue at the pooler instead of
  exhausting the whole cluster.
- Idle direct database connections now close after 30 minutes (per project
  role); application connection pools reconnect transparently. Frees the RAM
  that permanently-parked connections were pinning.
- Nightly backups moved from 09:00 to 03:00 IST - out of Indian daytime hours.
- All containers now rotate their logs (10MB x 3) - previously unbounded.

### Fixed
- The WAL archiver can no longer wedge permanently after a crash-retry:
  re-archiving an identical, already-archived segment now succeeds instead of
  failing forever (which could fill the disk). Archive logic now lives in a
  host-managed script, fixable without touching the database container.

## [1.2.15] - 2026-08-22

### Added
- Edge Functions are now resource-capped: each invocation gets a 128MB memory
  limit and at most 4 run concurrently (a 5th waits briefly, then gets a clean
  429 retry signal). Previously a single hungry or looping function could
  exhaust the whole server's RAM.
- The public status page title is customizable (System page) - put your
  company's name in front of your clients.
- The control plane itself now runs under a memory ceiling, so even a
  pathological case restarts one service in seconds instead of freezing the
  host.

### Changed
- New projects get a direct-connection limit of 10 (was 20) - the pooled port
  multiplexes far beyond it, and this doubles how many projects fit per server.
  Existing projects keep their current limit; the panel now accepts 1-100.
- Creating a project now warns when the combined connection limits approach the
  cluster's capacity.

## [1.2.14] - 2026-08-22

### Added
- Skip-unchanged backups: a database whose data and schema have not changed
  since its last dump is no longer re-dumped every night (with a weekly forced
  full as a safety valve). Sleeping and idle projects now cost essentially
  nothing in backup storage.
- Backup retention panel: daily dumps kept, weekly dumps kept, and standing
  snapshots are now editable on the Backups page, which also shows how much
  disk each backup tier uses.

### Fixed
- Update detection is now real-time: the version check reads GitHub's live tag
  list, so a release published seconds ago is detected immediately (the
  changelog file GitHub serves can lag a few minutes).
- A manual "Check for updates" now also refreshes the sidebar update dot.
- The sleeping badge and Sleep button now use a proper moon icon instead of an
  emoji, matching the rest of the interface.

## [1.2.13] - 2026-08-22

### Added
- Public status page at status.<your-domain>: overall platform health, 30-day
  uptime bars built from the platform's own heartbeat, and live per-service
  health - auto-refreshing, no login needed.
- Privacy first: projects appear on the status page only after you opt them in
  from their Settings page. Nothing is exposed by default.
- Custom status domain: serve the same page at your own hostname (e.g.
  status.mycompany.com) - set it on the System page, point DNS, HTTPS is
  automatic.

## [1.2.12] - 2026-08-22

### Added
- Update awareness: ForgeBase checks for new releases in the background and
  shows a pulsing dot on System in the sidebar the moment one exists - no more
  clicking "Check for updates". Discord gets one ping per new version too.
- Optional auto-install: a checkbox on the System page installs new releases
  automatically between 03:00-05:00 UTC, with the usual health-check and
  rollback. Off by default - updates wait for your click.
- Sleeping projects are visible: a moon badge on the dashboard, plus "Sleep
  now" and "Wake" buttons to park or rouse a project instantly.
- Monitoring charts now offer 24-hour, 7-day, and 30-day views.
- Creating a project with a taken name now just works: "profitzon" taken
  becomes "profitzon-2" automatically.

### Changed
- The point-in-time recovery picker got a proper design: styled date-time
  input, one-click presets (5 min ago, 1 hour ago, yesterday), and it now
  shows the actual restorable window instead of an outdated "7 days" claim.
- The Branches page now states plainly that a branch is currently a full copy
  (2x storage) - and that instant copy-on-write branching is in active
  development.

## [1.2.11] - 2026-08-22

### Security
- Panel login now has a per-account lockout: 5 failed attempts on an account
  lock it for 15 minutes, even with the correct password afterward. This is the
  defense per-IP limits cannot provide against botnets that try one password
  per IP.
- Every failed login now costs the attacker 1 second, rising to 4 seconds
  platform-wide while a distributed attack is underway.
- The fail2ban firewall jail for the panel is now part of the product (tighter:
  3 attempts = 1 hour IP ban, escalating for repeat offenders up to a week) and
  installs automatically wherever fail2ban is present.

### Added
- Discord alerts: paste a webhook URL on the System page and ForgeBase pings
  your Discord when something needs attention - starting with login
  brute-force waves (more alert types coming).
- "Remember me for 7 days" checkbox at login; sessions stay 12 hours without it.
- "Keep always awake" projects are now also kept WARM: their API process and
  realtime listener are never stopped for idleness, so pinned projects (your
  production apps) never pay a cold start.

## [1.2.10] - 2026-08-22

### Changed
- Idle projects now go to sleep instead of being suspended. Sleep releases
  everything that costs resources - the API process, the realtime listener,
  cached connections - but NEVER blocks logins and never touches data. A
  sleeping project wakes automatically on the next API call, panel visit, or
  direct database connection (direct connections are accepted instantly; the
  project is marked awake within minutes). Manual Pause remains the explicit
  hard lockout.
- The idle window is now 7 days (down from 14) and configurable on the project
  Settings page - sleep after N hours idle, or 0 to never sleep.

### Added
- "Keep always awake": pin any project (for example a production app) so it is
  never auto-slept, from its Settings page.

### Fixed
- The per-request activity tracker costs one database round-trip instead of two.

## [1.2.9] - 2026-08-22

### Changed
- Realtime and webhook listeners are now released when idle: a project whose
  realtime stream has had no subscribers for 15 minutes (and has no webhooks)
  gives back its dedicated database connection, and it comes back automatically
  on the next subscriber. Previously every project that ever used Realtime or
  webhooks held a database connection forever.
- Removing a project's last webhook, or disabling Realtime, now also releases
  the listener immediately when nothing else needs it.

### Fixed
- Idle-project detection now only counts real client connections. The
  platform's own internal connections previously counted as activity, which
  could keep an unused project marked active forever.

## [1.2.8] - 2026-08-22

### Changed
- Backup retention is now tiered: the newest 7 daily dumps per database plus
  one weekly dump for 4 weeks are kept, instead of 30 full nightly dumps.
  Cluster snapshots for point-in-time recovery are trimmed from 7 standing
  copies to 2 (older restores use the daily and weekly dumps). NOTE: the first
  nightly backup after this update deletes the now-out-of-policy older backups,
  typically freeing many GB, and the off-box mirror follows the same policy.
  Copy any dump you want to keep forever somewhere else before the next
  nightly run.
- Retention is enforced BEFORE the nightly backup work as well as after, so a
  failed dump or a full disk can never skip cleanup again.

### Fixed
- A monthly restore-drill failure could leave a full-size test database in the
  cluster forever (which then got backed up nightly at full size). It is now
  always cleaned up, is excluded from backups by name, and the drill skips
  safely when disk space is low.
- An empty retention settings file could silently abort the whole nightly
  backup before any cleanup ran.
- Point-in-time-recovery working files and deleted-project dump remnants are
  now cleaned up automatically.

## [1.2.7] - 2026-08-22

### Security
- Data API startup now cryptographically verifies it is talking to the right
  project's API process before routing any request to it. Previously, in a rare
  port-reuse scenario on a long-running server, requests could have been routed
  to a different project's API.

### Fixed
- "Check for updates" now always sees a just-published release immediately
  instead of waiting up to 5 minutes for GitHub's cache.
- Fixed a data race in the Data API idle tracker.
- Metrics collection is much lighter: one query instead of three per project,
  sleeping projects are sampled hourly instead of every 5 minutes, and history
  cleanup runs hourly on an index instead of scanning the whole table every
  5 minutes.

## [1.2.6] - 2026-08-22

### Added
- Self-updates now also refresh the server-side components (backup scripts and
  scheduled maintenance jobs), not just the app binary. Fixes to those land
  automatically with every update instead of requiring shell access.

### Fixed
- Database write-ahead-log safety settings are applied automatically on every
  start: the live WAL is bounded to 1GB and compressed. This prevents the class
  of incident where heavy activity (a large clone or import) could fill the
  disk and take the database down.
- The hourly backup-archive cleanup job is now installed correctly on fresh
  installs; previously it was shipped but never activated.
- The updater now cleans up its build caches and temporary files, which
  previously grew without limit with every update.

## [1.2.5] - 2026-08-21

### Fixed
- A self-update that wedged or never launched could leave the System page pinned
  on "updating..." forever and block all future updates as "already in progress".
  The in-progress state now also requires the update log to be recent, so a stale
  update is treated as finished and never locks updates out.
- During a self-update the control plane restarts for 1-3 seconds; a request that
  arrived in that window could return a 502. The HTTPS proxy now briefly retries
  the control plane instead, so the restart no longer surfaces an error page.

## [1.2.4] - 2026-08-21

### Fixed
- Clicking "Update now" now clearly shows that an update is in progress: the
  button turns into a spinner immediately, and the System page switches to an
  "updating..." state with a live update log that refreshes on its own until the
  update finishes.
- You can no longer start a second update while one is already running. The
  button is hidden during an update, and a duplicate request (for example from
  another tab) is rejected instead of launching a concurrent updater.

## [1.2.3] - 2026-08-18

### Fixed
- Pages with a running background operation now update on their own instead of
  sitting on a stale status until you manually reload. The Sync/Clone page, the
  Projects dashboard (while a project is cloning), and the System page during a
  self-update refresh automatically until the operation finishes. (The
  operations always completed - only the page was stale.)

## [1.2.2] - 2026-08-18

### Added
- Invite end users by email (needs SMTP): `POST /auth/v1/admin/invite` with the
  `service_role` key creates the account and emails a one-time sign-in link.

## [1.2.1] - 2026-08-18

### Added
- Self-service password reset (needs SMTP): `POST /auth/v1/recover` emails a
  reset link that opens a set-a-new-password page and signs out other sessions.
- Magic-link, passwordless sign-in (needs SMTP): `POST /auth/v1/magiclink` emails
  a one-time sign-in link.

## [1.2.0] - 2026-08-18

### Added
- Optional SMTP email. Configure an SMTP server per project on the Auth page to
  send transactional emails; it stays off (and nothing changes) until you set it.
- Email confirmation. When SMTP is set you can require confirmation before
  sign-in: new sign-ups receive a confirmation link and cannot log in until they
  click it.

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
  feature-by-feature capability review.

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

First public release. ForgeBase is a lightweight, self-hosted Postgres backend
platform that runs as a single Go binary against a shared Postgres cluster.

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

[Unreleased]: https://github.com/FutureForge-Studios/forgebase/compare/v1.4.1...HEAD
[1.4.1]: https://github.com/FutureForge-Studios/forgebase/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.39...v1.4.0
[1.3.39]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.38...v1.3.39
[1.3.38]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.37...v1.3.38
[1.3.37]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.36...v1.3.37
[1.3.36]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.35...v1.3.36
[1.3.35]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.34...v1.3.35
[1.3.34]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.33...v1.3.34
[1.3.33]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.32...v1.3.33
[1.3.32]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.31...v1.3.32
[1.3.31]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.30...v1.3.31
[1.3.30]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.29...v1.3.30
[1.3.29]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.28...v1.3.29
[1.3.28]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.27...v1.3.28
[1.3.27]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.26...v1.3.27
[1.3.26]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.25...v1.3.26
[1.3.25]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.24...v1.3.25
[1.3.24]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.23...v1.3.24
[1.3.23]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.22...v1.3.23
[1.3.22]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.21...v1.3.22
[1.3.21]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.20...v1.3.21
[1.3.20]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.19...v1.3.20
[1.3.19]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.18...v1.3.19
[1.3.18]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.17...v1.3.18
[1.3.17]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.16...v1.3.17
[1.3.16]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.15...v1.3.16
[1.3.15]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.14...v1.3.15
[1.3.14]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.13...v1.3.14
[1.3.13]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.12...v1.3.13
[1.3.12]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.11...v1.3.12
[1.3.11]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.10...v1.3.11
[1.3.10]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.9...v1.3.10
[1.3.9]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.8...v1.3.9
[1.3.8]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.7...v1.3.8
[1.3.7]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.6...v1.3.7
[1.3.6]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.5...v1.3.6
[1.3.5]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.4...v1.3.5
[1.3.4]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.3...v1.3.4
[1.3.3]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.2...v1.3.3
[1.3.2]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.1...v1.3.2
[1.3.1]: https://github.com/FutureForge-Studios/forgebase/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.21...v1.3.0
[1.2.21]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.20...v1.2.21
[1.2.20]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.19...v1.2.20
[1.2.19]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.18...v1.2.19
[1.2.18]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.17...v1.2.18
[1.2.17]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.16...v1.2.17
[1.2.16]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.15...v1.2.16
[1.2.15]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.14...v1.2.15
[1.2.14]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.13...v1.2.14
[1.2.13]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.12...v1.2.13
[1.2.12]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.11...v1.2.12
[1.2.11]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.10...v1.2.11
[1.2.10]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.9...v1.2.10
[1.2.9]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.8...v1.2.9
[1.2.8]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.7...v1.2.8
[1.2.7]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.6...v1.2.7
[1.2.6]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.5...v1.2.6
[1.2.5]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.4...v1.2.5
[1.2.4]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.3...v1.2.4
[1.2.3]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.2...v1.2.3
[1.2.2]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/FutureForge-Studios/forgebase/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/FutureForge-Studios/forgebase/compare/v1.1.9...v1.2.0
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
