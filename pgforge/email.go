package main

import (
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Optional per-project SMTP for the auth email lifecycle (confirmation, password
// reset, magic link, invites). Everything here is dark until an operator sets an
// SMTP host + from-address on the Auth page; nothing about the no-SMTP flows
// changes when it is unset.

// smtpConfig returns a project's SMTP settings; ok is true only when a host and
// from-address are set (i.e. email sending is actually configured).
func (a *app) smtpConfig(slug string) (host string, port int, user, pass, from string, ok bool) {
	a.db.QueryRow(`SELECT smtp_host, smtp_port, smtp_user,
		coalesce(pgp_sym_decrypt(smtp_pass_enc,$2),''), smtp_from
		FROM auth_config WHERE slug=$1`, slug, string(a.cfg.secret)).Scan(&host, &port, &user, &pass, &from)
	ok = host != "" && from != ""
	return
}

func (a *app) authConfirmEmail(slug string) bool {
	var v bool
	a.db.QueryRow(`SELECT confirm_email FROM auth_config WHERE slug=$1`, slug).Scan(&v)
	return v
}

// sendEmail delivers an HTML email via the project's configured SMTP server
// (PLAIN auth + STARTTLS). Returns an error if SMTP isn't configured.
func (a *app) sendEmail(slug, to, subject, htmlBody string) error {
	host, port, user, pass, from, ok := a.smtpConfig(slug)
	if !ok {
		return fmt.Errorf("email (SMTP) is not configured for this project")
	}
	msg := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" + htmlBody
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	return smtp.SendMail(fmt.Sprintf("%s:%d", host, port), auth, from, []string{to}, []byte(msg))
}

// signAuthToken makes a signed, project-scoped, time-limited token of the given
// kind (confirm / recover / magiclink) for an email address.
func (a *app) signAuthToken(kind, slug, email string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	return a.signState(fmt.Sprintf("%s|%s|%s|%d", kind, slug, email, exp))
}

// parseAuthToken verifies a signed auth token of the expected kind for this
// project and returns the email if valid and unexpired.
func (a *app) parseAuthToken(token, wantKind, slug string) (email string, ok bool) {
	payload, valid := a.verifyState(token)
	f := strings.Split(payload, "|")
	if !valid || len(f) != 4 || f[0] != wantKind || f[1] != slug {
		return "", false
	}
	if exp, _ := strconv.ParseInt(f[3], 10, 64); time.Now().Unix() > exp {
		return "", false
	}
	return f[2], true
}

// saveAuthEmail stores a project's SMTP settings and the confirm-email toggle.
func (a *app) saveAuthEmail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	host := strings.TrimSpace(r.FormValue("smtp_host"))
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("smtp_port")))
	if port == 0 {
		port = 587
	}
	user := strings.TrimSpace(r.FormValue("smtp_user"))
	from := strings.TrimSpace(r.FormValue("smtp_from"))
	pass := r.FormValue("smtp_pass")
	confirm := r.FormValue("confirm_email") == "on"
	if confirm && host == "" {
		redirectErr(w, r, "/p/"+slug+"/auth", "Configure SMTP before requiring email confirmation.")
		return
	}
	if pass == "" { // keep the stored password when left blank
		a.db.Exec(`UPDATE auth_config SET smtp_host=$2, smtp_port=$3, smtp_user=$4, smtp_from=$5, confirm_email=$6 WHERE slug=$1`,
			slug, host, port, user, from, confirm)
	} else {
		a.db.Exec(`UPDATE auth_config SET smtp_host=$2, smtp_port=$3, smtp_user=$4, smtp_from=$5, confirm_email=$6, smtp_pass_enc=pgp_sym_encrypt($7,$8) WHERE slug=$1`,
			slug, host, port, user, from, confirm, pass, string(a.cfg.secret))
	}
	a.audit(r, "auth-email-config", slug)
	redirectMsg(w, r, "/p/"+slug+"/auth", "Email settings saved.")
}

