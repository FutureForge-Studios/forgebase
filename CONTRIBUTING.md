# Contributing to ForgeBase

Thanks for your interest in ForgeBase. This guide covers how to get a development
copy running and how to propose changes.

## Project layout

- `pgforge/` - the Go control plane (single binary, `pgforged`). Server-rendered
  HTML, no frontend build step.
- `server/` - the Postgres 17 + PgBouncer + Caddy stack (Docker Compose) and the
  Deno edge-function runner.
- `scripts/` - backups, restore verification, firewall, and hardening.
- `systemd/` - unit and timer files for the control plane and backups.
- `install.sh` - the one-command installer (also the upgrade path; idempotent).

## Building

You need Go 1.22 or newer.

```sh
cd pgforge
go build ./...
go vet ./...
```

The full platform (Postgres, pooler, TLS, PostgREST, Deno) is meant to run on a
Linux server. The quickest way to try a complete environment is a throwaway VM
and `sudo bash install.sh`. See [docs/SELF-HOSTING.md](docs/SELF-HOSTING.md).

## Making a change

1. Open an issue first for anything non-trivial so we can agree on the approach.
2. Keep each pull request focused on one change.
3. Run `gofmt`, `go vet`, and `go build ./...` before pushing. CI runs all three.
4. Update [CHANGELOG.md](CHANGELOG.md) under the `Unreleased` section, and when a
   change is user-visible, add it to the in-app What's New data in
   `pgforge/changelog.go` so the panel and the changelog file stay in step.
5. Write commit messages in the conventional style: `feat:`, `fix:`, `docs:`,
   `chore:`, `refactor:`, `perf:`, `test:`.

## Reporting security issues

Please do not open a public issue for security problems. See
[SECURITY.md](SECURITY.md).
