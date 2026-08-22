package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// escapePath percent-encodes each path segment so filenames with spaces, '#' or
// '?' produce valid, unbroken URLs (the '#'/'?' would otherwise truncate the link
// and, for signed URLs, break HMAC verification).
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// Storage = file buckets per project on local disk, with metadata in the meta
// DB and signed + public object URLs served on the project subdomain:
//   public:  https://<slug>.<domain>/storage/v1/object/public/<bucket>/<path>
//   signed:  https://<slug>.<domain>/storage/v1/object/sign/<bucket>/<path>?token=...
// Files live under /opt/pgforge-storage/<slug>/<bucket>/<path>.

const storageRoot = "/opt/pgforge-storage"

var bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,40}$`)

func safeRel(p string) string {
	p = strings.TrimLeft(filepath.ToSlash(p), "/")
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

// bucketExists validates the bucket name AND confirms the project owns it.
// bucketRe forbids dots and slashes, so this is also the guard that stops a
// crafted bucket like "../.." from escaping storageRoot in filepath.Join.
func (a *app) bucketExists(slug, bucket string) bool {
	if !bucketRe.MatchString(bucket) {
		return false
	}
	var ok bool
	a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM storage_buckets WHERE slug=$1 AND bucket=$2)`, slug, bucket).Scan(&ok)
	return ok
}

// inlineSafeMime reports whether a stored object may be served inline. HTML/SVG/
// XML and anything script-like execute on the project's own origin (which has no
// CSP), so they must be forced to download instead of render.
func inlineSafeMime(m string) bool {
	m = strings.ToLower(strings.TrimSpace(m))
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	switch {
	case strings.HasPrefix(m, "image/") && m != "image/svg+xml":
		return true
	case strings.HasPrefix(m, "video/"), strings.HasPrefix(m, "audio/"):
		return true
	case m == "application/pdf", m == "text/plain", m == "application/json":
		return true
	}
	return false
}

func (a *app) storagePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	type bucket struct {
		Name    string
		Public  bool
		Objects int
		Size    string
	}
	var buckets []bucket
	rows, _ := a.db.Query(`SELECT b.bucket, b.public,
		count(o.id), coalesce(pg_size_pretty(sum(o.size))::text,'0 B')
		FROM storage_buckets b LEFT JOIN storage_objects o ON o.slug=b.slug AND o.bucket=b.bucket
		WHERE b.slug=$1 GROUP BY b.bucket, b.public ORDER BY b.bucket`, slug)
	if rows != nil {
		for rows.Next() {
			var b bucket
			rows.Scan(&b.Name, &b.Public, &b.Objects, &b.Size)
			buckets = append(buckets, b)
		}
		rows.Close()
	}

	// usage summary across all buckets + quota
	var totObjects int
	var totSize string
	a.db.QueryRow(`SELECT count(*), coalesce(pg_size_pretty(sum(size))::text,'0 B')
		FROM storage_objects WHERE slug=$1`, slug).Scan(&totObjects, &totSize)
	usedB := a.storageUsageBytes(slug)
	quotaB := a.storageQuotaBytes(slug)
	quotaMB := int(quotaB >> 20)
	usedPct := 0
	if quotaB > 0 {
		usedPct = int(usedB * 100 / quotaB)
		if usedPct > 100 {
			usedPct = 100
		}
	}

	sel := r.URL.Query().Get("b")
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	pfx := safeRel(r.URL.Query().Get("pfx"))
	if pfx != "" {
		pfx += "/"
	}
	type object struct {
		Path, Rel, Size, Mime, Created, URL string
	}
	var objects []object
	var folders []string
	var selPublic bool
	var selMaxMB int
	var selMime string
	if sel != "" {
		a.db.QueryRow(`SELECT public, max_size_mb, allowed_mime FROM storage_buckets WHERE slug=$1 AND bucket=$2`, slug, sel).Scan(&selPublic, &selMaxMB, &selMime)
		// searching looks across the whole bucket; browsing stays inside the
		// current folder and lists subfolders separately
		q := `SELECT path, pg_size_pretty(size), mime, to_char(created_at,'Mon DD, HH24:MI')
			FROM storage_objects WHERE slug=$1 AND bucket=$2`
		args := []any{slug, sel}
		if query != "" {
			args = append(args, "%"+query+"%")
			q += ` AND path ILIKE $3`
		} else if pfx != "" {
			args = append(args, pfx+"%")
			q += ` AND path LIKE $3`
		}
		q += ` ORDER BY path LIMIT 2000`
		orows, _ := a.db.Query(q, args...)
		if orows != nil {
			seenDir := map[string]bool{}
			for orows.Next() {
				var o object
				orows.Scan(&o.Path, &o.Size, &o.Mime, &o.Created)
				o.Rel = strings.TrimPrefix(o.Path, pfx)
				if query == "" {
					if i := strings.IndexByte(o.Rel, '/'); i >= 0 {
						d := o.Rel[:i]
						if !seenDir[d] {
							seenDir[d] = true
							folders = append(folders, d)
						}
						continue // shown via its folder, not as a file here
					}
				}
				if selPublic {
					o.URL = fmt.Sprintf("https://%s.%s/storage/v1/object/public/%s/%s", slug, a.cfg.domain, sel, escapePath(o.Path))
				} else {
					o.URL = a.signedURL(slug, sel, o.Path, 24*time.Hour)
				}
				objects = append(objects, o)
			}
			orows.Close()
		}
	}
	crumbs := []string{}
	if pfx != "" {
		crumbs = strings.Split(strings.TrimSuffix(pfx, "/"), "/")
	}
	parent := ""
	if len(crumbs) > 1 {
		parent = strings.Join(crumbs[:len(crumbs)-1], "/")
	}
	content := renderContent(storageBody, map[string]any{
		"Slug": slug, "Buckets": buckets, "Sel": sel, "SelPublic": selPublic, "Objects": objects,
		"SelMaxMB": selMaxMB, "SelMime": selMime, "Folders": folders,
		"Pfx": strings.TrimSuffix(pfx, "/"), "Parent": parent, "InFolder": pfx != "", "Query": query,
		"NBuckets": len(buckets), "TotObjects": totObjects, "TotSize": totSize,
		"QuotaMB": quotaMB, "UsedPct": usedPct, "HasQuota": quotaB > 0,
		"Rules": a.listStorageRules(slug), "Domain": a.cfg.domain,
		"S3Keys": func() []struct{ Access, Created string } {
			var out []struct{ Access, Created string }
			rows, err := a.db.Query(`SELECT access_key, to_char(created_at,'Mon DD, YYYY')
				FROM s3_keys WHERE slug=$1 ORDER BY created_at DESC`, slug)
			if err != nil {
				return out
			}
			defer rows.Close()
			for rows.Next() {
				var k struct{ Access, Created string }
				rows.Scan(&k.Access, &k.Created)
				out = append(out, k)
			}
			return out
		}(),
	})
	a.renderShell(w, r, shellData{Title: slug + " · Storage", Nav: "storage", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug}, {Label: "Storage"}}}, content)
}