// emailTemplate returns the operator-customized subject/body for one kind, or
// the built-in defaults. Bodies substitute {{link}} and {{code}}.
func (a *app) emailTemplate(slug, kind, defSubject, defBody string) (string, string) {
	var subj, body string
	a.db.QueryRow(`SELECT coalesce(tpl_`+kind+`_subject,''), coalesce(tpl_`+kind+`_body,'')
		FROM auth_config WHERE slug=$1`, slug).Scan(&subj, &body)
	if strings.TrimSpace(subj) == "" {
		subj = defSubject
	}
	if strings.TrimSpace(body) == "" {
		body = defBody
	}
	return subj, body
}

func renderEmailTpl(body, link, code string) string {
	body = strings.ReplaceAll(body, "{{link}}", link)
	body = strings.ReplaceAll(body, "{{code}}", code)
	return body
}

// saveEmailTemplates stores per-kind subject/body overrides (empty = default).
func (a *app) saveEmailTemplates(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	kinds := []string{"confirm", "magic", "recover", "otp"}
	for _, k := range kinds {
		subj := strings.TrimSpace(r.FormValue("subj_" + k))
		body := strings.TrimSpace(r.FormValue("body_" + k))
		if len(subj) > 200 || len(body) > 20000 {
			redirectErr(w, r, "/p/"+slug+"/auth", "Template too long ("+k+").")
			return
		}
		a.db.Exec(`UPDATE auth_config SET tpl_`+k+`_subject=$2, tpl_`+k+`_body=$3 WHERE slug=$1`,
			slug, subj, body)
	}
	a.audit(r, "auth-email-templates", slug)
	redirectMsg(w, r, "/p/"+slug+"/auth", "Email templates saved - empty fields fall back to the defaults.")
}

// emailShell wraps content in the platform's email design: a centered cream
// card with the project's name, built from tables and inline styles only so
// it renders identically in Gmail, Outlook, Apple Mail and the rest. Custom
// template overrides bypass this on purpose - what an operator authors is
// sent verbatim.
func emailShell(slug, preheader, content string) string {
	return `<!doctype html><html><body style="margin:0;padding:0;background-color:#f5f2eb;">
<span style="display:none;max-height:0;overflow:hidden;">` + preheader + `</span>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#f5f2eb;padding:32px 12px;">
<tr><td align="center">
<table role="presentation" width="440" cellpadding="0" cellspacing="0" style="max-width:440px;width:100%;">
<tr><td style="padding:0 8px 14px 8px;font-family:Georgia,'Times New Roman',serif;font-size:20px;color:#241f1a;letter-spacing:-0.2px;">` + slug + `</td></tr>
<tr><td style="background-color:#fdfcf9;border:1px solid #e2dccc;border-radius:14px;padding:30px 30px 26px 30px;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.65;color:#3a352e;">
` + content + `
</td></tr>
<tr><td style="padding:14px 8px 0 8px;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:11px;color:#8d8578;">
Sent by ` + slug + ` &middot; powered by ForgeBase</td></tr>
</table></td></tr></table></body></html>`
}

// emailButton renders the action button plus the plain-link fallback.
func emailButton(link, label string) string {
	return `<table role="presentation" cellpadding="0" cellspacing="0" style="margin:20px 0;"><tr>
<td style="background-color:#22775f;border-radius:999px;">
<a href="` + link + `" style="display:inline-block;padding:12px 28px;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:14px;font-weight:600;color:#fdfcf9;text-decoration:none;">` + label + `</a>
</td></tr></table>
<p style="margin:0;font-size:11.5px;color:#8d8578;">Button not working? Paste this into your browser:<br>
<a href="` + link + `" style="color:#22775f;word-break:break-all;">` + link + `</a></p>`
}

