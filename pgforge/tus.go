package main

// Resumable uploads: a minimal, correct tus 1.0.0 server (core + creation)
// at /storage/v1/tus/<bucket>. Works with tus-js-client, Uppy and every
// other standard tus client:
//
//	POST   /storage/v1/tus/<bucket>       create (Upload-Length + Upload-Metadata filename)
//	HEAD   /storage/v1/tus/<bucket>/<id>  current Upload-Offset
//	PATCH  /storage/v1/tus/<bucket>/<id>  append bytes at Upload-Offset
//	DELETE /storage/v1/tus/<bucket>/<id>  abandon
//
// Bytes accumulate in a .tus staging dir next to the bucket; when the last
// PATCH completes the object lands in the bucket exactly like a normal
// upload (S3 push, metadata row, quota). A .meta sidecar file keeps the
// upload resumable across daemon restarts.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const tusVersion = "1.0.0"

type tusMeta struct {
	Bucket, Path, Mime string
	Length             int64
}

func tusDir(slug string) string { return filepath.Join(storageRoot, slug, ".tus") }

func tusLoad(slug, id string) (tusMeta, bool) {
	var m tusMeta
	raw, err := os.ReadFile(filepath.Join(tusDir(slug), id+".meta"))
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return m, false
	}
	return m, true
}

// tusMetadata decodes the Upload-Metadata header (comma-separated
// "key base64value" pairs).
func tusMetadata(h string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(h, ",") {
		f := strings.Fields(strings.TrimSpace(pair))
		if len(f) == 0 {
			continue
		}
		v := ""
		if len(f) > 1 {
			if b, err := base64.StdEncoding.DecodeString(f[1]); err == nil {
				v = string(b)
			}
		}
		out[f[0]] = v
	}
	return out
}

func (a *app) serveTUS(w http.ResponseWriter, r *http.Request, slug string) {
	w.Header().Set("Tus-Resumable", tusVersion)
	w.Header().Set("Tus-Version", tusVersion)
	w.Header().Set("Tus-Extension", "creation,termination")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	role, ok := a.storageAuth(r, slug)
	if !ok || (role != "authenticated" && role != "service_role") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/storage/v1/tus/")
	parts := strings.SplitN(rest, "/", 2)
	bucket := parts[0]
	if !a.bucketExists(slug, bucket) {
		writeJSON(w, 404, map[string]string{"message": "unknown bucket"})
		return
	}

	// creation
	if len(parts) == 1 && r.Method == http.MethodPost {
		length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
		if err != nil || length <= 0 {
			writeJSON(w, 400, map[string]string{"message": "Upload-Length required"})
			return
		}
		meta := tusMetadata(r.Header.Get("Upload-Metadata"))
		rel := safeRel(meta["filename"])
		if rel == "" {
			rel = safeRel(meta["name"])
		}
		if rel == "" {
			writeJSON(w, 400, map[string]string{"message": "Upload-Metadata must include a filename"})
			return
		}
		if maxBytes, _ := a.bucketLimits(slug, bucket); maxBytes > 0 && length > maxBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": "file exceeds the bucket's size limit"})
			return
		}
		if a.quotaExceeded(slug) {
			writeJSON(w, http.StatusInsufficientStorage, map[string]string{"message": "storage quota reached"})
			return
		}
		// same path rules as plain uploads
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
		mime := meta["filetype"]
		if mime == "" {
			mime = "application/octet-stream"
		}
		if _, allowed := a.bucketLimits(slug, bucket); !mimeAllowed(mime, allowed) {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"message": "file type not allowed in this bucket"})
			return
		}
		id := randHex(16)
		os.MkdirAll(tusDir(slug), 0o755)
		if f, err := os.Create(filepath.Join(tusDir(slug), id)); err != nil {
			writeJSON(w, 500, map[string]string{"message": "could not stage upload"})
			return
		} else {
			f.Close()
		}
		raw, _ := json.Marshal(tusMeta{Bucket: bucket, Path: rel, Mime: mime, Length: length})
		os.WriteFile(filepath.Join(tusDir(slug), id+".meta"), raw, 0o644)
		w.Header().Set("Location", fmt.Sprintf("https://%s.%s/storage/v1/tus/%s/%s", slug, a.cfg.domain, bucket, id))
		w.WriteHeader(http.StatusCreated)
		return
	}

	if len(parts) != 2 || parts[1] == "" {
		writeJSON(w, 404, map[string]string{"message": "unknown upload"})
		return
	}
	id := parts[1]
	if strings.ContainsAny(id, "/\\.") {
		writeJSON(w, 404, map[string]string{"message": "unknown upload"})
		return
	}
	meta, found := tusLoad(slug, id)
	if !found || meta.Bucket != bucket {
		writeJSON(w, 404, map[string]string{"message": "unknown upload"})
		return
	}
	stage := filepath.Join(tusDir(slug), id)
	st, err := os.Stat(stage)
	if err != nil {
		writeJSON(w, 404, map[string]string{"message": "unknown upload"})
		return
	}

	switch r.Method {
	case http.MethodHead:
		w.Header().Set("Upload-Offset", strconv.FormatInt(st.Size(), 10))
		w.Header().Set("Upload-Length", strconv.FormatInt(meta.Length, 10))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

	case http.MethodPatch:
		if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"message": "Content-Type must be application/offset+octet-stream"})
			return
		}
		offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || offset != st.Size() {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f, err := os.OpenFile(stage, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			writeJSON(w, 500, map[string]string{"message": "could not open staged upload"})
			return
		}
		n, cErr := io.Copy(f, io.LimitReader(r.Body, meta.Length-offset))
		f.Close()
		if cErr != nil {
			// partial data is fine - that is the whole point of tus; the
			// client re-HEADs and resumes from the new offset
			w.Header().Set("Upload-Offset", strconv.FormatInt(offset+n, 10))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		newOff := offset + n
		w.Header().Set("Upload-Offset", strconv.FormatInt(newOff, 10))
		if newOff >= meta.Length {
			// complete: land it like a normal upload
			dst := filepath.Join(storageRoot, slug, meta.Bucket, meta.Path)
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := os.Rename(stage, dst); err != nil {
				writeJSON(w, 500, map[string]string{"message": "could not finalize upload"})
				return
			}
			os.Remove(stage + ".meta")
			if err := a.s3Push(dst, slug, meta.Bucket, meta.Path); err != nil {
				os.Remove(dst)
				writeJSON(w, 502, map[string]string{"message": "object storage unreachable - upload not saved"})
				return
			}
			a.db.Exec(`INSERT INTO storage_objects(slug,bucket,path,size,mime) VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (slug,bucket,path) DO UPDATE SET size=$4, mime=$5, created_at=now()`,
				slug, meta.Bucket, meta.Path, meta.Length, meta.Mime)
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		os.Remove(stage)
		os.Remove(stage + ".meta")
		w.WriteHeader(http.StatusNoContent)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
	}
}

// pruneStaleTUS drops staged uploads nobody has touched for a day - an
// abandoned resumable upload should not hold disk forever.
func pruneStaleTUS() {
	dirs, _ := filepath.Glob(filepath.Join(storageRoot, "*", ".tus"))
	for _, d := range dirs {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if time.Since(info.ModTime()) > 24*time.Hour {
				os.Remove(filepath.Join(d, e.Name()))
			}
		}
	}
}
