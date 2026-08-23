package main

import "net/http"

// appVersion is the human-facing semantic version shown in the UI. The git short
// SHA (version, in version.go) remains the exact build identifier used for the
// commit link and the self-update comparison.
const appVersion = "1.4.15"

// The changelog is kept in two places that must stay in step: CHANGELOG.md in the
// repo root (for GitHub) and this structured copy (for the in-app What's New
// page). When you cut a release, update both.

type changeSection struct {
	Kind    string // Added, Changed, Fixed, Security
	Entries []string
}

type release struct {
	Version  string
	Date     string
	Summary  string
	Sections []changeSection
}

// releases, newest first.
var releases = []release{
	{
		Version: "1.4.15", Date: "2026-08-23",
		Summary: "Disaster-proof backups and a documentation catch-up.",
		Sections: []changeSection{
			{"Added", []string{
				"Encrypted disaster kit in the nightly off-box sync: stack secrets, TLS certs and proxy config, AES-256 encrypted with a recovery passphrase kept at /opt/pgforge/recovery.pass - copy that passphrase somewhere OFF the server and a destroyed box can be rebuilt entirely from the S3 mirror (procedure: docs/DISASTER-RECOVERY.md).",
				"Documentation caught up with the platform everywhere: the README feature table, the in-panel Guide, every project's Docs page (OTP, MFA factors, OIDC/SAML, JWKS, tus, S3 access, image transforms, channels, queues, vault, replica port) and the self-hosting runbook.",
			}},
			{"Fixed", []string{
				"The WAL-cap alert named the wrong database (statistics are snapshot-frozen inside a single transaction, so every delta read as zero) - the sampler now uses two sessions and names the real top writer.",
				"The panel updater discards stray local edits in the deploy checkout before pulling, so an on-box hotfix can never wedge the Update button again.",
			}},
		},
	},
	{
		Version: "1.4.14", Date: "2026-08-23",
		Summary: "AI replies from reasoning models were invisible - fixed.",
		Sections: []changeSection{
			{"Fixed", []string{
				"AI assistant and Ask-AI replies showed up empty when the configured model does extended thinking (Claude Opus and friends): those models return a reasoning block BEFORE the answer, and the panel read only the first block. It now collects every text block, so the actual answer arrives. Conversations that already collected empty bubbles heal themselves - the server skips the empty entries.",
				"The infra apply script had a broken header (a v1.4.1 edit landed mid-comment), which silently stopped the startup reconciler ever since - the warm edge-function runner and the improved WAL alert were built but never installed on the box. Repaired and re-applied; everything is current now.",
			}},
		},
	},
	{
		Version: "1.4.13", Date: "2026-08-23",
		Summary: "The AI assistant is everywhere now.",
		Sections: []changeSection{
			{"Added", []string{
				"A floating AI assistant on every project page - the sparkle button in the corner opens a chat that knows this project's live schema AND every ForgeBase feature: ask \"which customers signed up this week?\" or \"how do I put RLS on orders?\" or \"what does the pooled port do?\" and get real answers, multi-turn, with your conversation kept per project for the session.",
				"SQL the assistant writes comes with Copy and Open-in-SQL-editor buttons - one click drops it into the editor ready to run.",
				"Same bring-your-own-key model as before: your Anthropic or OpenAI-compatible key from Account settings, encrypted at rest, only ever sent to the endpoint you chose.",
			}},
		},
	},
	{
		Version: "1.4.12", Date: "2026-08-23",
		Summary: "The last two: read replicas and SAML SSO. Plus a visible AI panel.",
		Sections: []changeSection{
			{"Added", []string{
				"Read replica (System page, one click): a hot standby of the whole cluster in its own container, streaming through a replication slot and serving READ-ONLY connections on port 5434 - same databases, same credentials, same TLS. Point dashboards, reports and exports at it and the primary never feels them. Live replication lag shows on the System page; removal is one click and returns the disk.",
				"SAML 2.0 SSO for your app's users: point the Auth page at any IdP's metadata (Okta, Azure AD, OneLogin, Keycloak, ADFS...) and users sign in there, landing back with normal tokens. Signature verification comes from the battle-tested crewjam/saml library - never hand-rolled. The SP metadata and login URLs to register are shown right on the card.",
				"The SQL editor's Ask AI is now a proper panel: a visible purple button opens an inline bar where you describe the query, see progress, and get the SQL inserted into the editor - with a pointer to Account settings when no key is configured yet.",
			}},
			{"Fixed", []string{
				"The WAL-cap alert now names the database writing the most at that moment, instead of a generic \"a tenant is churning\" - no detective work needed.",
				"A new Advisor rule detects TOAST write churn (a large column rewritten constantly, the pattern that filled the WAL archive) and explains how to fix it in the app.",
				"The replica engine refuses to start without twice the cluster's size in free disk, and the standby container matches the primary's connection limit (a mismatch refuses recovery).",
			}},
		},
	},
	{
		Version: "1.4.11", Date: "2026-08-23",
		Summary: "S3-protocol storage access with scoped keys.",
		Sections: []changeSection{
			{"Added", []string{
				"S3-compatible access to project storage: mint a scoped key pair on the Storage page and point rclone, the AWS CLI or any S3 SDK at the project domain (path-style). ListBuckets, ListObjectsV2, Head/Get/Put/DeleteObject all work; requests are verified with real AWS Signature V4, and the implementation passes the official AWS signature test vectors. Multipart and presigned URLs answer a clear 501 rather than failing silently - they come later.",
				"Keys are per project, revocable instantly, and the secret is shown exactly once at creation.",
			}},
		},
	},
	{
		Version: "1.4.10", Date: "2026-08-23",
		Summary: "Time-travel branches and adaptive instance sizing.",
		Sections: []changeSection{
			{"Added", []string{
				"Branch from any point in time: set a past timestamp on the branch form and the branch is rebuilt from the WAL archive exactly as the project was at that instant, down to the second - then it behaves like any branch (diff against the parent, reset, expiry). Reading yesterday's data next to today's is now a two-minute operation, and the source is never touched.",
				"Adaptive instance sizing: give a dedicated-instance project memory bounds and the platform resizes it hourly - growing the limit a quarter at a time under sustained pressure (above 85 percent), shrinking it a fifth at a time when mostly idle - never past the bounds you set.",
			}},
		},
	},
	{
		Version: "1.4.9", Date: "2026-08-23",
		Summary: "Edge functions grow up: warm starts, streaming, WebSockets.",
		Sections: []changeSection{
			{"Added", []string{
				"Warm process pool: the first call to a function starts a persistent server for it and every later call skips process boot entirely - busy functions answer in single-digit milliseconds, and module-level state (connection pools, caches) survives between requests. Idle processes are reaped after five minutes; redeploying or changing secrets swaps the process automatically.",
				"Streaming responses: return a ReadableStream (SSE, chunked JSON, LLM token streams) and chunks reach the client as they are produced instead of after the function finishes.",
				"WebSocket support: Deno.upgradeWebSocket works through the functions endpoint - realtime game loops, chat sockets, live dashboards, straight from a function.",
				"Background work after the response: setTimeout / queueMicrotask work past the reply now that the process outlives the request.",
			}},
			{"Changed", []string{
				"The previous one-process-per-request runner remains as an automatic fallback, so a function that cannot warm-start (or a box mid-update) keeps working exactly as before.",
			}},
		},
	},
	{
		Version: "1.4.8", Date: "2026-08-23",
		Summary: "RLS-filtered change streams and phone sign-in.",
		Sections: []changeSection{
			{"Added", []string{
				"Per-subscriber RLS on change streams (opt-in, Realtime page): every INSERT/UPDATE event is delivered only to subscribers whose token could SELECT that row under your policies - the same answer the REST API would give them. One lightweight visibility check per distinct subscriber token per event, memoized across duplicate connections; service_role passes through.",
				"Phone OTP sign-in, bring-your-own-SMS-provider: set an SMS webhook URL on the Auth page and clients get the standard signInWithOtp phone flow - we POST {phone, code, project} (HMAC-signed) to your endpoint, you forward it through Twilio, Vonage or anything else. Verified codes create phone-only user accounts with the usual tokens.",
			}},
		},
	},
	{
		Version: "1.4.7", Date: "2026-08-23",
		Summary: "Asymmetric JWT signing with a JWKS endpoint.",
		Sections: []changeSection{
			{"Added", []string{
				"RS256 user tokens, per project and opt-in: enable asymmetric signing on the Data API page and new user access tokens carry an RSA signature any third party can verify at https://<project>.<domain>/.well-known/jwks.json - your JWT secret never leaves the server. Key rotation is one click; the previous public key stays in the JWKS so outstanding tokens remain valid until they expire.",
				"Everything keeps working while it is on: anon and service keys stay HS256 (nothing you have distributed changes), and the project's REST API verifies both algorithms at once - a compatibility we tested against the exact PostgREST build in production before shipping.",
			}},
		},
	},
	{
		Version: "1.4.6", Date: "2026-08-23",
		Summary: "Storage grows up: path rules and resumable uploads.",
		Sections: []changeSection{
			{"Added", []string{
				"Path-level storage access rules: scope any bucket prefix to public, authenticated, owner (the first folder must be the user's own id - enforced on reads AND uploads) or private (service key and signed URLs only). Longest prefix wins; buckets without rules keep their plain public/private flag.",
				"Resumable uploads: a standard tus 1.0.0 endpoint at /storage/v1/tus/<bucket>. Works with tus-js-client, Uppy and every other tus client; interrupted uploads resume exactly where they stopped, survive daemon restarts, respect bucket size/type limits, quotas and path rules, and abandoned uploads are pruned after a day.",
			}},
		},
	},
	{
		Version: "1.4.5", Date: "2026-08-23",
		Summary: "Team depth: project scoping and real session control.",
		Sections: []changeSection{
			{"Added", []string{
				"Per-project member scoping: give a member a list of projects on the Team page and the panel shows them ONLY those (branches included) - other projects 404 as if they did not exist. Owners always see everything.",
				"Panel session management: every sign-in is now a listed device on the Account page - IP, browser, signed-in and last-seen times. Sign out any single device, or everywhere else in one click; revocation is immediate, even for a stolen cookie.",
				"Owners can sign any member out of every device from the Team page.",
			}},
		},
	},
	{
		Version: "1.4.4", Date: "2026-08-23",
		Summary: "Usage reports, foreign databases, and the last auth hooks.",
		Sections: []changeSection{
			{"Added", []string{
				"Usage page per project: database size and 30-day growth chart, storage against quota, function invocations with error rate and average latency, webhook deliveries, auth signups/actives, and live realtime connections - all in one place.",
				"Foreign databases (postgres_fdw) from the Database page: connect an external Postgres, import any of its schemas, and query its tables here as if they were local. Removal drops the imported tables with the server.",
				"Before-create auth hook: define auth.before_create(email text) RETURNS text in SQL and every signup (password or OAuth) consults it - return a message to reject, NULL to allow. Domain blocklists and invite gates in three lines of SQL.",
				"Assurance levels in tokens: every access token now carries an aal claim - aal2 when the login also passed a second factor, aal1 otherwise - so RLS can require step-up auth for sensitive tables.",
				"Per-instance compute controls: dedicated-instance projects get memory and CPU limits on the Settings page, applied live and preserved across sleep/wake.",
			}},
		},
	},
	{
		Version: "1.4.3", Date: "2026-08-23",
		Summary: "The finer auth cuts, and a smarter AI settings card.",
		Sections: []changeSection{
			{"Added", []string{
				"Generic OpenID Connect: point the new Custom (OIDC) provider at any issuer URL (Keycloak, Okta, Auth0, Authentik, your own) and its endpoints are discovered automatically - sign-in works like every built-in provider.",
				"Identity linking: signing in with any provider records the identity, and the same verified email across providers stays one user account.",
				"Configurable auth rate limits: set requests-per-minute-per-IP on the Auth page (0 turns the limiter off).",
				"Single-session mode: signing in anywhere signs the user out everywhere else.",
				"Leaked-password protection: optionally reject passwords found in known breaches, checked privately via the HIBP k-anonymity API - only five characters of the hash ever leave your server.",
				"User impersonation: mint a one-hour access token as any app user from the panel to see exactly what they see through RLS. Every use is audited.",
				"The AI assistant card now has a provider picker (Claude / OpenAI / custom) and a live model dropdown: paste your key, click Load models, pick from the endpoint's real list instead of typing a model id.",
			}},
		},
	},
	{
		Version: "1.4.2", Date: "2026-08-23",
		Summary: "Everything on the near-term roadmap, shipped in one release.",
		Sections: []changeSection{
			{"Added", []string{
				"Secrets vault: encrypted-at-rest secrets you read from SQL, functions and cron - forgebase.secret_set / secret_get / secret_list / secret_delete, guarded by SECURITY DEFINER functions so API roles can never read the key table. Enable it from Settings.",
				"Two-factor auth for YOUR app's users: TOTP enrollment (QR-compatible otpauth URI), verify-to-activate, ten single-use recovery codes, and password logins that require the code once enrolled - all via /auth/v1/factors/enroll|verify|disable.",
				"Bot protection: plug in Cloudflare Turnstile keys on the Auth page and signup/login verify captcha_token server-side. No keys, no change.",
				"API Explorer: build REST requests in the panel, run them as anon / authenticated / service_role against your live API, and inspect status, headers, body and the equivalent curl.",
				"AI SQL assistant (bring your own key): point Account settings at any Anthropic- or OpenAI-compatible endpoint and the SQL editor's Ask AI button writes queries against your real schema. Keys are encrypted at rest and only ever sent to the endpoint you chose.",
				"Image transformations: append ?width= / ?height= to any storage URL and get a resized rendition, computed once and cached - plus ETag/304 caching on all storage serving.",
				"Logical replication controls: create and drop publications (all tables or a chosen list) from the Database page, ready for external consumers to subscribe.",
				"Logs, upgraded: saved log views (store a filter set by name), and daily log shipping to any HTTPS endpoint you configure.",
				"Anonymized branches: list table.column rules when creating a branch and text becomes deterministic anon_ tokens (joins keep working), everything else is nulled - production-shaped data without production secrets.",
				"Per-database activity card on Monitoring: transactions, row writes, cache hit ratio, temp spill, backends and deadlocks for this project alone.",
				"Migrate to a dedicated instance: one button moves a shared-cluster project onto its own Postgres container (instant branches, true scale-to-zero, its own crash domain). The shared copy stays parked until you delete it yourself.",
				"Eight more OAuth providers: Microsoft, Facebook, Twitch, Slack, Spotify, LinkedIn, Bitbucket and Notion join Google, GitHub, GitLab and Discord.",
				"A CLI (scripts/forgebase): projects, types, openapi, migrations, migrate, csv - plain POSIX sh + curl, authenticated with a personal API key, which the panel API now accepts as a Bearer token.",
			}},
		},
	},
	{
		Version: "1.4.1", Date: "2026-08-23",
		Summary: "TLS on the pooled port.",
		Sections: []changeSection{
			{"Fixed", []string{
				"The connection pooler (port 6543) now serves TLS, so sslmode=require - the default for most drivers - works there exactly like on the direct port. Previously the pooler only spoke plaintext, which both rejected SSL-requiring clients and would have sent credentials unencrypted.",
			}},
		},
	},
	{
		Version: "1.4.0", Date: "2026-08-23",
		Summary: "The platform milestone: channels, queues, and deeper control everywhere.",
		Sections: []changeSection{
			{"Added", []string{
				"Realtime channels: clients join named channels for broadcast messaging (client-to-client or straight from SQL via forgebase.broadcast(channel, payload)), presence with join/leave events and a live member list, and private channels (private-* names refuse the anon key).",
				"Message queues: durable at-least-once queues inside your database - forgebase.queue_send / queue_read (with visibility-timeout locking) / queue_delete_msg / queue_archive from any SQL client, plus a Queues page with depth, lock and archive counts, test-send, purge and delete.",
				"Auth custom-claims hook: define auth.custom_claims(uuid) RETURNS jsonb in SQL and its result merges into every token's app_metadata at mint time - plans, roles and flags computed where your data lives.",
				"Email template editor: customize the subject and body of all four auth emails with {{link}} / {{code}} placeholders.",
				"Per-project IP allowlists covering the entire data plane (REST, Auth, Storage, Realtime, Functions).",
			}},
		},
	},
	{
		Version: "1.3.39", Date: "2026-08-23",
		Summary: "Storage quotas with a usage meter.",
		Sections: []changeSection{
			{"Added", []string{
				"Per-project storage quotas (default 1 GB, adjustable or unlimited): a usage bar on the Storage page shows how full you are, and every upload path - panel, API, signed URLs - refuses politely once the quota is reached.",
			}},
		},
	},
	{
		Version: "1.3.38", Date: "2026-08-23",
		Summary: "User sessions, visible and revocable.",
		Sections: []changeSection{
			{"Added", []string{
				"The Auth users table now shows each user's live session count, a search box (by email or id), an anonymous marker, and a one-click \"Sign out all\" that revokes every device's refresh token.",
			}},
		},
	},
	{
		Version: "1.3.37", Date: "2026-08-23",
		Summary: "Auth policies you control.",
		Sections: []changeSection{
			{"Added", []string{
				"Per-project auth policies (Auth page): access-token lifetime (5-1440 minutes), minimum password length (6-72), and a redirect allowlist so magic links and OAuth can land on your app's own domain safely.",
			}},
			{"Changed", []string{
				"Release notes and interface text now describe client compatibility neutrally; only real package names remain inside the code snippets themselves.",
			}},
		},
	},
	{
		Version: "1.3.36", Date: "2026-08-23",
		Summary: "Per-function timeouts and memory.",
		Sections: []changeSection{
			{"Added", []string{
				"Each edge function can now set its own execution timeout (5-120 s) and memory cap (64-256 MB) right in the editor - a heavy report generator no longer shares limits with a tiny webhook handler.",
			}},
		},
	},
	{
		Version: "1.3.35", Date: "2026-08-23",
		Summary: "Sign in with an emailed code.",
		Sections: []changeSection{
			{"Added", []string{
				"Email OTP sign-in: request a 6-digit code (rate-limited, hashed at rest, 10-minute expiry, 5 attempts) and verify it for a session - the standard signInWithOtp / verifyOtp email flow of JS client libraries. First-time emails get a confirmed account automatically, like magic links.",
			}},
		},
	},
	{
		Version: "1.3.34", Date: "2026-08-23",
		Summary: "Signed upload URLs.",
		Sections: []changeSection{
			{"Added", []string{
				"Signed upload URLs for Storage (createSignedUploadUrl-compatible with standard JS clients): mint a time-limited token bound to one exact bucket and path, hand it to a browser or service, and it can upload that single object without holding any API key. Bucket size and type limits still apply, and off-box object storage stays in sync.",
			}},
		},
	},
	{
		Version: "1.3.33", Date: "2026-08-23",
		Summary: "Branches that reset and expire.",
		Sections: []changeSection{
			{"Added", []string{
				"Reset from parent: one click throws away a branch's current state and recreates it as a fresh copy of the parent - same name, same connection string. Works for both shared and copy-on-write instance branches.",
				"Branch expiry: pick a lifetime at creation (1/7/30 days, or never - 7 days is the default). An expired branch is paused, never deleted: connections stop, data stays, and you get a notification to delete or resume it.",
			}},
		},
	},
	{
		Version: "1.3.32", Date: "2026-08-23",
		Summary: "See exactly how a branch drifted.",
		Sections: []changeSection{
			{"Added", []string{
				"Schema diff on the Branches page: compare the structure of your project against any branch (or two branches against each other) as a colored unified diff - spot drift before promoting anything. Structure only; data is never compared.",
			}},
		},
	},
	{
		Version: "1.3.31", Date: "2026-08-23",
		Summary: "Anonymous sign-ins.",
		Sections: []changeSection{
			{"Added", []string{
				"Anonymous sign-ins (opt-in toggle on the Auth page): clients call signup with no credentials and get a real, RLS-scoped user - then upgrade it to a permanent account later by setting an email and password on the same session. Anonymous sessions carry is_anonymous in their token metadata so policies can treat them differently.",
			}},
			{"Fixed", []string{
				"The two-factor QR/URI now labels the account with your email in authenticator apps instead of your display name.",
			}},
		},
	},
	{
		Version: "1.3.30", Date: "2026-08-23",
		Summary: "WAL storms contained for good, QR enrollment, private snippets.",
		Sections: []changeSection{
			{"Fixed", []string{
				"The WAL archive could balloon when two basebackups landed on the same date - pruning anchored on the wrong one and kept a full day of dead WAL (15 GB on 2026-08-22). Pruning now reads each basebackup's own manifest for the exact cut point.",
			}},
			{"Added", []string{
				"Three independent guards so a disk-full can never happen again: WAL hygiene now runs every 15 minutes instead of hourly; the archive has a hard 8 GB cap (oldest segments dropped with an alert - a shorter recovery window beats a dead server); and the archiver itself refuses to overflow past a 12 GB panic ceiling by ring-buffering the oldest segments at write time.",
				"Two-factor enrollment now shows a scannable QR code (plus the manual key, as before).",
				"Saved SQL snippets can be private (only you see them) or shared, and can be renamed. Private snippets are protected from other users loading, renaming or deleting them.",
			}},
		},
	},
	{
		Version: "1.3.29", Date: "2026-08-22",
		Summary: "Constraints, managed visually.",
		Sections: []changeSection{
			{"Added", []string{
				"Constraints tab on the Objects page: every constraint in the schema (primary keys, unique, foreign keys, checks, exclusions) with its full definition, guided creation of UNIQUE and CHECK constraints, and safe dropping - primary keys are protected.",
			}},
		},
	},
	{
		Version: "1.3.28", Date: "2026-08-22",
		Summary: "Two-factor login for the panel.",
		Sections: []changeSection{
			{"Added", []string{
				"Two-factor authentication for panel accounts (opt-in, Account page): pair any authenticator app and sign-in then requires your password plus a rotating 6-digit code. Enrollment only activates after a correct code confirms your app has the key, wrong codes count toward the account lockout, and you can turn it off with a current code.",
			}},
		},
	},
	{
		Version: "1.3.27", Date: "2026-08-22",
		Summary: "Versioned migrations.",
		Sections: []changeSection{
			{"Added", []string{
				"Migrations page: apply timestamped, named SQL migrations that run atomically - a failure applies nothing and records nothing. History is stored inside the project database itself, so it travels with every backup, branch and clone, and the whole history downloads as one replayable .sql file.",
			}},
		},
	},
	{
		Version: "1.3.26", Date: "2026-08-22",
		Summary: "Edge functions on a schedule.",
		Sections: []changeSection{
			{"Added", []string{
				"Scheduled edge functions: give any function a cron schedule (presets or custom, UTC) and the platform invokes it every matching minute with your service key - through the exact same path as an HTTP call, so concurrency limits, timeouts, logs and metrics all apply. The invocation log doubles as the run history.",
			}},
		},
	},
	{
		Version: "1.3.25", Date: "2026-08-22",
		Summary: "Edge functions, fully observable.",
		Sections: []changeSection{
			{"Added", []string{
				"Full edge function invocation logs: every call is recorded with its status code and duration (not just failures), and the log view shows the last 50 with clear ok/error badges.",
				"Per-function metrics in the sidebar: 24-hour call count, error count and average duration next to each function.",
			}},
		},
	},
	{
		Version: "1.3.24", Date: "2026-08-22",
		Summary: "Your API, as an OpenAPI spec.",
		Sections: []changeSection{
			{"Added", []string{
				"OpenAPI spec for the Data API: view or download the live, schema-generated spec from the Data API page - import it into Postman, Insomnia, or any client generator.",
			}},
		},
	},
	{
		Version: "1.3.23", Date: "2026-08-22",
		Summary: "Per-project timeouts.",
		Sections: []changeSection{
			{"Added", []string{
				"Statement and idle-session timeouts per project (Database page): kill runaway queries automatically and reclaim parked connections. Applied to the project role, so every new connection - direct, pooled or API - picks them up instantly. 0 keeps a timeout off.",
			}},
		},
	},
	{
		Version: "1.3.22", Date: "2026-08-22",
		Summary: "Logs you can slice, and the slowest queries surfaced.",
		Sections: []changeSection{
			{"Added", []string{
				"Logs filters: time range (hour / day / week / month), action and target text search across a project's activity trail.",
				"Slow statements: the Logs page now ranks this database's statements by mean execution time (via pg_stat_statements) with calls, rows and totals - your slow-query dashboard.",
			}},
		},
	},
	{
		Version: "1.3.21", Date: "2026-08-22",
		Summary: "Tune the Data API per project.",
		Sections: []changeSection{
			{"Added", []string{
				"Per-project API settings on the Data API page: a max-rows-per-response cap (protects clients from accidental full-table fetches) and extra exposed schemas beyond public (queryable via the Accept-Profile header, with usage grants applied automatically). Changes apply on the next request - no restart needed.",
			}},
		},
	},
	{
		Version: "1.3.20", Date: "2026-08-22",
		Summary: "Webhooks you can test and replay.",
		Sections: []changeSection{
			{"Added", []string{
				"Webhook replay: every delivery now stores its payload (up to 64 KB), so any past event - success or failure - can be re-sent with one click through the same signing, headers and retry ladder.",
				"Send-a-test-event button on every webhook, so you can verify an endpoint without touching real data.",
			}},
		},
	},
	{
		Version: "1.3.19", Date: "2026-08-22",
		Summary: "Clients can finally list objects.",
		Sections: []changeSection{
			{"Added", []string{
				"Storage list endpoint: the standard JS client's storage.from(bucket).list() now works - POST /storage/v1/object/list/<bucket> with prefix, limit, offset and search, returning folder and file entries exactly as typed clients expect. Public buckets list with any valid key; private buckets need an authenticated or service key.",
			}},
		},
	},
	{
		Version: "1.3.18", Date: "2026-08-22",
		Summary: "A real storage explorer.",
		Sections: []changeSection{
			{"Added", []string{
				"Folder navigation in Storage: objects with slashes in their paths browse as folders with an Up button, instead of one flat list.",
				"Bucket-wide search across every object path.",
				"Move/rename and copy for any object - metadata, disk and off-box object storage all stay in sync, and a move never loses data (the copy lands fully before the original goes).",
				"Bulk selection with select-all and one-click delete of many objects.",
			}},
		},
	},
	{
		Version: "1.3.17", Date: "2026-08-22",
		Summary: "Choose which changes each table publishes.",
		Sections: []changeSection{
			{"Added", []string{
				"Realtime publications: a per-table matrix on the Realtime page choosing which events (insert / update / delete) get captured and streamed. It governs webhooks too - an event switched off fires neither. New tables keep the everything-on default.",
			}},
		},
	},
	{
		Version: "1.3.16", Date: "2026-08-22",
		Summary: "Copy-paste client code for every table.",
		Sections: []changeSection{
			{"Added", []string{
				"Per-table code snippets on the Data API page: click the terminal icon next to any endpoint for ready-to-paste JS client, fetch, cURL and Python examples - reads, inserts, updates and deletes, pre-filled with your project URL and anon key (and the typed client import).",
			}},
		},
	},
	{
		Version: "1.3.15", Date: "2026-08-22",
		Summary: "Typed clients, straight from your schema.",
		Sections: []changeSection{
			{"Added", []string{
				"TypeScript type generation: view or download database.types.ts from the Data API page - a typed Database interface (Row/Insert/Update per table, views, enums, relationships) generated live from your schema, compatible with typed JS clients.",
			}},
		},
	},
	{
		Version: "1.3.14", Date: "2026-08-22",
		Summary: "Your database, reviewed automatically.",
		Sections: []changeSection{
			{"Added", []string{
				"Advisors: a new page that reviews your database live on every visit and flags issues with a concrete fix for each. Security checks: RLS disabled on API-exposed tables, blanket allow-everything write policies, SECURITY DEFINER functions, credential-looking columns readable by anon, RLS-on-but-no-policies. Performance checks: tables without a primary key, foreign keys without an index, large never-used indexes, sequential-scan-heavy tables, dead-row bloat, duplicate indexes.",
			}},
		},
	},
	{
		Version: "1.3.13", Date: "2026-08-22",
		Summary: "Scheduled database jobs, and a grid fix.",
		Sections: []changeSection{
			{"Added", []string{
				"Cron Jobs: schedule SQL against your database on a cron cadence (presets or custom) - cleanups, rollups, materialized view refreshes. Jobs can be paused, resumed, removed, or run once on demand, with a run history showing status, message and duration (kept 14 days).",
			}},
			{"Fixed", []string{
				"The Table Editor grid went blank in normal owner view on 1.3.12 (rows and columns only rendered while impersonating a role - inverted logic). Sorry about that one; it is fixed and covered by a regression test.",
			}},
		},
	},
	{
		Version: "1.3.12", Date: "2026-08-22",
		Summary: "See your data exactly as your users do.",
		Sections: []changeSection{
			{"Added", []string{
				"View-as-role in the Table Editor: switch the grid to anon, authenticated or service_role and see exactly the rows that role gets - RLS policies and grants fully applied, safely read-only, with a banner and one click back to owner view.",
				"RLS badge on every table: the editor header now shows whether Row Level Security is on and how many policies exist, linking straight to the Policies page.",
			}},
		},
	},
	{
		Version: "1.3.11", Date: "2026-08-22",
		Summary: "A real policy editor, and a zoomable diagram.",
		Sections: []changeSection{
			{"Added", []string{
				"New Policies page: every table with its Row Level Security state, full policy details (command, permissive/restrictive, roles, USING and WITH CHECK expressions), one-click enable/disable, and FORCE mode (policies apply to the table owner too).",
				"Custom policy builder: write any policy - pick the command, roles, USING and WITH CHECK expressions, permissive or restrictive - with matching table grants applied automatically so the policy actually works over the API.",
				"Edit existing policies in place (roles and expressions).",
				"Column-level privileges: grant SELECT/INSERT/UPDATE on specific columns to anon/authenticated/service_role - e.g. expose a table but hide a cost column - with a listing of every column grant and one-click revoke.",
				"The schema diagram is now zoomable: zoom in/out, reset, and a Fit button that scales the whole diagram into view (fit is the default).",
			}},
		},
	},
	{
		Version: "1.3.10", Date: "2026-08-22",
		Summary: "SQL editor tabs, table definitions, and diagram fixes.",
		Sections: []changeSection{
			{"Added", []string{
				"SQL editor tabs: keep several queries open at once - tabs persist in your browser per project, double-click to rename.",
				"Table definition view: a collapsible card in the Table Editor shows the full CREATE TABLE statement (columns, defaults, primary key, foreign keys, indexes) for any table.",
			}},
			{"Fixed", []string{
				"The schema diagram was rendering collapsed (a global icon style squashed it) and its colors could not resolve - it now draws at full size with proper theme colors.",
				"The Objects page no longer lists functions, enums or indexes that belong to installed extensions (pg_stat_statements internals and friends) - only your own objects show.",
			}},
		},
	},
	{
		Version: "1.3.9", Date: "2026-08-22",
		Summary: "See your schema as a diagram.",
		Sections: []changeSection{
			{"Added", []string{
				"Schema diagram: an auto-generated map of your tables and their foreign-key relationships - primary keys marked, arrows showing what references what, click any table to jump into the editor. One per schema, from the Diagram button in the Table Editor.",
				"Create (and drop empty) schemas right from the Table Editor's schema switcher.",
				"Your enum types now appear in the column type pickers - both when adding a column and when changing an existing column's type (values are cast through text).",
			}},
			{"Fixed", []string{
				"Tables, schemas and enum types created through the panel are now owned by your project role instead of the internal superuser, so your own migrations and tools can manage them.",
			}},
		},
	},
	{
		Version: "1.3.8", Date: "2026-08-22",
		Summary: "A visual home for functions, triggers, enums and indexes.",
		Sections: []changeSection{
			{"Added", []string{
				"New Objects page per project: browse and manage database functions, triggers, enum types and indexes visually, in any schema.",
				"Functions: full definitions with language, return type, volatility and security-definer flags; create with a guided editor; drop safely.",
				"Triggers: guided builder (table, timing, events, row/statement, trigger function picker), one-click enable/disable, drop.",
				"Enums: create types, add values (at a position), rename values, drop - with the current values shown as chips.",
				"Indexes: per-index size and usage counts (a big index with zero scans is a removal candidate), guided creation (method, unique, multi-column), drop - primary key indexes are protected.",
			}},
		},
	},
	{
		Version: "1.3.7", Date: "2026-08-22",
		Summary: "Browse every schema, views included.",
		Sections: []changeSection{
			{"Added", []string{
				"Schema switcher: the Table Editor now browses any schema in your database, not just public - every action (editing, filters, imports, exports, column changes) works in the schema you picked.",
				"Views, materialized views and foreign tables appear in the sidebar with badges. They open read-only with full filtering, sorting and export; a view's SQL definition is shown in a collapsible card.",
				"Materialized views get a one-click Refresh button.",
				"Creating tables and importing CSVs lands in the currently selected schema.",
			}},
			{"Fixed", []string{
				"Dropping from the editor now uses the right statement for the object kind (table, view, materialized view, foreign table).",
			}},
		},
	},
	{
		Version: "1.3.6", Date: "2026-08-22",
		Summary: "Table Editor, part two: smarter editing everywhere.",
		Sections: []changeSection{
			{"Added", []string{
				"Type-aware cell editing: booleans and enums edit as dropdowns, dates and timestamps get native pickers, numbers get numeric inputs - and nullable cells grow a one-click set-to-NULL button.",
				"JSON editor: double-clicking a json/jsonb cell opens a proper editor dialog with pretty-printing and validation before save.",
				"Row panel: open any row in a side panel to see and edit every field at full length (the grid truncates long values; the panel never does), with explicit null checkboxes and a single transactional save.",
				"Foreign keys, visible: FK columns are marked in the grid and column list with their target, and the row panel suggests live values from the referenced table as you type.",
				"Column management: rename a column, change its type (with automatic casting - a failed cast rolls back cleanly), edit its default, toggle not-null, and write per-column comments, all from the Columns card.",
				"Table rename and table comments.",
				"Insert form now uses dropdowns for enums and booleans and date/number inputs where the type calls for them.",
			}},
		},
	},
	{
		Version: "1.3.5", Date: "2026-08-22",
		Summary: "Snippet buttons and schema clicks paint instantly.",
		Sections: []changeSection{
			{"Fixed", []string{
				"Text inserted by the snippet buttons or by clicking tables/columns in the schema sidebar now appears immediately - inserting programmatically skipped the highlighter repaint, so the text stayed hidden until your first keystroke.",
			}},
		},
	},
	{
		Version: "1.3.4", Date: "2026-08-22",
		Summary: "SQL editor text is now fail-safe visible.",
		Sections: []changeSection{
			{"Fixed", []string{
				"The SQL editor keeps its text plainly visible until the syntax highlighter has actually painted - if the highlighter ever fails for any reason, you see normal text instead of an invisible query.",
			}},
		},
	},
	{
		Version: "1.3.3", Date: "2026-08-22",
		Summary: "SQL Editor interaction fix.",
		Sections: []changeSection{
			{"Fixed", []string{
				"Clicking a table or column in the schema sidebar inserts it into the editor again, and the snippet buttons (SELECT, INSERT, UPDATE...) work again - a malformed script literal was stopping the editor's JavaScript from loading at all.",
				"Syntax highlighting now correctly colors SQL keywords (the keyword patterns were matching a stray control character instead of word boundaries).",
			}},
		},
	},
	{
		Version: "1.3.2", Date: "2026-08-22",
		Summary: "A much stronger table editor.",
		Sections: []changeSection{
			{"Added", []string{
				"Stacked filters on any table: combine conditions (=, not equals, greater/less, contains, in-list, is null) as removable chips - values always travel as bound parameters.",
				"Click any column header to sort (click again for descending, again to clear), with multiple sort columns combining.",
				"Bulk row selection with select-all and one-click delete of the selected rows (transactional - all or nothing).",
				"Rows-per-page selector (25/100/500), with filters, sorts, and page size all preserved while you page through.",
				"Export any table as SQL INSERT statements (restores anywhere with plain psql), alongside the existing CSV export.",
				"Duplicate a table (structure with all indexes and constraints, optionally including data) in one click.",
			}},
		},
	},
	{
		Version: "1.3.1", Date: "2026-08-22",
		Summary: "A professional SQL editor.",
		Sections: []changeSection{
			{"Added", []string{
				"Syntax highlighting and schema-aware autocomplete in the SQL editor (Tab to accept, arrows to choose) - tables, columns, and keywords, generated from your live schema.",
				"Visual query plans: the new Explain button renders the plan as an indented tree with costs, row estimates, and filters - without executing writes.",
				"Persistent, team-visible query history (last 200 runs with status and timing) with one-click reload, alongside the existing quick-recall list.",
				"Run only the selected text, choose a result row limit (100/1000/5000), export results as CSV, JSON, or Markdown, Ctrl+click any cell to copy it, and a one-click SQL formatter.",
				"A destructive-query guard: DROP/TRUNCATE, or DELETE/UPDATE without WHERE, now ask before running.",
			}},
			{"Changed", []string{
				"Documentation and interface copy cleanup.",
			}},
		},
	},
	{
		Version: "1.3.0", Date: "2026-08-22",
		Summary: "Dedicated instances: instant branching and true scale-to-zero.",
		Sections: []changeSection{
			{"Added", []string{
				"Dedicated instances: choose \"Dedicated instance\" when creating a project and it runs as its OWN Postgres on copy-on-write storage - full isolation (its own engine, its own crash domain), with the project role as its superuser.",
				"INSTANT branching: for dedicated projects, a branch is a copy-on-write snapshot - it appears in about 2 seconds regardless of database size, never locks the parent, and shares unchanged storage with it (a branch of a 40MB database costs ~8MB).",
				"True scale-to-zero: an idle dedicated instance is STOPPED after 15 minutes - zero RAM, zero CPU. The next connection (app, API call, or panel visit) cold-starts it in about 1.3 seconds through the always-on proxy on port 5433.",
				"Everything else follows the mode automatically: connection strings, the Data API, Realtime, backups (dedicated instances are dumped with the same skip-unchanged smarts), Sleep/Wake buttons, and Pause (which locks the instance so connections can NOT wake it until you Resume).",
			}},
			{"Changed", []string{
				"Every dedicated instance uses password-only authentication (scram) from birth and runs the full ForgeBase image - pgvector and friends included.",
			}},
		},
	},
	{
		Version: "1.2.21", Date: "2026-08-22",
		Summary: "S3 object storage, and 20 fixes from a deep adversarial review.",
		Sections: []changeSection{
			{"Added", []string{
				"S3-compatible object storage for uploaded files (System page): files become durable in your object storage bucket while local disk acts as a fast ~1.5GB cache. Existing files migrate automatically in the background; without a remote configured, storage stays local exactly as before.",
			}},
			{"Security", []string{
				"Login lockout now keys on the account itself, so alternating between an account's email and username no longer doubles the attempts an attacker gets - and the emergency admin login can no longer be locked out by junk attempts against it.",
				"Waking a project can no longer silently unlock a deliberately paused one.",
			}},
			{"Fixed", []string{
				"CRITICAL: with numbered project names (like app and app-2), the backup pruner's pattern could match the sibling's backups and delete them, and the skip-unchanged check could trust the sibling's dump. Both patterns are now date-anchored and cannot cross projects.",
				"Skip-unchanged backups actually skip now: modern pg_dump embeds a randomized security token in every schema dump, which made the change-detection hash different on every run. The token is excluded from hashing.",
				"Auto-install could silently never run: the update check's timing could permanently miss the 03:00-05:00 installation window. Checks are hourly now.",
				"A fresh install's default backup age ceiling would have deleted the weekly backup tier; the ceiling now always reaches past it.",
				"Realtime could briefly strand a subscriber that connected exactly as the idle reaper closed the listener; one unreachable project database could also stall realtime for every project. Both races fixed.",
				"One project's busy or hung edge functions can no longer consume the whole platform's function capacity - each project is capped individually inside the global limit.",
				"Assorted hardening: concurrent update launches, partial cluster snapshots counting toward retention, infra apply failures being masked, unbounded lockout bookkeeping, settings caches on transient errors, and hostname-validation gaps in the new domain settings.",
			}},
		},
	},
	{
		Version: "1.2.20", Date: "2026-08-22",
		Summary: "Restore any off-box backup from the panel, in one click.",
		Sections: []changeSection{
			{"Added", []string{
				"Off-box archive browser: the Backups page can list your project's dumps stored in off-box storage (where older backups live after local pruning) and restore any of them into a NEW project - the original is never touched. Runs in the background; the restored project appears as soon as it is ready, and Discord is pinged on completion or failure.",
			}},
			{"Fixed", []string{
				"When an update was detected seconds after release, the \"What's new\" notes could be missing (GitHub serves the release notes file a couple of minutes later than the release itself). The panel now refetches the notes automatically and, if they are genuinely still syncing, says so instead of showing nothing.",
			}},
		},
	},
	{
		Version: "1.2.19", Date: "2026-08-22",
		Summary: "The platform now tells you when something needs attention - before it hurts.",
		Sections: []changeSection{
			{"Added", []string{
				"Watchdogs: an hourly check alerts if the database's write-ahead log is growing abnormally (the early symptom of the class of problem that once filled the disk) or if the disk passes 85%. You get a red banner on the System page AND a Discord ping - once per episode, with an all-clear when it recovers.",
				"Incident notes: post \"Investigating X...\" to your public status page from the System page while you work on something; resolving moves it into a visible history. Discord is pinged on open and resolve.",
				"Weekly Discord digest (Sundays ~10:00 IST): uptime, RAM and disk, project counts (sleeping/pinned), backup sizes per tier, and updates installed that week.",
				"Self-updates now ping Discord on success and, importantly, on an automatic rollback. Failed nightly backups alert too.",
			}},
			{"Fixed", []string{
				"Instance-mode setup can no longer create a storage image larger than the disk (it sizes to 40% of free space and refuses oversize requests).",
			}},
		},
	},
	{
		Version: "1.2.18", Date: "2026-08-22",
		Summary: "Serve your platform on a second domain - with zero risk to the first.",
		Sections: []changeSection{
			{"Added", []string{
				"Secondary domain support: add a domain on the System page and the panel, every project's API (project.yourdomain), and the status page all serve on it - HTTPS certificates issue automatically on first visit. The original domain keeps working forever, so nothing connected to it can break.",
				"Connection strings on the new domain use db.<domain> for Postgres, deliberately separate from the web hostnames - that split lets you put the web side behind a proxy/CDN later while database traffic stays direct.",
				"Optional redirect: once you trust the new domain, one checkbox makes browsers visiting the old panel land on the new one (APIs and database connections are never redirected).",
				"Project cards show the new-domain connection strings with the legacy ones one click away.",
			}},
		},
	},
	{
		Version: "1.2.17", Date: "2026-08-22",
		Summary: "Every internal log is now bounded, and deleted projects clean up fully.",
		Sections: []changeSection{
			{"Changed", []string{
				"The audit log and edge-function logs are now bounded (90 days / 20,000 entries and 30 days / 200 per project respectively). Previously both grew forever - a crash-looping function could write unlimited log rows.",
				"Deleting a project now also moves its backup dumps to a trash area (kept 7 days, then purged) and a one-time sweep does the same for dumps of projects deleted in the past. Backups of deleted projects previously lingered invisibly.",
				"Edge-function dependency downloads now cache in a managed location that is pruned automatically past 500MB.",
			}},
		},
	},
	{
		Version: "1.2.16", Date: "2026-08-22",
		Summary: "The server tunes itself to its hardware, and every log is bounded.",
		Sections: []changeSection{
			{"Changed", []string{
				"Postgres memory settings now auto-tune to the server's RAM (a 4GB box runs 512MB shared buffers instead of a hardcoded 768MB, freeing about 250MB), autovacuum is calmed for many-database hosts, and the database container gets a memory backstop.",
				"The connection pooler now caps per-project server connections (20) with smaller per-user pools, so many busy projects queue at the pooler instead of exhausting the whole cluster.",
				"Idle direct database connections now close after 30 minutes (per project role); application connection pools reconnect transparently. Frees the RAM that permanently-parked connections were pinning.",
				"Nightly backups moved from 09:00 to 03:00 IST - out of Indian daytime hours.",
				"All containers now rotate their logs (10MB x 3) - previously unbounded.",
			}},
			{"Fixed", []string{
				"The WAL archiver can no longer wedge permanently after a crash-retry: re-archiving an identical, already-archived segment now succeeds instead of failing forever (which could fill the disk). Archive logic now lives in a host-managed script, fixable without touching the database container.",
			}},
		},
	},
	{
		Version: "1.2.15", Date: "2026-08-22",
		Summary: "One tenant can no longer take down the box, and the status page is yours to brand.",
		Sections: []changeSection{
			{"Added", []string{
				"Edge Functions are now resource-capped: each invocation gets a 128MB memory limit and at most 4 run concurrently (a 5th waits briefly, then gets a clean 429 retry signal). Previously a single hungry or looping function could exhaust the whole server's RAM.",
				"The public status page title is customizable (System page) - put your company's name in front of your clients.",
				"The control plane itself now runs under a memory ceiling, so even a pathological case restarts one service in seconds instead of freezing the host.",
			}},
			{"Changed", []string{
				"New projects get a direct-connection limit of 10 (was 20) - the pooled port multiplexes far beyond it, and this doubles how many projects fit per server. Existing projects keep their current limit; the panel now accepts 1-100.",
				"Creating a project now warns when the combined connection limits approach the cluster's capacity.",
			}},
		},
	},
	{
		Version: "1.2.14", Date: "2026-08-22",
		Summary: "Smarter backups that skip unchanged databases, and a real retention panel.",
		Sections: []changeSection{
			{"Added", []string{
				"Skip-unchanged backups: a database whose data and schema have not changed since its last dump is no longer re-dumped every night (with a weekly forced full as a safety valve). Sleeping and idle projects now cost essentially nothing in backup storage.",
				"Backup retention panel: daily dumps kept, weekly dumps kept, and standing snapshots are now editable on the Backups page, which also shows how much disk each backup tier uses.",
			}},
			{"Fixed", []string{
				"Update detection is now real-time: the version check reads GitHub's live tag list, so a release published seconds ago is detected immediately (the changelog file GitHub serves can lag a few minutes).",
				"A manual \"Check for updates\" now also refreshes the sidebar update dot.",
				"The sleeping badge and Sleep button now use a proper moon icon instead of an emoji, matching the rest of the interface.",
			}},
		},
	},
	{
		Version: "1.2.13", Date: "2026-08-22",
		Summary: "A public status page for your platform.",
		Sections: []changeSection{
			{"Added", []string{
				"Public status page at status.<your-domain>: overall platform health, 30-day uptime bars built from the platform's own heartbeat, and live per-service health - auto-refreshing, no login needed. Point your users at it during incidents.",
				"Privacy first: projects appear on the status page only after you opt them in from their Settings page. Nothing is exposed by default.",
				"Custom status domain: serve the same page at your own hostname (e.g. status.mycompany.com) - set it on the System page, point DNS, HTTPS is automatic.",
			}},
		},
	},
	{
		Version: "1.2.12", Date: "2026-08-22",
		Summary: "You always know when an update exists, sleep is visible, and lots of polish.",
		Sections: []changeSection{
			{"Added", []string{
				"Update awareness: ForgeBase checks for new releases in the background and shows a pulsing dot on System in the sidebar the moment one exists - no more clicking \"Check for updates\". Discord gets one ping per new version too.",
				"Optional auto-install: a checkbox on the System page installs new releases automatically between 03:00-05:00 UTC, with the usual health-check and rollback. Off by default - updates wait for your click.",
				"Sleeping projects are visible: a moon badge on the dashboard, plus \"Sleep now\" and \"Wake\" buttons to park or rouse a project instantly.",
				"Monitoring charts now offer 24-hour, 7-day, and 30-day views.",
				"Creating a project with a taken name now just works: \"profitzon\" taken becomes \"profitzon-2\" automatically.",
			}},
			{"Changed", []string{
				"The point-in-time recovery picker got a proper design: styled date-time input, one-click presets (5 min ago, 1 hour ago, yesterday), and it now shows the actual restorable window instead of an outdated \"7 days\" claim.",
				"The Branches page now states plainly that a branch is currently a full copy (2x storage) - and that instant copy-on-write branching is in active development.",
			}},
		},
	},
	{
		Version: "1.2.11", Date: "2026-08-22",
		Summary: "Login hardening against brute-force attacks, Discord alerts, and always-warm pinned projects.",
		Sections: []changeSection{
			{"Security", []string{
				"Panel login now has a per-account lockout: 5 failed attempts on an account lock it for 15 minutes, even with the correct password afterward. This is the defense per-IP limits cannot provide against botnets that try one password per IP.",
				"Every failed login now costs the attacker 1 second, rising to 4 seconds platform-wide while a distributed attack is underway.",
				"The fail2ban firewall jail for the panel is now part of the product (tighter: 3 attempts = 1 hour IP ban, escalating for repeat offenders up to a week) and installs automatically wherever fail2ban is present.",
			}},
			{"Added", []string{
				"Discord alerts: paste a webhook URL on the System page and ForgeBase pings your Discord when something needs attention - starting with login brute-force waves (with more alert types coming).",
				"\"Remember me for 7 days\" checkbox at login; sessions stay 12 hours without it.",
				"\"Keep always awake\" projects are now also kept WARM: their API process and realtime listener are never stopped for idleness, so pinned projects (your production apps) never pay a cold start.",
			}},
		},
	},
	{
		Version: "1.2.10", Date: "2026-08-22",
		Summary: "Sleep mode: idle projects cost nothing and wake on any request.",
		Sections: []changeSection{
			{"Changed", []string{
				"Idle projects now go to sleep instead of being suspended. Sleep releases everything that costs resources - the API process, the realtime listener, cached connections - but NEVER blocks logins and never touches data. A sleeping project wakes automatically on the next API call, panel visit, or direct database connection (direct connections are accepted instantly; the project is marked awake within minutes). Manual Pause remains the explicit hard lockout.",
				"The idle window is now 7 days (down from 14) and configurable on the project Settings page - sleep after N hours idle, or 0 to never sleep.",
			}},
			{"Added", []string{
				"\"Keep always awake\": pin any project (for example a production app) so it is never auto-slept, from its Settings page.",
			}},
			{"Fixed", []string{
				"The per-request activity tracker costs one database round-trip instead of two.",
			}},
		},
	},
	{
		Version: "1.2.9", Date: "2026-08-22",
		Summary: "Idle projects now release their realtime resources.",
		Sections: []changeSection{
			{"Changed", []string{
				"Realtime and webhook listeners are now released when idle: a project whose realtime stream has had no subscribers for 15 minutes (and has no webhooks) gives back its dedicated database connection, and it comes back automatically on the next subscriber. Previously every project that ever used Realtime or webhooks held a database connection forever.",
				"Removing a project's last webhook, or disabling Realtime, now also releases the listener immediately when nothing else needs it.",
			}},
			{"Fixed", []string{
				"Idle-project detection now only counts real client connections. The platform's own internal connections previously counted as activity, which could keep an unused project marked active forever.",
			}},
		},
	},
	{
		Version: "1.2.8", Date: "2026-08-22",
		Summary: "Backups now use a fraction of the disk.",
		Sections: []changeSection{
			{"Changed", []string{
				"Backup retention is now tiered: the newest 7 daily dumps per database plus one weekly dump for 4 weeks are kept, instead of 30 full nightly dumps. Cluster snapshots for point-in-time recovery are trimmed from 7 standing copies to 2 (older restores use the daily and weekly dumps). NOTE: the first nightly backup after this update deletes the now-out-of-policy older backups - typically freeing many GB - and the off-box mirror follows the same policy. Copy any dump you want to keep forever somewhere else before the next nightly run.",
				"Retention is enforced BEFORE the nightly backup work as well as after, so a failed dump or a full disk can never skip cleanup again.",
			}},
			{"Fixed", []string{
				"A monthly restore-drill failure could leave a full-size test database in the cluster forever (which then got backed up nightly at full size). It is now always cleaned up, is excluded from backups by name, and the drill skips safely when disk space is low.",
				"An empty retention settings file could silently abort the whole nightly backup before any cleanup ran.",
				"Point-in-time-recovery working files and deleted-project dump remnants are now cleaned up automatically.",
			}},
		},
	},
	{
		Version: "1.2.7", Date: "2026-08-22",
		Summary: "Data API isolation hardening and instant update checks.",
		Sections: []changeSection{
			{"Security", []string{
				"Data API startup now cryptographically verifies it is talking to the right project's API process before routing any request to it. Previously, in a rare port-reuse scenario on a long-running server, requests could have been routed to a different project's API.",
			}},
			{"Fixed", []string{
				"\"Check for updates\" now always sees a just-published release immediately instead of waiting up to 5 minutes for GitHub's cache.",
				"Fixed a data race in the Data API idle tracker.",
				"Metrics collection is much lighter: one query instead of three per project, sleeping projects are sampled hourly instead of every 5 minutes, and history cleanup runs hourly on an index instead of scanning the whole table every 5 minutes.",
			}},
		},
	},
	{
		Version: "1.2.6", Date: "2026-08-22",
		Summary: "Updates now maintain the whole server, and the database can no longer fill the disk.",
		Sections: []changeSection{
			{"Added", []string{
				"Self-updates now also refresh the server-side components (backup scripts and scheduled maintenance jobs), not just the app binary. Fixes to those land automatically with every update instead of requiring shell access.",
			}},
			{"Fixed", []string{
				"Database write-ahead-log safety settings are applied automatically on every start: the live WAL is bounded to 1GB and compressed. This prevents the class of incident where heavy activity (a large clone or import) could fill the disk and take the database down.",
				"The hourly backup-archive cleanup job is now installed correctly on fresh installs; previously it was shipped but never activated.",
				"The updater now cleans up its build caches and temporary files, which previously grew without limit with every update.",
			}},
		},
	},
	{
		Version: "1.2.5", Date: "2026-08-21",
		Summary: "Self-update never gets stuck, and no more 502 during restart.",
		Sections: []changeSection{
			{"Fixed", []string{
				"A self-update that wedged or never launched could leave the System page pinned on \"updating...\" forever and block all future updates as \"already in progress\". The in-progress state now also requires the update log to be recent, so a stale update is treated as finished and never locks updates out.",
				"During a self-update the panel restarts for 1-3 seconds; a request arriving in that window could return a 502. The HTTPS proxy now briefly retries the control plane instead, so the restart no longer surfaces an error page.",
			}},
		},
	},
	{
		Version: "1.2.4", Date: "2026-08-21",
		Summary: "Clear \"updating\" state on the self-update.",
		Sections: []changeSection{
			{"Fixed", []string{
				"Clicking \"Update now\" now clearly shows that an update is in progress: the button becomes a spinner immediately, and the System page switches to an \"updating...\" state with a live update log that refreshes on its own until the update finishes.",
				"You can no longer start a second update while one is already running - the button is hidden during an update, and a duplicate request (from another tab) is rejected instead of launching a concurrent updater.",
			}},
		},
	},
	{
		Version: "1.2.3", Date: "2026-08-18",
		Summary: "Live progress on background operations.",
		Sections: []changeSection{
			{"Fixed", []string{
				"Pages with a running background operation now update on their own instead of sitting on a stale status until you manually reload. The Sync/Clone page, the Projects dashboard (while a project is cloning), and the System page during a self-update refresh automatically until the operation finishes. The operations themselves always completed - only the page was stale.",
			}},
		},
	},
	{
		Version: "1.2.2", Date: "2026-08-18",
		Summary: "Invite users by email.",
		Sections: []changeSection{
			{"Added", []string{
				"Invite end users by email (needs SMTP): POST /auth/v1/admin/invite with the service_role key creates the account and emails a one-time sign-in link.",
			}},
		},
	},
	{
		Version: "1.2.1", Date: "2026-08-18",
		Summary: "Password reset and magic-link sign-in.",
		Sections: []changeSection{
			{"Added", []string{
				"Self-service password reset (needs SMTP): POST /auth/v1/recover emails a reset link that opens a set-a-new-password page and signs out other sessions.",
				"Magic-link, passwordless sign-in (needs SMTP): POST /auth/v1/magiclink emails a one-time sign-in link.",
			}},
		},
	},
	{
		Version: "1.2.0", Date: "2026-08-18",
		Summary: "Optional email: SMTP + email confirmation.",
		Sections: []changeSection{
			{"Added", []string{
				"Optional SMTP email. Configure an SMTP server per project on the Auth page to send transactional emails; it stays off (and nothing changes) until you set it.",
				"Email confirmation. When SMTP is set you can require confirmation before sign-in: new sign-ups receive a confirmation link and cannot log in until they click it.",
			}},
		},
	},
	{
		Version: "1.1.9", Date: "2026-08-18",
		Summary: "Admin user management and bans.",
		Sections: []changeSection{
			{"Added", []string{
				"Admin user-management API using the service_role key at /auth/v1/admin/users: list, create, get, update, and delete users, plus ban and unban (ban_duration). Banned users cannot sign in and their sessions are revoked.",
			}},
			{"Fixed", []string{
				"Auth schema additions (metadata, session families, bans) now apply to already-enabled projects automatically on startup, so updating never leaves an existing auth project on an old schema.",
			}},
		},
	},
	{
		Version: "1.1.8", Date: "2026-08-18",
		Summary: "More sign-in providers.",
		Sections: []changeSection{
			{"Added", []string{
				"GitLab and Discord are now available as end-user OAuth sign-in providers, alongside Google and GitHub. Configure the client id and secret on the Auth page.",
			}},
		},
	},
	{
		Version: "1.1.7", Date: "2026-08-18",
		Summary: "Stronger session security.",
		Sections: []changeSection{
			{"Security", []string{
				"Refresh-token reuse detection. Tokens rotate on use; if an already-used token is replayed - a strong sign it was stolen - the entire session lineage is revoked, not just that one token.",
				"Global sign-out: POST /auth/v1/logout?scope=global revokes every active session for the user, not only the current one.",
			}},
		},
	},
	{
		Version: "1.1.6", Date: "2026-08-18",
		Summary: "User and app metadata on accounts.",
		Sections: []changeSection{
			{"Added", []string{
				"End-user accounts now carry user_metadata and app_metadata. Pass user_metadata as \"data\" at sign-up, read or update it via GET and PUT /auth/v1/user, and it is embedded in the access token so your app and RLS policies (via auth.jwt()) can read it. app_metadata is admin-controlled.",
				"Access tokens now include the standard aud (\"authenticated\") claim.",
			}},
		},
	},
	{
		Version: "1.1.5", Date: "2026-08-18",
		Summary: "Richer, more reliable webhooks.",
		Sections: []changeSection{
			{"Added", []string{
				"Webhook payloads now include old_record (the row's previous values on update and delete), so consumers can diff changes.",
				"Webhooks support a custom HTTP method (POST/PUT/PATCH) and a custom header (for example an Authorization token to the target).",
			}},
			{"Changed", []string{
				"Webhook delivery retries over a longer window - up to 5 attempts across about 7 minutes - so a target that is briefly down still receives the event.",
			}},
		},
	},
	{
		Version: "1.1.4", Date: "2026-08-18",
		Summary: "Realtime and Webhooks reliability fixes.",
		Sections: []changeSection{
			{"Fixed", []string{
				"Disabling Realtime no longer silently stops Webhooks. The two share a change-capture trigger, which is now kept as long as either feature needs it.",
				"Realtime and Webhooks now cover tables created after they were enabled: an event trigger auto-attaches change capture to new tables, so you no longer have to re-scan after adding a table.",
			}},
		},
	},
	{
		Version: "1.1.3", Date: "2026-08-18",
		Summary: "Safer Realtime + column filters.",
		Sections: []changeSection{
			{"Security", []string{
				"Realtime now requires an authenticated (or service) key by default. The stream is not per-row RLS filtered, so the public anon key could otherwise read every change. A toggle on the Realtime page allows the anon key per project when the data is not sensitive.",
			}},
			{"Added", []string{
				"Realtime column-equality subscription filter, e.g. subscribe with ?filter=id=eq.5 to receive only changes to matching rows.",
			}},
		},
	},
	{
		Version: "1.1.2", Date: "2026-08-18",
		Summary: "Per-bucket storage limits.",
		Sections: []changeSection{
			{"Added", []string{
				"Storage buckets can set a maximum file size (MB) and an allowed MIME-type list when created. Both the panel upload and the client upload API reject files that are too large or of the wrong type.",
			}},
		},
	},
	{
		Version: "1.1.1", Date: "2026-08-18",
		Summary: "Edge Functions can require a JWT.",
		Sections: []changeSection{
			{"Security", []string{
				"Edge Functions can now require a valid JWT to invoke. New functions default to JWT-required (toggle it per function on the Functions page); functions that were already deployed keep their current public setting. This closes a footgun where a public function using the injected service-role key was reachable by anonymous callers.",
			}},
		},
	},
	{
		Version: "1.1.0", Date: "2026-08-18",
		Summary: "Clearer update notes, and the foundation for copy-on-write branching.",
		Sections: []changeSection{
			{"Changed", []string{
				"The in-app update check now shows the real release notes from the changelog instead of raw developer commit messages, and tracks updates by version.",
			}},
			{"Added", []string{
				"Experimental, opt-in per-instance mode (enable with INSTANCES=1 at install): each project or branch runs as its own Postgres instance on a copy-on-write filesystem, which makes branching instant (no parent downtime) and lets idle projects scale to zero and wake on the next connection. Not yet wired into the panel; driven by the bundled tools for now.",
			}},
		},
	},
	{
		Version: "1.0.3", Date: "2026-08-17",
		Summary: "Real point-in-time recovery, and RLS-gated writes that actually work.",
		Sections: []changeSection{
			{"Added", []string{
				"Point-in-time recovery: from the Backup page, restore a project to any instant down to the second into a new project. It replays the continuously archived WAL forward from a basebackup to the moment you pick, non-destructively.",
			}},
			{"Fixed", []string{
				"RLS write policies now work end to end. Adding an \"authenticated write\" or \"owner\" policy also grants the authenticated role the table's write privileges and sequence usage, so signed-in users can write their own rows through the API (previously the write was denied before the policy was ever checked).",
			}},
		},
	},
	{
		Version: "1.0.2", Date: "2026-08-17",
		Summary: "Self-update reliability fix.",
		Sections: []changeSection{
			{"Fixed", []string{
				"The one-click self-update now builds correctly: the updater runs in an isolated environment and previously could not find the Go module cache, so the rebuild failed and the update was skipped (the running version was left untouched, with no downtime). It sets the toolchain environment explicitly now.",
			}},
		},
	},
	{
		Version: "1.0.1", Date: "2026-08-17",
		Summary: "Project hygiene for the public repository.",
		Sections: []changeSection{
			{"Added", []string{
				"Continuous integration that builds, vets, and format-checks the control plane on every push and pull request.",
				"Contributor guide, security policy, code of conduct, and issue and pull-request templates.",
			}},
			{"Changed", []string{
				"Formatted the whole control plane with gofmt and pinned Go sources to LF line endings.",
			}},
		},
	},
	{
		Version: "1.0.0", Date: "2026-08-17",
		Summary: "First public release. A lightweight, self-hosted Postgres backend platform in a single Go binary.",
		Sections: []changeSection{
			{"Added", []string{
				"In-app self-update: check GitHub for a newer build, read the changelog, and install it with one click, with an atomic binary swap, health check, and automatic rollback. Admin only, opt-in, audit-logged.",
				"This What's New page: the full version history plus a catalog of every feature.",
				"Public repository and a one-command installer that prompts for a domain, verifies DNS, and provisions Let's Encrypt TLS automatically.",
			}},
			{"Changed", []string{
				"Rebranded to ForgeBase across the panel, docs, and installer.",
				"Backups copy now describes exactly what the daily restore does, separate from continuous WAL and point-in-time recovery.",
			}},
			{"Security", []string{
				"Edge functions no longer inherit the host environment: they see only their own scoped secrets plus the project URL and keys.",
			}},
		},
	},
	{
		Version: "0.9.0", Date: "2026-08",
		Summary: "Depth for the streaming and eventing surfaces.",
		Sections: []changeSection{
			{"Added", []string{
				"Realtime per-subscription filters: subscribe to a specific table and event instead of one global change firehose.",
				"Webhook HMAC signing, automatic retries with backoff, and a per-endpoint delivery log.",
				"Edge-function scoped secrets and a per-function log viewer.",
			}},
		},
	},
	{
		Version: "0.8.0", Date: "2026-08",
		Summary: "Client-facing auth and storage APIs.",
		Sections: []changeSection{
			{"Added", []string{
				"End-user auth refresh tokens with rotation and revocation.",
				"Client-facing Storage API authenticated by the project JWT: upload, download, delete, and signed URLs, with per-object path authorization.",
			}},
			{"Changed", []string{
				"Backups now include the storage and edge-function file planes, not just the database.",
			}},
		},
	},
	{
		Version: "0.7.0", Date: "2026-08",
		Summary: "Editor depth for day-to-day data work.",
		Sections: []changeSection{
			{"Added", []string{
				"Table editor: row pagination, in-panel schema editing, typed input widgets, and CSV export.",
				"SQL editor: saved queries and history, CSV and JSON export, and run-as-role to test policies.",
			}},
		},
	},
	{
		Version: "0.6.0", Date: "2026-08",
		Summary: "Secure-by-default Row Level Security.",
		Sections: []changeSection{
			{"Added", []string{
				"One-click Enable RLS per table with starter policy templates.",
				"SQL auth helpers: auth.uid(), auth.jwt(), auth.role(), and auth.email().",
			}},
			{"Changed", []string{
				"GraphQL propagates the request JWT claims and enforces a depth limit and statement timeout.",
				"PostgREST reloads its schema cache after DDL so new tables appear immediately.",
			}},
		},
	},
	{
		Version: "0.5.0", Date: "2026-07",
		Summary: "Full mobile layout.",
		Sections: []changeSection{
			{"Added", []string{
				"The sidebar collapses into an off-canvas drawer on phones, cards stack to one column, and wide tables scroll in place. No horizontal page scroll.",
			}},
		},
	},
	{
		Version: "0.4.0", Date: "2026-07",
		Summary: "Security hardening.",
		Sections: []changeSection{
			{"Security", []string{
				"External database access is TLS-only.",
				"Server access moved to SSH key authentication.",
			}},
			{"Added", []string{
				"Per-project CREATEDB toggle and editable connection limits from the panel.",
			}},
		},
	},
	{
		Version: "0.3.0", Date: "2026-06",
		Summary: "The full platform surface.",
		Sections: []changeSection{
			{"Added", []string{
				"End-user Auth with email, password, and OAuth providers.",
				"Storage buckets, Realtime change streams, Database Webhooks, and Edge Functions.",
				"Backups (nightly dumps, WAL, basebackups, verified restore test, off-box S3).",
				"Monitoring, Branches, Sync and Clone, and a Team and Audit log.",
			}},
		},
	},
	{
		Version: "0.2.0", Date: "2026-06",
		Summary: "APIs on top of every project.",
		Sections: []changeSection{
			{"Added", []string{
				"Auto Data API (PostgREST) with anon and service JWT keys.",
				"GraphQL API (pg_graphql) and per-project docs with client snippets.",
			}},
		},
	},
	{
		Version: "0.1.0", Date: "2026-05",
		Summary: "The core platform.",
		Sections: []changeSection{
			{"Added", []string{
				"Multi-tenant Postgres 17 cluster with a per-project database and role, PgBouncer pooling, and Caddy on-demand TLS.",
				"Panel authentication, project lifecycle, a table editor, and a SQL editor.",
			}},
		},
	},
}

