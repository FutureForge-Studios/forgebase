package main

// Path-level storage access rules. Each rule scopes a bucket prefix to an
// access class; the longest matching prefix wins, and a bucket with no
// matching rule falls back to its public/private flag:
//
//	public         anyone can read
//	authenticated  any signed-in user of this project
//	owner          the path's first segment after the prefix must equal the
//	               requesting user's id ("avatars/<uid>/...")
//	private        service_role and signed URLs only
//
// Rules also gate uploads: owner paths only accept the owner's own uid,
// private paths only service_role. service_role passes everything.

import (
	"net/http"
	"strings"
)

// storageRuleFor returns the access class and prefix of the longest rule
// matching this object path ("" when no rule matches).
func (a *app) storageRuleFor(slug, bucket, path string) (string, string) {
	rows, err := a.db.Query(`SELECT access, prefix FROM storage_rules
		WHERE slug=$1 AND bucket=$2 ORDER BY length(prefix) DESC, id`, slug, bucket)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	for rows.Next() {
		var access, prefix string
		rows.Scan(&access, &prefix)
		if strings.HasPrefix(path, prefix) {
			return access, prefix
		}
	}
	return "", ""
}

// storageClaims verifies the request's bearer/apikey against the project
// secret and returns its claims.
func (a *app) storageClaims(r *http.Request, slug string) (map[string]any, bool) {
	secret, _ := a.apiConfig(slug)
	if secret == "" {
		return nil, false
	}
	key := r.Header.Get("apikey")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return verifyUserJWT([]byte(secret), key)
}

// ownerSegment extracts the path segment right after the rule prefix - the
// part an "owner" rule compares to the user id.
func ownerSegment(path, prefix string) string {
	rest := strings.TrimPrefix(strings.TrimPrefix(path, prefix), "/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// storageRuleAllows evaluates one access class for a request. Returns
// (allowed, hadRule): hadRule=false means no rule matched and the caller
// should fall back to the bucket flag.
func (a *app) storageRuleAllows(r *http.Request, slug, bucket, path string) (bool, bool) {
	access, prefix := a.storageRuleFor(slug, bucket, path)
	if access == "" {
		return false, false
	}
	if access == "public" {
		return true, true
	}
	claims, ok := a.storageClaims(r, slug)
	if !ok {
		return false, true
	}
	role, _ := claims["role"].(string)
	if role == "service_role" {
		return true, true
	}
	switch access {
	case "authenticated":
		return role == "authenticated", true
	case "owner":
		sub, _ := claims["sub"].(string)
		return role == "authenticated" && sub != "" && ownerSegment(path, prefix) == sub, true
	}
	return false, true // private
}

func (a *app) storageRuleAdd(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/storage"
	bucket := strings.TrimSpace(r.FormValue("bucket"))
	prefix := strings.Trim(strings.TrimSpace(r.FormValue("prefix")), "/")
	access := r.FormValue("access")
	switch access {
	case "public", "authenticated", "owner", "private":
	default:
		redirectErr(w, r, back, "Access must be public, authenticated, owner or private.")
		return
	}
	if !a.bucketExists(slug, bucket) {
		redirectErr(w, r, back, "Unknown bucket.")
		return
	}
	if strings.Contains(prefix, "..") {
		redirectErr(w, r, back, "Invalid prefix.")
		return
	}
	a.db.Exec(`INSERT INTO storage_rules(slug,bucket,prefix,access) VALUES ($1,$2,$3,$4)`,
		slug, bucket, prefix, access)
	a.audit(r, "storage-rule-add", slug+"/"+bucket+"/"+prefix+"="+access)
	redirectMsg(w, r, back, "Rule added: "+bucket+"/"+prefix+" is now "+access+".")
}

func (a *app) storageRuleDelete(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a.db.Exec(`DELETE FROM storage_rules WHERE slug=$1 AND id=$2`, slug, r.FormValue("id"))
	a.audit(r, "storage-rule-del", slug)
	redirectMsg(w, r, "/p/"+slug+"/storage", "Rule removed.")
}

type storageRule struct {
	ID                     int64
	Bucket, Prefix, Access string
}

func (a *app) listStorageRules(slug string) []storageRule {
	rows, err := a.db.Query(`SELECT id, bucket, prefix, access FROM storage_rules
		WHERE slug=$1 ORDER BY bucket, length(prefix) DESC`, slug)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []storageRule
	for rows.Next() {
		var s storageRule
		rows.Scan(&s.ID, &s.Bucket, &s.Prefix, &s.Access)
		out = append(out, s)
	}
	return out
}
