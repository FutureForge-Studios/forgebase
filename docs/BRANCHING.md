# Copy-on-write branching (design + proven mechanism)

The audit found that today's branching does a full `CREATE DATABASE ... TEMPLATE`
copy that briefly takes the parent offline. This document records the real
copy-on-write design that replaces it, the proof that the mechanism works, and
the honest path to shipping it in the product.

## The mechanism (proven)

Put the Postgres data directory on a copy-on-write filesystem (btrfs or ZFS). A
branch is then a filesystem snapshot of the running cluster plus a second Postgres
process started on that snapshot. Because the snapshot is atomic and O(1):

- it is **instant** (constant time, independent of database size),
- it is **copy-on-write** on disk (the branch shares the parent's blocks; only
  divergent writes consume new space),
- the **parent is never stopped or locked** (a snapshot does not require
  disconnecting anyone),
- the branch recovers crash-consistently on start and then **diverges
  independently** from the parent.

### Proof (measured on a throwaway VM, btrfs)

```
snapshot took         0.018 s      instant, not proportional to database size
btrfs disk delta      812 KB       branching a ~46 MB cluster = copy-on-write
parent postmaster     pid unchanged  never restarted, never locked
parent kept writing   before + after the snapshot, no downtime
branch captured       exactly the parent's state at snapshot time
divergence            parent and branch each took independent writes
```

The proof script is `scripts/cow-branch-demo.sh`. Contrast with `CREATE DATABASE
... TEMPLATE`, which is O(database size), doubles disk, and requires zero
connections on the source (so it force-disconnects the parent for the copy).

## Productization plan

Shipping this in the product is a real architectural step, not a wording change,
because a branch becomes its own Postgres instance rather than another database in
the shared cluster:

1. **Storage.** `install.sh` places `/opt/pgforge/data` on a btrfs subvolume on
   new installs. Existing installs keep ext4 and the `TEMPLATE` path (with the
   honest "briefly locks the parent" note) until an operator migrates.
2. **Branch create.** If the data dir is a CoW subvolume: `btrfs subvolume
   snapshot` the cluster, start a branch postmaster on the snapshot on an
   allocated port, register the branch and its port. Else: fall back to
   `TEMPLATE`.
3. **Routing.** Send `<branch>.<domain>` database and API traffic to the branch
   instance's port; the per-project PostgREST/Deno sidecars run against it.
4. **Lifecycle.** Delete = stop the branch postmaster + `btrfs subvolume delete`.
   Idle branches can be stopped (see scale-to-zero) since each is its own process.
5. **Backups/monitoring.** Extend the existing per-project coverage to branch
   instances.

## Relationship to scale-to-zero

The same per-instance foundation enables true scale-to-zero of compute: because a
branch (or project) is its own Postgres process, it can be **stopped** when idle
(freeing real CPU/RAM, not just blocking logins) and **cold-started** on the next
connection by a small routing proxy. On the current shared cluster the engine
serves every tenant and cannot be zeroed for one; per-instance changes that.

## Built and proven end-to-end

The per-instance subsystem is implemented and proven on a throwaway VM:

- `scripts/pg-instance.sh` - instance lifecycle: create / branch (btrfs snapshot)
  / stop / start / delete / reap, one Postgres container per project on a btrfs
  store.
- `tools/pgproxy` - a Postgres-protocol cold-start proxy: reads the startup
  packet, wakes the target instance if stopped, then splices the connection.
- `systemd/pgforge-pgproxy.service` + `systemd/pgforge-reaper.{service,timer}` -
  the proxy and the idle reaper.
- Enable at install with `INSTANCES=1 bash install.sh` (runs
  `scripts/setup-instances.sh`). Off by default; the classic shared cluster is
  used otherwise.

Measured results:

```
branch create           ~1 s   (18 ms btrfs snapshot + Postgres boot), parent never restarted
two 39 MB instances     46 MB total on disk   (copy-on-write block sharing, not 78 MB)
scale-to-zero           idle instance stopped by the reaper -> 0 CPU/RAM
cold-start on connect    ~0.7 s   client wakes a stopped instance transparently, data intact
```

## What remains: panel integration

The engine, proxy, and reaper are done and installable. The remaining work is
wiring them into the panel so it is a one-click feature rather than opt-in
infrastructure: the Branches page creates CoW instance branches when the mode is
on, project connection strings point at the proxy, and a project can be slept and
woken from the UI. Until that lands, the panel's built-in branching still uses the
`TEMPLATE` copy (described honestly as a full copy that briefly locks the source),
and per-instance mode is driven by `pg-instance.sh` / the proxy directly.
