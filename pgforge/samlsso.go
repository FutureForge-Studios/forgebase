package main

// SAML 2.0 SSO for end users, per project - the enterprise door. The panel
// is the Service Provider; point it at any IdP's metadata (Okta, Azure AD,
// OneLogin, Keycloak, ADFS...) and users sign in there, landing back here
// with normal ForgeBase tokens. XML signature verification is deliberately
// NOT hand-rolled: it comes from the battle-tested crewjam/saml library.
//
//	GET  /auth/v1/saml/metadata  SP metadata XML (register this at the IdP)
//	GET  /auth/v1/saml/login     start sign-in (?redirect_to=...)
//	POST /auth/v1/saml/acs       assertion consumer (IdP posts back here)

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/crewjam/saml"
)

// pending AuthnRequest IDs -> relay target, so unsolicited assertions are
// refused (a core SAML safety property).
var (
	samlMu   sync.Mutex
	samlPend = map[string]struct {
		relay string
		exp   time.Time
	}{}
	samlIDPCache = map[string]*saml.EntityDescriptor{}
)

// samlConfig reads the project's SAML settings.
func (a *app) samlConfig(slug string) (enabled bool, metaURL, metaXML, cert string, keyPEM string) {
	a.db.QueryRow(`SELECT coalesce(saml_enabled,false), coalesce(saml_idp_url,''),
			coalesce(saml_idp_xml,''), coalesce(saml_sp_cert,''),
			coalesce(pgp_sym_decrypt(saml_sp_key_enc,$2),'')
		FROM auth_config WHERE slug=$1`, slug, string(a.cfg.secret)).
		Scan(&enabled, &metaURL, &metaXML, &cert, &keyPEM)
	return
}

// samlIDP resolves the IdP's metadata (fetched once per URL, or parsed from
// pasted XML).
func samlIDP(metaURL, metaXML string) (*saml.EntityDescriptor, error) {
	if metaXML != "" {
		var ed saml.EntityDescriptor
		if err := xml.Unmarshal([]byte(metaXML), &ed); err != nil {
			return nil, fmt.Errorf("could not parse the pasted IdP metadata XML: %w", err)
		}
		return &ed, nil
	}
	samlMu.Lock()
	if ed, ok := samlIDPCache[metaURL]; ok {
		samlMu.Unlock()
		return ed, nil
	}
	samlMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("IdP metadata unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(raw, &ed); err != nil {
		return nil, fmt.Errorf("could not parse IdP metadata: %w", err)
	}
	samlMu.Lock()
	samlIDPCache[metaURL] = &ed
	samlMu.Unlock()
	return &ed, nil
}

// samlSP builds the per-project ServiceProvider.
func (a *app) samlSP(slug string) (*saml.ServiceProvider, error) {
	enabled, metaURL, metaXML, certPEM, keyPEM := a.samlConfig(slug)
	if !enabled {
		return nil, fmt.Errorf("SAML is not enabled for this project")
	}
	if metaURL == "" && metaXML == "" {
		return nil, fmt.Errorf("no IdP metadata configured")
	}
	idp, err := samlIDP(metaURL, metaXML)
	if err != nil {
		return nil, err
	}
	kb, _ := pem.Decode([]byte(keyPEM))
	cb, _ := pem.Decode([]byte(certPEM))
	if kb == nil || cb == nil {
		return nil, fmt.Errorf("SP key material missing - disable and re-enable SAML")
	}
	key, err := x509.ParsePKCS1PrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	base := "https://" + slug + "." + a.cfg.domain + "/auth/v1/saml/"
	metaU, _ := url.Parse(base + "metadata")
	acsU, _ := url.Parse(base + "acs")
	return &saml.ServiceProvider{
		EntityID:    metaU.String(),
		Key:         key,
		Certificate: cert,
		MetadataURL: *metaU,
		AcsURL:      *acsU,
		IDPMetadata: idp,
	}, nil
}

