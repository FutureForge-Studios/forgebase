# Disaster recovery

What survives if the server is destroyed, and how to rebuild on a fresh box.

## What is off-box, automatically

The nightly backup (21:30 UTC) syncs `/opt/pgforge-backups` to the rclone
remote in `/opt/pgforge/backup_remote`. After the sync, the remote holds:

- `dumps/` - logical dumps of EVERY database: all projects, the `pgforge`
  meta database (projects, users, settings, webhooks, keys), and
  `globals-*.sql` (roles WITH their password hashes). Tiered retention:
  7 daily + 4 weekly per database.
- `physical/` - the newest cluster basebackups.
- `wal/` - the continuous WAL archive (point-in-time recovery).
- `files/` - snapshots of storage uploads and edge function sources.
- `disaster-kit.tar.gz.enc` - the config kit: stack `.env` (SESSION_SECRET),
  TLS certs, Caddy/PgBouncer config, the rclone config. AES-256 encrypted.

## How you know the backups are actually running

A backup set that silently stopped growing is worse than no backup set, because
it still looks like protection. Two independent checks guard against that, and
they are deliberately not the same check:

- **The run reports itself.** Any non-success exit from the nightly unit writes
  `/opt/pgforge/alerts/backup_failed` and sends the Discord alert, carrying the
  last 20 lines of `/var/log/pgforge-backup.log`. It is wired through
  `ExecStopPost`, so it fires even when the script dies before it could log
  anything of its own. A successful run clears it.
- **The outcome is checked separately.** An hourly watchdog asks only "has any
  `.dump` landed in the last 48 hours?" and alerts if not. It does not care why
  dumping stopped, which is exactly why it catches the failures the backup
  script is too broken to report.

Both render as a red banner on the System page for as long as the condition
lasts. If you want to confirm by hand at any time:

```sh
ls -lt /opt/pgforge-backups/dumps/*.dump | head
systemctl start pgforge-backup.service && tail -30 /var/log/pgforge-backup.log
```

## Where the data actually lives

On a box with a separate block volume, the data directory and the backup tree
are bind mounts, not symlinks (rclone skips symlinks, and Docker bind mounts use
`rprivate` propagation, so a mount created after a container starts is invisible
to it). The mounts are recorded in `/etc/fstab` with `nofail`:

```
/mnt/<volume>/pgforge/data              /opt/pgforge/data                 none bind,nofail 0 0
/mnt/<volume>/pgforge-backups/wal       /opt/pgforge-backups/wal          none bind,nofail 0 0
/mnt/<volume>/pgforge-backups/physical  /opt/pgforge-backups/physical     none bind,nofail 0 0
```

Nothing in the stack knows about the volume: every path above is the one the
containers were always given. To move onto a volume, rsync live, stop the
database, rsync the delta, move the originals aside, bind-mount, start. Measured
downtime on the reference box was 35 seconds. `nofail` matters - without it a
detached volume turns a reboot into an emergency-mode console.

## The one thing YOU must keep somewhere else

The kit passphrase, generated once at `/opt/pgforge/recovery.pass`.
Copy it into your password manager NOW. Without it the kit cannot be
opened, and without the kit you lose SESSION_SECRET - the panel would
need new credentials everywhere (data still restores fine; convenience
does not).

## Rebuild procedure (fresh Ubuntu box)

1. Install ForgeBase normally: clone the repo, run `install.sh`.
2. Fetch the kit from the remote and unpack it over the fresh install:

       rclone copy <remote>:<path>/disaster-kit.tar.gz.enc /tmp/
       openssl enc -d -aes-256-cbc -pbkdf2 -pass pass:<YOUR-PASSPHRASE> \
         -in /tmp/disaster-kit.tar.gz.enc | tar xz -C /
       docker compose -f /opt/pgforge/stack/docker-compose.yml up -d

3. Restore the databases (roles first, then every dump):

       rclone copy <remote>:<path>/dumps /opt/restore/
       docker exec -i pgforge-db psql -U postgres < /opt/restore/globals-<date>.sql
       for d in /opt/restore/*.dump; do
         db="$(basename "$d" | sed 's/-[0-9-]*\.dump//')"
         docker exec pgforge-db createdb -U postgres "$db" 2>/dev/null || true
         docker exec -i pgforge-db pg_restore -U postgres -d "$db" --no-owner < "$d"
       done

   Restore `pgforge-<date>.dump` first - it is the control plane's own
   database and brings back projects, users and settings.

4. Restore files: `rclone copy <remote>:<path>/files/daily-<newest> /opt/pgforge-storage/`
   (and the `functions` part to `/opt/pgforge-functions/`).
5. Point DNS at the new box; Caddy re-issues certificates on demand.
6. For recovery to an exact moment instead of last night: use `physical/` +
   `wal/` with `scripts/pitr-restore.sh` - see that script's header.

## What does NOT protect against box loss

The read replica runs on the SAME server - it protects against primary
process/database trouble and offloads reads, not against the machine
burning down. The off-box mirror above is the real insurance.
