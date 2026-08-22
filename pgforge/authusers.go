package main

import (
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// Native end-user Auth: email + password signup/login that issues project-scoped
// JWTs (role=authenticated) signed with the project's Data API secret, so
// PostgREST honours them directly. Users live in the project's auth.users table.
// Public endpoints on <slug>.<domain>/auth/v1/{signup,token,user}.

// accessTokenTTL is short-lived (a common 1h default); clients keep a session
// alive by exchanging the long-lived refresh token for a new access token.
const accessTokenTTL = 3600 // fallback when no per-project policy is set

// authPolicy returns a project's token TTL (seconds), minimum password length
// and redirect allowlist entries.

// signUserJWT mints a short-lived authenticated-user access token. user_metadata
// and app_metadata are embedded as raw JSON objects so apps (and RLS via
// auth.jwt()) can read them, using the industry-standard claim shape.
func signUserJWT(secret []byte, sub, email, userMeta, appMeta string) string {
	return signUserJWTTTL(secret, sub, email, userMeta, appMeta, accessTokenTTL)
}

func signUserJWTTTL(secret []byte, sub, email, userMeta, appMeta string, ttlSec int, aal ...string) string {
	if !json.Valid([]byte(userMeta)) {
		userMeta = "{}"
	}
	if !json.Valid([]byte(appMeta)) {
		appMeta = "{}"
	}
	// assurance level: aal1 = one factor (password/OAuth/magic link),
	// aal2 = the login also passed a second factor (TOTP/recovery code)
	level := "aal1"
	if len(aal) > 0 && aal[0] != "" {
		level = aal[0]
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	now := time.Now().Unix()
	claims, _ := json.Marshal(map[string]any{
		"sub": sub, "email": email, "role": "authenticated", "aud": "authenticated",
		"iss": "pgforge", "iat": now, "exp": now + int64(ttlSec), "aal": level,
		"user_metadata": json.RawMessage(userMeta),
		"app_metadata":  json.RawMessage(appMeta),
	})
	payload := b64(claims)
	signing := header + "." + payload
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(signing))
	return signing + "." + b64(m.Sum(nil))
}

// issueTokens mints a new pair. familyID ties a refresh token to a session
// lineage: empty starts a new session (new family); on rotation we pass the
// parent's family so reuse of any token in the lineage can revoke the whole thing.
func (a *app) authPolicy(slug string) (int, int, []string) {
	ttlMin, minPw := 60, 6
	var allowRaw string
	a.db.QueryRow(`SELECT coalesce(access_ttl_min,60), coalesce(min_pw_len,6), coalesce(redirect_allowlist,'')
		FROM auth_config WHERE slug=$1`, slug).Scan(&ttlMin, &minPw, &allowRaw)
	if ttlMin < 5 || ttlMin > 1440 {
		ttlMin = 60
	}
	if minPw < 6 || minPw > 72 {
		minPw = 6
	}
	var allow []string
	for _, e := range strings.FieldsFunc(allowRaw, func(r rune) bool { return r == ',' || r == '\n' }) {
		if e = strings.TrimSpace(e); e != "" {
			allow = append(allow, e)
		}
	}
	return ttlMin * 60, minPw, allow
}

func (a *app) issueTokens(db *sql.DB, secret, slug, sub, email, familyID string) (string, string, error) {
	return a.issueTokensAAL(db, secret, slug, sub, email, familyID, "aal1")
}

func (a *app) issueTokensAAL(db *sql.DB, secret, slug, sub, email, familyID, aal string) (string, string, error) {
	// Single-session mode: minting a session kills every other one, so a
	// user is signed in from exactly one place at a time.
	var single bool
	a.db.QueryRow(`SELECT coalesce(single_session,false) FROM auth_config WHERE slug=$1`, slug).Scan(&single)
	if single {
		db.Exec(`UPDATE auth.refresh_tokens SET revoked=true WHERE user_id=$1 AND NOT revoked`, sub)
	}
	refresh := randHex(32)
	sum := sha256.Sum256([]byte(refresh))
	var err error
	if familyID == "" {
		_, err = db.Exec(`INSERT INTO auth.refresh_tokens(user_id, token_hash, expires_at)
			VALUES ($1, $2, now() + interval '30 days')`, sub, hex.EncodeToString(sum[:]))
	} else {
		_, err = db.Exec(`INSERT INTO auth.refresh_tokens(user_id, token_hash, expires_at, family_id)
			VALUES ($1, $2, now() + interval '30 days', $3)`, sub, hex.EncodeToString(sum[:]), familyID)
	}
	if err != nil {
		return "", "", err
	}
	userMeta, appMeta := "{}", "{}"
	isAnon := false
	db.QueryRow(`SELECT raw_user_meta_data::text, raw_app_meta_data::text, coalesce(is_anonymous,false)
		FROM auth.users WHERE id=$1`, sub).Scan(&userMeta, &appMeta, &isAnon)
	// Auth hook: if the project defines auth.custom_claims(uuid) RETURNS jsonb,
	// its result is merged into app_metadata at mint time - plans, roles,
	// feature flags computed in SQL land in every token automatically.
	var hook string
	if db.QueryRow(`SELECT coalesce(auth.custom_claims($1::uuid)::text,'')`, sub).Scan(&hook) == nil &&
		hook != "" && json.Valid([]byte(hook)) {
		var base, extra map[string]any
		if json.Unmarshal([]byte(appMeta), &base) == nil && json.Unmarshal([]byte(hook), &extra) == nil {
			for k, v := range extra {
				base[k] = v
			}
			if merged, err := json.Marshal(base); err == nil {
				appMeta = string(merged)
			}
		}
	}
	ttlSec, _, _ := a.authPolicy(slug)
	tok := signUserJWTTTL([]byte(secret), sub, email, userMeta, appMeta, ttlSec, aal)
	if isAnon {
		// re-sign with the is_anonymous claim folded into app_metadata so
		// policies can gate on (auth.jwt()->'app_metadata'->>'is_anonymous')
		am := strings.TrimSuffix(strings.TrimSpace(appMeta), "}")
		if am == "{" {
			am += `"is_anonymous":true}`
		} else {
			am += `,"is_anonymous":true}`
		}
		tok = signUserJWTTTL([]byte(secret), sub, email, userMeta, am, ttlSec, aal)
	}
	// asymmetric-mode projects: same claims, RS256 signature + kid header
	tok = a.resignRS(slug, tok)
	return tok, refresh, nil
}

// beforeCreateHook consults the optional auth.before_create(text) SQL hook
// before a new user row is created. Define it in the project to gate
// signups: return NULL or ” to allow, or a message to reject with.
//
//	CREATE FUNCTION auth.before_create(email text) RETURNS text AS $$
//	  SELECT CASE WHEN email LIKE '%@rival.com' THEN 'not here, thanks' END
//	$$ LANGUAGE sql;
func beforeCreateHook(db *sql.DB, email string) string {
	var defined bool
	db.QueryRow(`SELECT to_regprocedure('auth.before_create(text)') IS NOT NULL`).Scan(&defined)
	if !defined {
		return ""
	}
	var msg sql.NullString
	if db.QueryRow(`SELECT auth.before_create($1)`, email).Scan(&msg) != nil {
		return "" // hook failing: allow rather than lock signups out
	}
	return strings.TrimSpace(msg.String)
}

// handleRefresh validates a refresh token, ROTATES it (revoke the used one, mint
// a new pair in the same family) and returns a fresh access token. If an already-
// revoked token is presented (reuse - i.e. it was stolen and replayed), the whole
// token family is revoked so the compromised session cannot continue.
func (a *app) handleRefresh(w http.ResponseWriter, r *http.Request, db *sql.DB, secret, slug string) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.RefreshToken == "" {
		writeJSON(w, 400, map[string]string{"message": "refresh_token required"})
		return
	}
	sum := sha256.Sum256([]byte(body.RefreshToken))
	th := hex.EncodeToString(sum[:])
	var id, email, familyID string
	var revoked, expired bool
	err := db.QueryRow(`SELECT rt.user_id, coalesce(u.email,''), rt.family_id, rt.revoked, rt.expires_at <= now()
		FROM auth.refresh_tokens rt JOIN auth.users u ON u.id = rt.user_id
		WHERE rt.token_hash=$1`, th).Scan(&id, &email, &familyID, &revoked, &expired)
	if err != nil {
		writeJSON(w, 401, map[string]string{"message": "invalid refresh token"})
		return
	}
	if revoked {
		db.Exec(`UPDATE auth.refresh_tokens SET revoked=true WHERE family_id=$1`, familyID)
		writeJSON(w, 401, map[string]string{"message": "refresh token reuse detected; session revoked"})
		return
	}
	if expired {
		writeJSON(w, 401, map[string]string{"message": "expired refresh token"})
		return
	}
	db.Exec(`UPDATE auth.refresh_tokens SET revoked=true WHERE token_hash=$1`, th)
	ttlSec, _, _ := a.authPolicy(slug)
	acc, ref, terr := a.issueTokens(db, secret, slug, id, email, familyID)
	if terr != nil {
		writeJSON(w, 500, map[string]string{"message": terr.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
		"token_type": "bearer", "expires_in": ttlSec,
		"user": map[string]string{"id": id, "email": email}})
}

func verifyUserJWT(secret []byte, token string) (map[string]any, bool) {
	if tokenAlgIsRS(token) {
		// asymmetric-mode projects sign user tokens RS256; the kid in the
		// header finds the right public key
		return verifyRS256(token)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if want != parts[2] {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil, false
	}
	if exp, ok := claims["exp"].(float64); ok && time.Now().Unix() > int64(exp) {
		return nil, false
	}
	return claims, true
}

// captchaOK verifies a Cloudflare Turnstile token when the project has a
// captcha secret configured; unset secret means the check is off.
func (a *app) captchaOK(slug, token, ip string) bool {
	var secret string
	a.db.QueryRow(`SELECT coalesce(captcha_secret,'') FROM auth_config WHERE slug=$1`, slug).Scan(&secret)
	if secret == "" {
		return true
	}
	if token == "" {
		return false
	}
	form := url.Values{"secret": {secret}, "response": {token}, "remoteip": {ip}}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).PostForm(
		"https://challenges.cloudflare.com/turnstile/v0/siteverify", form)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Success
}

// authRateFor returns the project's per-IP auth requests-per-minute cap.
// 0 disables the limiter entirely (the owner's call to make).
func (a *app) authRateFor(slug string) int {
	limit := 30
	a.db.QueryRow(`SELECT coalesce(rate_limit_per_min,30) FROM auth_config WHERE slug=$1`, slug).Scan(&limit)
	if limit < 0 {
		limit = 30
	}
	if limit > 1000 {
		limit = 1000
	}
	return limit
}

// passwordPwned checks the HIBP k-anonymity range API: only the first five
// hex characters of the SHA-1 ever leave this server, never the password or
// even its full hash. Fails open on any network error so an outage over
// there can never block signups here.
func passwordPwned(pw string) bool {
	sum := sha1.Sum([]byte(pw))
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	resp, err := (&http.Client{Timeout: 6 * time.Second}).Get("https://api.pwnedpasswords.com/range/" + h[:5])
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	suffix := h[5:]
	for _, ln := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), suffix+":") {
			return true
		}
	}
	return false
}