// serveSAML handles /auth/v1/saml/{metadata,login,acs} for a project.
func (a *app) serveSAML(w http.ResponseWriter, r *http.Request, secret, slug, action string) {
	sp, err := a.samlSP(slug)
	if err != nil {
		writeJSON(w, 400, map[string]string{"message": err.Error()})
		return
	}
	switch action {
	case "metadata":
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		raw, _ := xml.MarshalIndent(sp.Metadata(), "", "  ")
		w.Write([]byte(xml.Header))
		w.Write(raw)

	case "login":
		redirectTo := r.URL.Query().Get("redirect_to")
		if !a.safeOAuthRedirect(slug, redirectTo) {
			writeJSON(w, 400, map[string]string{"message": "redirect_to must be on this project's domain or allowlist"})
			return
		}
		authReq, err := sp.MakeAuthenticationRequest(
			sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
			saml.HTTPRedirectBinding, saml.HTTPPostBinding)
		if err != nil {
			writeJSON(w, 502, map[string]string{"message": "could not build the sign-in request: " + err.Error()})
			return
		}
		samlMu.Lock()
		for id, p := range samlPend { // opportunistic expiry sweep
			if time.Now().After(p.exp) {
				delete(samlPend, id)
			}
		}
		samlPend[authReq.ID] = struct {
			relay string
			exp   time.Time
		}{redirectTo, time.Now().Add(10 * time.Minute)}
		samlMu.Unlock()
		dest, err := authReq.Redirect("", sp)
		if err != nil {
			writeJSON(w, 502, map[string]string{"message": err.Error()})
			return
		}
		http.Redirect(w, r, dest.String(), http.StatusSeeOther)

	case "acs":
		r.ParseForm()
		samlMu.Lock()
		var ids []string
		for id := range samlPend {
			ids = append(ids, id)
		}
		samlMu.Unlock()
		assertion, err := sp.ParseResponse(r, ids)
		if err != nil {
			msg := "assertion rejected"
			if ie, ok := err.(*saml.InvalidResponseError); ok {
				msg = "assertion rejected: " + ie.PrivateErr.Error()
			}
			writeJSON(w, 403, map[string]string{"message": msg})
			return
		}
		email := samlEmail(assertion)
		if email == "" {
			writeJSON(w, 502, map[string]string{"message": "the IdP sent no email - map an email/NameID attribute in the IdP app settings"})
			return
		}
		// find the request this answers, for its relay target
		relay := ""
		if resp := r.PostForm.Get("SAMLResponse"); resp != "" {
			samlMu.Lock()
			// InResponseTo is inside the (already verified) assertion
			if assertion.Subject != nil {
				for _, sc := range assertion.Subject.SubjectConfirmations {
					if sc.SubjectConfirmationData != nil {
						if p, ok := samlPend[sc.SubjectConfirmationData.InResponseTo]; ok {
							relay = p.relay
							delete(samlPend, sc.SubjectConfirmationData.InResponseTo)
						}
					}
				}
			}
			samlMu.Unlock()
		}
		db, err := a.dbFor(slug)
		if err != nil {
			writeJSON(w, 500, map[string]string{"message": err.Error()})
			return
		}
		var uid string
		if db.QueryRow(`SELECT id FROM auth.users WHERE email=$1`, email).Scan(&uid) != nil {
			if msg := beforeCreateHook(db, email); msg != "" {
				writeJSON(w, 400, map[string]string{"message": msg})
				return
			}
			db.QueryRow(`INSERT INTO auth.users(email, encrypted_password, email_confirmed_at)
				VALUES ($1,'saml',now()) RETURNING id`, email).Scan(&uid)
		}
		if uid == "" {
			writeJSON(w, 500, map[string]string{"message": "could not create the user"})
			return
		}
		db.Exec(`UPDATE auth.users SET last_sign_in_at=now(), email_confirmed_at=coalesce(email_confirmed_at,now()) WHERE id=$1`, uid)
		db.Exec(`INSERT INTO auth.identities(user_id, provider, email) VALUES ($1,'saml',$2)
			ON CONFLICT (user_id, provider) DO UPDATE SET last_sign_in_at=now(), email=$2`, uid, email)
		a.auditRaw(email, clientIP(r), "user-saml", slug)
		acc, ref, terr := a.issueTokens(db, secret, slug, uid, email, "")
		if terr != nil {
			writeJSON(w, 500, map[string]string{"message": terr.Error()})
			return
		}
		if relay != "" && a.safeOAuthRedirect(slug, relay) {
			http.Redirect(w, r, relay+"#access_token="+acc+"&refresh_token="+ref+"&token_type=bearer", http.StatusSeeOther)
			return
		}
		writeJSON(w, 200, map[string]any{"access_token": acc, "refresh_token": ref, "token_type": "bearer",
			"user": map[string]string{"id": uid, "email": email}})

	default:
		writeJSON(w, 404, map[string]string{"message": "unknown SAML endpoint"})
	}
}

