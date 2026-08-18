# ForgeBase roadmap to parity

An honest, ordered checklist of what it takes to stand shoulder to shoulder with
Supabase and Neon, feature by feature. Grounded in the codebase audit, not
marketing. We work these **top to bottom, one at a time**, each as its own version
bump + changelog entry + push.

Effort: **S** small (hours), **M** medium (a day-ish), **L** large (multi-day),
**XL** architectural. "Needs" flags an external dependency.

See [COMPARISON.md](COMPARISON.md) for the honest per-feature parity verdicts.

---

## Done (shipped)

- [x] Honest `COMPARISON.md` + corrected README/UI over-claims
- [x] **Real point-in-time recovery** to any second, into a new project (`1.0.3`)
- [x] **RLS-gated writes work** end to end (`1.0.3`)
- [x] In-app update panel shows human release notes, not commit messages (`1.1.0`)
- [x] **Copy-on-write branching + scale-to-zero** engine proven + shipped opt-in `INSTANCES=1` (`1.1.0`) - not yet in the panel (see Band G)

---

## Band A - security & correctness (do first)

These are bugs or footguns that bite today. Mostly small.

- [x] 1. Edge Functions `verify_jwt` per-function toggle, **default on for new functions** - closes public-by-default + service-key-injected footgun. *(Supabase defaults to on.)* **S** — shipped `1.1.1`
- [x] 2. Cap the storage client-upload body + per-bucket size/MIME limits - stop disk-fill; match Supabase bucket limits. **S** — shipped `1.1.2`
- [x] 3. Realtime: restrict subscriptions to `authenticated`/`service_role` (not the public `anon` key) + add per-column value filters (`id=eq.5`). *(Supabase has both.)* **S-M** — shipped `1.1.3`
- [x] 4. Fix Realtime/Webhook shared-trigger coupling (disabling Realtime kills webhooks) and cover tables created after enable. **S** — shipped `1.1.4`
- [x] 5. Webhooks: `old_record`, custom headers/method, and a longer backoff (up to 5 attempts / ~7 min). *(Supabase `pg_net` parity and better.)* **M** — shipped `1.1.5`
- [ ] 5b. Webhooks: manual replay from the delivery log, and bypass the 8KB NOTIFY cap for very large rows (re-read the row). **S-M**

## Band B - Auth lifecycle (our biggest gap vs Supabase)

One optional SMTP client unlocks the top four at once.

- [x] 6. Optional SMTP config (feature stays off when unset). **S** — shipped `1.2.0`
- [x] 7. Email confirmation - `email_confirmed_at`, block login until confirmed. **M** *(Needs SMTP)* — shipped `1.2.0` (resend still pending)
- [x] 8. Password reset - self-service `/recover` email + reset page. **M** *(Needs SMTP)* — shipped `1.2.1`
- [x] 9. Magic link - passwordless sign-in. **M** *(Needs SMTP)* — shipped `1.2.1` (email OTP still pending)
- [x] 10. Invite end users by email (admin invite). **S** *(Needs SMTP)* — shipped `1.2.2` (panel team-invite emails still pending; need platform-level SMTP)
- [x] 11. `user_metadata` / `app_metadata` columns, surfaced in the JWT + `/user`. *(Supabase core; apps drive RLS off these.)* **S-M** — shipped `1.1.6`
- [ ] 12. Configurable token TTLs per project (the `aud` claim shipped in `1.1.6`). **S**
- [x] 13. More OAuth providers - GitLab + Discord added (Google/GitHub/GitLab/Discord). **M** — shipped `1.1.8` (Apple/Microsoft/Facebook/LinkedIn still pending; they need id_token or special email endpoints)
- [x] 14. Refresh-token reuse detection + family revocation + global logout. **M** — shipped `1.1.7` (session-list UI still pending)
- [x] 15. Admin users REST API (list/create/get/update/delete) + ban. **M** — shipped `1.1.9`
- [ ] 16. TOTP MFA (enroll/verify, AAL in JWT, recovery codes). **L**
- [ ] 17. SAML 2.0 SSO; phone/SMS OTP. **L** *(SMS needs a provider)*