// storageQuotaBytes returns the project's storage quota (0 = unlimited).
func (a *app) storageQuotaBytes(slug string) int64 {
	var mb int64
	a.db.QueryRow(`SELECT coalesce(storage_quota_mb,1024) FROM projects WHERE slug=$1`, slug).Scan(&mb)
	if mb <= 0 {
		return 0
	}
	return mb << 20
}

// storageUsageBytes sums the recorded object sizes for a project.
func (a *app) storageUsageBytes(slug string) int64 {
	var n int64
	a.db.QueryRow(`SELECT coalesce(sum(size),0) FROM storage_objects WHERE slug=$1`, slug).Scan(&n)
	return n
}

// quotaExceeded reports whether a project is at/over its storage quota.
func (a *app) quotaExceeded(slug string) bool {
	q := a.storageQuotaBytes(slug)
	return q > 0 && a.storageUsageBytes(slug) >= q
}

// setStorageQuota stores a per-project quota (panel, admin).
func (a *app) setStorageQuota(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/storage"
	mb, err := strconv.Atoi(r.FormValue("quota_mb"))
	if err != nil || mb < 0 || mb > 1024*1024 {
		redirectErr(w, r, back, "Quota 0 (unlimited) to 1048576 MB.")
		return
	}
	a.db.Exec(`UPDATE projects SET storage_quota_mb=$2 WHERE slug=$1`, slug, mb)
	a.audit(r, "storage-quota", fmt.Sprintf("%s=%dMB", slug, mb))
	redirectMsg(w, r, back, "Storage quota saved.")
}

// bucketLimits returns a bucket's max object size in bytes (0 = unlimited) and
// the list of allowed MIME prefixes (empty = any type).
func (a *app) bucketLimits(slug, bucket string) (maxBytes int64, allowed []string) {
	var mb int
	var mime string
	a.db.QueryRow(`SELECT max_size_mb, allowed_mime FROM storage_buckets WHERE slug=$1 AND bucket=$2`, slug, bucket).Scan(&mb, &mime)
	if mb > 0 {
		maxBytes = int64(mb) << 20
	}
	for _, m := range strings.Split(mime, ",") {
		if m = strings.TrimSpace(m); m != "" {
			allowed = append(allowed, m)
		}
	}
	return
}