// pwRejected applies password policy beyond length: the optional
// leaked-password screen. Returns a user-facing message, or "" when fine.
func (a *app) pwRejected(slug, pw string) string {
	var on bool
	a.db.QueryRow(`SELECT coalesce(leaked_check,false) FROM auth_config WHERE slug=$1`, slug).Scan(&on)
	if on && passwordPwned(pw) {
		return "that password has appeared in known data breaches - please choose a different one"
	}
	return ""
}

// normalizePhone reduces a phone number to +E.164-ish digits ("" = invalid).
func normalizePhone(p string) string {
	p = strings.TrimSpace(p)
	var b strings.Builder
	for i, r := range p {
		if r == '+' && i == 0 {
			b.WriteRune(r)
		} else if r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '(' || r == ')' {
			continue
		} else {
			return ""
		}
	}
	digits := strings.TrimPrefix(b.String(), "+")
	if len(digits) < 7 || len(digits) > 15 {
		return ""
	}
	return b.String()
}

// sendSMSCode delivers a sign-in code through the project's SMS webhook -
// a bring-your-own-provider adapter: the owner points it at any HTTPS
// endpoint (a serverless function wrapping Twilio, Vonage, MessageBird...)
// and we POST {phone, code, project} with an HMAC signature header so the
// receiver can verify it really came from this server.
func (a *app) sendSMSCode(slug, phone, code string) {
	var hook string
	a.db.QueryRow(`SELECT coalesce(sms_webhook_url,'') FROM auth_config WHERE slug=$1`, slug).Scan(&hook)
	if !strings.HasPrefix(hook, "https://") {
		return
	}
	payload, _ := json.Marshal(map[string]string{"phone": phone, "code": code, "project": slug})
	req, _ := http.NewRequest(http.MethodPost, hook, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ForgeBase-Signature", a.sign("sms:"+string(payload)))
	go func() {
		defer func() { recover() }()
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
}

// randInt returns a uniform random int in [0, max) from crypto/rand.
func randInt(max int64) (int64, error) {
	n, err := crand.Int(crand.Reader, big.NewInt(max))
	if err != nil {
		return 0, err
	}
	return n.Int64(), nil
}

// migrateAuthProjects re-runs the idempotent auth setup for every already-enabled
// project on boot, so schema additions (metadata, family_id, banned_until, ...)
// land on existing projects after a self-update, not only on re-enable.
func (a *app) migrateAuthProjects() {
	defer func() { recover() }()
	rows, err := a.db.Query(`SELECT slug FROM auth_config WHERE enabled`)
	if err != nil {
		return
	}
	var slugs []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		slugs = append(slugs, s)
	}
	rows.Close()
	for _, s := range slugs {
		if _, err := a.ensureAuth(s); err != nil {
			log.Printf("auth migrate %s: %v", s, err)
		}
	}
}

// ensureAuth sets up auth schema + authenticated role for a project.
func (a *app) ensureAuth(slug string) (string, error) {
	db, err := a.dbFor(slug)
	if err != nil {
		return "", err
	}
	stmts := []string{
		`CREATE SCHEMA IF NOT EXISTS auth`,
		`CREATE TABLE IF NOT EXISTS auth.refresh_tokens (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id uuid NOT NULL,
			token_hash text NOT NULL,
			family_id uuid NOT NULL DEFAULT gen_random_uuid(),
			expires_at timestamptz NOT NULL,
			revoked boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE auth.refresh_tokens ADD COLUMN IF NOT EXISTS family_id uuid NOT NULL DEFAULT gen_random_uuid()`,
		`CREATE INDEX IF NOT EXISTS refresh_tokens_hash ON auth.refresh_tokens(token_hash)`,
		`CREATE TABLE IF NOT EXISTS auth.users (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			email text UNIQUE NOT NULL,
			encrypted_password text NOT NULL,
			raw_user_meta_data jsonb NOT NULL DEFAULT '{}',
			raw_app_meta_data jsonb NOT NULL DEFAULT '{}',
			banned_until timestamptz,
			email_confirmed_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now(),
			last_sign_in_at timestamptz
		)`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS raw_user_meta_data jsonb NOT NULL DEFAULT '{}'`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS raw_app_meta_data jsonb NOT NULL DEFAULT '{}'`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS banned_until timestamptz`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS email_confirmed_at timestamptz`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS is_anonymous boolean NOT NULL DEFAULT false`,
		`ALTER TABLE auth.users ALTER COLUMN email DROP NOT NULL`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS totp_secret text NOT NULL DEFAULT ''`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS totp_enabled boolean NOT NULL DEFAULT false`,
		`CREATE TABLE IF NOT EXISTS auth.recovery_codes (
			user_id uuid NOT NULL,
			code_hash text NOT NULL,
			used_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS recovery_codes_user ON auth.recovery_codes(user_id)`,
		`CREATE TABLE IF NOT EXISTS auth.identities (
			user_id uuid NOT NULL,
			provider text NOT NULL,
			email text NOT NULL DEFAULT '',
			created_at timestamptz NOT NULL DEFAULT now(),
			last_sign_in_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, provider)
		)`,
		`CREATE TABLE IF NOT EXISTS auth.one_time_tokens (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			email text NOT NULL,
			code_hash text NOT NULL,
			attempts integer NOT NULL DEFAULT 0,
			expires_at timestamptz NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS one_time_tokens_email ON auth.one_time_tokens(email)`,
		`ALTER TABLE auth.one_time_tokens ADD COLUMN IF NOT EXISTS phone text NOT NULL DEFAULT ''`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS phone text`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS phone_confirmed_at timestamptz`,
		`CREATE UNIQUE INDEX IF NOT EXISTS users_phone_uniq ON auth.users(phone) WHERE phone IS NOT NULL`,
		`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='authenticated') THEN CREATE ROLE authenticated NOLOGIN NOINHERIT; END IF; END $$`,
		`GRANT USAGE ON SCHEMA public TO authenticated`,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO authenticated`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO authenticated`,
		`GRANT authenticated TO ` + pq.QuoteIdentifier(slug),
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return "", err
		}
	}
	a.ensureAuthHelpers(db) // auth.uid()/jwt()/role()/email() for RLS policies
	// reuse the Data API JWT secret; create one if the API isn't enabled yet
	secret, _ := a.apiConfig(slug)
	if secret == "" {
		secret = randHex(32)
		a.db.Exec(`INSERT INTO api_config(slug, jwt_secret, enabled) VALUES ($1,$2,false)
			ON CONFLICT (slug) DO NOTHING`, slug, secret)
		secret, _ = a.apiConfig(slug)
	}
	a.db.Exec(`INSERT INTO auth_config(slug, enabled) VALUES ($1,true)
		ON CONFLICT (slug) DO UPDATE SET enabled=true`, slug)
	return secret, nil
}

// ----------------------------------------------------------------- public endpoints

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (a *app) serveAuth(w http.ResponseWriter, r *http.Request, slug string) {
	secret, enabled := a.authConfig(slug)
	if !enabled || secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "auth not enabled"})
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": err.Error()})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/auth/v1")
	ttlSec, minPw, _ := a.authPolicy(slug)
	_ = minPw

	switch {
	case path == "/authorize" && r.Method == http.MethodGet:
		a.handleOAuthAuthorize(w, r, slug)
	case path == "/callback" && r.Method == http.MethodGet:
		a.handleOAuthCallback(w, r, slug, secret)

	case path == "/signup" && r.Method == http.MethodPost:
		if a.authRateLimited(clientIP(r), a.authRateFor(slug)) {
			writeJSON(w, 429, map[string]string{"message": "too many requests"})
			return
		}
		var body struct {
			Email, Password string
			Data            json.RawMessage `json:"data"` // optional user_metadata
			CaptchaToken    string          `json:"captcha_token"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		if !a.captchaOK(slug, body.CaptchaToken, clientIP(r)) {
			writeJSON(w, 400, map[string]string{"message": "captcha verification failed"})
			return
		}
		if body.Email == "" && body.Password == "" {
			// Anonymous sign-in: a real user row without credentials; later
			// upgradeable to permanent via PUT /user with email+password.
			if !a.authAnonEnabled(slug) {
				writeJSON(w, http.StatusForbidden, map[string]string{"message": "anonymous sign-ins are disabled for this project"})
				return
			}
			meta := "{}"
			if len(body.Data) > 0 && json.Valid(body.Data) {
				meta = string(body.Data)
			}
			var id string
			if err := db.QueryRow(`INSERT INTO auth.users(email, encrypted_password, raw_user_meta_data, is_anonymous, email_confirmed_at)
					VALUES (NULL, '', $1::jsonb, true, now()) RETURNING id`, meta).Scan(&id); err != nil {
				writeJSON(w, 500, map[string]string{"message": err.Error()})
				return
			}
			acc, ref, terr := a.issueTokens(db, secret, slug, id, "", "")
			if terr != nil {
				writeJSON(w, 500, map[string]string{"message": terr.Error()})
				return
			}
			a.auditRaw("anonymous:"+id, clientIP(r), "user-signup-anon", slug)
			writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
				"token_type": "bearer", "expires_in": ttlSec,
				"user": map[string]any{"id": id, "email": nil, "is_anonymous": true}})
			return
		}
		if !strings.Contains(body.Email, "@") || len(body.Password) < minPw {
			writeJSON(w, 400, map[string]string{"message": fmt.Sprintf("valid email and password (%d+ chars) required", minPw)})
			return
		}
		if msg := a.pwRejected(slug, body.Password); msg != "" {
			writeJSON(w, 400, map[string]string{"message": msg})
			return
		}
		hash, herr := hashPassword(body.Password)
		if herr != nil {
			writeJSON(w, 400, map[string]string{"message": "password too long (max 72 bytes)"})
			return
		}
		meta := "{}"
		if len(body.Data) > 0 && json.Valid(body.Data) {
			meta = string(body.Data)
		}
		if msg := beforeCreateHook(db, body.Email); msg != "" {
			writeJSON(w, 400, map[string]string{"message": msg})
			return
		}
		var id string
		err := db.QueryRow(`INSERT INTO auth.users(email, encrypted_password, raw_user_meta_data) VALUES ($1,$2,$3::jsonb) RETURNING id`,
			body.Email, hash, meta).Scan(&id)
		if err != nil {
			writeJSON(w, 400, map[string]string{"message": "user already exists"})
			return
		}
		if a.authConfirmEmail(slug) {
			if serr := a.sendConfirmationEmail(slug, body.Email); serr != nil {
				db.Exec(`DELETE FROM auth.users WHERE id=$1`, id) // let them retry
				writeJSON(w, 500, map[string]string{"message": "could not send confirmation email: " + serr.Error()})
				return
			}
			a.auditRaw(body.Email, clientIP(r), "user-signup-pending", slug)
			writeJSON(w, 200, map[string]any{"message": "confirmation email sent; confirm your email to sign in",
				"user": map[string]any{"id": id, "email": body.Email, "email_confirmed_at": nil}})
			return
		}
		acc, ref, terr := a.issueTokens(db, secret, slug, id, body.Email, "")
		if terr != nil {
			writeJSON(w, 500, map[string]string{"message": terr.Error()})
			return
		}
		a.auditRaw(body.Email, clientIP(r), "user-signup", slug)
		writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
			"token_type": "bearer", "expires_in": ttlSec,
			"user": map[string]string{"id": id, "email": body.Email}})

	case path == "/token" && r.Method == http.MethodPost:
		// grant_type=refresh_token exchanges a refresh token for a fresh pair.
		if r.URL.Query().Get("grant_type") == "refresh_token" {
			a.handleRefresh(w, r, db, secret, slug)
			return
		}
		if a.authRateLimited(clientIP(r), a.authRateFor(slug)) {
			writeJSON(w, 429, map[string]string{"message": "too many requests"})
			return
		}
		var body struct {
			Email, Password string
			TOTPCode        string `json:"totp_code"`
			RecoveryCode    string `json:"recovery_code"`
			CaptchaToken    string `json:"captcha_token"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		if !a.captchaOK(slug, body.CaptchaToken, clientIP(r)) {
			writeJSON(w, 400, map[string]string{"message": "captcha verification failed"})
			return
		}
		var id, hash string
		var bannedUntil, emailConfirmed sql.NullTime
		err := db.QueryRow(`SELECT id, encrypted_password, banned_until, email_confirmed_at FROM auth.users WHERE email=$1`, body.Email).Scan(&id, &hash, &bannedUntil, &emailConfirmed)
		if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil {
			writeJSON(w, 400, map[string]string{"message": "invalid login credentials"})
			return
		}
		if bannedUntil.Valid && bannedUntil.Time.After(time.Now()) {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "account is banned"})
			return
		}
		if a.authConfirmEmail(slug) && !emailConfirmed.Valid {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "email not confirmed"})
			return
		}
		aal := "aal1"
		if sec, on := mfaState(db, id); on && sec != "" {
			if !totpVerify(sec, body.TOTPCode, time.Now()) && !tryRecoveryCode(db, id, body.RecoveryCode) {
				writeJSON(w, 401, map[string]any{"mfa_required": true,
					"message": "two-factor code required - resend with totp_code (or recovery_code)"})
				return
			}
			aal = "aal2" // password + second factor
		}
		db.Exec(`UPDATE auth.users SET last_sign_in_at=now() WHERE id=$1`, id)
		acc, ref, terr := a.issueTokensAAL(db, secret, slug, id, body.Email, "", aal)
		if terr != nil {
			writeJSON(w, 500, map[string]string{"message": terr.Error()})
			return
		}
		a.auditRaw(body.Email, clientIP(r), "user-login", slug)
		writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
			"token_type": "bearer", "expires_in": ttlSec,
			"user": map[string]string{"id": id, "email": body.Email}})

	case path == "/logout" && r.Method == http.MethodPost:
		// Revoke the presented refresh token so the session can't be renewed.
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.RefreshToken != "" {
			sum := sha256.Sum256([]byte(body.RefreshToken))
			th := hex.EncodeToString(sum[:])
			if r.URL.Query().Get("scope") == "global" {
				// revoke every session for the owning user, not just this token
				db.Exec(`UPDATE auth.refresh_tokens SET revoked=true
					WHERE user_id=(SELECT user_id FROM auth.refresh_tokens WHERE token_hash=$1)`, th)
			} else {
				db.Exec(`UPDATE auth.refresh_tokens SET revoked=true WHERE token_hash=$1`, th)
			}
		}
		writeJSON(w, 200, map[string]string{"message": "signed out"})

	case strings.HasPrefix(path, "/factors/") && r.Method == http.MethodPost:
		a.serveAuthMFA(w, r, db, secret, slug, strings.TrimPrefix(path, "/factors/"))

	case path == "/user" && r.Method == http.MethodGet:
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims, ok := verifyUserJWT([]byte(secret), tok)
		if !ok {
			writeJSON(w, 401, map[string]string{"message": "invalid token"})
			return
		}
		writeJSON(w, 200, map[string]any{"id": claims["sub"], "email": claims["email"], "role": claims["role"],
			"user_metadata": claims["user_metadata"], "app_metadata": claims["app_metadata"]})

	case path == "/user" && r.Method == http.MethodPut:
		// self-service update of user_metadata (app_metadata is admin-only)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims, ok := verifyUserJWT([]byte(secret), tok)
		if !ok {
			writeJSON(w, 401, map[string]string{"message": "invalid token"})
			return
		}
		sub, _ := claims["sub"].(string)
		var body struct {
			Data     json.RawMessage `json:"data"`
			Email    string          `json:"email"`
			Password string          `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		// Upgrade an anonymous account to permanent: email + password arrive
		// on the authenticated (anonymous) session.
		if body.Email != "" || body.Password != "" {
			var isAnon bool
			db.QueryRow(`SELECT coalesce(is_anonymous,false) FROM auth.users WHERE id=$1`, sub).Scan(&isAnon)
			if !isAnon {
				writeJSON(w, 400, map[string]string{"message": "email/password changes are only supported when upgrading an anonymous account"})
				return
			}
			body.Email = strings.ToLower(strings.TrimSpace(body.Email))
			if !strings.Contains(body.Email, "@") || len(body.Password) < minPw {
				writeJSON(w, 400, map[string]string{"message": fmt.Sprintf("valid email and password (%d+ chars) required to upgrade", minPw)})
				return
			}
			if msg := a.pwRejected(slug, body.Password); msg != "" {
				writeJSON(w, 400, map[string]string{"message": msg})
				return
			}
			hash, herr := hashPassword(body.Password)
			if herr != nil {
				writeJSON(w, 400, map[string]string{"message": "password too long (max 72 bytes)"})
				return
			}
			confirmed := "now()"
			if a.authConfirmEmail(slug) {
				confirmed = "NULL"
			}
			if _, err := db.Exec(`UPDATE auth.users SET email=$2, encrypted_password=$3,
					is_anonymous=false, email_confirmed_at=`+confirmed+` WHERE id=$1`,
				sub, body.Email, hash); err != nil {
				writeJSON(w, 400, map[string]string{"message": "that email is already registered"})
				return
			}
			if a.authConfirmEmail(slug) {
				a.sendConfirmationEmail(slug, body.Email)
			}
			a.auditRaw(body.Email, clientIP(r), "user-anon-upgrade", slug)
			writeJSON(w, 200, map[string]any{"id": sub, "email": body.Email, "is_anonymous": false})
			return
		}
		if len(body.Data) == 0 || !json.Valid(body.Data) {
			writeJSON(w, 400, map[string]string{"message": "a JSON object in \"data\" is required"})
			return
		}
		db.Exec(`UPDATE auth.users SET raw_user_meta_data=$2::jsonb WHERE id=$1`, sub, string(body.Data))
		var um string
		db.QueryRow(`SELECT raw_user_meta_data::text FROM auth.users WHERE id=$1`, sub).Scan(&um)
		writeJSON(w, 200, map[string]any{"id": sub, "email": claims["email"], "user_metadata": json.RawMessage(um)})

	case path == "/verify" && r.Method == http.MethodGet:
		token := r.URL.Query().Get("token")
		// Magic-link sign-in: verify, find-or-create a confirmed user, issue tokens.
		if email, ok := a.parseAuthToken(token, "magiclink", slug); ok {
			var uid string
			if db.QueryRow(`SELECT id FROM auth.users WHERE email=$1`, email).Scan(&uid) != nil {
				db.QueryRow(`INSERT INTO auth.users(email, encrypted_password, email_confirmed_at) VALUES ($1,'magiclink',now()) RETURNING id`, email).Scan(&uid)
			} else {
				db.Exec(`UPDATE auth.users SET email_confirmed_at=coalesce(email_confirmed_at,now()), last_sign_in_at=now() WHERE id=$1`, uid)
			}
			acc, ref, terr := a.issueTokens(db, secret, slug, uid, email, "")
			if terr != nil {
				writeJSON(w, 500, map[string]string{"message": terr.Error()})
				return
			}
			a.auditRaw(email, clientIP(r), "user-magiclink", slug)
			if rt := r.URL.Query().Get("redirect_to"); rt != "" && a.safeOAuthRedirect(slug, rt) {
				http.Redirect(w, r, rt+"#access_token="+acc+"&refresh_token="+ref+"&token_type=bearer", http.StatusSeeOther)
				return
			}
			writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref, "token_type": "bearer",
				"user": map[string]string{"id": uid, "email": email}})
			return
		}
		// Email confirmation.
		if email, ok := a.parseAuthToken(token, "confirm", slug); ok {
			db.Exec(`UPDATE auth.users SET email_confirmed_at=now() WHERE email=$1 AND email_confirmed_at IS NULL`, email)
			a.auditRaw(email, clientIP(r), "email-confirmed", slug)
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<h2>Email confirmed</h2><p>Thanks - you can now sign in.</p>"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(400)
		w.Write([]byte("<h2>Invalid or expired link</h2>"))

	case path == "/otp" && r.Method == http.MethodPost:
		// A 6-digit sign-in code: by email, or by SMS when the project has
		// an SMS webhook configured (signInWithOtp email/phone flows).
		if a.authRateLimited(clientIP(r), a.authRateFor(slug)) {
			writeJSON(w, 429, map[string]string{"message": "too many requests"})
			return
		}
		var body struct {
			Email string `json:"email"`
			Phone string `json:"phone"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		if phone := normalizePhone(body.Phone); phone != "" {
			var recent int
			db.QueryRow(`SELECT count(*) FROM auth.one_time_tokens
				WHERE phone=$1 AND created_at > now() - interval '1 hour'`, phone).Scan(&recent)
			if recent < 4 {
				n, _ := randInt(1000000)
				code := fmt.Sprintf("%06d", n)
				sum := sha256.Sum256([]byte(code))
				db.Exec(`DELETE FROM auth.one_time_tokens WHERE phone=$1`, phone)
				db.Exec(`INSERT INTO auth.one_time_tokens(email, phone, code_hash, expires_at)
					VALUES ('', $1, $2, now() + interval '10 minutes')`, phone, hex.EncodeToString(sum[:]))
				a.sendSMSCode(slug, phone, code) // best-effort; never reveal existence
			}
			writeJSON(w, 200, map[string]string{"message": "if that number can sign in, a code is on its way"})
			return
		}
		if strings.Contains(body.Email, "@") {
			var recent int
			db.QueryRow(`SELECT count(*) FROM auth.one_time_tokens
				WHERE email=$1 AND created_at > now() - interval '1 hour'`, body.Email).Scan(&recent)
			if recent < 4 {
				n, _ := randInt(1000000)
				code := fmt.Sprintf("%06d", n)
				sum := sha256.Sum256([]byte(code))
				db.Exec(`DELETE FROM auth.one_time_tokens WHERE email=$1`, body.Email)
				db.Exec(`INSERT INTO auth.one_time_tokens(email, code_hash, expires_at)
					VALUES ($1,$2, now() + interval '10 minutes')`, body.Email, hex.EncodeToString(sum[:]))
				a.sendOTPEmail(slug, body.Email, code) // best-effort; never reveal existence
			}
		}
		writeJSON(w, 200, map[string]string{"message": "if that email can sign in, a code is on its way"})

	case path == "/verify" && r.Method == http.MethodPost:
		// Verify an emailed OTP code and issue a session (verifyOtp type:email).
		if a.authRateLimited(clientIP(r), a.authRateFor(slug)) {
			writeJSON(w, 429, map[string]string{"message": "too many requests"})
			return
		}
		var body struct {
			Email string `json:"email"`
			Phone string `json:"phone"`
			Token string `json:"token"`
			Type  string `json:"type"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		body.Token = strings.TrimSpace(body.Token)
		sum := sha256.Sum256([]byte(body.Token))
		if phone := normalizePhone(body.Phone); phone != "" {
			// SMS code -> session, creating the phone-only user on first login
			var otpID string
			err := db.QueryRow(`SELECT id FROM auth.one_time_tokens
				WHERE phone=$1 AND code_hash=$2 AND expires_at > now() AND attempts < 5`,
				phone, hex.EncodeToString(sum[:])).Scan(&otpID)
			if err != nil {
				db.Exec(`UPDATE auth.one_time_tokens SET attempts=attempts+1 WHERE phone=$1`, phone)
				writeJSON(w, 401, map[string]string{"message": "invalid or expired code"})
				return
			}
			db.Exec(`DELETE FROM auth.one_time_tokens WHERE id=$1`, otpID)
			var uid string
			if db.QueryRow(`SELECT id FROM auth.users WHERE phone=$1`, phone).Scan(&uid) != nil {
				db.QueryRow(`INSERT INTO auth.users(phone, encrypted_password, phone_confirmed_at)
					VALUES ($1,'otp',now()) RETURNING id`, phone).Scan(&uid)
			} else {
				db.Exec(`UPDATE auth.users SET phone_confirmed_at=coalesce(phone_confirmed_at,now()), last_sign_in_at=now() WHERE id=$1`, uid)
			}
			if uid == "" {
				writeJSON(w, 500, map[string]string{"message": "could not create the user"})
				return
			}
			acc, ref, terr := a.issueTokens(db, secret, slug, uid, "", "")
			if terr != nil {
				writeJSON(w, 500, map[string]string{"message": terr.Error()})
				return
			}
			a.auditRaw(phone, clientIP(r), "user-sms-login", slug)
			writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
				"token_type": "bearer", "expires_in": ttlSec,
				"user": map[string]string{"id": uid, "phone": phone}})
			return
		}
		var otpID string
		err := db.QueryRow(`SELECT id FROM auth.one_time_tokens
			WHERE email=$1 AND code_hash=$2 AND expires_at > now() AND attempts < 5`,
			body.Email, hex.EncodeToString(sum[:])).Scan(&otpID)
		if err != nil {
			db.Exec(`UPDATE auth.one_time_tokens SET attempts=attempts+1 WHERE email=$1`, body.Email)
			writeJSON(w, 401, map[string]string{"message": "invalid or expired code"})
			return
		}
		db.Exec(`DELETE FROM auth.one_time_tokens WHERE id=$1`, otpID)
		var uid string
		if db.QueryRow(`SELECT id FROM auth.users WHERE email=$1`, body.Email).Scan(&uid) != nil {
			db.QueryRow(`INSERT INTO auth.users(email, encrypted_password, email_confirmed_at)
				VALUES ($1,'otp',now()) RETURNING id`, body.Email).Scan(&uid)
		} else {
			db.Exec(`UPDATE auth.users SET email_confirmed_at=coalesce(email_confirmed_at,now()), last_sign_in_at=now() WHERE id=$1`, uid)
		}
		acc, ref, terr := a.issueTokens(db, secret, slug, uid, body.Email, "")
		if terr != nil {
			writeJSON(w, 500, map[string]string{"message": terr.Error()})
			return
		}
		a.auditRaw(body.Email, clientIP(r), "user-otp-login", slug)
		writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
			"token_type": "bearer", "expires_in": ttlSec,
			"user": map[string]string{"id": uid, "email": body.Email}})

	case path == "/magiclink" && r.Method == http.MethodPost:
		if a.authRateLimited(clientIP(r), a.authRateFor(slug)) {
			writeJSON(w, 429, map[string]string{"message": "too many requests"})
			return
		}
		var body struct {
			Email      string `json:"email"`
			RedirectTo string `json:"redirect_to"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		if strings.Contains(body.Email, "@") {
			a.sendMagicLinkEmail(slug, body.Email, body.RedirectTo) // best-effort; never reveal existence
		}
		writeJSON(w, 200, map[string]string{"message": "if that email can sign in, a magic link is on its way"})

	case path == "/recover" && r.Method == http.MethodPost && r.URL.Query().Get("token") == "":
		// Request a password-reset email (no token in the request).
		if a.authRateLimited(clientIP(r), a.authRateFor(slug)) {
			writeJSON(w, 429, map[string]string{"message": "too many requests"})
			return
		}
		var body struct {
			Email string `json:"email"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		var exists bool
		db.QueryRow(`SELECT true FROM auth.users WHERE email=$1`, body.Email).Scan(&exists)
		if exists {
			a.sendRecoveryEmail(slug, body.Email) // best-effort; response is the same regardless
		}
		writeJSON(w, 200, map[string]string{"message": "if that account exists, a reset email is on its way"})

	case path == "/recover" && r.Method == http.MethodGet:
		// The reset page (opened from the email link): a form to set a new password.
		if _, ok := a.parseAuthToken(r.URL.Query().Get("token"), "recover", slug); !ok {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(400)
			w.Write([]byte("<h2>Invalid or expired link</h2>"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><meta name=viewport content="width=device-width,initial-scale=1">
<body style="font-family:system-ui;max-width:380px;margin:3rem auto;padding:0 1rem">
<h2>Set a new password</h2>
<form method="post" action="/auth/v1/recover?token=` + r.URL.Query().Get("token") + `">
<input name=password type=password placeholder="new password (6+)" required minlength=6 style="width:100%;padding:.6rem;margin:.5rem 0;box-sizing:border-box">
<button type=submit style="padding:.6rem 1rem">Reset password</button></form></body>`))

	case path == "/recover" && r.Method == http.MethodPost:
		// Apply the new password using the token from the reset page.
		email, ok := a.parseAuthToken(r.URL.Query().Get("token"), "recover", slug)
		if !ok {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(400)
			w.Write([]byte("<h2>Invalid or expired link</h2>"))
			return
		}
		r.ParseForm()
		pw := r.FormValue("password")
		if len(pw) < 6 {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(400)
			w.Write([]byte("<h2>Password too short</h2><p>Use at least 6 characters.</p>"))
			return
		}
		if msg := a.pwRejected(slug, pw); msg != "" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(400)
			w.Write([]byte("<h2>Password not allowed</h2><p>It has appeared in known data breaches - please choose a different one.</p>"))
			return
		}
		hash, herr := hashPassword(pw)
		if herr != nil {
			writeJSON(w, 400, map[string]string{"message": "password too long"})
			return
		}
		db.Exec(`UPDATE auth.users SET encrypted_password=$2, email_confirmed_at=coalesce(email_confirmed_at,now()) WHERE email=$1`, email, hash)
		db.Exec(`UPDATE auth.refresh_tokens SET revoked=true WHERE user_id=(SELECT id FROM auth.users WHERE email=$1)`, email) // sign out other sessions
		a.auditRaw(email, clientIP(r), "password-reset", slug)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<h2>Password updated</h2><p>You can now sign in with your new password.</p>"))

	case path == "/admin/invite" && r.Method == http.MethodPost:
		// Invite an end user: create the account (if new) and email a sign-in link.
		if !a.isServiceRole(r, secret) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "service_role key required"})
			return
		}
		var body struct {
			Email      string `json:"email"`
			RedirectTo string `json:"redirect_to"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		if !strings.Contains(body.Email, "@") {
			writeJSON(w, 400, map[string]string{"message": "valid email required"})
			return
		}
		db.Exec(`INSERT INTO auth.users(email, encrypted_password) VALUES ($1,'invited') ON CONFLICT (email) DO NOTHING`, body.Email)
		if err := a.sendMagicLinkEmail(slug, body.Email, body.RedirectTo); err != nil {
			writeJSON(w, 500, map[string]string{"message": "could not send invite: " + err.Error()})
			return
		}
		a.auditRaw(body.Email, clientIP(r), "user-invited", slug)
		writeJSON(w, 200, map[string]string{"message": "invitation email sent"})

	case strings.HasPrefix(path, "/admin/users"):
		// Admin user management, gated by the service_role key.
		if !a.isServiceRole(r, secret) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "service_role key required"})
			return
		}
		a.handleAdminUsers(w, r, db, strings.TrimPrefix(path, "/admin/users"))

	default:
		writeJSON(w, 404, map[string]string{"message": "not found"})
	}
}

