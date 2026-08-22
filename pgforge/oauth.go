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
	"sync"
	"time"
)

// OAuth sign-in for end users (Google, GitHub). Per project, per provider config
// (client id/secret) lives in oauth_providers. Flow on <slug>.<domain>:
//   /auth/v1/authorize?provider=google&redirect_to=... -> provider consent
//   /auth/v1/callback?code=...&state=... -> exchange, find/create auth.users,
//       mint our JWT, redirect to redirect_to#access_token=...

type oauthMeta struct{ AuthURL, TokenURL, UserURL, Scope string }

var oauthProviders = map[string]oauthMeta{
	"google":    {"https://accounts.google.com/o/oauth2/v2/auth", "https://oauth2.googleapis.com/token", "https://www.googleapis.com/oauth2/v2/userinfo", "openid email profile"},
	"github":    {"https://github.com/login/oauth/authorize", "https://github.com/login/oauth/access_token", "https://api.github.com/user", "read:user user:email"},
	"gitlab":    {"https://gitlab.com/oauth/authorize", "https://gitlab.com/oauth/token", "https://gitlab.com/oauth/userinfo", "openid email"},
	"discord":   {"https://discord.com/api/oauth2/authorize", "https://discord.com/api/oauth2/token", "https://discord.com/api/users/@me", "identify email"},
	"microsoft": {"https://login.microsoftonline.com/common/oauth2/v2.0/authorize", "https://login.microsoftonline.com/common/oauth2/v2.0/token", "https://graph.microsoft.com/oidc/userinfo", "openid email profile"},
	"facebook":  {"https://www.facebook.com/v18.0/dialog/oauth", "https://graph.facebook.com/v18.0/oauth/access_token", "https://graph.facebook.com/me?fields=id,name,email", "email public_profile"},
	"twitch":    {"https://id.twitch.tv/oauth2/authorize", "https://id.twitch.tv/oauth2/token", "https://id.twitch.tv/oauth2/userinfo", "openid user:read:email"},
	"slack":     {"https://slack.com/openid/connect/authorize", "https://slack.com/api/openid.connect.token", "https://slack.com/api/openid.connect.userInfo", "openid email profile"},
	"spotify":   {"https://accounts.spotify.com/authorize", "https://accounts.spotify.com/api/token", "https://api.spotify.com/v1/me", "user-read-email"},
	"linkedin":  {"https://www.linkedin.com/oauth/v2/authorization", "https://www.linkedin.com/oauth/v2/accessToken", "https://api.linkedin.com/v2/userinfo", "openid email profile"},
	"bitbucket": {"https://bitbucket.org/site/oauth2/authorize", "https://bitbucket.org/site/oauth2/access_token", "https://api.bitbucket.org/2.0/user", "account email"},
	"notion":    {"https://api.notion.com/v1/oauth/authorize", "https://api.notion.com/v1/oauth/token", "https://api.notion.com/v1/users/me", ""},
}

func (a *app) oauthConfig(slug, provider string) (id, secret string, enabled bool) {
	a.db.QueryRow(`SELECT client_id, client_secret, enabled FROM oauth_providers WHERE slug=$1 AND provider=$2`,
		slug, provider).Scan(&id, &secret, &enabled)
	return
}

// oauthIssuer returns the project's generic-OIDC issuer URL ("" when unset).
func (a *app) oauthIssuer(slug string) string {
	var iss string
	a.db.QueryRow(`SELECT coalesce(issuer,'') FROM oauth_providers WHERE slug=$1 AND provider='oidc'`,
		slug).Scan(&iss)
	return strings.TrimRight(strings.TrimSpace(iss), "/")
}

var (
	oidcMu    sync.Mutex
	oidcCache = map[string]oauthMeta{}
)

