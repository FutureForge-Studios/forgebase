package main

// S3-compatible protocol access to project storage, with scoped keys.
// Point rclone, the AWS CLI or any S3 SDK at the project domain
// (path-style) with a key pair minted on the Storage page and the core
// object operations work against the project's buckets:
//
//	ListBuckets, ListObjectsV2, HeadObject, GetObject, PutObject,
//	DeleteObject
//
// Requests are verified with real AWS Signature V4 (single-chunk signed
// or UNSIGNED-PAYLOAD; streaming-chunked and multipart uploads answer
// 501 so clients fall back or fail loudly, never silently corrupt).
// Routing is by signature: any request carrying an AWS4-HMAC-SHA256
// Authorization header on the project subdomain lands here, so no path
// prefix gets in the way of path-style clients.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// isS3Request detects SigV4-authenticated requests (header or presigned).
func isS3Request(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") ||
		r.URL.Query().Get("X-Amz-Signature") != ""
}

func s3Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// s3Secret looks up the secret for an access key id, scoped to this project.
func (a *app) s3Secret(slug, accessKey string) string {
	var secret string
	a.db.QueryRow(`SELECT secret FROM s3_keys WHERE slug=$1 AND access_key=$2`, slug, accessKey).Scan(&secret)
	return secret
}

// verifySigV4 checks the request signature and returns whether it is valid.
// Only header-based auth is supported (what rclone/CLIs/SDKs send).
func (a *app) verifySigV4(r *http.Request, slug string, payloadHash string) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return false
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			fields[kv[0]] = kv[1]
		}
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 || cred[3] != "s3" || cred[4] != "aws4_request" {
		return false
	}
	secret := a.s3Secret(slug, cred[0])
	if secret == "" {
		return false
	}
	amzDate := r.Header.Get("x-amz-date")
	if t, err := time.Parse("20060102T150405Z", amzDate); err != nil ||
		t.Before(time.Now().Add(-15*time.Minute)) || t.After(time.Now().Add(15*time.Minute)) {
		return false
	}
	return sigV4Matches(r, secret, payloadHash, fields)
}

// sigV4Matches recomputes the AWS Signature V4 for a request against a known
// secret - the pure math, verified against the official AWS test vector.
func sigV4Matches(r *http.Request, secret, payloadHash string, fields map[string]string) bool {
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 {
		return false
	}
	date, region := cred[1], cred[2]
	amzDate := r.Header.Get("x-amz-date")
	signedHeaders := strings.Split(fields["SignedHeaders"], ";")
	var canonHdr strings.Builder
	for _, h := range signedHeaders {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}
		canonHdr.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}

	// canonical query: every parameter URI-encoded and sorted
	var qparts []string
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			qparts = append(qparts, url.QueryEscape(k)+"="+strings.ReplaceAll(url.QueryEscape(v), "+", "%20"))
		}
	}
	sort.Strings(qparts)

	canonReq := strings.Join([]string{
		r.Method,
		r.URL.EscapedPath(),
		strings.Join(qparts, "&"),
		canonHdr.String(),
		fields["SignedHeaders"],
		payloadHash,
	}, "\n")
	crSum := sha256.Sum256([]byte(canonReq))
	scope := date + "/" + region + "/s3/aws4_request"
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(crSum[:])}, "\n")

	k := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte("s3"))
	k = hmacSHA256(k, []byte("aws4_request"))
	want := hex.EncodeToString(hmacSHA256(k, []byte(toSign)))
	return hmac.Equal([]byte(want), []byte(fields["Signature"]))
}