func (a *app) isServiceRole(r *http.Request, secret string) bool {
	key := r.Header.Get("apikey")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	claims, ok := verifyUserJWT([]byte(secret), key)
	if !ok {
		return false
	}
	role, _ := claims["role"].(string)
	return role == "service_role"
}

func nullTimeStr(t sql.NullTime) any {
	if t.Valid {
		return t.Time.UTC().Format(time.RFC3339)
	}
	return nil
}

func jsonOr(raw json.RawMessage, def string) string {
	if len(raw) > 0 && json.Valid(raw) {
		return string(raw)
	}
	return def
}

// handleAdminUsers is the service-role user-management API under
// /auth/v1/admin/users : list, create, get, update (ban/metadata/password), delete.
func (a *app) handleAdminUsers(w http.ResponseWriter, r *http.Request, db *sql.DB, rest string) {
	id := strings.Trim(rest, "/")
	switch {
	case id == "" && r.Method == http.MethodGet:
		rows, err := db.Query(`SELECT id, email, raw_user_meta_data::text, raw_app_meta_data::text,
			banned_until, created_at, last_sign_in_at FROM auth.users ORDER BY created_at DESC LIMIT 500`)
		if err != nil {
			writeJSON(w, 500, map[string]string{"message": err.Error()})
			return
		}
		users := []map[string]any{}
		for rows.Next() {
			var uid, email, um, am string
			var banned, created, last sql.NullTime
			rows.Scan(&uid, &email, &um, &am, &banned, &created, &last)
			users = append(users, map[string]any{"id": uid, "email": email,
				"user_metadata": json.RawMessage(um), "app_metadata": json.RawMessage(am),
				"banned_until": nullTimeStr(banned), "created_at": nullTimeStr(created), "last_sign_in_at": nullTimeStr(last)})
		}
		rows.Close()
		writeJSON(w, 200, map[string]any{"users": users})

	case id == "" && r.Method == http.MethodPost:
		var body struct {
			Email, Password string
			UserMetadata    json.RawMessage `json:"user_metadata"`
			AppMetadata     json.RawMessage `json:"app_metadata"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		if !strings.Contains(body.Email, "@") || len(body.Password) < 6 {
			writeJSON(w, 400, map[string]string{"message": "valid email and password (6+) required"})
			return
		}
		hash, herr := hashPassword(body.Password)
		if herr != nil {
			writeJSON(w, 400, map[string]string{"message": "password too long"})
			return
		}
		um, am := jsonOr(body.UserMetadata, "{}"), jsonOr(body.AppMetadata, "{}")
		var uid string
		if err := db.QueryRow(`INSERT INTO auth.users(email, encrypted_password, raw_user_meta_data, raw_app_meta_data)
			VALUES ($1,$2,$3::jsonb,$4::jsonb) RETURNING id`, body.Email, hash, um, am).Scan(&uid); err != nil {
			writeJSON(w, 400, map[string]string{"message": "user already exists"})
			return
		}
		a.adminUserJSON(w, db, uid)

	case id != "" && r.Method == http.MethodGet:
		a.adminUserJSON(w, db, id)

	case id != "" && r.Method == http.MethodPut:
		var body struct {
			Password     string          `json:"password"`
			BanDuration  string          `json:"ban_duration"` // "24h" to ban, "none"/"0" to unban
			UserMetadata json.RawMessage `json:"user_metadata"`
			AppMetadata  json.RawMessage `json:"app_metadata"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Password != "" {
			if hash, err := hashPassword(body.Password); err == nil {
				db.Exec(`UPDATE auth.users SET encrypted_password=$2 WHERE id=$1`, id, hash)
				db.Exec(`UPDATE auth.refresh_tokens SET revoked=true WHERE user_id=$1`, id)
			}
		}
		if len(body.UserMetadata) > 0 && json.Valid(body.UserMetadata) {
			db.Exec(`UPDATE auth.users SET raw_user_meta_data=$2::jsonb WHERE id=$1`, id, string(body.UserMetadata))
		}
		if len(body.AppMetadata) > 0 && json.Valid(body.AppMetadata) {
			db.Exec(`UPDATE auth.users SET raw_app_meta_data=$2::jsonb WHERE id=$1`, id, string(body.AppMetadata))
		}
		if body.BanDuration == "none" || body.BanDuration == "0" {
			db.Exec(`UPDATE auth.users SET banned_until=NULL WHERE id=$1`, id)
		} else if body.BanDuration != "" {
			if d, err := time.ParseDuration(body.BanDuration); err == nil {
				db.Exec(`UPDATE auth.users SET banned_until = now() + make_interval(secs => $2) WHERE id=$1`, id, int(d.Seconds()))
				db.Exec(`UPDATE auth.refresh_tokens SET revoked=true WHERE user_id=$1`, id)
			}
		}
		a.adminUserJSON(w, db, id)

	case id != "" && r.Method == http.MethodDelete:
		db.Exec(`DELETE FROM auth.refresh_tokens WHERE user_id=$1`, id)
		db.Exec(`DELETE FROM auth.users WHERE id=$1`, id)
		writeJSON(w, 200, map[string]string{"message": "user deleted"})

	default:
		writeJSON(w, 404, map[string]string{"message": "not found"})
	}
}

func (a *app) adminUserJSON(w http.ResponseWriter, db *sql.DB, id string) {
	var email, um, am string
	var banned, created, last sql.NullTime
	err := db.QueryRow(`SELECT email, raw_user_meta_data::text, raw_app_meta_data::text, banned_until, created_at, last_sign_in_at
		FROM auth.users WHERE id=$1`, id).Scan(&email, &um, &am, &banned, &created, &last)
	if err != nil {
		writeJSON(w, 404, map[string]string{"message": "user not found"})
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "email": email,
		"user_metadata": json.RawMessage(um), "app_metadata": json.RawMessage(am),
		"banned_until": nullTimeStr(banned), "created_at": nullTimeStr(created), "last_sign_in_at": nullTimeStr(last)})
}

