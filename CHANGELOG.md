# Changelog

All notable changes to Taper are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Project plan and milestones (`docs/plan.md`).
- Milestone 1, the foundation:
  - One self-contained program (`taper`) with an SQLite database and numbered migrations.
  - `install.sh` for Debian 12/13: service account, systemd service, plain HTTP, automatic
    HTTPS with Let's Encrypt (`--domain`), or behind a reverse proxy (`--behind-proxy`,
    `--proxy-ip`). Re-running upgrades in place and keeps data and settings. `uninstall.sh`.
  - First-time setup in the browser with a one-time setup code: school name and first admin.
  - Sign-in with "keep me signed in", sign-out, sign-in throttling, CSRF and cross-origin
    protection, and a strict Content Security Policy.
  - Admin, mentor and scholar roles. Admins add people (with a temporary password they
    change on first sign-in), edit them, reset passwords and deactivate accounts.
  - My account: name, email, password, and signing out other devices.
  - Recovery commands: `taper passwd`, `taper setup-code`, `taper backup`.
  - Installable web app (PWA) with the Taper candle icon, an offline page, and a
    layout for phones, tablets and computers in light and dark themes.
  - The user guide, built into the app and on GitHub.
  - The tagline "Light your taper at mine".
  - GitHub Actions: lint, tests, release archives, and a real install, upgrade and
    uninstall test; tagged releases are published automatically.