func (a *app) serveS3(w http.ResponseWriter, r *http.Request, slug string) {
	payloadHash := r.Header.Get("x-amz-content-sha256")
	if payloadHash == "" {
		payloadHash = "UNSIGNED-PAYLOAD"
	}
	if strings.HasPrefix(payloadHash, "STREAMING-") {
		s3Error(w, http.StatusNotImplemented, "NotImplemented",
			"streaming-chunked uploads are not supported; disable chunked transfer (rclone: this happens automatically on retry, aws cli: aws configure set default.s3.payload_signing_enabled true)")
		return
	}
	if r.URL.Query().Get("X-Amz-Signature") != "" {
		s3Error(w, http.StatusNotImplemented, "NotImplemented", "presigned URLs are not supported; use header-signed requests")
		return
	}
	// body is needed both for hash verification (signed payload) and PUT
	var body []byte
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 512<<20))
		if payloadHash != "UNSIGNED-PAYLOAD" {
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) != payloadHash {
				s3Error(w, http.StatusBadRequest, "XAmzContentSHA256Mismatch", "payload hash mismatch")
				return
			}
		}
	}
	if !a.verifySigV4(r, slug, payloadHash) {
		s3Error(w, http.StatusForbidden, "SignatureDoesNotMatch", "signature verification failed")
		return
	}
	if _, ok := r.URL.Query()["uploads"]; ok || r.URL.Query().Get("uploadId") != "" {
		s3Error(w, http.StatusNotImplemented, "NotImplemented", "multipart uploads are not supported yet; upload files in one request")
		return
	}

	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		a.s3ListBuckets(w, slug)
		return
	}
	bucket := path
	key := ""
	if i := strings.IndexByte(path, '/'); i >= 0 {
		bucket, key = path[:i], path[i+1:]
	}
	if !a.bucketExists(slug, bucket) {
		s3Error(w, 404, "NoSuchBucket", "no such bucket")
		return
	}
	if key == "" {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			a.s3ListObjects(w, r, slug, bucket)
			return
		}
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "bucket-level writes are managed in the panel")
		return
	}
	rel := safeRel(key)
	if rel == "" {
		s3Error(w, 400, "InvalidArgument", "bad key")
		return
	}

	switch r.Method {
	case http.MethodHead, http.MethodGet:
		full := filepath.Join(storageRoot, slug, bucket, rel)
		a.ensureLocal(full, slug, bucket, rel)
		st, err := os.Stat(full)
		if err != nil {
			s3Error(w, 404, "NoSuchKey", "no such key")
			return
		}
		var mime string
		a.db.QueryRow(`SELECT mime FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3`, slug, bucket, rel).Scan(&mime)
		if mime == "" {
			mime = "application/octet-stream"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		w.Header().Set("Last-Modified", st.ModTime().UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, st.Size(), st.ModTime().UnixNano()))
		if r.Method == http.MethodHead {
			w.WriteHeader(200)
			return
		}
		http.ServeFile(w, r, full)

	case http.MethodPut:
		if a.quotaExceeded(slug) {
			s3Error(w, http.StatusInsufficientStorage, "QuotaExceeded", "storage quota reached")
			return
		}
		maxBytes, allowed := a.bucketLimits(slug, bucket)
		if maxBytes > 0 && int64(len(body)) > maxBytes {
			s3Error(w, http.StatusRequestEntityTooLarge, "EntityTooLarge", "exceeds the bucket's size limit")
			return
		}
		mime := r.Header.Get("Content-Type")
		if mime == "" {
			mime = "application/octet-stream"
		}
		if !mimeAllowed(mime, allowed) {
			s3Error(w, http.StatusUnsupportedMediaType, "InvalidArgument", "file type not allowed in this bucket")
			return
		}
		dst := filepath.Join(storageRoot, slug, bucket, rel)
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			s3Error(w, 500, "InternalError", "could not write the object")
			return
		}
		if err := a.s3Push(dst, slug, bucket, rel); err != nil {
			os.Remove(dst)
			s3Error(w, 502, "InternalError", "object storage unreachable")
			return
		}
		a.db.Exec(`INSERT INTO storage_objects(slug,bucket,path,size,mime) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (slug,bucket,path) DO UPDATE SET size=$4, mime=$5, created_at=now()`,
			slug, bucket, rel, len(body), mime)
		st, _ := os.Stat(dst)
		w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, st.Size(), st.ModTime().UnixNano()))
		w.WriteHeader(200)

	case http.MethodDelete:
		os.Remove(filepath.Join(storageRoot, slug, bucket, rel))
		a.s3Delete(slug, bucket, rel)
		a.db.Exec(`DELETE FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path=$3`, slug, bucket, rel)
		w.WriteHeader(http.StatusNoContent)

	default:
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "unsupported method")
	}
}