// authAnonEnabled reports whether this project allows anonymous sign-ins.
func (a *app) authAnonEnabled(slug string) bool {
	var v bool
	a.db.QueryRow(`SELECT coalesce(anon_signins,false) FROM auth_config WHERE slug=$1`, slug).Scan(&v)
	return v
}

// saveAuthPolicy stores token TTL, password minimum and redirect allowlist.
func (a *app) saveAuthPolicy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/auth"
	ttlMin, e1 := strconv.Atoi(r.FormValue("ttl_min"))
	minPw, e2 := strconv.Atoi(r.FormValue("min_pw"))
	if e1 != nil || e2 != nil || ttlMin < 5 || ttlMin > 1440 || minPw < 6 || minPw > 72 {
		redirectErr(w, r, back, "Token lifetime 5-1440 minutes, password minimum 6-72 chars.")
		return
	}
	capSite := strings.TrimSpace(r.FormValue("captcha_site"))
	capSecret := strings.TrimSpace(r.FormValue("captcha_secret"))
	rate, rerr := strconv.Atoi(r.FormValue("rate_limit"))
	if rerr != nil || rate < 0 || rate > 1000 {
		redirectErr(w, r, back, "Rate limit must be 0 (off) to 1000 requests per minute per IP.")
		return
	}
	single := r.FormValue("single_session") == "on"
	leaked := r.FormValue("leaked_check") == "on"
	smsHook := strings.TrimSpace(r.FormValue("sms_webhook"))
	if smsHook != "" && !strings.HasPrefix(smsHook, "https://") {
		redirectErr(w, r, back, "The SMS webhook must be an https:// URL.")
		return
	}
	allow := strings.TrimSpace(r.FormValue("redirects"))
	for _, e := range strings.Split(allow, ",") {
		e = strings.TrimSpace(e)
		if e != "" && !strings.HasPrefix(e, "https://") {
			redirectErr(w, r, back, "Redirect allowlist entries must start with https:// ("+e+").")
			return
		}
	}
	a.db.Exec(`UPDATE auth_config SET access_ttl_min=$2, min_pw_len=$3, redirect_allowlist=$4,
		captcha_site=$5, captcha_secret=$6, rate_limit_per_min=$7, single_session=$8, leaked_check=$9,
		sms_webhook_url=$10 WHERE slug=$1`,
		slug, ttlMin, minPw, allow, capSite, capSecret, rate, single, leaked, smsHook)
	a.audit(r, "auth-policy", fmt.Sprintf("%s ttl=%dm pw>=%d", slug, ttlMin, minPw))
	redirectMsg(w, r, back, "Auth policies saved.")
}