## Band C - Storage depth (vs Supabase Storage)

- [ ] 18. Path-prefix authz from the `sub` claim (a user can only touch their own folder) - the 80% of Supabase storage-RLS most apps need. **S-M**
- [ ] 19. `move` / `copy` / `list` with pagination. **S**
- [ ] 20. Full RLS-backed storage policies (model objects as a table, route through the RLS path). **L**
- [ ] 21. Image transformations (`?width=&height=&resize=&format=`). **M-L**
- [ ] 22. Resumable / TUS uploads; S3-compatible endpoint. **L**

## Band D - Realtime depth (vs Supabase Realtime)

- [ ] 23. Broadcast - arbitrary client-to-client pub/sub messages. **M**
- [ ] 24. Presence - track/sync online state. **M**
- [ ] 25. Per-subscriber RLS enforcement - re-check each change against the subscriber's policies before delivery. *(The genuinely hard one; closes the data-exposure gap.)* **L**

## Band E - Edge Functions depth (vs Supabase Edge Functions)

- [ ] 26. Cron / scheduled functions (a Go ticker + a `schedule` column). **S-M**
- [ ] 27. Per-function config (timeout/memory) + a concurrency cap + memory limit on the Deno child. **S**
- [ ] 28. Request/invocation logs + metrics (today only errors are logged). **S-M**
- [ ] 29. Warm-isolate pool to kill per-request cold starts; streaming responses. **L**

## Band F - Data-plane depth (vs Supabase)

- [ ] 30. Custom RLS policies - per-command, per-role, arbitrary `USING`/`WITH CHECK`, and edit existing. *(Today: 4 fixed templates.)* **M**
- [ ] 31. Multi-language per-table code snippets (supabase-js / Python / curl) generated from the real schema. *(Today: static curl.)* **M**
- [ ] 32. API key rotation - rotate the JWT secret / keys without invalidating user tokens. **S-M**
- [ ] 33. Configurable exposed schemas (beyond `public`) + a `db-max-rows` cap on the REST API. **S**
- [ ] 34. Per-user saved queries (today they're team-shared). **S**
- [ ] 35. Table editor: per-column filter/sort, `ALTER COLUMN` (rename/retype/default), FK dropdowns, custom primary key at create, constraint/index management. **M**
- [ ] 36. Surface the auto-generated OpenAPI + an offline Swagger UI. **S-M**
- [ ] 37. Monaco/CodeMirror SQL editor - syntax highlighting, autocomplete, formatting. **M-L**

## Band G - Platform (vs Neon + Supabase)

- [ ] 38. Wire the proven **copy-on-write branching** into the Branches panel when `INSTANCES=1` (instant, no parent lock) + route branch connection strings through the proxy. *(Neon's headline; engine already built.)* **M-L**
- [ ] 39. Wire **scale-to-zero + sleep/wake** into the panel; show per-project instance status. *(Neon scale-to-zero; engine already built.)* **M**
- [ ] 40. Monitoring: threshold alerts via the existing webhook plumbing; longer/zoomable time ranges. **S-M**
- [ ] 41. Per-project CPU/RAM attribution (arrives free once projects run as instances). **M** *(depends on 38/39)*
- [ ] 42. Backups: per-project retention, an at-rest encryption option, and restore-to-same-project PITR. **M**
- [ ] 43. Audit log: retention/export + server-side query + data-plane coverage. **S-M**
- [ ] 44. Read replica - a streaming standby + read routing. **L**
- [ ] 45. Regions / multi-region. **XL** (out of scope for the single-box model)

---

## How we run it

1. Take the next unchecked item, top down (Band A first).
2. Build it, verify on a throwaway VM, ship as a version bump with a changelog
   entry in both `CHANGELOG.md` and `pgforge/changelog.go`, tag, and push.
3. Deploy the vetted build to production.
4. Check the box here in the same commit.

Bands A-F stay on the single shared-cluster model and carry no production
migration risk. Band G items 38, 39, 41 depend on per-instance mode (`INSTANCES=1`)
and get proven on a fresh box before any production path.
