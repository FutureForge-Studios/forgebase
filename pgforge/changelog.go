package main

import "net/http"

// appVersion is the human-facing semantic version shown in the UI. The git short
// SHA (version, in version.go) remains the exact build identifier used for the
// commit link and the self-update comparison.
const appVersion = "1.2.15"

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
		Summary: "Neon-style sleep: idle projects cost nothing and wake on any request.",
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
		Summary: "First public release. A lightweight, self-hosted Supabase and Neon alternative in a single Go binary.",
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