func (a *app) s3ListBuckets(w http.ResponseWriter, slug string) {
	type bkt struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
	}
	var out struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Owner   struct {
			ID string `xml:"ID"`
		} `xml:"Owner"`
		Buckets struct {
			Bucket []bkt `xml:"Bucket"`
		} `xml:"Buckets"`
	}
	out.Owner.ID = slug
	rows, err := a.db.Query(`SELECT bucket, created_at FROM storage_buckets WHERE slug=$1 ORDER BY bucket`, slug)
	if err == nil {
		for rows.Next() {
			var b bkt
			var t time.Time
			rows.Scan(&b.Name, &t)
			b.CreationDate = t.UTC().Format(time.RFC3339)
			out.Buckets.Bucket = append(out.Buckets.Bucket, b)
		}
		rows.Close()
	}
	w.Header().Set("Content-Type", "application/xml")
	raw, _ := xml.Marshal(out)
	w.Write([]byte(xml.Header))
	w.Write(raw)
}

func (a *app) s3ListObjects(w http.ResponseWriter, r *http.Request, slug, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	maxKeys := 1000
	if v, err := strconv.Atoi(q.Get("max-keys")); err == nil && v > 0 && v <= 1000 {
		maxKeys = v
	}
	after := q.Get("continuation-token")
	if after == "" {
		after = q.Get("start-after")
	}
	type obj struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
		StorageClass string `xml:"StorageClass"`
	}
	var out struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		Name                  string   `xml:"Name"`
		Prefix                string   `xml:"Prefix"`
		KeyCount              int      `xml:"KeyCount"`
		MaxKeys               int      `xml:"MaxKeys"`
		IsTruncated           bool     `xml:"IsTruncated"`
		NextContinuationToken string   `xml:"NextContinuationToken,omitempty"`
		Contents              []obj    `xml:"Contents"`
	}
	out.Name, out.Prefix, out.MaxKeys = bucket, prefix, maxKeys
	rows, err := a.db.Query(`SELECT path, size, extract(epoch from created_at)
		FROM storage_objects WHERE slug=$1 AND bucket=$2 AND path LIKE $3 || '%' AND path > $4
		ORDER BY path LIMIT $5`, slug, bucket, prefix, after, maxKeys+1)
	if err == nil {
		for rows.Next() {
			var o obj
			var epoch float64
			rows.Scan(&o.Key, &o.Size, &epoch)
			o.LastModified = time.Unix(int64(epoch), 0).UTC().Format(time.RFC3339)
			o.ETag = fmt.Sprintf(`"%x"`, o.Size)
			o.StorageClass = "STANDARD"
			out.Contents = append(out.Contents, o)
		}
		rows.Close()
	}
	if len(out.Contents) > maxKeys {
		out.Contents = out.Contents[:maxKeys]
		out.IsTruncated = true
		out.NextContinuationToken = out.Contents[len(out.Contents)-1].Key
	}
	out.KeyCount = len(out.Contents)
	w.Header().Set("Content-Type", "application/xml")
	raw, _ := xml.Marshal(out)
	w.Write([]byte(xml.Header))
	w.Write(raw)
}

// ---- scoped key management (Storage page) ----

func (a *app) s3KeyCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/storage"
	access := "FB" + strings.ToUpper(randHex(9))
	secret := randHex(20)
	if _, err := a.db.Exec(`INSERT INTO s3_keys(slug, access_key, secret) VALUES ($1,$2,$3)`,
		slug, access, secret); err != nil {
		redirectErr(w, r, back, "Could not create the key: "+err.Error())
		return
	}
	a.audit(r, "s3-key-create", slug+"/"+access)
	redirectMsg(w, r, back, "S3 key created. Access key: "+access+"  Secret: "+secret+" - copy the secret NOW, it is shown once.")
}

func (a *app) s3KeyRevoke(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a.db.Exec(`DELETE FROM s3_keys WHERE slug=$1 AND access_key=$2`, slug, r.FormValue("access_key"))
	a.audit(r, "s3-key-revoke", slug+"/"+r.FormValue("access_key"))
	redirectMsg(w, r, "/p/"+slug+"/storage", "S3 key revoked.")
}
