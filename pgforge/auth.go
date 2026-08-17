package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func ctxWith(r *http.Request, k ctxKey, v string) context.Context {
	return context.WithValue(r.Context(), k, v)
}

// ----------------------------------------------------------------- sessions
//
// Cookie value: base64(name) . exp . hmac("session:"+exp+":"+b64name). The name
// is display-only; the HMAC is what authorises. A separate break-glass path
// accepts the env PANEL_USER/PANEL_PASS so we can never be locked out.

func (a *app) sign(msg string) string {
	m := hmac.New(sha256.New, a.cfg.secret)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *app) setSession(w http.ResponseWriter, r *http.Request, name string) {
	exp := strconv.FormatInt(time.Now().Add(12*time.Hour).Unix(), 10)
	b64 := base64.RawURLEncoding.EncodeToString([]byte(name))
	val := b64 + "." + exp + "." + a.sign("session:"+exp+":"+b64)
	http.SetCookie(w, &http.Cookie{
		Name: "pgforge_session", Value: val, Path: "/",
		// Secure only when the request is actually HTTPS, so the cookie works
		// behind Caddy (production) and on a plain-HTTP dev/test box alike.
		HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: 12 * 3600,
	})
}

// hashPassword bcrypts a password, rejecting inputs over bcrypt's 72-byte limit
// (x/crypto returns an error there; discarding it stored an empty hash and
// locked the account out permanently).
func hashPassword(pw string) (string, error) {
	if len(pw) > 72 {
		return "", bcrypt.ErrPasswordTooLong
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func (a *app) sessionName(r *http.Request) (string, bool) {
	c, err := r.Cookie("pgforge_session")
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(c.Value, ".", 3)
	if len(parts) != 3 {
		return "", false
	}
	b64, exp, sig := parts[0], parts[1], parts[2]
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > e {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(a.sign("session:"+exp+":"+b64)), []byte(sig)) != 1 {
		return "", false
	}
	nb, _ := base64.RawURLEncoding.DecodeString(b64)
	return string(nb), true
}

// currentUser reads the display name off the request (set by middleware).
type ctxKey int

const userKey ctxKey = 0

func currentUser(r *http.Request) string {
	if v, ok := r.Context().Value(userKey).(string); ok {
		return v
	}
	return ""
}

func (a *app) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, ok := a.sessionName(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// A valid HMAC isn't enough: confirm the account still exists, so a
		// removed member's outstanding 12h cookie stops working immediately
		// instead of retaining full access. The break-glass env admin has no DB
		// row and is exempt.
		if name != a.cfg.panelUser {
			var exists bool
			a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE name=$1)`, name).Scan(&exists)
			if !exists {
				http.SetCookie(w, &http.Cookie{Name: "pgforge_session", Value: "", Path: "/", MaxAge: -1})
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
		}
		r = r.WithContext(ctxWith(r, userKey, name))
		next(w, r)
	}
}

// ----------------------------------------------------------------- login

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.sessionName(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	a.renderAuth(w, "Welcome back", "Sign in to your ForgeBase dashboard",
		renderContent(loginForm, map[string]any{"Err": r.URL.Query().Get("m"), "NoUsers": n == 0}))
}

func (a *app) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if a.rateLimited(ip) {
		a.renderAuth(w, "Slow down", "", renderContent(loginForm, map[string]any{"Err": "Too many attempts. Wait 10 minutes."}))
		return
	}
	id := strings.TrimSpace(r.FormValue("email"))
	pass := r.FormValue("pass")

	// 1) real user by email or name
	var name, hash string
	err := a.db.QueryRow(`SELECT name, pass_hash FROM users WHERE lower(email)=lower($1) OR lower(name)=lower($1)`, id).Scan(&name, &hash)
	if err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil {
		a.setSession(w, r, name)
		a.auditRaw(name, ip, "login", "panel")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// 2) break-glass env admin
	if subtle.ConstantTimeCompare([]byte(id), []byte(a.cfg.panelUser)) == 1 &&
		subtle.ConstantTimeCompare([]byte(pass), []byte(a.cfg.panelPass)) == 1 {
		a.setSession(w, r, a.cfg.panelUser)
		a.auditRaw(a.cfg.panelUser, ip, "login", "panel (admin)")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.recordAttempt(ip)
	a.auditRaw(id, ip, "login-failed", "panel")
	log.Printf("FAILED LOGIN ip=%s user=%q", ip, id) // parseable for fail2ban
	a.renderAuth(w, "Welcome back", "", renderContent(loginForm, map[string]any{"Err": "Wrong email or password."}))
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "pgforge_session", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ----------------------------------------------------------------- register

func (a *app) registerPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.sessionName(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	if n > 0 {
		// invite-only: registration is closed once the first owner exists
		a.renderAuth(w, "Invite only", "Registration is closed",
			renderContent(closedForm, nil))
		return
	}
	a.renderAuth(w, "Create your account", "Set up the first owner of your ForgeBase platform",
		renderContent(registerForm, map[string]any{"Err": r.URL.Query().Get("m")}))
}

func (a *app) registerSubmit(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	pass := r.FormValue("pass")
	fail := func(msg string) {
		a.renderAuth(w, "Create your account", "",
			renderContent(registerForm, map[string]any{"Err": msg, "Name": name, "Email": email}))
	}
	// invite-only: only the very first account may self-register (becomes owner);
	// everyone else must be invited by an owner from the Team page.
	var existing int
	a.db.QueryRow(`SELECT count(*) FROM users`).Scan(&existing)
	if existing > 0 {
		a.renderAuth(w, "Invite only", "Registration is closed", renderContent(closedForm, nil))
		return
	}
	if name == "" || !strings.Contains(email, "@") || len(pass) < 8 {
		fail("Enter a name, a valid email, and a password of at least 8 characters.")
		return
	}
	var exists bool
	a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, email).Scan(&exists)
	if exists {
		fail("An account with that email already exists.")
		return
	}
	hash, err := hashPassword(pass)
	if err != nil {
		fail("Password is too long (max 72 characters).")
		return
	}
	// Atomic first-owner insert: only succeeds while the users table is still
	// empty, so two concurrent registrations on a fresh deploy can't both become
	// owner (the second inserts 0 rows).
	res, err := a.db.Exec(`INSERT INTO users(name,email,pass_hash,role)
		SELECT $1,$2,$3,'owner' WHERE NOT EXISTS (SELECT 1 FROM users)`,
		name, email, string(hash))
	if err != nil {
		fail("Could not create account: " + err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		a.renderAuth(w, "Invite only", "Registration is closed", renderContent(closedForm, nil))
		return
	}
	a.auditRaw(email, clientIP(r), "register", name)
	a.setSession(w, r, name)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ----------------------------------------------------------------- invites

// parseInvite validates a signed invite token and returns the email if valid.
func (a *app) parseInvite(token string) (string, bool) {
	payload, ok := a.verifyState(token)
	if !ok {
		return "", false
	}
	f := strings.Split(payload, "|")
	if len(f) != 3 || f[0] != "invite" {
		return "", false
	}
	exp, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return f[1], true
}

func (a *app) invitePage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	email, ok := a.parseInvite(token)
	if !ok {
		a.renderAuth(w, "Invite expired", "This invite link is invalid or has expired",
			renderContent(closedForm, nil))
		return
	}
	a.renderAuth(w, "Set your password", "Finish joining as "+email,
		renderContent(inviteForm, map[string]any{"Token": token, "Email": email}))
}

func (a *app) inviteSubmit(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	email, ok := a.parseInvite(token)
	if !ok {
		a.renderAuth(w, "Invite expired", "", renderContent(closedForm, nil))
		return
	}
	pass := r.FormValue("pass")
	if len(pass) < 8 {
		a.renderAuth(w, "Set your password", "Finish joining as "+email,
			renderContent(inviteForm, map[string]any{"Token": token, "Email": email, "Err": "Password must be at least 8 characters."}))
		return
	}
	var name string
	var pending bool
	if a.db.QueryRow(`SELECT name, invite_pending FROM users WHERE lower(email)=$1`, email).Scan(&name, &pending) != nil {
		a.renderAuth(w, "Invite expired", "", renderContent(closedForm, nil))
		return
	}
	// One-time: once accepted, the invite link is dead (it was reusable for its
	// whole 7-day life and could reset the member's password repeatedly).
	if !pending {
		a.renderAuth(w, "Invite already used", "This invite has already been accepted. Use the login page.",
			renderContent(closedForm, nil))
		return
	}
	hash, err := hashPassword(pass)
	if err != nil {
		a.renderAuth(w, "Set your password", "Finish joining as "+email,
			renderContent(inviteForm, map[string]any{"Token": token, "Email": email, "Err": "Password is too long (max 72 characters)."}))
		return
	}
	a.db.Exec(`UPDATE users SET pass_hash=$1, invite_pending=false WHERE lower(email)=$2`, string(hash), email)
	a.auditRaw(email, clientIP(r), "invite-accept", "")
	a.setSession(w, r, name)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ----------------------------------------------------------------- account

func (a *app) accountPage(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	var email, first, last string
	hasRow := a.db.QueryRow(`SELECT email, coalesce(first_name,''), coalesce(last_name,'') FROM users WHERE name=$1`, name).
		Scan(&email, &first, &last) == nil

	type keyRow struct{ ID, Name, Prefix, Created, LastUsed string }
	var keys []keyRow
	if email != "" {
		rows, _ := a.db.Query(`SELECT id, name, token_prefix, to_char(created_at,'Mon DD, YYYY'),
			coalesce(to_char(last_used,'Mon DD, YYYY'),'never') FROM user_api_keys WHERE owner_email=$1 ORDER BY created_at DESC`, email)
		if rows != nil {
			for rows.Next() {
				var k keyRow
				rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Created, &k.LastUsed)
				keys = append(keys, k)
			}
			rows.Close()
		}
	}
	content := renderContent(accountBody, map[string]any{
		"User": name, "Email": email, "First": first, "Last": last,
		"HasRow": hasRow, "Keys": keys, "NewKey": r.URL.Query().Get("k"),
	})
	a.renderShell(w, r, shellData{Title: "Account", Nav: "account",
		Crumbs: []crumb{{Label: "Account"}}}, content)
}

func (a *app) updateProfile(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	first := strings.TrimSpace(r.FormValue("first"))
	last := strings.TrimSpace(r.FormValue("last"))
	if _, err := a.db.Exec(`UPDATE users SET first_name=$1, last_name=$2 WHERE name=$3`, first, last, name); err != nil {
		redirectErr(w, r, "/account", "Could not save profile.")
		return
	}
	a.audit(r, "profile-update", name)
	redirectMsg(w, r, "/account", "Profile saved.")
}

func (a *app) changeEmail(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if !strings.Contains(email, "@") {
		redirectErr(w, r, "/account", "Enter a valid email.")
		return
	}
	if _, err := a.db.Exec(`UPDATE users SET email=$1 WHERE name=$2`, email, name); err != nil {
		redirectErr(w, r, "/account", "That email is already in use.")
		return
	}
	a.audit(r, "change-email", email)
	redirectMsg(w, r, "/account", "Email updated.")
}

func (a *app) createAPIKey(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	label := strings.TrimSpace(r.FormValue("name"))
	if label == "" {
		label = "api key"
	}
	var email string
	if a.db.QueryRow(`SELECT email FROM users WHERE name=$1`, name).Scan(&email) != nil {
		redirectErr(w, r, "/account", "The built-in admin login cannot own API keys; register an account first.")
		return
	}
	token := "pgf_" + randHex(24)
	sum := sha256.Sum256([]byte(token))
	a.db.Exec(`INSERT INTO user_api_keys(owner_email,name,token_prefix,token_hash) VALUES ($1,$2,$3,$4)`,
		email, label, token[:12], hex.EncodeToString(sum[:]))
	a.audit(r, "apikey-create", label)
	redirectMsg(w, r, "/account?k="+token, "API key created - copy it now, it won't be shown again.")
}

func (a *app) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	id := r.FormValue("id")
	var email string
	a.db.QueryRow(`SELECT email FROM users WHERE name=$1`, name).Scan(&email)
	a.db.Exec(`DELETE FROM user_api_keys WHERE id=$1 AND owner_email=$2`, id, email)
	a.audit(r, "apikey-revoke", email)
	redirectMsg(w, r, "/account", "API key revoked.")
}

func (a *app) changePassword(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	cur := r.FormValue("current")
	next := r.FormValue("new")
	if len(next) < 8 {
		redirectErr(w, r, "/account", "New password must be at least 8 characters.")
		return
	}
	var hash string
	err := a.db.QueryRow(`SELECT pass_hash FROM users WHERE name=$1`, name).Scan(&hash)
	if err != nil {
		// env break-glass account has no DB row; tell them to register
		redirectErr(w, r, "/account", "This is the built-in admin login; create a real account on the register page to manage a password.")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(cur)) != nil {
		redirectErr(w, r, "/account", "Current password is incorrect.")
		return
	}
	nh, err := hashPassword(next)
	if err != nil {
		redirectErr(w, r, "/account", "New password is too long (max 72 characters).")
		return
	}
	a.db.Exec(`UPDATE users SET pass_hash=$1 WHERE name=$2`, string(nh), name)
	a.audit(r, "change-password", "")
	redirectMsg(w, r, "/account", "Password updated.")
}

// renderAuth renders a standalone auth page (login/register) with the aurora shell.
func (a *app) renderAuth(w http.ResponseWriter, title, subtitle string, body template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	authTmpl.Execute(w, map[string]any{
		"Title": title, "Subtitle": subtitle, "Body": body, "Brand": brandBurst(true),
	})
}
