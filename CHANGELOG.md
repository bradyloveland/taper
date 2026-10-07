# Changelog

All notable changes to Taper are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Milestone 5, calendars:
  - **Calendar**: month, week and list views of the school calendar and your classes'
    calendars together, or one at a time. Phones get dots on the month and a list below.
  - **Events**: admins add to the school calendar (including "no school" days), mentors to
    their classes'. All-day, timed and multi-day events, with a place and formatted details.
  - **Repeating events**: every day, every week on chosen days, every month or every year,
    with an optional end date; remove a single date, or change every date.
  - **Coming up** on the home page (two weeks), and a **Coming up** card at the top of each
    class's page (that class's next month, Add event, Subscribe to this class).
  - **Subscribe**: private iCal links for Google Calendar, Apple Calendar and Outlook (my
    whole calendar, the school calendar, and each class). Each link has its own address
    and can be reset on its own, or all at once.
  - The event form moves the end with the start (keeping the length) and won't save an end
    before the start; the problem field is highlighted.
  - **Settings → School → Time zone**, so calendars use the school's local time across
    daylight saving changes. The time zone database is built in.

### Changed
- The installer says "Changed from version…" instead of "Upgraded" when installing an older
  or development version.

## [0.4.0] - 2026-10-07

### Added
- Milestone 4, classes:
  - **Classes**: name, term, meeting time, color and a description (with simple
    formatting). Admins create, archive, bring back and delete them; a class's mentors can
    edit it and its scholars. Scholars see only their own classes.
  - **Members**: add mentors (admins and mentors) and scholars from a list you can search,
    and remove them.
  - **Import people from a spreadsheet** (CSV or pasted cells): check the list first, with
    problem rows shown and skipped, usernames made from names, and optional class
    enrollment. The temporary passwords are shown once, to print or download.
  - Home pages show each person's classes; admins get a classes count and an updated
    getting-started list. People pages list each person's classes.

## [0.3.0] - 2026-10-06

The first release of Taper: milestones 1 to 3 of the [plan](https://github.com/bradyloveland/taper/blob/main/docs/plan.md).
Install it with `curl -fsSL https://raw.githubusercontent.com/bradyloveland/taper/main/install.sh | sudo bash`.

### Added
- Milestone 3, security and server settings:
  - **Two-step sign-in**: codes from an authenticator app, set up with a QR code under
    My account, with 10 single-use recovery codes. Codes can't be reused, and wrong codes
    are limited. Admins can require it for admins, mentors and/or scholars, and turn it
    off for someone who lost their phone. `taper mfa-reset` on the server as a last resort.
  - **Settings → Network & HTTPS**: plain HTTP, Let's Encrypt or behind a reverse proxy,
    port, listen address and trusted proxies, from the web interface. A change runs
    alongside the old settings until it's confirmed from the new address, and is undone
    by itself if it isn't. `taper network` shows the settings in use; `taper network
    --reset` goes back to the installer's.
  - **Public address** setting, used in links Taper sends.
  - **Settings → Email**: SMTP server settings (password stored encrypted) with a test
    button. "Forgot your password?" sends a single-use reset link (valid for an hour) to
    people with an email address. Admins can get email about new versions, undone updates
    and problem reports.
  - CI test of changing network settings from the web interface on a real systemd machine.

- Milestone 2, updates and problem reports:
  - **Updates** page for admins: check GitHub for new versions (daily, or on demand), read
    the release notes, download and install. Releases must be signed by the Taper project.
    Release files can also be uploaded, for servers without internet access.
  - Before each update the database is backed up (the last five are kept). If the new
    version doesn't start, the previous one is put back on its own. Admins can also go
    back by hand, from the Updates page or with `taper rollback`.
  - **Download a backup** of the database from Settings.
  - **Report a problem**, at the bottom of every page: reports are saved for admins and,
    with a GitHub token (stored encrypted), filed as GitHub issues. Reporters' names are
    never included; `@mentions` are defused. Without a token there's a pre-filled
    GitHub link instead.
  - Signed release builds in GitHub Actions, and a CI test of updating, going back and
    recovering from an update that doesn't start, on a real systemd machine.

### Fixed
- The installer printed no setup code when root's `PATH` didn't include `/usr/local/bin`.

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
