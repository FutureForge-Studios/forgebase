package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
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
const accessTokenTTL = 3600

// signUserJWT mints a short-lived authenticated-user access token. user_metadata
// and app_metadata are embedded as raw JSON objects so apps (and RLS via
// auth.jwt()) can read them, matching the Supabase claim shape.
func signUserJWT(secret []byte, sub, email, userMeta, appMeta string) string {
	if !json.Valid([]byte(userMeta)) {
		userMeta = "{}"
	}
	if !json.Valid([]byte(appMeta)) {
		appMeta = "{}"
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	now := time.Now().Unix()
	claims, _ := json.Marshal(map[string]any{
		"sub": sub, "email": email, "role": "authenticated", "aud": "authenticated",
		"iss": "pgforge", "iat": now, "exp": now + accessTokenTTL,
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
func (a *app) issueTokens(db *sql.DB, secret, sub, email, familyID string) (string, string, error) {
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
	db.QueryRow(`SELECT raw_user_meta_data::text, raw_app_meta_data::text FROM auth.users WHERE id=$1`, sub).Scan(&userMeta, &appMeta)
	return signUserJWT([]byte(secret), sub, email, userMeta, appMeta), refresh, nil
}

// handleRefresh validates a refresh token, ROTATES it (revoke the used one, mint
// a new pair in the same family) and returns a fresh access token. If an already-
// revoked token is presented (reuse - i.e. it was stolen and replayed), the whole
// token family is revoked so the compromised session cannot continue.
func (a *app) handleRefresh(w http.ResponseWriter, r *http.Request, db *sql.DB, secret string) {
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
	err := db.QueryRow(`SELECT rt.user_id, u.email, rt.family_id, rt.revoked, rt.expires_at <= now()
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
	acc, ref, terr := a.issueTokens(db, secret, id, email, familyID)
	if terr != nil {
		writeJSON(w, 500, map[string]string{"message": terr.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
		"token_type": "bearer", "expires_in": accessTokenTTL,
		"user": map[string]string{"id": id, "email": email}})
}

func verifyUserJWT(secret []byte, token string) (map[string]any, bool) {
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
			created_at timestamptz NOT NULL DEFAULT now(),
			last_sign_in_at timestamptz
		)`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS raw_user_meta_data jsonb NOT NULL DEFAULT '{}'`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS raw_app_meta_data jsonb NOT NULL DEFAULT '{}'`,
		`ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS banned_until timestamptz`,
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

	switch {
	case path == "/authorize" && r.Method == http.MethodGet:
		a.handleOAuthAuthorize(w, r, slug)
	case path == "/callback" && r.Method == http.MethodGet:
		a.handleOAuthCallback(w, r, slug, secret)

	case path == "/signup" && r.Method == http.MethodPost:
		if a.authRateLimited(clientIP(r)) {
			writeJSON(w, 429, map[string]string{"message": "too many requests"})
			return
		}
		var body struct {
			Email, Password string
			Data            json.RawMessage `json:"data"` // optional user_metadata
		}
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		if !strings.Contains(body.Email, "@") || len(body.Password) < 6 {
			writeJSON(w, 400, map[string]string{"message": "valid email and password (6+) required"})
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
		var id string
		err := db.QueryRow(`INSERT INTO auth.users(email, encrypted_password, raw_user_meta_data) VALUES ($1,$2,$3::jsonb) RETURNING id`,
			body.Email, hash, meta).Scan(&id)
		if err != nil {
			writeJSON(w, 400, map[string]string{"message": "user already exists"})
			return
		}
		acc, ref, terr := a.issueTokens(db, secret, id, body.Email, "")
		if terr != nil {
			writeJSON(w, 500, map[string]string{"message": terr.Error()})
			return
		}
		a.auditRaw(body.Email, clientIP(r), "user-signup", slug)
		writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
			"token_type": "bearer", "expires_in": accessTokenTTL,
			"user": map[string]string{"id": id, "email": body.Email}})

	case path == "/token" && r.Method == http.MethodPost:
		// grant_type=refresh_token exchanges a refresh token for a fresh pair.
		if r.URL.Query().Get("grant_type") == "refresh_token" {
			a.handleRefresh(w, r, db, secret)
			return
		}
		if a.authRateLimited(clientIP(r)) {
			writeJSON(w, 429, map[string]string{"message": "too many requests"})
			return
		}
		var body struct{ Email, Password string }
		json.NewDecoder(r.Body).Decode(&body)
		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		var id, hash string
		var bannedUntil sql.NullTime
		err := db.QueryRow(`SELECT id, encrypted_password, banned_until FROM auth.users WHERE email=$1`, body.Email).Scan(&id, &hash, &bannedUntil)
		if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil {
			writeJSON(w, 400, map[string]string{"message": "invalid login credentials"})
			return
		}
		if bannedUntil.Valid && bannedUntil.Time.After(time.Now()) {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "account is banned"})
			return
		}
		db.Exec(`UPDATE auth.users SET last_sign_in_at=now() WHERE id=$1`, id)
		acc, ref, terr := a.issueTokens(db, secret, id, body.Email, "")
		if terr != nil {
			writeJSON(w, 500, map[string]string{"message": terr.Error()})
			return
		}
		a.auditRaw(body.Email, clientIP(r), "user-login", slug)
		writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref,
			"token_type": "bearer", "expires_in": accessTokenTTL,
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
			Data json.RawMessage `json:"data"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Data) == 0 || !json.Valid(body.Data) {
			writeJSON(w, 400, map[string]string{"message": "a JSON object in \"data\" is required"})
			return
		}
		db.Exec(`UPDATE auth.users SET raw_user_meta_data=$2::jsonb WHERE id=$1`, sub, string(body.Data))
		var um string
		db.QueryRow(`SELECT raw_user_meta_data::text FROM auth.users WHERE id=$1`, sub).Scan(&um)
		writeJSON(w, 200, map[string]any{"id": sub, "email": claims["email"], "user_metadata": json.RawMessage(um)})

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
	type authUser struct{ ID, Email, Created, LastSeen string }
	var users []authUser
	var count int
	if enabled {
		if db, err := a.dbFor(slug); err == nil {
			rows, _ := db.Query(`SELECT id, email, to_char(created_at,'Mon DD, YYYY'),
				coalesce(to_char(last_sign_in_at,'Mon DD, HH24:MI'),'never') FROM auth.users ORDER BY created_at DESC LIMIT 200`)
			if rows != nil {
				for rows.Next() {
					var u authUser
					rows.Scan(&u.ID, &u.Email, &u.Created, &u.LastSeen)
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
	for _, p := range []string{"google", "github", "gitlab", "discord"} {
		id, _, en := a.oauthConfig(slug, p)
		providers = append(providers, provCfg{Name: p, ClientID: id, Enabled: en})
	}
	content := renderContent(authPageBody, map[string]any{
		"Slug": slug, "Enabled": enabled, "Users": users, "Count": count,
		"Base":      "https://" + slug + "." + a.cfg.domain + "/auth/v1",
		"Callback":  "https://" + slug + "." + a.cfg.domain + "/auth/v1/callback",
		"Providers": providers,
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
