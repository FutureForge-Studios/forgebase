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

func (a *app) sendConfirmationEmail(slug, email string) error {
	link := "https://" + slug + "." + a.cfg.domain + "/auth/v1/verify?token=" + a.signAuthToken("confirm", slug, email, 24*time.Hour)
	def := fmt.Sprintf(`<p>Confirm your email to finish signing up.</p>
<p><a href="%s">Confirm my email</a></p>
<p>Or paste this link into your browser:<br>%s</p>
<p>This link expires in 24 hours.</p>`, link, link)
	subj, body := a.emailTemplate(slug, "confirm", "Confirm your email", def)
	return a.sendEmail(slug, email, subj, renderEmailTpl(body, link, ""))
}

func (a *app) sendRecoveryEmail(slug, email string) error {
	link := "https://" + slug + "." + a.cfg.domain + "/auth/v1/recover?token=" + a.signAuthToken("recover", slug, email, 1*time.Hour)
	def := fmt.Sprintf(`<p>Reset your password:</p>
<p><a href="%s">Choose a new password</a></p>
<p>Or paste this link into your browser:<br>%s</p>
<p>This link expires in 1 hour. If you didn't request it, ignore this email.</p>`, link, link)
	subj, body := a.emailTemplate(slug, "recover", "Reset your password", def)
	return a.sendEmail(slug, email, subj, renderEmailTpl(body, link, ""))
}

// sendOTPEmail delivers a short-lived numeric sign-in code.
func (a *app) sendOTPEmail(slug, email, code string) error {
	def := fmt.Sprintf(`<p>Your sign-in code:</p>
<p style="font-size:28px;letter-spacing:.3em;font-family:monospace"><b>%s</b></p>
<p>It expires in 10 minutes. If you did not request it, ignore this email.</p>`, code)
	subj, body := a.emailTemplate(slug, "otp", "Your sign-in code", def)
	return a.sendEmail(slug, email, subj, renderEmailTpl(body, "", code))
}

func (a *app) sendMagicLinkEmail(slug, email, redirectTo string) error {
	link := "https://" + slug + "." + a.cfg.domain + "/auth/v1/verify?token=" + a.signAuthToken("magiclink", slug, email, 1*time.Hour)
	if redirectTo != "" {
		link += "&redirect_to=" + url.QueryEscape(redirectTo)
	}
	def := fmt.Sprintf(`<p>Click to sign in:</p>
<p><a href="%s">Sign in</a></p>
<p>Or paste this link into your browser:<br>%s</p>
<p>This link expires in 1 hour.</p>`, link, link)
	subj, body := a.emailTemplate(slug, "magic", "Your sign-in link", def)
	return a.sendEmail(slug, email, subj, renderEmailTpl(body, link, ""))
}
