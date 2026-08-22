# ForgeBase Roadmap - beat them in every function

**Mission (owner's words): "we should be better than them in every single function
they have. we can't just make a simple function while they have 100 things inside
of it."**

The exhaustive sub-feature scoreboard lives in [PARITY.md](PARITY.md) - every
capability Supabase and Neon ship, marked HAVE / PARTIAL / MISSING against this
codebase (compiled 2026-08-22 by a 7-agent documentation crawl + code audit).

**Standing at v1.3.0: 74 HAVE · 74 PARTIAL · 490 MISSING** (sub-feature count,
both platforms combined). What we already do BETTER: one-binary install,
one-click self-update, Neon-style sleep on a shared cluster, instant CoW
branching AND scale-to-zero on one box, clone-from-any-Postgres + live sync,
skip-unchanged tiered backups, built-in status page + incidents, Discord ops
alerts, dual-domain serving. The list below closes everything else, in order.

Rules: ship in small versioned releases (changelog every time); each shipped
item flips its PARITY.md line to HAVE in the same commit; every function we
touch must land DEEPER than theirs, not checkbox-equal.

---

## Phase 1 - Daily-driver depth: Table Editor + SQL Editor + schema UIs
*(owner-chosen first UX deep-dive; densest Studio gap: 46 missing sub-features)*

- [ ] 1.1 Table Editor Pro, part 1 - grid: type-aware cell editors (date/time
      pickers, bool dropdown, explicit NULL toggle), JSON viewer/editor modal,
      row side-panel editor, bulk select + delete, stacked filter builder
      (=, <>, >, <, like, ilike, in, is), multi-column sort, estimated-vs-exact
      row counts, column resize/reorder persistence. **M**
- [ ] 1.2 Table Editor Pro, part 2 - schema: full column options (nullable,
      unique, default incl. expressions, check constraints), identity/serial
      picker, composite PKs, array toggle, enum picker, visual FK selector with
      ON DELETE/UPDATE actions, FK-aware cell row-picker, table comments,
      per-table RLS toggle + warning banner, duplicate table, export-as-SQL,
      table definition (CREATE TABLE) tab. **L**
- [ ] 1.3 Schema browsing: schema switcher + create schema, views +
      materialized views (grid + definition), foreign tables listing. **M**
- [ ] 1.4 SQL Editor Pro: CodeMirror with syntax highlighting + schema-aware
      autocomplete, tabs, run-selection, query history, result export
      (CSV/JSON/MD) + copy, row-limit selector, destructive-query guard,
      SQL formatter, EXPLAIN/ANALYZE visual plan, run-as-role (anon/
      authenticated/service or a user JWT) - the RLS test-bench Supabase has.
      Private vs shared snippets with folders + favorites. **L**
- [ ] 1.5 Schema object UIs: database functions editor (args, returns,
      language, SECURITY DEFINER/INVOKER), triggers UI (event/timing/level/
      function, enable-disable), enum types manager, indexes UI (column picker,
      btree/gin/gist/brin/hash) with usage stats. **L**
- [ ] 1.6 RLS to full depth: complete policy editor (name, command, roles,
      USING + WITH CHECK), policies overview page grouped by table, template
      gallery (grow 4 -> 15+ real patterns), column-level privileges UI. **M-L**
- [ ] 1.7 Schema Visualizer: auto-ERD of tables + relationships (we render
      server-side SVG - no heavy JS libs). **M**

## Phase 2 - Edge Functions + Team (owner: edge is in production, team of 3+ coming)

- [ ] 2.1 Cron / scheduled functions: schedule column + panel UI, ticker
      driven, overlap protection, run history. **M**
- [ ] 2.2 Invocation logs + metrics: every invoke logged (status, duration,
      cold/warm), per-function charts, live tail in panel. **M**
- [ ] 2.3 Per-function config: timeout, memory, verify_jwt already; add
      per-function env overrides. **S**
- [ ] 2.4 Warm-isolate pool: keep N Deno processes hot per busy function -
      kill the per-request cold start. **L**
- [ ] 2.5 Streaming responses + background tasks (respond-then-continue),
      WebSocket support in functions. **L**
- [ ] 2.6 Queues: pgmq extension + panel UI (create queue, send, peek, DLQ)
      + function consumers. **M-L**
- [ ] 2.7 Team depth: per-project role scoping (member sees only assigned
      projects), granular permissions matrix, panel session list + revoke,
      audit filter by member. **M-L**

## Phase 3 - Auth: from good to their 100-things deep

- [ ] 3.1 Quick wins: anonymous sign-ins (+ convert-to-permanent), email OTP
      codes, configurable access/refresh TTLs per project, redirect-URL
      allowlist with wildcards, resend-with-cooldown endpoints. **M**
- [ ] 3.2 Password depth: min-length/character-class policy, leaked-password
      protection (HaveIBeenPwned k-anonymity), password change requiring
      current password/recent auth. **S-M**
- [ ] 3.3 Session management: per-user session list + revoke (API + UI),
      single-session-per-user option, inactivity timeout, session timebox. **M**
- [ ] 3.4 Bot protection: CAPTCHA (Turnstile + hCaptcha) on signup/signin/
      recover; per-endpoint auth rate-limit config in panel. **M**
- [ ] 3.5 Auth hooks: custom-access-token claims hook, before-user-created,
      password-verification, send-email/send-sms overrides - as HTTP hooks to
      user endpoints or per-project edge functions. **M-L**
- [ ] 3.6 OAuth to 12+ providers: add Apple, Microsoft/Azure, Facebook,
      LinkedIn, Twitter/X, Slack, Spotify, Notion, Figma (+ generic OIDC
      connector = infinite providers). Identity linking/unlinking + multiple
      identities per user. **L**
- [ ] 3.7 MFA: TOTP enroll/challenge/verify, recovery codes, AAL1/AAL2 claims,
      per-project enforcement policy - AND panel-login 2FA (owner-bundled). **L**
- [ ] 3.8 JWT depth: asymmetric signing (RS256/ES256) + JWKS endpoint, signing
      key rotation without logout, key revocation. **M-L**
- [ ] 3.9 Email template editor per project (variables, preview, test-send). **M**
- [ ] 3.10 Enterprise (needs providers/scale): SAML 2.0 SSO, phone SMS/WhatsApp
      OTP via Twilio/MessageBird/Vonage adapters. **XL**
- [ ] 3.11 Admin depth: user impersonation (mint a user JWT from panel),
      paginated user search/filters, per-user identities + sessions view,
      soft-delete. **M**

## Phase 4 - Storage: every operation they have, then better

- [ ] 4.1 Object ops: move, copy, bulk delete, list with pagination + search +
      sortBy, update/upsert flag, custom object metadata. **M**
- [ ] 4.2 Signed UPLOAD URLs (client uploads without keys) + download-name
      option + expiring public URLs. **S-M**
- [ ] 4.3 Access control depth: path-prefix policies from JWT sub (own-folder
      pattern), then full per-operation policy rules on objects (SELECT/
      INSERT/UPDATE/DELETE per role/path). **L**
- [ ] 4.4 Image transformations: width/height/resize modes/quality/format +
      auto-WebP, on public + signed URLs, cached renditions (libvips
      sidecar). **L**
- [ ] 4.5 Resumable uploads: TUS protocol endpoint (mobile/flaky-network
      uploads). **L**
- [ ] 4.6 Cache-Control per object/bucket + ETag/conditional GET (we already
      have the S3 source of truth - serve smart). **S-M**
- [ ] 4.7 Storage explorer UI: folder tree, drag-drop multi-upload, previews
      (image/video/pdf), rename/move in UI, per-bucket usage bars. **M**
- [ ] 4.8 Per-project storage quotas (owner: 1GB default new projects) with
      usage meter + 413 handling. **S-M**
- [ ] 4.9 Client-facing S3-compatible protocol (scoped access keys) so any S3
      SDK/tool talks straight to ForgeBase storage. **L**

## Phase 5 - Realtime: the other two thirds

- [ ] 5.1 Broadcast: client pub/sub channels (self/ack options) + FROM
      DATABASE via SQL function (realtime.send equivalent) - the low-latency
      recommended path Supabase pushes. **M-L**
- [ ] 5.2 Presence: track/sync/leave with automatic cleanup. **M**
- [ ] 5.3 Private channels: per-channel authorization policies (who may
      subscribe/publish), token-refreshed subscriptions. **M**
- [ ] 5.4 Per-subscriber RLS on postgres_changes: re-check each row against
      the subscriber's policies before delivery - closes the last data-exposure
      gap and beats their WALRUS on simplicity. **L**
- [ ] 5.5 Realtime settings UI: per-project rates/payload caps/channel limits
      + live connection counts. **S-M**

## Phase 6 - Neon-class database platform

- [ ] 6.1 Branch governance: reset-from-parent, protected branches, branch
      TTL/expiry, default-branch marker, branch list with created-from info. **M**
- [ ] 6.2 Schema diff viewer between branches (we have pg_dump -s + our
      deterministic hash - render a real diff). **M**
- [ ] 6.3 Branch-from-time: create an instance branch from any PITR timestamp
      (unify our WAL replay with instant branching = Neon's restore). **L**
- [ ] 6.4 Time-travel reads: ephemeral instance at a timestamp for a
      read-only query session (uses 6.3 machinery). **M** *(after 6.3)*
- [ ] 6.5 Migration action: move a shared-cluster project into a dedicated
      instance (dump -> create -> restore -> flip mode -> retire old after N
      days) + the reverse. **M**
- [ ] 6.6 Instance compute controls: per-instance memory/CPU presets (the
      honest single-box version of Neon CU sizing) + per-instance connection
      pooling (pgbouncer sidecar or shared bouncer per instance). **M-L**
- [ ] 6.7 Per-project CPU/RAM attribution (docker stats for instances; shared
      cluster gets pg_stat-based estimates) on Monitoring. **M**
- [ ] 6.8 Read replica: streaming standby + read-only connection string
      (shared cluster first, per-instance later). **XL**
- [ ] 6.9 Anonymized branches: branch + scrub PII by column rules - their
      preview feature, our differentiator done right. **L**
- [ ] 6.10 Logical replication OUT (publications UI) - we already do IN via
      live-sync clone. **M**
- [ ] 6.11 Autoscaling-lite: raise/lower an instance's shared_buffers +
      conn cap from load signals within owner-set bounds. **L**

## Phase 7 - Platform + DX (the long tail that makes it feel first-class)

- [ ] 7.1 TypeScript type generation from schema (their `gen types` - we
      serve it from the panel + curl endpoint). **M**
- [ ] 7.2 OpenAPI surface + bundled Swagger-style explorer per project. **S-M**
- [ ] 7.3 Multi-language snippets on every table (supabase-js compatible,
      Python, curl) generated from live schema. **M**
- [ ] 7.4 CLI (`forgebase` binary): login, projects list/create, db dump/
      restore, branch, deploy function, gen types, logs tail. **L**
- [ ] 7.5 Migrations flow: versioned SQL migrations table + panel apply/
      history + CLI push/pull + per-branch application. **L**
- [ ] 7.6 Logs explorer: queryable Postgres/API/auth/function logs with time
      ranges + saved log queries; log drains (ship to owner's endpoint). **L**
- [ ] 7.7 Advisors: index advisor (hypopg), security advisor (RLS-off tables,
      exposed columns, weak policies), performance advisor (pg_stat_statements
      top queries UI - slow query dashboard). **M-L**
- [ ] 7.8 AI assistant (bring-your-own Claude API key setting): NL->SQL with
      schema context, error explain/fix, policy generation - clearly scoped,
      off by default. **L**
- [ ] 7.9 Vault: encrypted secrets store usable from SQL + functions. **M**
- [ ] 7.10 pg_cron UI (jobs list, schedule editor, run history) - extension
      already installed. **S-M**
- [ ] 7.11 Webhooks: manual replay from delivery log + >8KB payload re-read. **S-M**
- [ ] 7.12 Network security: per-project IP allowlists (db + API), SSL
      enforcement toggle, connection-security page. **M**
- [ ] 7.13 Database settings UI: per-project GUC overrides (statement_timeout,
      work_mem caps), timezone, search_path, exposed schemas + max-rows for
      REST. **M**
- [ ] 7.14 Reports page: request volumes, error rates, auth activity, storage
      egress - weekly digest's big brother. **M**
- [ ] 7.15 Wrappers/FDW UI for external sources (Stripe/S3/BigQuery/...) -
      biggest XL, last. **XL**

---

## How we run it
1. Top-down inside a phase; phases can interleave when the owner asks.
2. Every item ships as a versioned release with a human changelog; build +
   race-check on the box before tagging; owner installs via the Update button.
3. On ship: check the box here AND flip the matching PARITY.md lines to HAVE
   in the same commit.
4. Every item must exceed the reference implementation in at least one way
   (depth, speed, simplicity, or honesty of its docs).
