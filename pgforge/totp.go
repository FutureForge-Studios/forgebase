package main

// Panel two-factor authentication (TOTP, RFC 6238): opt-in per user. After a
// correct password, an enrolled account must present a 6-digit authenticator
// code carried across the step by a short-lived HMAC-signed pre-auth token.
// The break-glass env admin has no database row and is deliberately exempt -
// recovering it already requires shell access to the server.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() string {
	buf := make([]byte, 20)
	rand.Read(buf)
	return b32.EncodeToString(buf)
}

func totpAt(secret string, t time.Time) (string, bool) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", "")))
	if err != nil || len(key) == 0 {
		return "", false
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(buf[:])
	h := m.Sum(nil)
	off := h[len(h)-1] & 0xf
	v := (uint32(h[off])&0x7f)<<24 | uint32(h[off+1])<<16 | uint32(h[off+2])<<8 | uint32(h[off+3])
	return fmt.Sprintf("%06d", v%1000000), true
}

// totpVerify accepts the current step plus one step either side (clock skew).
func totpVerify(secret, code string, t time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	ok := false
	for _, dt := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		if c, valid := totpAt(secret, t.Add(dt)); valid &&
			subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			ok = true
		}
	}
	return ok
}

// userTOTP returns (secret, enabled) for a panel user.
func (a *app) userTOTP(name string) (string, bool) {
	var sec string
	var on bool
	a.db.QueryRow(`SELECT coalesce(totp_secret,''), coalesce(totp_enabled,false)
		FROM users WHERE name=$1`, name).Scan(&sec, &on)
	return sec, on
}

// preauthToken carries "password verified, code pending" across the 2FA step.
func (a *app) preauthToken(name string, remember bool) string {
	exp := strconv.FormatInt(time.Now().Add(5*time.Minute).Unix(), 10)
	rem := "0"
	if remember {
		rem = "1"
	}
	return name + "|" + exp + "|" + rem + "|" + a.sign("preauth|"+name+"|"+exp+"|"+rem)
}

func (a *app) parsePreauth(tok string) (name string, remember, ok bool) {
	parts := strings.Split(tok, "|")
	if len(parts) != 4 {
		return "", false, false
	}
	name, expS, rem, sig := parts[0], parts[1], parts[2], parts[3]
	if subtle.ConstantTimeCompare([]byte(sig), []byte(a.sign("preauth|"+name+"|"+expS+"|"+rem))) != 1 {
		return "", false, false
	}
	exp, err := strconv.ParseInt(expS, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false, false
	}
	return name, rem == "1", true
}

// totpSubmit finishes a 2FA login.
func (a *app) totpSubmit(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if a.rateLimited(ip) {
		a.renderAuth(w, "Slow down", "", renderContent(loginForm, map[string]any{"Err": "Too many attempts. Wait 10 minutes."}))
		return
	}
	name, remember, ok := a.parsePreauth(r.FormValue("token"))
	if !ok {
		a.renderAuth(w, "Welcome back", "", renderContent(loginForm, map[string]any{"Err": "The sign-in step expired - start again."}))
		return
	}
	if a.acctLocked(name) {
		a.auditRaw(name, ip, "login-locked", "panel 2fa")
		a.renderAuth(w, "Locked", "", renderContent(loginForm, map[string]any{"Err": "This account is temporarily locked. Try again in 15 minutes."}))
		return
	}
	sec, on := a.userTOTP(name)
	if !on || sec == "" {
		// 2FA was disabled between steps; the password already passed
		a.setSession(w, r, name, remember)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !totpVerify(sec, r.FormValue("code"), time.Now()) {
		a.recordAttempt(ip)
		a.recordAcctFail(name)
		a.auditRaw(name, ip, "login-2fa-failed", "panel")
		time.Sleep(time.Second)
		a.renderAuth(w, "Two-factor code", "", renderContent(totpForm, map[string]any{
			"Token": r.FormValue("token"), "Err": "Wrong code - codes rotate every 30 seconds."}))
		return
	}
	a.setSession(w, r, name, remember)
	a.auditRaw(name, ip, "login", "panel (2fa)")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// totpSetup mints a pending secret; it only becomes active after a correct
// code confirms the authenticator actually has it.
func (a *app) totpSetup(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	sec := newTOTPSecret()
	if _, err := a.db.Exec(`UPDATE users SET totp_secret=$2, totp_enabled=false WHERE name=$1`, name, sec); err != nil {
		redirectErr(w, r, "/account", err.Error())
		return
	}
	a.audit(r, "totp-setup", name)
	redirectMsg(w, r, "/account", "Scan or enter the key in your authenticator app, then confirm with a code.")
}

func (a *app) totpConfirm(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	sec, _ := a.userTOTP(name)
	if sec == "" || !totpVerify(sec, r.FormValue("code"), time.Now()) {
		redirectErr(w, r, "/account", "That code did not match - try the current one.")
		return
	}
	a.db.Exec(`UPDATE users SET totp_enabled=true WHERE name=$1`, name)
	a.audit(r, "totp-enabled", name)
	redirectMsg(w, r, "/account", "Two-factor authentication is ON for your account.")
}

func (a *app) totpDisable(w http.ResponseWriter, r *http.Request) {
	name := currentUser(r)
	sec, on := a.userTOTP(name)
	if on && !totpVerify(sec, r.FormValue("code"), time.Now()) {
		redirectErr(w, r, "/account", "Enter a current code to turn 2FA off.")
		return
	}
	a.db.Exec(`UPDATE users SET totp_secret='', totp_enabled=false WHERE name=$1`, name)
	a.audit(r, "totp-disabled", name)
	redirectMsg(w, r, "/account", "Two-factor authentication is off.")
}

const totpForm = `
<form method="post" action="/login/totp" style="display:grid;gap: .9rem">
  {{if .Err}}<div class="flash err">{{.Err}}</div>{{end}}
  <input type="hidden" name="token" value="{{.Token}}">
  <label class="fld"><span class="lt">Authenticator code</span>
    <input type="text" name="code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" maxlength="6" placeholder="123456" required autofocus style="font-size:22px;letter-spacing:.35em;text-align:center;font-family:var(--mono)"></label>
  <button class="btn btn-primary" type="submit">Verify</button>
  <a href="/login" class="muted" style="font-size:12px;text-align:center">Back to sign in</a>
</form>`
