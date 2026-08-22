package main

// Warm process pool for edge functions. The first invocation of a function
// starts a persistent Deno server (edge-server.ts) and later requests are
// proxied to it - no per-request process boot. Because the path is a real
// HTTP proxy, streaming responses, WebSocket upgrades and post-response
// background work come along for free. A function's process is replaced
// when its file or environment changes, evicted LRU beyond the pool cap,
// reaped after five idle minutes, and any warm-path failure falls back to
// the old one-per-request runner so functions can never break.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const edgeServerRunner = "/opt/pgforge/edge-server.ts"

type warmProc struct {
	cmd     *exec.Cmd
	port    int
	proxy   *httputil.ReverseProxy
	mtime   time.Time
	envSig  string
	lastUse time.Time
	dead    atomic.Bool
}

var (
	warmMu     sync.Mutex
	warmProcs  = map[string]*warmProc{}
	warmNext   = 21500
	warmOnce   sync.Once
	warmMaxPer = func() int {
		if v := os.Getenv("EDGE_WARM_MAX"); v != "" {
			var n int
			fmt.Sscanf(v, "%d", &n)
			if n > 0 {
				return n
			}
		}
		return 8
	}()
)

func (p *warmProc) kill() {
	p.dead.Store(true)
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}

// warmReaper kills processes nobody has called for five minutes.
func warmReaper() {
	for range time.Tick(time.Minute) {
		warmMu.Lock()
		for key, p := range warmProcs {
			if p.dead.Load() || time.Since(p.lastUse) > 5*time.Minute {
				p.kill()
				delete(warmProcs, key)
			}
		}
		warmMu.Unlock()
	}
}

// warmProxy returns a live proxy for this function, starting or replacing
// the backing process as needed. nil = use the one-shot fallback.
func (a *app) warmProxy(slug, name, file string, env []string, allow string, memMB int) *httputil.ReverseProxy {
	if _, err := os.Stat(edgeServerRunner); err != nil {
		return nil // box not provisioned yet (pre-reconciler): fallback
	}
	st, err := os.Stat(file)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256([]byte(strings.Join(env, "\x00") + "|" + allow + "|" + fmt.Sprint(memMB)))
	sig := hex.EncodeToString(sum[:8])
	key := slug + "/" + name

	warmOnce.Do(func() { go warmReaper() })
	warmMu.Lock()
	defer warmMu.Unlock()
	if p, ok := warmProcs[key]; ok {
		if !p.dead.Load() && p.mtime.Equal(st.ModTime()) && p.envSig == sig {
			p.lastUse = time.Now()
			return p.proxy
		}
		p.kill()
		delete(warmProcs, key)
	}
	// LRU-evict beyond the cap so warm memory stays bounded
	for len(warmProcs) >= warmMaxPer {
		oldestKey, oldest := "", time.Now()
		for k, p := range warmProcs {
			if p.lastUse.Before(oldest) {
				oldest, oldestKey = p.lastUse, k
			}
		}
		if oldestKey == "" {
			break
		}
		warmProcs[oldestKey].kill()
		delete(warmProcs, oldestKey)
	}
	// pick a free localhost port from the warm range
	port := 0
	for i := 0; i < 500; i++ {
		cand := warmNext
		warmNext++
		if warmNext > 21999 {
			warmNext = 21500
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cand))
		if err == nil {
			l.Close()
			port = cand
			break
		}
	}
	if port == 0 {
		return nil
	}
	cmd := exec.Command("/usr/local/bin/deno", "run", "--quiet",
		fmt.Sprintf("--v8-flags=--max-old-space-size=%d", memMB),
		"--allow-net", "--allow-env="+allow, "--allow-read="+funcRoot,
		edgeServerRunner, file, fmt.Sprint(port))
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return nil
	}
	p := &warmProc{cmd: cmd, port: port, mtime: st.ModTime(), envSig: sig, lastUse: time.Now()}
	go func() {
		cmd.Wait()
		p.dead.Store(true)
	}()
	// readiness: the server accepts connections once the module is loaded
	ready := false
	for i := 0; i < 60; i++ {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			c.Close()
			ready = true
			break
		}
		if p.dead.Load() {
			return nil // module failed to load: one-shot path reports the error
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		p.kill()
		return nil
	}
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		p.dead.Store(true) // next request respawns
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"message":"function process restarting, retry shortly"}`)
	}
	proxy.FlushInterval = 100 * time.Millisecond // pass streamed chunks through promptly
	p.proxy = proxy
	warmProcs[key] = p
	return proxy
}

// edgeStatusWriter records the status for invocation logs while passing
// Hijack through so WebSocket upgrades keep working under the proxy.
type edgeStatusWriter struct {
	http.ResponseWriter
	status int
}

func (s *edgeStatusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *edgeStatusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijacking not supported")
}

func (s *edgeStatusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// serveWarm proxies one request to the function's warm process.
func (a *app) serveWarm(w http.ResponseWriter, r *http.Request, proxy *httputil.ReverseProxy,
	slug, name, subpath string, timeoutS int) {
	ctx := r.Context()
	isUpgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if !isUpgrade {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
		defer cancel()
	}
	r2 := r.Clone(ctx)
	r2.URL.Path = subpath
	sw := &edgeStatusWriter{ResponseWriter: w, status: 200}
	started := time.Now()
	proxy.ServeHTTP(sw, r2)
	tookMs := int(time.Since(started).Milliseconds())
	a.db.Exec(`INSERT INTO edge_logs(slug, name, error, status, ms, ok) VALUES ($1,$2,'',$3,$4,$5)`,
		slug, name, sw.status, tookMs, sw.status < 500)
}