// impersonateUser mints a short-lived (1 hour) access token AS an app user,
// so an admin can reproduce exactly what that user sees through RLS and the
// APIs. Panel-admin only; every use is audited.
func (a *app) impersonateUser(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	uid := strings.TrimSpace(r.FormValue("id"))
	secret, enabled := a.authConfig(slug)
	if !enabled || uid == "" {
		writeJSON(w, 400, map[string]string{"message": "auth is not enabled, or no user id given"})
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": err.Error()})
		return
	}
	var email, userMeta, appMeta string
	if err := db.QueryRow(`SELECT coalesce(email,''), raw_user_meta_data::text, raw_app_meta_data::text
			FROM auth.users WHERE id=$1`, uid).Scan(&email, &userMeta, &appMeta); err != nil {
		writeJSON(w, 404, map[string]string{"message": "no such user"})
		return
	}
	tok := signUserJWTTTL([]byte(secret), uid, email, userMeta, appMeta, 3600)
	a.audit(r, "auth-impersonate", slug+"/"+email)
	writeJSON(w, 200, map[string]string{"access_token": tok, "expires_in": "3600"})
}

func (a *app) setAuthAnon(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	on := r.FormValue("anon") == "on"
	a.db.Exec(`UPDATE auth_config SET anon_signins=$2 WHERE slug=$1`, slug, on)
	a.audit(r, "auth-anon", fmt.Sprintf("%s=%t", slug, on))
	redirectMsg(w, r, "/p/"+slug+"/auth", "Anonymous sign-ins updated.")
}