func (a *app) sendConfirmationEmail(slug, email string) error {
	link := "https://" + slug + "." + a.cfg.domain + "/auth/v1/verify?token=" + a.signAuthToken("confirm", slug, email, 24*time.Hour)
	def := emailShell(slug, "Confirm your email to finish signing up.",
		`<h1 style="margin:0 0 8px 0;font-family:Georgia,'Times New Roman',serif;font-size:22px;font-weight:500;color:#241f1a;">Confirm your email</h1>
<p style="margin:0;">One click and your account is ready.</p>`+
			emailButton(link, "Confirm my email")+
			`<p style="margin:18px 0 0 0;font-size:11.5px;color:#8d8578;">This link expires in 24 hours. If you did not sign up, you can ignore this email.</p>`)
	subj, body := a.emailTemplate(slug, "confirm", "Confirm your email", def)
	return a.sendEmail(slug, email, subj, renderEmailTpl(body, link, ""))
}

func (a *app) sendRecoveryEmail(slug, email string) error {
	link := "https://" + slug + "." + a.cfg.domain + "/auth/v1/recover?token=" + a.signAuthToken("recover", slug, email, 1*time.Hour)
	def := emailShell(slug, "Choose a new password.",
		`<h1 style="margin:0 0 8px 0;font-family:Georgia,'Times New Roman',serif;font-size:22px;font-weight:500;color:#241f1a;">Reset your password</h1>
<p style="margin:0;">Somebody (hopefully you) asked to reset the password for this account.</p>`+
			emailButton(link, "Choose a new password")+
			`<p style="margin:18px 0 0 0;font-size:11.5px;color:#8d8578;">This link expires in 1 hour. If you did not request it, ignore this email - nothing changes without the link.</p>`)
	subj, body := a.emailTemplate(slug, "recover", "Reset your password", def)
	return a.sendEmail(slug, email, subj, renderEmailTpl(body, link, ""))
}

// sendOTPEmail delivers a short-lived numeric sign-in code.
func (a *app) sendOTPEmail(slug, email, code string) error {
	def := emailShell(slug, "Your sign-in code: "+code,
		`<h1 style="margin:0 0 8px 0;font-family:Georgia,'Times New Roman',serif;font-size:22px;font-weight:500;color:#241f1a;">Your sign-in code</h1>
<p style="margin:0;">Enter this code to sign in:</p>
<table role="presentation" cellpadding="0" cellspacing="0" style="margin:20px 0;"><tr>
<td style="background-color:#f5f2eb;border:1px solid #e2dccc;border-radius:10px;padding:16px 26px;font-family:'Courier New',monospace;font-size:30px;font-weight:700;letter-spacing:10px;color:#22775f;">`+code+`</td>
</tr></table>
<p style="margin:0;font-size:11.5px;color:#8d8578;">It expires in 10 minutes and works once. If you did not request it, ignore this email.</p>`)
	subj, body := a.emailTemplate(slug, "otp", "Your sign-in code", def)
	return a.sendEmail(slug, email, subj, renderEmailTpl(body, "", code))
}

func (a *app) sendMagicLinkEmail(slug, email, redirectTo string) error {
	link := "https://" + slug + "." + a.cfg.domain + "/auth/v1/verify?token=" + a.signAuthToken("magiclink", slug, email, 1*time.Hour)
	if redirectTo != "" {
		link += "&redirect_to=" + url.QueryEscape(redirectTo)
	}
	def := emailShell(slug, "Your one-click sign-in link.",
		`<h1 style="margin:0 0 8px 0;font-family:Georgia,'Times New Roman',serif;font-size:22px;font-weight:500;color:#241f1a;">Sign in to `+slug+`</h1>
<p style="margin:0;">No password needed - this link signs you straight in.</p>`+
			emailButton(link, "Sign in")+
			`<p style="margin:18px 0 0 0;font-size:11.5px;color:#8d8578;">This link expires in 1 hour and works once. If you did not request it, ignore this email.</p>`)
	subj, body := a.emailTemplate(slug, "magic", "Your sign-in link", def)
	return a.sendEmail(slug, email, subj, renderEmailTpl(body, link, ""))
}

