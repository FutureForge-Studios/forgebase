package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuth sign-in for end users (Google, GitHub). Per project, per provider config
// (client id/secret) lives in oauth_providers. Flow on <slug>.<domain>:
//   /auth/v1/authorize?provider=google&redirect_to=... -> provider consent
//   /auth/v1/callback?code=...&state=... -> exchange, find/create auth.users,
//       mint our JWT, redirect to redirect_to#access_token=...

type oauthMeta struct{ AuthURL, TokenURL, UserURL, Scope string }

var oauthProviders = map[string]oauthMeta{
	"google":  {"https://accounts.google.com/o/oauth2/v2/auth", "https://oauth2.googleapis.com/token", "https://www.googleapis.com/oauth2/v2/userinfo", "openid email profile"},
	"github":  {"https://github.com/login/oauth/authorize", "https://github.com/login/oauth/access_token", "https://api.github.com/user", "read:user user:email"},
	"gitlab":  {"https://gitlab.com/oauth/authorize", "https://gitlab.com/oauth/token", "https://gitlab.com/oauth/userinfo", "openid email"},
	"discord": {"https://discord.com/api/oauth2/authorize", "https://discord.com/api/oauth2/token", "https://discord.com/api/users/@me", "identify email"},
}

func (a *app) oauthConfig(slug, provider string) (id, secret string, enabled bool) {
	a.db.QueryRow(`SELECT client_id, client_secret, enabled FROM oauth_providers WHERE slug=$1 AND provider=$2`,
		slug, provider).Scan(&id, &secret, &enabled)
	return
}

func (a *app) signState(payload string) string {
	b := base64.RawURLEncoding.EncodeToString([]byte(payload))
	m := hmac.New(sha256.New, a.cfg.secret)
	m.Write([]byte(b))
	return b + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *app) verifyState(state string) (string, bool) {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	m := hmac.New(sha256.New, a.cfg.secret)
	m.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return "", false
	}
	b, _ := base64.RawURLEncoding.DecodeString(parts[0])
	return string(b), true
}

// safeOAuthRedirect only permits post-login redirects back to this project's own
// origin (or the panel), so redirect_to can't be pointed at an attacker's site
// to steal the freshly minted user JWT. Empty is allowed (JSON response instead).
func (a *app) safeOAuthRedirect(slug, redirectTo string) bool {
	if redirectTo == "" {
		return true
	}
	u, err := url.Parse(redirectTo)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	return host == slug+"."+a.cfg.domain || host == a.cfg.domain
}