// mimeAllowed reports whether a MIME type matches one of the allowed prefixes
// (so "image/" allows image/png). An empty allow-list permits anything.
func mimeAllowed(mime string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, p := range allowed {
		if strings.HasPrefix(mime, p) {
			return true
		}
	}
	return false
}

func (a *app) createBucket(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	public := r.FormValue("public") == "on"
	maxMB, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("max_size_mb")))
	if maxMB < 0 {
		maxMB = 0
	}
	allowedMime := strings.TrimSpace(r.FormValue("allowed_mime"))
	if !bucketRe.MatchString(name) {
		redirectErr(w, r, "/p/"+slug+"/storage", "Bucket name: 2-41 chars, a-z 0-9 _ -.")
		return
	}
	if _, err := a.db.Exec(`INSERT INTO storage_buckets(slug,bucket,public,max_size_mb,allowed_mime) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT DO NOTHING`, slug, name, public, maxMB, allowedMime); err != nil {
		redirectErr(w, r, "/p/"+slug+"/storage", err.Error())
		return
	}
	os.MkdirAll(filepath.Join(storageRoot, slug, name), 0o755)
	a.audit(r, "bucket-create", slug+"/"+name)
	redirectMsg(w, r, "/p/"+slug+"/storage?b="+name, "Bucket "+name+" created.")
}

func (a *app) uploadObject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		redirectErr(w, r, "/p/"+slug+"/storage", "Upload too large or malformed.")
		return
	}
	bucket := r.FormValue("bucket")
	if !a.bucketExists(slug, bucket) {
		redirectErr(w, r, "/p/"+slug+"/storage", "Unknown bucket.")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/storage?b="+bucket, "No file provided.")
		return
	}
	defer file.Close()
	rel := safeRel(hdr.Filename)
	if rel == "" {
		redirectErr(w, r, "/p/"+slug+"/storage?b="+bucket, "Bad filename.")
		return
	}
	mime := hdr.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	maxBytes, allowed := a.bucketLimits(slug, bucket)
	if !mimeAllowed(mime, allowed) {
		redirectErr(w, r, "/p/"+slug+"/storage?b="+bucket, "File type "+mime+" is not allowed in this bucket.")
		return
	}
	if a.quotaExceeded(slug) {
		redirectErr(w, r, "/p/"+slug+"/storage?b="+bucket, "Storage quota reached - delete objects or raise the quota below.")
		return
	}
	dst := filepath.Join(storageRoot, slug, bucket, rel)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.Create(dst)
	if err != nil {
		redirectErr(w, r, "/p/"+slug+"/storage?b="+bucket, "Could not write file.")
		return
	}
	var src io.Reader = file
	if maxBytes > 0 {
		src = io.LimitReader(file, maxBytes+1) // read one extra byte to detect overflow
	}
	n, cErr := io.Copy(out, src)
	closeErr := out.Close()
	if cErr != nil || closeErr != nil {
		os.Remove(dst) // don't record a truncated file as a complete object
		redirectErr(w, r, "/p/"+slug+"/storage?b="+bucket, "Write failed (disk full?); nothing saved.")
		return
	}
	if maxBytes > 0 && n > maxBytes {
		os.Remove(dst)
		redirectErr(w, r, "/p/"+slug+"/storage?b="+bucket, fmt.Sprintf("File exceeds this bucket's %d MB limit.", maxBytes>>20))
		return
	}
	if err := a.s3Push(dst, slug, bucket, rel); err != nil {
		os.Remove(dst)
		redirectErr(w, r, "/p/"+slug+"/storage?b="+bucket, "Object storage unreachable - nothing saved. "+err.Error())
		return
	}
	a.db.Exec(`INSERT INTO storage_objects(slug,bucket,path,size,mime) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (slug,bucket,path) DO UPDATE SET size=$4, mime=$5, created_at=now()`,
		slug, bucket, rel, n, mime)
	a.audit(r, "upload", slug+"/"+bucket+"/"+rel)
	redirectMsg(w, r, "/p/"+slug+"/storage?b="+bucket, "Uploaded "+rel+".")
}