func (a *app) authConfig(slug string) (string, bool) {
	var enabled bool
	a.db.QueryRow(`SELECT enabled FROM auth_config WHERE slug=$1`, slug).Scan(&enabled)
	secret, _ := a.apiConfig(slug)
	return secret, enabled
}

// ----------------------------------------------------------------- panel page

func (a *app) authPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	_, enabled := a.authConfig(slug)
	type authUser struct {
		ID, Email, Created, LastSeen string
		Sessions                     int
		Anon                         bool
	}
	var users []authUser
	var count int
	search := strings.TrimSpace(r.URL.Query().Get("uq"))
	if enabled {
		if db, err := a.dbFor(slug); err == nil {
			rows, _ := db.Query(`SELECT u.id, coalesce(u.email,''), to_char(u.created_at,'Mon DD, YYYY'),
				coalesce(to_char(u.last_sign_in_at,'Mon DD, HH24:MI'),'never'),
				coalesce(u.is_anonymous,false),
				(SELECT count(*) FROM auth.refresh_tokens rt
					WHERE rt.user_id = u.id AND NOT rt.revoked AND rt.expires_at > now())
				FROM auth.users u
				WHERE $1 = '' OR u.email ILIKE '%'||$1||'%' OR u.id::text = $1
				ORDER BY u.created_at DESC LIMIT 200`, search)
			if rows != nil {
				for rows.Next() {
					var u authUser
					rows.Scan(&u.ID, &u.Email, &u.Created, &u.LastSeen, &u.Anon, &u.Sessions)
					users = append(users, u)
				}
				rows.Close()
			}
			db.QueryRow(`SELECT count(*) FROM auth.users`).Scan(&count)
		}
	}
	type provCfg struct {
		Name, ClientID string
		Enabled        bool
	}
	var providers []provCfg
	for _, p := range []string{"google", "github", "gitlab", "discord", "microsoft", "facebook", "twitch", "slack", "spotify", "linkedin", "bitbucket", "notion", "oidc"} {
		id, _, en := a.oauthConfig(slug, p)
		providers = append(providers, provCfg{Name: p, ClientID: id, Enabled: en})
	}
	var smtpHost, smtpUser, smtpFrom string
	var smtpPort int
	var confirmEmail bool
	a.db.QueryRow(`SELECT smtp_host, smtp_port, smtp_user, smtp_from, confirm_email FROM auth_config WHERE slug=$1`,
		slug).Scan(&smtpHost, &smtpPort, &smtpUser, &smtpFrom, &confirmEmail)
	if smtpPort == 0 {
		smtpPort = 587
	}
	content := renderContent(authPageBody, map[string]any{
		"Slug": slug, "Enabled": enabled, "Users": users, "Count": count,
		"Base":      "https://" + slug + "." + a.cfg.domain + "/auth/v1",
		"Callback":  "https://" + slug + "." + a.cfg.domain + "/auth/v1/callback",
		"Providers": providers,
		"SMTPHost":  smtpHost, "SMTPPort": smtpPort, "SMTPUser": smtpUser, "SMTPFrom": smtpFrom,
		"ConfirmEmail": confirmEmail, "AnonOn": a.authAnonEnabled(slug), "UserQuery": search,
		"Tpls": func() map[string]string {
			out := map[string]string{}
			for _, k := range []string{"confirm", "magic", "recover", "otp"} {
				var subj, body string
				a.db.QueryRow(`SELECT coalesce(tpl_`+k+`_subject,''), coalesce(tpl_`+k+`_body,'')
					FROM auth_config WHERE slug=$1`, slug).Scan(&subj, &body)
				out["subj_"+k], out["body_"+k] = subj, body
			}
			return out
		}(),
		"CaptchaSite": func() string {
			var v string
			a.db.QueryRow(`SELECT coalesce(captcha_site,'') FROM auth_config WHERE slug=$1`, slug).Scan(&v)
			return v
		}(),
		"CaptchaSecret": func() string {
			var v string
			a.db.QueryRow(`SELECT coalesce(captcha_secret,'') FROM auth_config WHERE slug=$1`, slug).Scan(&v)
			return v
		}(),
		"TTLMin": func() int { t, _, _ := a.authPolicy(slug); return t / 60 }(),
		"MinPw":  func() int { _, m, _ := a.authPolicy(slug); return m }(),
		"Redirects": func() string {
			var raw string
			a.db.QueryRow(`SELECT coalesce(redirect_allowlist,'') FROM auth_config WHERE slug=$1`, slug).Scan(&raw)
			return raw
		}(),
		"RateLimit": a.authRateFor(slug),
		"SingleSession": func() bool {
			var v bool
			a.db.QueryRow(`SELECT coalesce(single_session,false) FROM auth_config WHERE slug=$1`, slug).Scan(&v)
			return v
		}(),
		"LeakedCheck": func() bool {
			var v bool
			a.db.QueryRow(`SELECT coalesce(leaked_check,false) FROM auth_config WHERE slug=$1`, slug).Scan(&v)
			return v
		}(),
		"OIDCIssuer": a.oauthIssuer(slug),
		"SMSHook": func() string {
			var v string
			a.db.QueryRow(`SELECT coalesce(sms_webhook_url,'') FROM auth_config WHERE slug=$1`, slug).Scan(&v)
			return v
		}(),
	})
	a.renderShell(w, r, shellData{Title: slug + " · Auth", Nav: "authn", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Auth"}}}, content)
}

func (a *app) enableAuth(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, err := a.ensureAuth(slug); err != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", "Setup failed: "+err.Error())
		return
	}
	a.audit(r, "auth-enable", slug)
	redirectMsg(w, r, "/p/"+slug+"/auth", "Auth enabled.")
}