// emailPreview renders a template exactly as it would be emailed - the
// browser shows the real HTML, custom overrides included.
func (a *app) emailPreview(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	kind := r.URL.Query().Get("kind")
	link := "https://" + slug + "." + a.cfg.domain + "/auth/v1/verify?token=EXAMPLE"
	var subj, body string
	switch kind {
	case "confirm":
		def := emailShell(slug, "Confirm your email to finish signing up.",
			`<h1 style="margin:0 0 8px 0;font-family:Georgia,'Times New Roman',serif;font-size:22px;font-weight:500;color:#241f1a;">Confirm your email</h1>
<p style="margin:0;">One click and your account is ready.</p>`+
				emailButton(link, "Confirm my email")+
				`<p style="margin:18px 0 0 0;font-size:11.5px;color:#8d8578;">This link expires in 24 hours. If you did not sign up, you can ignore this email.</p>`)
		subj, body = a.emailTemplate(slug, "confirm", "Confirm your email", def)
	case "magic":
		def := emailShell(slug, "Your one-click sign-in link.",
			`<h1 style="margin:0 0 8px 0;font-family:Georgia,'Times New Roman',serif;font-size:22px;font-weight:500;color:#241f1a;">Sign in to `+slug+`</h1>
<p style="margin:0;">No password needed - this link signs you straight in.</p>`+
				emailButton(link, "Sign in")+
				`<p style="margin:18px 0 0 0;font-size:11.5px;color:#8d8578;">This link expires in 1 hour and works once. If you did not request it, ignore this email.</p>`)
		subj, body = a.emailTemplate(slug, "magic", "Your sign-in link", def)
	case "recover":
		def := emailShell(slug, "Choose a new password.",
			`<h1 style="margin:0 0 8px 0;font-family:Georgia,'Times New Roman',serif;font-size:22px;font-weight:500;color:#241f1a;">Reset your password</h1>
<p style="margin:0;">Somebody (hopefully you) asked to reset the password for this account.</p>`+
				emailButton(link, "Choose a new password")+
				`<p style="margin:18px 0 0 0;font-size:11.5px;color:#8d8578;">This link expires in 1 hour. If you did not request it, ignore this email - nothing changes without the link.</p>`)
		subj, body = a.emailTemplate(slug, "recover", "Reset your password", def)
	default:
		def := emailShell(slug, "Your sign-in code: 483 921",
			`<h1 style="margin:0 0 8px 0;font-family:Georgia,'Times New Roman',serif;font-size:22px;font-weight:500;color:#241f1a;">Your sign-in code</h1>
<p style="margin:0;">Enter this code to sign in:</p>
<table role="presentation" cellpadding="0" cellspacing="0" style="margin:20px 0;"><tr>
<td style="background-color:#f5f2eb;border:1px solid #e2dccc;border-radius:10px;padding:16px 26px;font-family:'Courier New',monospace;font-size:30px;font-weight:700;letter-spacing:10px;color:#22775f;">483921</td>
</tr></table>
<p style="margin:0;font-size:11.5px;color:#8d8578;">It expires in 10 minutes and works once. If you did not request it, ignore this email.</p>`)
		subj, body = a.emailTemplate(slug, "otp", "Your sign-in code", def)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(renderEmailTpl(body, link, "483921")))
	_ = subj
}

// sendTestEmail proves the SMTP settings end to end: it sends the sign-in
// code template to an address the operator types, via this project's SMTP.
func (a *app) sendTestEmail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/auth"
	to := strings.TrimSpace(r.FormValue("to"))
	if !strings.Contains(to, "@") {
		redirectErr(w, r, back, "Enter the address to send the test to.")
		return
	}
	if err := a.sendOTPEmail(slug, to, "483921"); err != nil {
		redirectErr(w, r, back, "Test email failed: "+err.Error())
		return
	}
	a.audit(r, "email-test", slug+" -> "+to)
	redirectMsg(w, r, back, "Test email sent to "+to+" - check the inbox (and spam, the first time).")
}