// samlEmail digs the email out of an assertion: NameID when it looks like an
// address, else any attribute commonly used for mail.
func samlEmail(as *saml.Assertion) string {
	if as.Subject != nil && as.Subject.NameID != nil {
		if v := strings.TrimSpace(as.Subject.NameID.Value); strings.Contains(v, "@") {
			return strings.ToLower(v)
		}
	}
	for _, st := range as.AttributeStatements {
		for _, attr := range st.Attributes {
			n := strings.ToLower(attr.Name + " " + attr.FriendlyName)
			if strings.Contains(n, "email") || strings.Contains(n, "mail") {
				for _, v := range attr.Values {
					if strings.Contains(v.Value, "@") {
						return strings.ToLower(strings.TrimSpace(v.Value))
					}
				}
			}
		}
	}
	return ""
}

// saveSAML stores IdP settings; the first enable mints the SP keypair and
// self-signed certificate (10 years - it only identifies the SP to the IdP).
func (a *app) saveSAML(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/auth"
	enabled := r.FormValue("saml_enabled") == "on"
	metaURL := strings.TrimSpace(r.FormValue("saml_idp_url"))
	metaXML := strings.TrimSpace(r.FormValue("saml_idp_xml"))
	if enabled && metaURL == "" && metaXML == "" {
		redirectErr(w, r, back, "Give the IdP metadata URL (or paste its XML) to enable SAML.")
		return
	}
	if metaURL != "" && !strings.HasPrefix(metaURL, "https://") {
		redirectErr(w, r, back, "The IdP metadata URL must be https.")
		return
	}
	var haveCert string
	a.db.QueryRow(`SELECT coalesce(saml_sp_cert,'') FROM auth_config WHERE slug=$1`, slug).Scan(&haveCert)
	if enabled && haveCert == "" {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			redirectErr(w, r, back, "Key generation failed.")
			return
		}
		tpl := x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: slug + "." + a.cfg.domain},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().AddDate(10, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		}
		der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
		if err != nil {
			redirectErr(w, r, back, "Certificate generation failed.")
			return
		}
		keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
		certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		a.db.Exec(`UPDATE auth_config SET saml_sp_cert=$2, saml_sp_key_enc=pgp_sym_encrypt($3,$4) WHERE slug=$1`,
			slug, certPEM, keyPEM, string(a.cfg.secret))
	}
	// pasted XML replaces the URL and vice versa - whichever was filled wins
	a.db.Exec(`UPDATE auth_config SET saml_enabled=$2, saml_idp_url=$3, saml_idp_xml=$4 WHERE slug=$1`,
		slug, enabled, metaURL, metaXML)
	samlMu.Lock()
	delete(samlIDPCache, metaURL) // re-fetch on next use
	samlMu.Unlock()
	a.audit(r, "saml-config", fmt.Sprintf("%s enabled=%v", slug, enabled))
	if enabled {
		redirectMsg(w, r, back, "SAML is on. Register the SP metadata URL shown on this page with your IdP, then sign in via /auth/v1/saml/login.")
	} else {
		redirectMsg(w, r, back, "SAML settings saved.")
	}
}
