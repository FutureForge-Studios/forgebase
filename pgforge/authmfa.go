package main

// End-user TOTP MFA for project Auth. Enrollment is verify-to-activate (the
// authenticator must prove it holds the secret), activation returns ten
// single-use recovery codes (stored only as hashes), and password logins on
// enrolled accounts require a totp_code or an unused recovery_code.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// mfaState returns (secret, enabled) for an end user.
func mfaState(db *sql.DB, uid string) (string, bool) {
	var sec string
	var on bool
	db.QueryRow(`SELECT coalesce(totp_secret,''), coalesce(totp_enabled,false)
		FROM auth.users WHERE id=$1`, uid).Scan(&sec, &on)
	return sec, on
}

// tryRecoveryCode consumes one unused recovery code; true when it matched.
func tryRecoveryCode(db *sql.DB, uid, code string) bool {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	res, err := db.Exec(`UPDATE auth.recovery_codes SET used_at = now()
		WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`,
		uid, hex.EncodeToString(sum[:]))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// serveAuthMFA handles /auth/v1/factors/* for an authenticated end user.
func (a *app) serveAuthMFA(w http.ResponseWriter, r *http.Request, db *sql.DB, secret, slug, action string) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, ok := verifyUserJWT([]byte(secret), tok)
	if !ok {
		writeJSON(w, 401, map[string]string{"message": "invalid token"})
		return
	}
	uid, _ := claims["sub"].(string)
	var body struct {
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	switch action {
	case "enroll":
		sec := newTOTPSecret()
		if _, err := db.Exec(`UPDATE auth.users SET totp_secret=$2, totp_enabled=false WHERE id=$1`, uid, sec); err != nil {
			writeJSON(w, 500, map[string]string{"message": err.Error()})
			return
		}
		email, _ := claims["email"].(string)
		label := email
		if label == "" {
			label = uid
		}
		writeJSON(w, 200, map[string]string{
			"secret": sec,
			"uri":    "otpauth://totp/" + url.PathEscape(slug) + ":" + url.PathEscape(label) + "?secret=" + sec + "&issuer=" + url.QueryEscape(slug),
		})

	case "verify":
		sec, _ := mfaState(db, uid)
		if sec == "" || !totpVerify(sec, body.Code, time.Now()) {
			writeJSON(w, 401, map[string]string{"message": "wrong code - use the current one from your app"})
			return
		}
		db.Exec(`UPDATE auth.users SET totp_enabled=true WHERE id=$1`, uid)
		db.Exec(`DELETE FROM auth.recovery_codes WHERE user_id=$1`, uid)
		codes := make([]string, 10)
		for i := range codes {
			codes[i] = randHex(5) // 10 hex chars each
			sum := sha256.Sum256([]byte(codes[i]))
			db.Exec(`INSERT INTO auth.recovery_codes(user_id, code_hash) VALUES ($1,$2)`,
				uid, hex.EncodeToString(sum[:]))
		}
		a.auditRaw(uid, clientIP(r), "user-mfa-enabled", slug)
		writeJSON(w, 200, map[string]any{"enabled": true, "recovery_codes": codes,
			"message": "store these recovery codes now - they are shown once"})

	case "disable":
		sec, on := mfaState(db, uid)
		okCode := on && (totpVerify(sec, body.Code, time.Now()) || tryRecoveryCode(db, uid, body.RecoveryCode))
		if on && !okCode {
			writeJSON(w, 401, map[string]string{"message": "a current code or recovery code is required to disable MFA"})
			return
		}
		db.Exec(`UPDATE auth.users SET totp_secret='', totp_enabled=false WHERE id=$1`, uid)
		db.Exec(`DELETE FROM auth.recovery_codes WHERE user_id=$1`, uid)
		a.auditRaw(uid, clientIP(r), "user-mfa-disabled", slug)
		writeJSON(w, 200, map[string]any{"enabled": false})

	default:
		writeJSON(w, 404, map[string]string{"message": "unknown factor action"})
	}
}
