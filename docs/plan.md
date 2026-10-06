# Taper: plan and milestones

*Light your taper at mine.*

Taper is a self-hosted learning platform for a Thomas Jefferson Education (TJEd)
commonwealth school. One school runs one copy on its own Debian server.

## Who uses it

| Role | Who | Can do |
|------|-----|--------|
| **Admin** | School leadership, the person who installs it | Everything a mentor can, plus: manage people and classes, the school calendar, settings, updates and bug-report setup |
| **Mentor** | Teachers | Run their classes: class calendar, assignments, feedback, class chat moderation |
| **Scholar** | Students | See their classes and calendars, work on and turn in assignments, take part in chat |

There are no letter grades. TJEd mentors give written feedback, and an
assignment's status says where it stands (for example *Turned in*, *Needs another
look*, *Complete*).

## Technology

**Go + SQLite, one self-contained program.**

- **Go** (the language of the last project) builds one static binary with the web
  pages, styles, scripts, icons and this guide built in. Nothing else needs
  installing on the server: no PHP, Node, Python or web server.
- **SQLite** (through `modernc.org/sqlite`, pure Go, `CGO_ENABLED=0`) instead
  of MySQL. A school's data is small, and SQLite means no database server to
  install, secure, tune or upgrade. The whole database is one file, so a backup
  is one file copy. The program backs it up on its own before every update.
  MySQL would only be worth it for many servers sharing one database, which a
  school doesn't need.
- **Pages are built on the server** (Go `html/template`) with a little plain
  JavaScript for the parts that update live, like chat. There's no JavaScript
  framework or build step, so there's less to break and less to keep up to date.
- **Live updates** (chat, notifications) use Server-Sent Events, which work through
  proxies and on every current browser.
- **Installable app (PWA)**: a web app manifest, icons and a service worker
  let Android and iOS users add Taper to their home screen. iOS and Android need
  HTTPS for that, so the installer can get a free Let's Encrypt certificate on its
  own (or sit behind an existing reverse proxy).

Dependencies are kept few on purpose: `modernc.org/sqlite`, `golang.org/x/crypto`
(Let's Encrypt), and `github.com/yuin/goldmark` (Markdown, for the guide and for
assignment text). Anything new needs a reason in its pull request.

## How updates work

1. A version is released by tagging it on GitHub. GitHub Actions builds the
   program for x86-64 and ARM64, **signs** the files, and publishes a release.
2. In Taper, **Admin → Updates** shows when a newer release is out, with its
   notes. Clicking **Install** downloads it, checks the signature, backs up the
   database, swaps the program and restarts.
3. If the new version doesn't start, it's put back on its own. An admin can
   also roll back from the same page.

## Bug reports

Anyone signed in can use **Report a problem**. Taper adds the version, page and
browser, and files it as a GitHub issue in this repository. The repository is
public, so the form says not to include names or private details. The admin adds a
GitHub token that's only allowed to create issues; without one, the form opens a
pre-filled GitHub issue for someone with a GitHub account to send.

## Milestones

Each milestone is one or more pull requests. Every pull request has tests and
updated documentation, and passes `make check`, before it's opened for review.

### M0: project setup (done in the first commit)
- Public GitHub repository, plan (this file), README, license, changelog, working rules for contributors.

### M1: foundation
- One Go program: `taper serve`, plus a few commands for setup and recovering a locked-out admin account.
- SQLite database with numbered migrations.
- `install.sh` for Debian 12/13: service account, folders, systemd service, HTTP,
  automatic HTTPS (Let's Encrypt) or reverse-proxy mode. Re-running it upgrades and
  keeps data. `uninstall.sh`.
- First-run setup in the browser with a one-time setup code: school name and first admin.
- Sign in and out, sessions, sign-in throttling, CSRF protection, strict security headers.
- Roles: admin, mentor, scholar. Admins add, edit, deactivate and reset passwords for people.
  New accounts get a temporary password that must be changed when they first sign in.
- My account: name, email, password.
- Responsive layout for phones, tablets and computers, in light and dark themes.
- PWA: manifest, icons, service worker, offline page.
- The in-app **Guide** shows the same Markdown files that are in `docs/guide/` on GitHub.
- GitHub Actions CI: formatting, vet, staticcheck, shellcheck, tests.

### M2: updates and bug reports
- Signed release builds from GitHub Actions on version tags.
- Admin → Updates: check, see notes, install, automatic rollback if the new version fails, manual rollback.
- Database backup before every update, plus a download-a-backup button.
- Report a problem → GitHub issue (token stored encrypted), with a fallback link.

### M3: classes
- Admins create classes (name, description, term, color) and archive old ones.
- Assign one or more mentors; enroll scholars (by hand, or a CSV list of scholars).
- Class page with members and, later, its calendar, assignments and chat.
- A home page for each role showing their classes.

### M4: calendars and iCal
- School calendar (admins add events): holidays, gatherings, terms.
- Class calendars (mentors add events). Repeating events (weekly and so on).
- My calendar: the school calendar plus my classes, in month, week and list views.
- iCal subscriptions with a private link for each person (their whole calendar,
  or one class), for Google Calendar, Apple Calendar and Outlook. Links can be reset.

### M5: assignments
- Mentors create assignments with formatted text and attached files, an optional
  due date (which shows on the class calendar), and publish now or later.
- Scholars download the attachments, and either write their work online (saved
  as they go) or upload files, then turn it in.
- Mentors see who has turned in work, give written feedback and set a status
  (*Needs another look*, *Complete*). Scholars can turn in again after feedback.
- No letter grades.

### M6: chat
- A community channel for the whole school and a channel for each class.
- Live updates, unread counts, and notifications in the app.
- Mentors moderate their class channels and admins moderate everything (delete messages, mute).
- Admins can turn channels off.

### M7: polish and 1.0
- Notifications (in-app first; web push where the phone supports it).
- Accessibility check, performance, a full guide with screenshots.
- Tested upgrade path from every pre-release, then **1.0.0**.

Later ideas, not planned yet: parent accounts, portfolios, direct messages,
attendance, a school library or reading lists.
