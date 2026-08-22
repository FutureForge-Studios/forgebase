# ForgeBase Roadmap

What we are building next, in order. Every item ships as a versioned release
with a human-readable changelog, and each feature is built to full depth - the
small affordances, the edge cases, and the settings included, not just the
happy path.

## Phase 1 - Table Editor + SQL Editor depth
- [x] Table Editor Pro: type-aware cell editors, JSON viewer/editor, row side
      panel, bulk select + delete, stacked filters, multi-column sort,
      estimated row counts, saved grid layouts.
- [x] Schema editing depth: full column options (defaults, checks, identity),
      composite keys, arrays, enums, visual foreign-key management, table
      comments, duplicate table, export as SQL, CREATE TABLE view.
- [x] Schema browsing: multiple schemas, views + materialized views, foreign
      tables.
- [x] SQL Editor Pro: syntax highlighting, schema-aware autocomplete, run
      selection, persistent history, result export (CSV/JSON/Markdown),
      row-limit control, destructive-query guard, SQL formatter, visual
      EXPLAIN plans, run-as-role for policy testing.
- [x] Object UIs: database functions, triggers, enum types, and indexes -
      created and managed visually.
- [x] Row-level security depth: full policy editor, per-table policy overview,
      a larger template gallery, column-level privileges.
- [x] Auto-generated schema diagram (ERD).

## Phase 2 - Edge Functions + Team
- [x] Scheduled (cron) functions with run history.
- [x] Full invocation logs and per-function metrics.
- [x] Per-function timeout/memory/env configuration.
- [ ] Warm process pool - no cold starts for busy functions.
- [ ] Streaming responses, background tasks, WebSocket support.
- [ ] Message queues with a panel UI and function consumers.
- [ ] Team depth: per-project member scoping, granular permissions, panel
      session management.

## Phase 3 - Auth depth
- [x] Anonymous sign-ins with upgrade-to-permanent.
- [x] Email OTP codes; configurable token lifetimes; redirect allowlists.
- [x] Password policies + leaked-password protection. (minimum length shipped; leaked-password screening tracked separately)
- [ ] Per-user session list/revoke, single-session mode, inactivity timeouts.
- [ ] CAPTCHA on auth endpoints; configurable rate limits.
- [ ] Auth hooks (custom claims, before-create, send overrides).
- [ ] 12+ OAuth providers + a generic OIDC connector; identity linking.
- [ ] TOTP MFA with recovery codes and assurance levels; panel-login 2FA.
- [ ] Asymmetric JWT signing with JWKS and key rotation.
- [ ] Per-project email template editor.
- [ ] SAML SSO and phone OTP (provider adapters).
- [ ] Admin depth: impersonation, rich search, per-user sessions/identities.

## Phase 4 - Storage depth
- [x] Move, copy, bulk delete, paginated + searchable listing, upsert,
      custom object metadata.
- [x] Signed upload URLs and richer download options.
- [ ] Path-level and rule-based access policies.
- [ ] Image transformations with cached renditions.
- [ ] Resumable (TUS) uploads.
- [ ] Smart caching (Cache-Control, ETags, conditional requests).
- [ ] A real storage explorer UI: folders, drag-drop, previews.
- [ ] Per-project storage quotas with usage meters.
- [ ] S3-compatible protocol access with scoped keys.

## Phase 5 - Realtime depth
- [ ] Broadcast channels (client pub/sub + send-from-SQL).
- [ ] Presence tracking.
- [ ] Private channels with authorization rules.
- [ ] Per-subscriber row-level security on change streams.
- [x] Per-project realtime settings and live connection stats.

## Phase 6 - Database platform depth
- [x] Branch governance: reset from parent, protected branches, branch
      expiry, richer branch metadata.
- [x] Schema diff between branches.
- [ ] Branch from any point in time; time-travel read sessions.
- [ ] One-click migration between shared-cluster and dedicated-instance modes.
- [ ] Per-instance compute controls and pooled connections.
- [ ] Per-project CPU/RAM attribution in Monitoring.
- [ ] Read replicas with a read-only connection string.
- [ ] Anonymized branches (PII scrubbing rules).
- [ ] Logical replication publications UI.
- [ ] Adaptive instance sizing within owner-set bounds.

## Phase 7 - Developer experience
- [x] TypeScript type generation from the schema.
- [ ] OpenAPI surface + built-in API explorer.
- [x] Multi-language per-table code snippets.
- [ ] A `forgebase` CLI: projects, dumps, branches, functions, types, logs.
- [x] Versioned migrations flow (panel + CLI).
- [ ] Logs explorer with time ranges, saved queries, and log shipping.
- [x] Advisors: index suggestions, security checks, slow-query dashboard.
- [ ] Optional AI assistant (bring your own key): SQL generation, error
      explanations, policy drafting.
- [ ] Encrypted secrets vault usable from SQL and functions.
- [x] Job scheduler UI for database cron.
- [x] Webhook replay + large-payload delivery.
- [ ] Per-project IP allowlists and connection security settings.
- [x] Per-project database settings (timeouts, exposed schemas, API row caps).
- [ ] Usage reports.
- [ ] Foreign-data wrappers UI for external sources.

## How we run it
1. Top-down inside a phase; phases interleave when priorities demand.
2. Ship small, versioned, changelogged; verify on the server before tagging.
3. Check items off here in the shipping commit.
