package main

// Asymmetric JWT signing (RS256) with a JWKS endpoint, per project and
// opt-in. When enabled, END-USER access tokens are signed with a project
// RSA key and any third party can verify them against
// https://<slug>.<domain>/.well-known/jwks.json - no shared secret needed.
// The anon/service_role API keys stay HS256 (already distributed), and
// PostgREST verifies BOTH by receiving a JWK Set that contains the
// symmetric secret as an oct key alongside the RSA public keys (verified
// live against PostgREST 14.15 before shipping). Rotation keeps the
// previous public key in the set so outstanding tokens stay valid.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"sync"
)

// package-level verifier cache so verifyUserJWT (a free function) can check
// RS256 tokens by kid without knowing which project minted them.
var (
	rsMu   sync.Mutex
	rsPubs = map[string]*rsa.PublicKey{}
)

func rsRegister(kid, pubPEM string) {
	if kid == "" || pubPEM == "" {
		return
	}
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil {
		return
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return
	}
	if rpub, ok := pub.(*rsa.PublicKey); ok {
		rsMu.Lock()
		rsPubs[kid] = rpub
		rsMu.Unlock()
	}
}

// verifyRS256 checks an RS256 token against the registered key for its kid.
func verifyRS256(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	rawH, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	var hdr struct{ Alg, Kid string }
	if json.Unmarshal(rawH, &hdr) != nil || hdr.Alg != "RS256" {
		return nil, false
	}
	rsMu.Lock()
	pub := rsPubs[hdr.Kid]
	rsMu.Unlock()
	if pub == nil {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		return nil, false
	}
	rawP, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if json.Unmarshal(rawP, &claims) != nil {
		return nil, false
	}
	return claims, true
}

// tokenAlgIsRS reports whether a token's header says RS256, so the HS
// verifier knows to hand it over.
func tokenAlgIsRS(token string) bool {
	i := strings.IndexByte(token, '.')
	if i < 0 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[:i])
	if err != nil {
		return false
	}
	var hdr struct{ Alg string }
	json.Unmarshal(raw, &hdr)
	return hdr.Alg == "RS256"
}

// rsInfo returns the project's signing state.
func (a *app) rsInfo(slug string) (kid, pub, oldKid, oldPub string, enabled bool) {
	a.db.QueryRow(`SELECT coalesce(rs_kid,''), coalesce(rs_pub,''), coalesce(rs_old_kid,''),
			coalesce(rs_old_pub,''), coalesce(rs_enabled,false)
		FROM api_config WHERE slug=$1`, slug).Scan(&kid, &pub, &oldKid, &oldPub, &enabled)
	return
}

// rsPrivate decrypts a project's RSA private key ("" state = nil).
func (a *app) rsPrivate(slug string) *rsa.PrivateKey {
	var privPEM string
	a.db.QueryRow(`SELECT coalesce(pgp_sym_decrypt(rs_priv_enc,$2),'') FROM api_config WHERE slug=$1`,
		slug, string(a.cfg.secret)).Scan(&privPEM)
	if privPEM == "" {
		return nil
	}
	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		return nil
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil
	}
	return key
}

