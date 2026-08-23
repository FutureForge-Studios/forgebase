package main

import (
	"database/sql"
	"net/http"
	"strings"
	"sync/atomic"
)

// Dual-domain support. The platform's original domain (cfg.domain) keeps
// working forever - existing apps' connection strings must never break. A
// secondary domain (settings key domain_secondary, e.g. base.ffstudios.io)
// can be added at runtime: the panel, project APIs (slug.<domain>), and the
// status page all answer on it, and TLS certificates are issued on demand once
// tlsCheck approves the hostnames.
//
// New-domain connection strings use db.<secondary> for Postgres instead of the
// apex. That split is deliberate: it lets the HTTP hostnames (apex + wildcard)
// move behind a CDN proxy later, while db.<secondary> stays a direct DNS
// record - proxies cannot carry the Postgres protocol.

var secDomainCache atomic.Value // string; "" = unset

func (a *app) secondaryDomain() string {
	if v := secDomainCache.Load(); v != nil {
		return v.(string)
	}
	var s string
	err := a.db.QueryRow(`SELECT value FROM settings WHERE key='domain_secondary'`).Scan(&s)
	s = strings.TrimSpace(strings.ToLower(s))
	if err != nil && err != sql.ErrNoRows {
		// transient DB error: return without caching, so one failed query at
		// boot cannot pin the secondary domain to "" for the process lifetime
		return s
	}
	secDomainCache.Store(s)
	return s
}

// panelRedirectOn: when enabled (and a secondary domain exists), browser GETs
// to the OLD panel apex 302 to the new domain. Only the human-facing panel -
// project subdomain traffic (APIs) and non-GET requests are never redirected.
func (a *app) panelRedirectOn() bool {
	return a.settingOn("panel_redirect")
}

// hostMatchesDomain reports whether host belongs to domain (apex or subdomain)
// and returns the subdomain part ("" for the apex).
func hostMatchesDomain(host, domain string) (string, bool) {
	if domain == "" {
		return "", false
	}
	if host == domain {
		return "", true
	}
	if strings.HasSuffix(host, "."+domain) {
		return strings.TrimSuffix(host, "."+domain), true
	}
	return "", false
}

func (a *app) setSecondaryDomain(w http.ResponseWriter, r *http.Request) {
	d := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))
	if d != "" && (!hostRe.MatchString(d) || d == a.cfg.domain ||
		strings.HasSuffix(d, "."+a.cfg.domain) || strings.HasSuffix(a.cfg.domain, "."+d)) {
		redirectErr(w, r, "/system", "Enter a bare domain like base.example.com - different from the current domain and not a subdomain of it (subdomains of the primary are project addresses).")
		return
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('domain_secondary',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, d)
	secDomainCache.Store(d)
	redir := "0"
	if r.FormValue("redirect") == "on" && d != "" {
		redir = "1"
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('panel_redirect',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, redir)
	a.audit(r, "secondary-domain", d)
	if d == "" {
		redirectMsg(w, r, "/system", "Secondary domain removed. The primary domain keeps serving everything.")
		return
	}
	redirectMsg(w, r, "/system", "Legacy domain alias active: "+d+" and *."+d+" keep serving existing links and connection strings; with the redirect on, browsers landing on its panel move to the primary. Certificates are issued automatically on first visit.")
}

// dbHostForDisplay is the hostname shown in connection strings. With a
// secondary (legacy) domain configured, database traffic standardizes on
// db.<primary> - a plain DNS record that stays direct even if the HTTP
// hostnames move behind a proxy. Single-domain installs keep the apex.
func (a *app) dbHostForDisplay() string {
	if a.secondaryDomain() != "" {
		return "db." + a.cfg.domain
	}
	return a.cfg.domain
}
