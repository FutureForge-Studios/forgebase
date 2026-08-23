package main

// Read replica: a hot standby of the whole shared cluster in its own
// container, streaming through a replication slot and serving READ-ONLY
// connections on port 5434 - same databases, same credentials (roles
// replicate), same TLS. Heavy reporting queries and read-mostly services
// move off the primary with one connection-string change. The engine is
// scripts/setup-replica.sh; this file drives it and shows its state.

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const replicaScript = "/opt/pgforge/bin/setup-replica.sh"
const replicaPort = 5434

var (
	replicaMu   sync.Mutex
	replicaBusy bool // enable runs minutes (basebackup); one at a time
)

// replicaState runs the status subcommand: "off", "streaming lag=0.3s", ...
func (a *app) replicaState() string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", replicaScript, "status").Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}

func replicaOn(state string) bool { return strings.HasPrefix(state, "streaming") }

// replicaToggle enables or disables the read replica (owner action).
// Enable runs in the background - the basebackup copies the whole cluster.
func (a *app) replicaToggle(w http.ResponseWriter, r *http.Request) {
	op := r.FormValue("op")
	replicaMu.Lock()
	if replicaBusy {
		replicaMu.Unlock()
		redirectErr(w, r, "/system", "A replica operation is already running - refresh in a minute.")
		return
	}
	replicaBusy = true
	replicaMu.Unlock()
	done := func() {
		replicaMu.Lock()
		replicaBusy = false
		replicaMu.Unlock()
	}
	switch op {
	case "enable":
		a.audit(r, "replica-enable", "started")
		go func() {
			defer done()
			defer func() { recover() }()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, "bash", replicaScript, "enable").CombinedOutput()
			if err != nil {
				a.auditRaw("system", "-", "replica-enable-failed", tail(string(out), 300))
				a.notifyDiscord("Read replica enable FAILED: " + tail(string(out), 200))
				return
			}
			a.auditRaw("system", "-", "replica-enabled", tail(string(out), 100))
			a.notifyDiscord("Read replica is up: read-only connections on port 5434, same credentials.")
		}()
		redirectMsg(w, r, "/system", "Read replica setup started - it copies the whole cluster, so give it a few minutes. Discord pings when it is streaming.")
	case "disable":
		defer done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		exec.CommandContext(ctx, "bash", replicaScript, "disable").Run()
		a.audit(r, "replica-disable", "done")
		redirectMsg(w, r, "/system", "Read replica removed; its replication slot and data are gone.")
	default:
		done()
		redirectErr(w, r, "/system", "Unknown operation.")
	}
}