// resignRS re-signs an HS256 token's payload with the project key. On any
// failure the original HS token is returned - signing must never break login.
func (a *app) resignRS(slug, hsToken string) string {
	kid, _, _, _, enabled := a.rsInfo(slug)
	if !enabled || kid == "" {
		return hsToken
	}
	priv := a.rsPrivate(slug)
	if priv == nil {
		return hsToken
	}
	parts := strings.Split(hsToken, ".")
	if len(parts) != 3 {
		return hsToken
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + parts[1]
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		return hsToken
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// jwkFromPub converts a public key to its JWK map form.
func jwkFromPub(kid string, pub *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func parsePubPEM(pubPEM string) *rsa.PublicKey {
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil {
		return nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil
	}
	rpub, _ := pub.(*rsa.PublicKey)
	return rpub
}

// jwksJSON renders the public JWK Set for a project ("" when none).
func (a *app) jwksJSON(slug string) string {
	kid, pubPEM, oldKid, oldPubPEM, _ := a.rsInfo(slug)
	var keys []map[string]string
	if p := parsePubPEM(pubPEM); p != nil {
		keys = append(keys, jwkFromPub(kid, p))
	}
	if p := parsePubPEM(oldPubPEM); p != nil && oldKid != "" {
		keys = append(keys, jwkFromPub(oldKid, p))
	}
	if len(keys) == 0 {
		return ""
	}
	out, _ := json.Marshal(map[string]any{"keys": keys})
	return string(out)
}

// pgrstJWKSecret builds PGRST_JWT_SECRET: the plain secret normally, or a
// JWK Set (oct secret + RSA public keys) when asymmetric signing is on.
func (a *app) pgrstJWKSecret(slug, secret string) string {
	_, _, _, _, enabled := a.rsInfo(slug)
	if !enabled {
		return secret
	}
	kid, pubPEM, oldKid, oldPubPEM, _ := a.rsInfo(slug)
	keys := []map[string]string{{
		"kty": "oct", "alg": "HS256",
		"k": base64.RawURLEncoding.EncodeToString([]byte(secret)),
	}}
	if p := parsePubPEM(pubPEM); p != nil {
		keys = append(keys, jwkFromPub(kid, p))
	}
	if p := parsePubPEM(oldPubPEM); p != nil && oldKid != "" {
		keys = append(keys, jwkFromPub(oldKid, p))
	}
	out, _ := json.Marshal(map[string]any{"keys": keys})
	return string(out)
}

// loadAllRSKeys warms the verifier cache at boot.
func (a *app) loadAllRSKeys() {
	rows, err := a.db.Query(`SELECT coalesce(rs_kid,''), coalesce(rs_pub,''),
			coalesce(rs_old_kid,''), coalesce(rs_old_pub,'')
		FROM api_config WHERE coalesce(rs_kid,'') <> ''`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kid, pub, oldKid, oldPub string
		rows.Scan(&kid, &pub, &oldKid, &oldPub)
		rsRegister(kid, pub)
		rsRegister(oldKid, oldPub)
	}
}

// rsGenerate makes and stores a fresh keypair, rotating the current one
// into the "old" slot (its tokens stay verifiable).
func (a *app) rsGenerate(slug string) error {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	kid := randHex(8)
	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}))
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return err
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	_, err = a.db.Exec(`UPDATE api_config SET rs_old_kid=coalesce(rs_kid,''), rs_old_pub=coalesce(rs_pub,''),
			rs_kid=$2, rs_pub=$3, rs_priv_enc=pgp_sym_encrypt($4,$5) WHERE slug=$1`,
		slug, kid, pubPEM, privPEM, string(a.cfg.secret))
	if err != nil {
		return err
	}
	rsRegister(kid, pubPEM)
	return nil
}

// serveJWKS answers /.well-known/jwks.json on the project subdomain.
func (a *app) serveJWKS(w http.ResponseWriter, slug string) {
	set := a.jwksJSON(slug)
	if set == "" {
		writeJSON(w, 404, map[string]string{"message": "asymmetric signing is not enabled for this project"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write([]byte(set))
}

// jwksToggle enables, rotates or disables asymmetric signing (API page).
func (a *app) jwksToggle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/api"
	switch r.FormValue("op") {
	case "enable":
		kid, _, _, _, _ := a.rsInfo(slug)
		if kid == "" {
			if err := a.rsGenerate(slug); err != nil {
				redirectErr(w, r, back, "Key generation failed: "+err.Error())
				return
			}
		}
		a.db.Exec(`UPDATE api_config SET rs_enabled=true WHERE slug=$1`, slug)
		a.stopPostgREST(slug) // restart with the JWK Set secret
		a.audit(r, "jwks-enable", slug)
		redirectMsg(w, r, back, "Asymmetric signing is on: new user tokens are RS256, verifiable at /.well-known/jwks.json. Anon and service keys are unchanged.")
	case "rotate":
		if err := a.rsGenerate(slug); err != nil {
			redirectErr(w, r, back, "Rotation failed: "+err.Error())
			return
		}
		a.stopPostgREST(slug)
		a.audit(r, "jwks-rotate", slug)
		redirectMsg(w, r, back, "Key rotated. The previous public key stays in the JWKS until the next rotation, so outstanding tokens remain valid.")
	case "disable":
		a.db.Exec(`UPDATE api_config SET rs_enabled=false WHERE slug=$1`, slug)
		a.stopPostgREST(slug)
		a.audit(r, "jwks-disable", slug)
		redirectMsg(w, r, back, "Back to HS256 signing. The JWKS endpoint stays up so old RS256 tokens verify until they expire.")
	default:
		redirectErr(w, r, back, "Unknown operation.")
	}
}