func (a *app) deleteObject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	bucket := r.FormValue("bucket")
	if !a.bucketExists(slug, bucket) {
		redirectErr(w, r, "/p/"+slug+"/storage", "Unknown bucket.")
		return
	}
	path := r.FormValue("path")
	rel := safeRel(path)
	if rel != "" {
		os.Remove(filepath.Join(storageRoot, slug, bucket, rel))
		a.s3Delete(slug, bucket, rel)
	}
	a.db.Exec(`DELETE FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3`, slug, bucket, rel)
	a.audit(r, "object-delete", slug+"/"+bucket+"/"+rel)
	redirectMsg(w, r, "/p/"+slug+"/storage?b="+bucket, "Deleted "+rel+".")
}

// moveObject renames/moves an object within its bucket (disk + S3 + metadata).
func (a *app) moveObject(w http.ResponseWriter, r *http.Request) {
	a.transferObject(w, r, true)
}

// copyObject duplicates an object under a new path.
func (a *app) copyObject(w http.ResponseWriter, r *http.Request) {
	a.transferObject(w, r, false)
}

func (a *app) transferObject(w http.ResponseWriter, r *http.Request, move bool) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	bucket := r.FormValue("bucket")
	verb := "Copied"
	if move {
		verb = "Moved"
	}
	back := "/p/" + slug + "/storage?b=" + bucket
	if !a.bucketExists(slug, bucket) {
		redirectErr(w, r, "/p/"+slug+"/storage", "Unknown bucket.")
		return
	}
	from := safeRel(r.FormValue("from"))
	to := safeRel(r.FormValue("to"))
	if from == "" || to == "" || from == to {
		redirectErr(w, r, back, "Give both a source and a distinct destination path.")
		return
	}
	var size int64
	var mime string
	if err := a.db.QueryRow(`SELECT size, mime FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3`,
		slug, bucket, from).Scan(&size, &mime); err != nil {
		redirectErr(w, r, back, "Unknown object.")
		return
	}
	var exists bool
	a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3)`,
		slug, bucket, to).Scan(&exists)
	if exists {
		redirectErr(w, r, back, "Destination already exists.")
		return
	}
	src := filepath.Join(storageRoot, slug, bucket, from)
	dst := filepath.Join(storageRoot, slug, bucket, to)
	// the local file may be evicted from cache; restore it from S3 first
	if !a.ensureLocal(src, slug, bucket, from) {
		redirectErr(w, r, back, "Object data unavailable (offline object storage?).")
		return
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := copyFile(src, dst); err != nil {
		redirectErr(w, r, back, "Copy failed: "+err.Error())
		return
	}
	if err := a.s3Push(dst, slug, bucket, to); err != nil {
		os.Remove(dst)
		redirectErr(w, r, back, "Object storage unreachable - nothing changed. "+err.Error())
		return
	}
	a.db.Exec(`INSERT INTO storage_objects(slug,bucket,path,size,mime) VALUES ($1,$2,$3,$4,$5)`,
		slug, bucket, to, size, mime)
	if move {
		os.Remove(src)
		a.s3Delete(slug, bucket, from)
		a.db.Exec(`DELETE FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3`, slug, bucket, from)
	}
	a.audit(r, "object-"+strings.ToLower(verb), slug+"/"+bucket+": "+from+" -> "+to)
	redirectMsg(w, r, back, verb+" to "+to+".")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// bulkDeleteObjects removes many objects in one request (paths as JSON array).
func (a *app) bulkDeleteObjects(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	bucket := r.FormValue("bucket")
	back := "/p/" + slug + "/storage?b=" + bucket
	if !a.bucketExists(slug, bucket) {
		redirectErr(w, r, "/p/"+slug+"/storage", "Unknown bucket.")
		return
	}
	var paths []string
	if json.Unmarshal([]byte(r.FormValue("paths")), &paths) != nil || len(paths) == 0 || len(paths) > 500 {
		redirectErr(w, r, back, "Nothing selected.")
		return
	}
	n := 0
	for _, p := range paths {
		rel := safeRel(p)
		if rel == "" {
			continue
		}
		os.Remove(filepath.Join(storageRoot, slug, bucket, rel))
		a.s3Delete(slug, bucket, rel)
		res, _ := a.db.Exec(`DELETE FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3`, slug, bucket, rel)
		if res != nil {
			if aff, _ := res.RowsAffected(); aff > 0 {
				n++
			}
		}
	}
	a.audit(r, "objects-bulk-delete", fmt.Sprintf("%s/%s x%d", slug, bucket, n))
	redirectMsg(w, r, back, fmt.Sprintf("Deleted %d object(s).", n))
}

func (a *app) deleteBucket(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	bucket := r.FormValue("bucket")
	// bucketRe forbids dots/slashes, so this is what stops a "../.." bucket from
	// making os.RemoveAll escape storageRoot and wipe the host.
	if !bucketRe.MatchString(bucket) {
		redirectErr(w, r, "/p/"+slug+"/storage", "Invalid bucket name.")
		return
	}
	os.RemoveAll(filepath.Join(storageRoot, slug, bucket))
	a.s3Purge(slug + "/" + bucket)
	a.db.Exec(`DELETE FROM storage_objects WHERE slug=$1 AND bucket=$2`, slug, bucket)
	a.db.Exec(`DELETE FROM storage_buckets WHERE slug=$1 AND bucket=$2`, slug, bucket)
	a.audit(r, "bucket-delete", slug+"/"+bucket)
	redirectMsg(w, r, "/p/"+slug+"/storage", "Bucket "+bucket+" deleted.")
}

// signedURL builds a time-limited URL for a private object.
func (a *app) signedURL(slug, bucket, path string, ttl time.Duration) string {
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	mac := hmac.New(sha256.New, a.cfg.secret)
	mac.Write([]byte(slug + "/" + bucket + "/" + path + "|" + exp))
	sig := hex.EncodeToString(mac.Sum(nil))[:32]
	return fmt.Sprintf("https://%s.%s/storage/v1/object/sign/%s/%s?token=%s.%s",
		slug, a.cfg.domain, bucket, escapePath(path), exp, sig)
}

// serveStorage is the object API on <slug>.<domain>/storage/v1/object/... It
// dispatches:
//
//	GET  public/<bucket>/<path>   -> unauthenticated read of a public bucket
//	GET  sign/<bucket>/<path>?token=... -> signed read
//	POST sign/<bucket>/<path>     -> mint a signed URL (JWT)
//	POST/PUT <bucket>/<path>      -> upload (JWT: authenticated/service_role)
//	GET  <bucket>/<path>          -> authenticated read (JWT)
//	DELETE <bucket>/<path>        -> delete (JWT: authenticated/service_role)
func (a *app) serveStorage(w http.ResponseWriter, r *http.Request, slug string) {
	p := strings.TrimPrefix(r.URL.Path, "/storage/v1/object/")
	if strings.HasPrefix(p, "public/") || (strings.HasPrefix(p, "sign/") && r.Method == http.MethodGet) {
		a.serveStorageRead(w, r, slug, p)
		return
	}
	if strings.HasPrefix(p, "sign/") && r.Method == http.MethodPost {
		a.storageCreateSignedURL(w, r, slug, strings.TrimPrefix(p, "sign/"))
		return
	}
	if strings.HasPrefix(p, "list/") && r.Method == http.MethodPost {
		a.storageListAPI(w, r, slug, strings.TrimPrefix(p, "list/"))
		return
	}
	if strings.HasPrefix(p, "upload/sign/") {
		rest := strings.TrimPrefix(p, "upload/sign/")
		switch r.Method {
		case http.MethodPost:
			a.storageSignUpload(w, r, slug, rest)
		case http.MethodPut:
			a.storageSignedUploadPut(w, r, slug, rest)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		}
		return
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut:
		a.storageUploadAPI(w, r, slug, p)
	case http.MethodGet:
		a.storageDownloadAPI(w, r, slug, p)
	case http.MethodDelete:
		a.storageDeleteAPI(w, r, slug, p)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
	}
}

// storageListAPI implements the JS client's storage.from(bucket).list(): POST
// /storage/v1/object/list/<bucket> with {prefix, limit, offset, search}.
// One level per call - subpaths appear as folder entries with null id and
// metadata, exactly as typed clients expect.
func (a *app) storageListAPI(w http.ResponseWriter, r *http.Request, slug, bucket string) {
	role, ok := a.storageAuth(r, slug)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
		return
	}
	if !a.bucketExists(slug, bucket) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "bucket not found"})
		return
	}
	var public bool
	a.db.QueryRow(`SELECT public FROM storage_buckets WHERE slug=$1 AND bucket=$2`, slug, bucket).Scan(&public)
	if !public && role != "authenticated" && role != "service_role" {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "private bucket - authenticated key required"})
		return
	}
	var body struct {
		Prefix string `json:"prefix"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
		Search string `json:"search"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	if body.Limit <= 0 || body.Limit > 1000 {
		body.Limit = 100
	}
	if body.Offset < 0 {
		body.Offset = 0
	}
	pfx := safeRel(body.Prefix)
	if pfx != "" {
		pfx += "/"
	}
	rows, err := a.db.Query(`SELECT path, size, mime,
			to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path LIKE $3
		ORDER BY path LIMIT 5000`, slug, bucket, pfx+"%")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "listing failed"})
		return
	}
	defer rows.Close()
	type entry struct {
		Name      string         `json:"name"`
		ID        *string        `json:"id"`
		UpdatedAt *string        `json:"updated_at"`
		CreatedAt *string        `json:"created_at"`
		Metadata  map[string]any `json:"metadata"`
	}
	seenDir := map[string]bool{}
	var all []entry
	for rows.Next() {
		var path, mime, created string
		var size int64
		rows.Scan(&path, &size, &mime, &created)
		rel := strings.TrimPrefix(path, pfx)
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			d := rel[:i]
			if !seenDir[d] {
				seenDir[d] = true
				all = append(all, entry{Name: d})
			}
			continue
		}
		if body.Search != "" && !strings.Contains(strings.ToLower(rel), strings.ToLower(body.Search)) {
			continue
		}
		id := bucket + "/" + path
		c := created
		all = append(all, entry{Name: rel, ID: &id, UpdatedAt: &c, CreatedAt: &c,
			Metadata: map[string]any{"size": size, "mimetype": mime}})
	}
	if body.Offset >= len(all) {
		all = nil
	} else {
		all = all[body.Offset:]
	}
	if len(all) > body.Limit {
		all = all[:body.Limit]
	}
	if all == nil {
		all = []entry{}
	}
	writeJSON(w, http.StatusOK, all)
}

// storageSignUpload mints a time-limited token that authorizes ONE upload to a
// fixed bucket/path without any JWT (the JS client's createSignedUploadUrl).
func (a *app) storageSignUpload(w http.ResponseWriter, r *http.Request, slug, p string) {
	role, ok := a.storageAuth(r, slug)
	if !ok || (role != "authenticated" && role != "service_role") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authenticated key required"})
		return
	}
	bucket, rel, okp := splitObjectPath(p)
	if !okp || !a.bucketExists(slug, bucket) {
		writeJSON(w, 404, map[string]string{"message": "unknown bucket or path"})
		return
	}
	exp := strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, a.cfg.secret)
	mac.Write([]byte("upload|" + slug + "/" + bucket + "/" + rel + "|" + exp))
	tok := exp + "." + hex.EncodeToString(mac.Sum(nil))[:32]
	writeJSON(w, 200, map[string]string{
		"token": tok,
		"url": fmt.Sprintf("https://%s.%s/storage/v1/object/upload/sign/%s/%s?token=%s",
			slug, a.cfg.domain, bucket, escapePath(rel), tok),
		"path": rel,
	})
}

// storageSignedUploadPut accepts the body for a previously signed upload slot.
func (a *app) storageSignedUploadPut(w http.ResponseWriter, r *http.Request, slug, p string) {
	bucket, rel, okp := splitObjectPath(p)
	if !okp || !a.bucketExists(slug, bucket) {
		writeJSON(w, 404, map[string]string{"message": "unknown bucket or path"})
		return
	}
	tok := r.URL.Query().Get("token")
	parts := strings.SplitN(tok, ".", 2)
	valid := false
	if len(parts) == 2 {
		mac := hmac.New(sha256.New, a.cfg.secret)
		mac.Write([]byte("upload|" + slug + "/" + bucket + "/" + rel + "|" + parts[0]))
		want := hex.EncodeToString(mac.Sum(nil))[:32]
		if exp, err := strconv.ParseInt(parts[0], 10, 64); err == nil && time.Now().Unix() <= exp &&
			hmac.Equal([]byte(want), []byte(parts[1])) {
			valid = true
		}
	}
	if !valid {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "invalid or expired upload token"})
		return
	}
	mime := r.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	maxBytes, allowed := a.bucketLimits(slug, bucket)
	if !mimeAllowed(mime, allowed) {
		writeJSON(w, 415, map[string]string{"message": "file type not allowed in this bucket"})
		return
	}
	if a.quotaExceeded(slug) {
		writeJSON(w, http.StatusInsufficientStorage, map[string]string{"message": "storage quota reached"})
		return
	}
	dst := filepath.Join(storageRoot, slug, bucket, rel)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.Create(dst)
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": "could not write file"})
		return
	}
	var src io.Reader = r.Body
	if maxBytes > 0 {
		src = io.LimitReader(r.Body, maxBytes+1)
	}
	n, cErr := io.Copy(out, src)
	closeErr := out.Close()
	if cErr != nil || closeErr != nil {
		os.Remove(dst)
		writeJSON(w, 500, map[string]string{"message": "write failed; nothing saved"})
		return
	}
	if maxBytes > 0 && n > maxBytes {
		os.Remove(dst)
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": "file exceeds the bucket size limit"})
		return
	}
	if err := a.s3Push(dst, slug, bucket, rel); err != nil {
		os.Remove(dst)
		writeJSON(w, 502, map[string]string{"message": "object storage unreachable - nothing saved"})
		return
	}
	a.db.Exec(`INSERT INTO storage_objects(slug,bucket,path,size,mime) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (slug,bucket,path) DO UPDATE SET size=$4, mime=$5, created_at=now()`,
		slug, bucket, rel, n, mime)
	writeJSON(w, 200, map[string]string{"Key": bucket + "/" + rel, "path": rel})
}

// storageAuth verifies the caller's project JWT (apikey / Bearer) and returns
// its role. Used to gate the client-facing object API.
func (a *app) storageAuth(r *http.Request, slug string) (string, bool) {
	secret, _ := a.apiConfig(slug)
	if secret == "" {
		return "", false
	}
	key := r.Header.Get("apikey")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	claims, ok := verifyUserJWT([]byte(secret), key)
	if !ok {
		return "", false
	}
	role, _ := claims["role"].(string)
	return role, true
}

// splitObjectPath splits "<bucket>/<path>" and sanitizes the path.
func splitObjectPath(p string) (bucket, rel string, ok bool) {
	i := strings.IndexByte(p, '/')
	if i < 0 {
		return "", "", false
	}
	rel = safeRel(p[i+1:])
	return p[:i], rel, rel != ""
}

// serveStorageFile writes the object to the response with anti-XSS headers.
func (a *app) serveStorageFile(w http.ResponseWriter, r *http.Request, slug, bucket, path string) {
	full := filepath.Join(storageRoot, slug, bucket, path)
	// cache miss -> pull from object storage (no-op when S3 is off)
	a.ensureLocal(full, slug, bucket, path)
	var mime string
	a.db.QueryRow(`SELECT mime FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3`, slug, bucket, path).Scan(&mime)
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	// The project subdomain has no CSP and is same-site with the panel, so an
	// uploaded HTML/SVG served inline would be stored XSS. Force download for
	// anything not on the inline-safe allowlist, and never let the browser sniff.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !inlineSafeMime(mime) {
		w.Header().Set("Content-Disposition", "attachment")
	}
	// smart caching: a strong-enough ETag from size+mtime lets browsers and
	// CDNs revalidate with 304s instead of refetching bodies
	if st, err := os.Stat(full); err == nil {
		etag := fmt.Sprintf(`"%x-%x"`, st.Size(), st.ModTime().UnixNano())
		w.Header().Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if a.serveTransformed(w, r, slug, bucket, path, full, mime) {
		return
	}
	http.ServeFile(w, r, full)
}

// serveStorageRead handles the public + signed (token) GET paths.
func (a *app) serveStorageRead(w http.ResponseWriter, r *http.Request, slug, p string) {
	mode := "public"
	if strings.HasPrefix(p, "public/") {
		p = strings.TrimPrefix(p, "public/")
	} else {
		mode = "sign"
		p = strings.TrimPrefix(p, "sign/")
	}
	bucket, path, ok := splitObjectPath(p)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if mode == "public" {
		// path rules first (longest prefix wins); fall back to the bucket flag
		if allowed, hadRule := a.storageRuleAllows(r, slug, bucket, path); hadRule {
			if !allowed {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}
		} else {
			var public bool
			a.db.QueryRow(`SELECT public FROM storage_buckets WHERE slug=$1 AND bucket=$2`, slug, bucket).Scan(&public)
			if !public {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}
		}
	} else {
		tok := r.URL.Query().Get("token")
		parts := strings.SplitN(tok, ".", 2)
		if len(parts) != 2 {
			http.Error(w, `{"message":"invalid token"}`, http.StatusForbidden)
			return
		}
		exp, _ := strconv.ParseInt(parts[0], 10, 64)
		mac := hmac.New(sha256.New, a.cfg.secret)
		mac.Write([]byte(slug + "/" + bucket + "/" + path + "|" + parts[0]))
		want := hex.EncodeToString(mac.Sum(nil))[:32]
		if time.Now().Unix() > exp || want != parts[1] {
			http.Error(w, `{"message":"expired or invalid token"}`, http.StatusForbidden)
			return
		}
	}
	a.serveStorageFile(w, r, slug, bucket, path)
}

// storageUploadAPI: authenticated client upload (POST/PUT <bucket>/<path>).
func (a *app) storageUploadAPI(w http.ResponseWriter, r *http.Request, slug, p string) {
	role, ok := a.storageAuth(r, slug)
	if !ok || (role != "authenticated" && role != "service_role") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
		return
	}
	bucket, rel, ok := splitObjectPath(p)
	if !ok || !a.bucketExists(slug, bucket) {
		writeJSON(w, 400, map[string]string{"message": "unknown bucket or path"})
		return
	}
	// owner/private path rules gate writes too (service_role passes)
	if access, prefix := a.storageRuleFor(slug, bucket, rel); role != "service_role" {
		if access == "private" {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "this path is service-role only"})
			return
		}
		if access == "owner" {
			claims, _ := a.storageClaims(r, slug)
			sub, _ := claims["sub"].(string)
			if sub == "" || ownerSegment(rel, prefix) != sub {
				writeJSON(w, http.StatusForbidden, map[string]string{"message": "you can only write under your own folder here"})
				return
			}
		}
	}
	mime := r.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	maxBytes, allowed := a.bucketLimits(slug, bucket)
	if !mimeAllowed(mime, allowed) {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"message": "file type not allowed in this bucket"})
		return
	}
	if a.quotaExceeded(slug) {
		writeJSON(w, http.StatusInsufficientStorage, map[string]string{"message": "storage quota reached"})
		return
	}
	dst := filepath.Join(storageRoot, slug, bucket, rel)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.Create(dst)
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": "could not write file"})
		return
	}
	var src io.Reader = r.Body
	if maxBytes > 0 {
		src = io.LimitReader(r.Body, maxBytes+1)
	}
	n, cErr := io.Copy(out, src)
	ce := out.Close()
	if cErr != nil || ce != nil {
		os.Remove(dst)
		writeJSON(w, 500, map[string]string{"message": "write failed (disk full?)"})
		return
	}
	if maxBytes > 0 && n > maxBytes {
		os.Remove(dst)
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": fmt.Sprintf("file exceeds the bucket's %d MB limit", maxBytes>>20)})
		return
	}
	if err := a.s3Push(dst, slug, bucket, rel); err != nil {
		os.Remove(dst)
		writeJSON(w, 502, map[string]string{"message": "object storage unreachable - nothing saved"})
		return
	}
	a.db.Exec(`INSERT INTO storage_objects(slug,bucket,path,size,mime) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (slug,bucket,path) DO UPDATE SET size=$4, mime=$5, created_at=now()`,
		slug, bucket, rel, n, mime)
	writeJSON(w, 200, map[string]any{"Key": bucket + "/" + rel, "size": n})
}

// storageDownloadAPI: authenticated read (any valid project JWT) - covers
// private buckets without needing a pre-signed URL.
func (a *app) storageDownloadAPI(w http.ResponseWriter, r *http.Request, slug, p string) {
	if _, ok := a.storageAuth(r, slug); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
		return
	}
	bucket, rel, ok := splitObjectPath(p)
	if !ok || !a.bucketExists(slug, bucket) {
		http.NotFound(w, r)
		return
	}
	a.serveStorageFile(w, r, slug, bucket, rel)
}

// storageDeleteAPI: authenticated delete.
func (a *app) storageDeleteAPI(w http.ResponseWriter, r *http.Request, slug, p string) {
	role, ok := a.storageAuth(r, slug)
	if !ok || (role != "authenticated" && role != "service_role") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
		return
	}
	bucket, rel, ok := splitObjectPath(p)
	if !ok || !a.bucketExists(slug, bucket) {
		writeJSON(w, 400, map[string]string{"message": "unknown bucket or path"})
		return
	}
	os.Remove(filepath.Join(storageRoot, slug, bucket, rel))
	a.s3Delete(slug, bucket, rel)
	a.db.Exec(`DELETE FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3`, slug, bucket, rel)
	writeJSON(w, 200, map[string]string{"message": "deleted"})
}

// storageCreateSignedURL: authenticated client mints a time-limited URL.
func (a *app) storageCreateSignedURL(w http.ResponseWriter, r *http.Request, slug, p string) {
	if _, ok := a.storageAuth(r, slug); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
		return
	}
	bucket, rel, ok := splitObjectPath(p)
	if !ok || !a.bucketExists(slug, bucket) {
		writeJSON(w, 404, map[string]string{"message": "unknown bucket or path"})
		return
	}
	var body struct {
		ExpiresIn int `json:"expiresIn"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	ttl := 3600
	if body.ExpiresIn > 0 {
		ttl = body.ExpiresIn
	}
	writeJSON(w, 200, map[string]string{"signedURL": a.signedURL(slug, bucket, rel, time.Duration(ttl)*time.Second)})
}