func (a *app) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request, slug string) {
	provider := r.URL.Query().Get("provider")
	meta, ok := oauthProviders[provider]
	if !ok {
		writeJSON(w, 400, map[string]string{"message": "unknown provider"})
		return
	}
	id, _, enabled := a.oauthConfig(slug, provider)
	if !enabled || id == "" {
		writeJSON(w, 400, map[string]string{"message": provider + " is not configured"})
		return
	}
	redirectTo := r.URL.Query().Get("redirect_to")
	if !a.safeOAuthRedirect(slug, redirectTo) {
		writeJSON(w, 400, map[string]string{"message": "redirect_to must be on this project's domain"})
		return
	}
	state := a.signState(fmt.Sprintf("%s|%s|%s|%d", slug, provider, redirectTo, time.Now().Unix()))
	cb := "https://" + slug + "." + a.cfg.domain + "/auth/v1/callback"
	u, _ := url.Parse(meta.AuthURL)
	q := u.Query()
	q.Set("client_id", id)
	q.Set("redirect_uri", cb)
	q.Set("response_type", "code")
	q.Set("scope", meta.Scope)
	q.Set("state", state)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (a *app) handleOAuthCallback(w http.ResponseWriter, r *http.Request, slug, jwtSecret string) {
	code := r.URL.Query().Get("code")
	payload, ok := a.verifyState(r.URL.Query().Get("state"))
	if !ok || code == "" {
		writeJSON(w, 400, map[string]string{"message": "invalid oauth state"})
		return
	}
	f := strings.Split(payload, "|")
	if len(f) < 3 || f[0] != slug {
		writeJSON(w, 400, map[string]string{"message": "state mismatch"})
		return
	}
	provider, redirectTo := f[1], f[2]
	meta := oauthProviders[provider]
	id, sec, _ := a.oauthConfig(slug, provider)
	cb := "https://" + slug + "." + a.cfg.domain + "/auth/v1/callback"

	// exchange code for an access token
	form := url.Values{"client_id": {id}, "client_secret": {sec}, "code": {code},
		"redirect_uri": {cb}, "grant_type": {"authorization_code"}}
	req, _ := http.NewRequest("POST", meta.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, 502, map[string]string{"message": "token exchange failed"})
		return
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if tok.AccessToken == "" {
		writeJSON(w, 502, map[string]string{"message": "no access token"})
		return
	}

	// fetch the user's email
	email := a.oauthEmail(provider, meta, tok.AccessToken)
	if email == "" {
		writeJSON(w, 502, map[string]string{"message": "could not read email from provider"})
		return
	}

	// find or create the auth user
	db, err := a.dbFor(slug)
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": err.Error()})
		return
	}
	var uid string
	err = db.QueryRow(`SELECT id FROM auth.users WHERE email=$1`, email).Scan(&uid)
	if err != nil {
		db.QueryRow(`INSERT INTO auth.users(email, encrypted_password) VALUES ($1,'oauth') RETURNING id`, email).Scan(&uid)
	}
	db.Exec(`UPDATE auth.users SET last_sign_in_at=now() WHERE id=$1`, uid)
	a.auditRaw(email, clientIP(r), "user-oauth", slug+"/"+provider)

	acc, ref, terr := a.issueTokens(db, jwtSecret, uid, email, "")
	if terr != nil {
		writeJSON(w, 500, map[string]string{"message": terr.Error()})
		return
	}
	if redirectTo != "" && a.safeOAuthRedirect(slug, redirectTo) {
		http.Redirect(w, r, redirectTo+"#access_token="+acc+"&refresh_token="+ref+"&token_type=bearer", http.StatusSeeOther)
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref, "token_type": "bearer",
		"user": map[string]string{"id": uid, "email": email}})
}

func (a *app) oauthEmail(provider string, meta oauthMeta, accessToken string) string {
	get := func(u string) []byte {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "ForgeBase")
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b
	}
	if provider == "github" {
		// GitHub's /user email is the public profile field and may be
		// unverified; only trust a verified address from /user/emails, or an
		// attacker who sets a victim's email on their account could log in as
		// that user.
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		json.Unmarshal(get("https://api.github.com/user/emails"), &emails)
		for _, e := range emails {
			if e.Primary && e.Verified {
				return strings.ToLower(e.Email)
			}
		}
		for _, e := range emails {
			if e.Verified {
				return strings.ToLower(e.Email)
			}
		}
		return ""
	}
	// Google / GitLab / Discord (and OIDC-style providers): require a verified
	// email. Different providers name the flag differently.
	var info struct {
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"verified_email"` // Google
		EmailVerified bool   `json:"email_verified"` // GitLab / OIDC
		Verified      bool   `json:"verified"`       // Discord
	}
	json.Unmarshal(get(meta.UserURL), &info)
	if info.Email != "" && (info.VerifiedEmail || info.EmailVerified || info.Verified) {
		return strings.ToLower(info.Email)
	}
	return ""
}

// ----------------------------------------------------------------- panel config

func (a *app) saveOAuth(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	provider := r.FormValue("provider")
	if _, ok := oauthProviders[provider]; !ok {
		redirectErr(w, r, "/p/"+slug+"/auth", "Unknown provider.")
		return
	}
	id := strings.TrimSpace(r.FormValue("client_id"))
	sec := strings.TrimSpace(r.FormValue("client_secret"))
	enabled := r.FormValue("enabled") == "on"
	if sec == "" {
		// keep the existing secret when left blank
		a.db.Exec(`INSERT INTO oauth_providers(slug,provider,client_id,enabled) VALUES ($1,$2,$3,$4)
			ON CONFLICT (slug,provider) DO UPDATE SET client_id=$3, enabled=$4`,
			slug, provider, id, enabled)
	} else {
		a.db.Exec(`INSERT INTO oauth_providers(slug,provider,client_id,client_secret,enabled)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (slug,provider) DO UPDATE SET client_id=$3, client_secret=$4, enabled=$5`,
			slug, provider, id, sec, enabled)
	}
	a.audit(r, "oauth-config", slug+"/"+provider)
	redirectMsg(w, r, "/p/"+slug+"/auth", provider+" settings saved.")
}
