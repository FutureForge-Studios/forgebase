# Security Policy

ForgeBase manages databases and their credentials, so we take security reports
seriously and appreciate responsible disclosure.

## Reporting a vulnerability

Please do not open a public GitHub issue for security problems.

Instead, use one of these private channels:

- GitHub's private vulnerability reporting: the **Security** tab of this
  repository, then **Report a vulnerability**.
- Email: security@ffstudios.io

Include enough detail to reproduce the issue (affected version or commit,
steps, and impact). We will acknowledge receipt, keep you updated on progress,
and credit you in the release notes once a fix ships, unless you prefer to
remain anonymous.

## Supported versions

ForgeBase is pre-1.x moving to 1.0. Security fixes land on the latest release.
Because the installer is idempotent and doubles as the upgrade path, the
recommended remediation is always to update to the newest version.

## Hardening built in

For context on the platform's default protections (TLS-only external database
access, SSH key authentication, fail2ban, automatic security updates, scoped
edge-function environments, and HMAC-signed webhooks), see
[docs/SELF-HOSTING.md](docs/SELF-HOSTING.md).
