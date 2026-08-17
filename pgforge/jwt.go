package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

// signJWT mints an HS256 JWT - used for the per-project anon and service_role
// API keys that PostgREST validates against the project's JWT secret.
func signJWT(secret []byte, role string) string {
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	now := time.Now().Unix()
	claims, _ := json.Marshal(map[string]any{
		"role": role,
		"iss":  "pgforge",
		"iat":  now,
		"exp":  now + 10*365*24*3600, // 10 years
	})
	payload := b64(claims)
	signing := header + "." + payload
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(signing))
	return signing + "." + b64(m.Sum(nil))
}
