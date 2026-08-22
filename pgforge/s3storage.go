package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// S3-backed storage. When a storage remote is configured (settings key
// storage_remote, an rclone path like "hetzner:bucket/forgebase-storage"),
// object storage becomes the SOURCE OF TRUTH for every uploaded file and the
// local tree at storageRoot becomes a bounded read cache:
//
//	upload  -> written locally (fast), then pushed to the remote; the upload
//	           fails if the push fails, so the remote is never behind
//	read    -> served from the local cache; on a miss, pulled from the remote
//	delete  -> removed from both
//	prune   -> hourly, the cache is trimmed to ~1.5GB by recency; anything
//	           pruned is re-fetched on demand
//
// With no remote configured nothing changes: plain local storage, exactly as
// before. Transport is rclone (already installed for off-box backups), so the
// credentials live in the server's existing rclone config - the panel only
// stores the remote path.

const storageCacheCap = int64(1536) << 20 // ~1.5GB local cache when S3 is on

var storageRemoteCache atomic.Value // string; "" = local-only

func (a *app) storageRemote() string {
	if v := storageRemoteCache.Load(); v != nil {
		return v.(string)
	}
	var s string
	a.db.QueryRow(`SELECT value FROM settings WHERE key='storage_remote'`).Scan(&s)
	s = strings.TrimSpace(s)
	storageRemoteCache.Store(s)
	return s
}

func (a *app) s3Enabled() bool { return a.storageRemote() != "" }

// s3Key is the object key for a stored file: <slug>/<bucket>/<path>.
func s3Key(slug, bucket, rel string) string {
	return slug + "/" + bucket + "/" + rel
}

func (a *app) s3run(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "rclone", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rclone %s: %v: %s", args[0], err, tail(string(out), 200))
	}
	return nil
}

// s3Push uploads a local file to the remote. Called after a successful local
// write; the caller treats an error as a failed upload.
func (a *app) s3Push(local, slug, bucket, rel string) error {
	if !a.s3Enabled() {
		return nil
	}
	return a.s3run(10*time.Minute, "copyto", local, a.storageRemote()+"/"+s3Key(slug, bucket, rel))
}

// ensureLocal makes sure the file exists in the local cache, pulling it from
// the remote on a miss. Returns true when the file is present locally.
func (a *app) ensureLocal(full, slug, bucket, rel string) bool {
	if _, err := os.Stat(full); err == nil {
		os.Chtimes(full, time.Now(), time.Now()) // recency for the cache pruner
		return true
	}
	if !a.s3Enabled() {
		return false
	}
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := a.s3run(10*time.Minute, "copyto", a.storageRemote()+"/"+s3Key(slug, bucket, rel), full); err != nil {
		return false
	}
	return true
}

func (a *app) s3Delete(slug, bucket, rel string) {
	if a.s3Enabled() {
		a.s3run(time.Minute, "deletefile", a.storageRemote()+"/"+s3Key(slug, bucket, rel))
	}
}

// s3Purge removes a whole prefix (bucket delete, project delete).
func (a *app) s3Purge(prefix string) {
	if a.s3Enabled() {
		a.s3run(10*time.Minute, "purge", a.storageRemote()+"/"+prefix)
	}
}

// pruneStorageCache trims the local tree to the cache cap by recency. Only
// runs when S3 holds the source of truth - without a remote, local files are
// the only copy and must never be pruned.
func (a *app) pruneStorageCache() {
	if !a.s3Enabled() {
		return
	}
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var files []entry
	var total int64
	filepath.Walk(storageRoot, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			files = append(files, entry{p, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	if total <= storageCacheCap {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= storageCacheCap {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// migrateStorageToS3 seeds the remote with every existing local file, once.
// Local files stay in place - they simply become the (pre-warmed) cache.
func (a *app) migrateStorageToS3() {
	if !a.s3Enabled() || a.settingOn("storage_migrated") {
		return
	}
	go func() {
		defer func() { recover() }()
		if err := a.s3run(6*time.Hour, "copy", storageRoot, a.storageRemote()); err != nil {
			a.auditRaw("system", "-", "storage-migrate-failed", err.Error())
			a.notifyDiscord("WARNING ForgeBase: storage migration to object storage failed - uploads still work, retry from the System page. " + err.Error())
			return
		}
		a.db.Exec(`INSERT INTO settings(key,value) VALUES ('storage_migrated','1')
			ON CONFLICT (key) DO UPDATE SET value='1'`)
		a.auditRaw("system", "-", "storage-migrated", a.storageRemote())
		a.notifyDiscord("ForgeBase: existing storage files are now safely in object storage (" + a.storageRemote() + "). Local disk acts as a fast cache.")
	}()
}

// setStorageRemote saves (or clears) the storage remote after verifying it is
// reachable, then kicks off the one-time migration of existing files.
func (a *app) setStorageRemote(w http.ResponseWriter, r *http.Request) {
	remote := strings.TrimSpace(r.FormValue("remote"))
	if remote != "" {
		if strings.ContainsAny(remote, " \t\n'\"") || !strings.Contains(remote, ":") {
			redirectErr(w, r, "/system", "That does not look like an rclone remote path (expected remote:bucket/prefix).")
			return
		}
		if err := a.s3run(20*time.Second, "mkdir", remote); err != nil {
			redirectErr(w, r, "/system", "Cannot reach that remote: "+err.Error())
			return
		}
	}
	a.db.Exec(`INSERT INTO settings(key,value) VALUES ('storage_remote',$1)
		ON CONFLICT (key) DO UPDATE SET value=$1`, remote)
	storageRemoteCache.Store(remote)
	if remote == "" {
		a.db.Exec(`UPDATE settings SET value='0' WHERE key='storage_migrated'`)
		a.audit(r, "storage-remote", "cleared")
		redirectMsg(w, r, "/system", "Object storage disabled - files stay on local disk only.")
		return
	}
	a.audit(r, "storage-remote", remote)
	a.migrateStorageToS3()
	redirectMsg(w, r, "/system", "Object storage enabled ("+remote+"). Existing files are being copied over in the background; local disk now acts as a fast cache.")
}