func (a *app) disableAuth(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a.db.Exec(`UPDATE auth_config SET enabled=false WHERE slug=$1`, slug)
	a.audit(r, "auth-disable", slug)
	redirectMsg(w, r, "/p/"+slug+"/auth", "Auth disabled.")
}

// addAuthUser lets an admin create an end-user manually.
func (a *app) addAuthUser(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	pass := r.FormValue("password")
	if !strings.Contains(email, "@") || len(pass) < 6 {
		redirectErr(w, r, "/p/"+slug+"/auth", "Enter a valid email and a password of 6+ characters.")
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", err.Error())
		return
	}
	hash, herr := hashPassword(pass)
	if herr != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", "Password too long (max 72 characters).")
		return
	}
	if _, err := db.Exec(`INSERT INTO auth.users(email, encrypted_password) VALUES ($1,$2)`, email, hash); err != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", "User already exists.")
		return
	}
	a.audit(r, "auth-user-add", slug+"/"+email)
	redirectMsg(w, r, "/p/"+slug+"/auth", "User "+email+" added.")
}

// setAuthUserPassword resets an end-user's password.
func (a *app) setAuthUserPassword(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	id := r.FormValue("id")
	pass := r.FormValue("password")
	if len(pass) < 6 {
		redirectErr(w, r, "/p/"+slug+"/auth", "Password must be 6+ characters.")
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", err.Error())
		return
	}
	hash, herr := hashPassword(pass)
	if herr != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", "Password too long (max 72 characters).")
		return
	}
	var email string
	if db.QueryRow(`SELECT email FROM auth.users WHERE id=$1`, id).Scan(&email) != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", "No such user.")
		return
	}
	if _, err := db.Exec(`UPDATE auth.users SET encrypted_password=$1 WHERE id=$2`, hash, id); err != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", "Could not reset password.")
		return
	}
	a.audit(r, "auth-user-password", slug+"/"+email)
	redirectMsg(w, r, "/p/"+slug+"/auth", "Password reset.")
}

// revokeUserSessions kills every live refresh token for one end user - their
// current access tokens age out within the project TTL.
func (a *app) revokeUserSessions(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	uid := r.FormValue("id")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", err.Error())
		return
	}
	res, err := db.Exec(`UPDATE auth.refresh_tokens SET revoked=true
		WHERE user_id = $1::uuid AND NOT revoked`, uid)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", "Revoke failed: "+err.Error())
		return
	}
	n, _ := res.RowsAffected()
	a.audit(r, "user-sessions-revoked", slug+"/"+uid)
	redirectMsg(w, r, "/p/"+slug+"/auth", fmt.Sprintf("Signed the user out everywhere (%d session(s) revoked).", n))
}

func (a *app) deleteAuthUser(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	id := r.FormValue("id")
	db, err := a.dbFor(slug)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/auth", err.Error())
		return
	}
	var email string
	db.QueryRow(`SELECT email FROM auth.users WHERE id=$1`, id).Scan(&email)
	db.Exec(`DELETE FROM auth.users WHERE id=$1`, id)
	a.audit(r, "auth-user-delete", slug+"/"+email)
	redirectMsg(w, r, "/p/"+slug+"/auth", "User "+email+" deleted.")
}
