package main

import "net/http"

// securityHeaders wraps every response with standard hardening headers. The
// panel (apex host) also gets a Content-Security-Policy; API/storage/auth
// responses on project subdomains skip CSP so they stay usable by any client.
func (a *app) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		host := r.Host
		if i := indexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		if host == a.cfg.domain {
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy",
				"default-src 'self'; "+
					"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
					"font-src https://fonts.gstatic.com; "+
					"script-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data:; "+
					"connect-src 'self' https://*."+a.cfg.domain+" wss://*."+a.cfg.domain+"; "+
					"frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// ----------------------------------------------------------------- roles

func roleRank(role string) int {
	switch role {
	case "owner":
		return 3
	case "admin":
		return 2
	default:
		return 1 // member
	}
}

// userRole returns the current panel user's role. The break-glass env admin is
// always treated as owner.
func (a *app) userRole(r *http.Request) string {
	name := currentUser(r)
	if name == a.cfg.panelUser {
		return "owner"
	}
	var role string
	a.db.QueryRow(`SELECT role FROM users WHERE name=$1`, name).Scan(&role)
	if role == "" {
		return "member"
	}
	return role
}

func (a *app) atLeast(r *http.Request, min string) bool {
	return roleRank(a.userRole(r)) >= roleRank(min)
}

// requireRole gates a handler: members are refused admin/owner-only actions.
func (a *app) requireRole(min string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.atLeast(r, min) {
			redirectErr(w, r, "/", "You need the "+min+" role to do that.")
			a.auditRaw(currentUser(r), clientIP(r), "denied", r.URL.Path)
			return
		}
		next(w, r)
	}
}

// proj wraps every /p/{slug}/... handler: it validates the slug shape and
// confirms the project exists before the handler runs. This is the single choke
// point that stops a request for a non-existent (or malformed) slug from
// reaching dbFor and leaking a cached *sql.DB into projConns.
func (a *app) proj(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		if !slugRe.MatchString(slug) || !a.projectExists(slug) {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}
