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

## Versioning and releases

ForgeBase follows [Semantic Versioning](https://semver.org). Every shipped change
raises the version, and the version lives in one place: `appVersion` in
`pgforge/changelog.go`.

- Patch (`1.0.0` to `1.0.1`): fixes and small improvements.
- Minor (`1.0.x` to `1.1.0`): new, backwards-compatible features.
- Major (`1.x` to `2.0.0`): breaking changes.

When cutting a release: bump `appVersion`, add the release to both `releases` in
`pgforge/changelog.go` and `CHANGELOG.md`, then tag the commit `vX.Y.Z`. The
in-app self-update compares the running build against the latest commit on
`main`, so users see the new version as soon as it is pushed.

## Licensing of contributions

ForgeBase is AGPL-3.0. By submitting a pull request you agree that your
contribution is licensed under the same terms, and you grant FutureForge
Studios Private Limited the right to also distribute your contribution under
other licensing terms, including commercial licenses.

## Reporting security issues

Please do not open a public issue for security problems. See
[SECURITY.md](SECURITY.md).