// discoverOIDC resolves an issuer's endpoints via OpenID Connect discovery.
// Results are cached for the process lifetime - endpoints do not move.
func discoverOIDC(issuer string) (oauthMeta, error) {
	oidcMu.Lock()
	if m, ok := oidcCache[issuer]; ok {
		oidcMu.Unlock()
		return m, nil
	}
	oidcMu.Unlock()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(issuer + "/.well-known/openid-configuration")
	if err != nil {
		return oauthMeta{}, err
	}
	defer resp.Body.Close()
	var d struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserinfoEndpoint      string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return oauthMeta{}, err
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.UserinfoEndpoint == "" {
		return oauthMeta{}, fmt.Errorf("issuer discovery incomplete (missing endpoints)")
	}
	m := oauthMeta{d.AuthorizationEndpoint, d.TokenEndpoint, d.UserinfoEndpoint, "openid email profile"}
	oidcMu.Lock()
	oidcCache[issuer] = m
	oidcMu.Unlock()
	return m, nil
}

// resolveProvider maps a provider name to its endpoints, handling the generic
// "oidc" provider via per-project issuer discovery.
func (a *app) resolveProvider(slug, provider string) (oauthMeta, error) {
	if provider == "oidc" {
		iss := a.oauthIssuer(slug)
		if iss == "" {
			return oauthMeta{}, fmt.Errorf("set the OIDC issuer URL on the Auth page first")
		}
		return discoverOIDC(iss)
	}
	meta, ok := oauthProviders[provider]
	if !ok {
		return oauthMeta{}, fmt.Errorf("unknown provider")
	}
	return meta, nil
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
	// project-configured allowlist first: exact-prefix matches let apps live
	// on their own domains
	if _, _, allow := a.authPolicy(slug); len(allow) > 0 {
		for _, p := range allow {
			if strings.HasPrefix(redirectTo, p) {
				return true
			}
		}
	}
	host := u.Hostname()
	return host == slug+"."+a.cfg.domain || host == a.cfg.domain
}

func (a *app) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request, slug string) {
	provider := r.URL.Query().Get("provider")
	meta, merr := a.resolveProvider(slug, provider)
	if merr != nil {
		writeJSON(w, 400, map[string]string{"message": merr.Error()})
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
	meta, merr := a.resolveProvider(slug, provider)
	if merr != nil {
		writeJSON(w, 400, map[string]string{"message": merr.Error()})
		return
	}
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
		if msg := beforeCreateHook(db, email); msg != "" {
			writeJSON(w, 400, map[string]string{"message": msg})
			return
		}
		db.QueryRow(`INSERT INTO auth.users(email, encrypted_password) VALUES ($1,'oauth') RETURNING id`, email).Scan(&uid)
	}
	db.Exec(`UPDATE auth.users SET last_sign_in_at=now() WHERE id=$1`, uid)
	// record the linked identity: same verified email across providers is the
	// same user, and this table shows which doors they have come through
	db.Exec(`INSERT INTO auth.identities(user_id, provider, email) VALUES ($1,$2,$3)
		ON CONFLICT (user_id, provider) DO UPDATE SET last_sign_in_at=now(), email=$3`,
		uid, provider, email)
	a.auditRaw(email, clientIP(r), "user-oauth", slug+"/"+provider)

	acc, ref, terr := a.issueTokens(db, jwtSecret, slug, uid, email, "")
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
	if _, ok := oauthProviders[provider]; !ok && provider != "oidc" {
		redirectErr(w, r, "/p/"+slug+"/auth", "Unknown provider.")
		return
	}
	id := strings.TrimSpace(r.FormValue("client_id"))
	sec := strings.TrimSpace(r.FormValue("client_secret"))
	enabled := r.FormValue("enabled") == "on"
	if provider == "oidc" {
		iss := strings.TrimRight(strings.TrimSpace(r.FormValue("issuer")), "/")
		if enabled && !strings.HasPrefix(iss, "https://") {
			redirectErr(w, r, "/p/"+slug+"/auth", "The OIDC issuer must be an https:// URL.")
			return
		}
		defer a.db.Exec(`UPDATE oauth_providers SET issuer=$3 WHERE slug=$1 AND provider=$2`, slug, provider, iss)
	}
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
