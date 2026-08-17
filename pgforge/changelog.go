package main

import "net/http"

// appVersion is the human-facing semantic version shown in the UI. The git short
// SHA (version, in version.go) remains the exact build identifier used for the
// commit link and the self-update comparison.
const appVersion = "1.0.0"

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