// The feature catalog is the "all features" half of the What's New page: what
// ForgeBase can do today, grouped by area.

type featureItem struct{ Name, Desc string }
type featureGroup struct {
	Name  string
	Items []featureItem
}

var featureCatalog = []featureGroup{
	{"Database", []featureItem{
		{"Managed Postgres 17", "A per-project database and role on a shared, pooled cluster."},
		{"Table editor", "Browse, edit, paginate, import and export rows, and change the schema from the panel."},
		{"SQL editor", "Run queries with history, export, run-as-role, and a statement timeout."},
		{"Extensions and admin", "Toggle extensions, rotate passwords, and tune connection limits."},
	}},
	{"APIs", []featureItem{
		{"REST Data API", "An auto-generated PostgREST API for every project, with anon and service keys."},
		{"GraphQL", "pg_graphql with JWT claims, a depth limit, and a statement timeout."},
		{"Auto docs", "Connection strings and copy-paste client snippets per project."},
	}},
	{"Auth and security", []featureItem{
		{"End-user auth", "Email, password, and OAuth with JWT access and rotating refresh tokens."},
		{"Row Level Security", "One-click RLS with policy templates and auth.uid() style helpers."},
		{"Team and audit", "Owner, admin, and member roles with a full actor and IP audit trail."},
	}},
	{"Storage and files", []featureItem{
		{"Object storage", "Public and private buckets with panel and client upload APIs."},
		{"Signed URLs", "Mint time-limited download links, with per-object path authorization."},
	}},
	{"Realtime and events", []featureItem{
		{"Realtime", "WebSocket change streams with per-subscription filters."},
		{"Webhooks", "HMAC-signed database webhooks with retries and a delivery log."},
		{"Edge Functions", "Deno functions with scoped secrets and per-function logs."},
	}},
	{"Platform", []featureItem{
		{"Backups and PITR", "Nightly dumps, continuous WAL, basebackups, and a verified restore test."},
		{"Branches", "Spin up a copy of a project database."},
		{"Sync and Clone", "Import from an external Postgres and keep it in sync."},
		{"Monitoring", "Host and per-project resource stats."},
		{"Self-update", "Check for and install new versions from inside the panel."},
	}},
}

func (a *app) changelogPage(w http.ResponseWriter, r *http.Request) {
	content := renderContent(changelogBody, map[string]any{
		"Releases":   releases,
		"Features":   featureCatalog,
		"AppVersion": appVersion,
		"Build":      version,
	})
	a.renderShell(w, r, shellData{Title: "What's New", Nav: "changelog",
		Crumbs: []crumb{{Label: "What's New"}}}, content)
}
