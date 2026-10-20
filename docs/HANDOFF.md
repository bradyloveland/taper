# Handoff

*Light your taper at mine.*

A snapshot of where Taper stands, for whoever picks it up next. Last updated
2026-10-08. This file isn't part of the in-app guide. The plan is
[plan.md](plan.md), the code tour is [development.md](development.md), the rules
for working on the code are in [`CLAUDE.md`](../CLAUDE.md), and the user guide
is in [guide/](guide/).

## What Taper is

A self-hosted learning platform for a Thomas Jefferson Education commonwealth
school (built first for **Kindred**). One school runs one copy on its own Debian
server. Roles: **admin**, **board**, **mentor** (teacher), **scholar**
(student). There are no letter grades: work is turned in, then marked
*Complete* or *Needs another look*, with written feedback.

## What's built

Milestones 1–7 of 8 are done. Releases v0.3.0, v0.4.0 and v0.5.0 are tagged.
PR #11 set the version to 0.6.0 and is merged, but `v0.6.0` isn't tagged yet
(see [Next steps](#next-steps)).

| Milestone | What it added |
|-----------|---------------|
| **M1 Foundation** | One Go program with SQLite. Installer for Debian. First-time setup in the browser with a one-time setup code. Sign-in, sessions (remember-me 30 days, otherwise 12 hours), people management, temporary passwords, spreadsheet import. PWA (home-screen app). |
| **M2 Updates and bug reports** | Settings → Updates checks GitHub, downloads signed releases, installs, and rolls back by itself if the new version fails. A database backup is taken before each update. "Report a problem" files GitHub issues through a token stored encrypted. |
| **M3 Security and server settings** | Two-step sign-in (TOTP, recovery codes; can be required per role). Network & HTTPS settings in the web interface: plain HTTP, Let's Encrypt, or behind a reverse proxy. Changes are tried first and undone unless confirmed from the new address. Public URL. SMTP email for password resets. |
| **M4 Classes** | Classes with terms, colors, descriptions, mentors and scholars. Archive, then delete. |
| **M5 Calendars** | School and class calendars, repeating events, school time zone. Month, week and list views. Private iCal subscription links per calendar, each resettable on its own. |
| **M6 Assignments** | Formatted instructions and attachments, due dates (on the calendars and feeds), publish now, later or as a draft. Scholar work saves itself as they type; files; turn in or take back. Mentor review with feedback, plus Undo complete and Return to scholar. Backups that include uploaded files. |
| **M7 Chat** | Community, class and group chats, live over server-sent events, with unread counts. Photos and files. Moderation (remove messages, mute people). Admins and the board can turn chats off. Plus the **Board** role. |

Between M6 and M7 the header was simplified. The top bar has Home, Calendar,
Classes and Chat. The person's initials open a menu with My account, Guide,
Settings (admins) or People (board), and Sign out. The admin pages are tabs
under Settings.

## Decisions and why

**Technology**
- **Go + SQLite (`modernc.org/sqlite`), one static binary, `CGO_ENABLED=0`.**
  Nothing else to install or keep patched on a school's server. A school's data
  is small, and backups are one file.
- **Only three dependencies:** sqlite, `golang.org/x/crypto` (autocert) and
  goldmark (Markdown).
- **No front-end framework or build step.** Pages are rendered on the server with
  `html/template`, and plain JavaScript adds the live parts. Every page works
  without JavaScript.
- **Strict CSP:** no inline scripts, styles or handlers. Script hooks are
  `data-*` attributes in `web/static/app.js`. Setting styles from script through
  the CSSOM (`el.style.height`) is allowed by the policy.

**Database**
- **Migrations are numbered and never edited after release.**
- **Table rebuilds:** migration 0009 had to widen CHECK constraints, so it
  rebuilds `users` and `files`. A migration whose first line is
  `-- taper:foreign-keys-off` runs with foreign keys off, then runs
  `PRAGMA foreign_key_check` before committing, as SQLite's documentation
  describes. This relies on the store using one connection.
- **Events are stored as the school's local wall-clock time**, with the time
  zone as a setting. A weekly 10:00 class stays at 10:00 across daylight saving.

**Security**
- **Releases are signed with ed25519** (key id `taper-2026`). The private key is
  in `/home/brady/taper-signing-key.txt` on the dev VM and in the
  `TAPER_SIGNING_KEY` GitHub secret. Never print it.
- **Secrets are write-only and encrypted** (`internal/secret`, `secret.key` in
  the data folder): the GitHub token, the SMTP password, TOTP secrets and
  calendar link tokens.
- **Uploaded files** are stored under `DataDir/files` with random names.
  - They're served only after a permission check on the owning assignment,
    work or chat message.
  - Responses send `nosniff`.
  - Images, PDFs and plain text show in the browser; everything else downloads.
  - Every file except PDFs is sent with a `sandbox` CSP. Browsers' PDF viewers
    don't work inside a sandbox.

**Network changes** are tried beside the current settings. If they aren't
confirmed from the new address within `TAPER_CONFIRM_SECONDS` (default 3
minutes), they're undone, so a school can't lock itself out from the browser.

**Live chat**
- It uses **server-sent events**, not WebSockets: no extra dependency, and they
  pass through proxies.
- Streams send `X-Accel-Buffering: no` and a keep-alive every 25 seconds.
- Every 25 seconds each stream also rechecks the session and the person's
  access.
- Streams end when the listeners shut down, through `RegisterOnShutdown` in
  `network.go`, so updates and network changes aren't held up. Shutdown takes
  about 0.1 s.
- After a reconnect, missed *new* messages are sent from the `Last-Event-ID`.

**Roles**
- **Leaders** are admins and the board (`User.IsLeader()`). They run the whole
  school: classes, calendars, assignments and every chat.
- **Admin-only** (`User.IsAdmin()`): server settings, updates, network, email,
  problem reports and backups.
- **Board members manage mentors and scholars, but not admin or board
  accounts.** Otherwise a board member could reset an admin's password and take
  over the server. The owner was asked to confirm this choice (see Next steps).
- **Community chat is read-only except for leaders**, and **group chats are
  made by leaders.** Both were the owner's requirements.
- **Assignments have no page of their own.** They live in their class, and the
  home page brings everyone's to-do or to-review list together (owner's
  request).

**Workflow**
- **No grades anywhere.** This is a TJEd principle.
- **One pull request per milestone, CI green before review.** The CI jobs are
  `check`, `install`, `update` and `network`. The install job also uploads and
  downloads a file under the real systemd sandbox.

## Current state

- **`main`** has M7 chat (PR #12). VERSION is `0.7.0-dev`.
- **PR #13** (`m7-board-groups`) is open with green CI: the Board role,
  read-only Community, group chats, and photos and files in chat. It's deployed
  on the dev server. This file is on that branch too.
- **Dev server:**
  - Hostname `dev`, at 10.10.10.34:8088. Proxied as
    https://taper-dev.ltekk.com by Nginx Proxy Manager.
  - Runs the PR #13 build.
  - Database backups taken before each deploy are in `/var/lib/taper/before-*.db`.
  - Port **8099 on the dev VM is now used by the owner's `pbcm` program**. Use
    another port, such as 8097, for throwaway test servers.
- **Testing:**
  - `make check`: gofmt, vet, staticcheck, shellcheck, and tests with the race
    detector.
  - Server tests run the real server against a temporary database.
  - Browser walkthroughs used geckodriver and headless Firefox with throwaway
    Python scripts (not in the repo).
  - Headless Firefox reports no pointer at all, which is why Enter-to-send checks
    `(pointer: coarse)` rather than `(hover: hover)`.

## Open bugs and loose ends

- **Live chat through the reverse proxy hasn't been checked** on the dev server.
  It needs two signed-in devices.
  - If messages only appear after reloading, turn on *Websockets Support* for
    the host in Nginx Proxy Manager.
  - Uploads bigger than the proxy's limit need `client_max_body_size 110m;`
    under Advanced.
- **iOS 27.2 beta shows a letter instead of the home-screen icon** over HTTPS.
  This is an iOS beta bug: apple.com and github.com behave the same way.
  Nothing to fix in Taper; check again when iOS is released.
- **Chat:**
  - Removals that happen while a device is disconnected aren't replayed when it
    reconnects. Only new messages are. A reload shows them.
  - Muting someone doesn't take away their message box until they reload.
    Posting is refused on the server either way.
  - Messages can't be edited, only removed.
  - If saving a chat attachment fails after the message is posted (a disk
    error), the message stays without the file. This is logged, and the person
    isn't told.
  - Photos are served at full size; there are no thumbnails.
- **People page for board members:** admin and board rows still link to their
  account page, which then says only admins can change it. Hiding those links
  would be tidier.
- **Uploads go through `/tmp`** (`ParseMultipartForm`, 8 MB in memory). Under
  systemd `PrivateTmp` that's often RAM-backed `tmpfs`. That's fine at 105 MB a
  request, but worth knowing.

## Next steps

1. **Merge PR #13.**
2. **Tag `v0.6.0`** on the PR #11 merge commit (`1e3fb65`), where VERSION is
   `0.6.0`, so 0.6.0 shows in Settings → Updates. Tag only after the owner says
   to.
3. **Release 0.7.0:** in a pull request, set VERSION to `0.7.0` and rename
   `## [Unreleased]` in `CHANGELOG.md`. Tag `v0.7.0` after the owner confirms
   the merge, then check that the release has the archives, `MANIFEST` and
   `MANIFEST.sig`.
4. **The owner checks live chat** through Nginx Proxy Manager on two devices,
   and confirms (or changes) what board members may do with accounts.
5. **M8, polish and 1.0** (see [plan.md](plan.md#m8-polish-and-10)):
   - Notifications in the app, by email (each person chooses) and by web push.
     New messages, work turned in and feedback are the obvious first ones.
   - Passkeys.
   - An accessibility pass, a performance check, and the guide with screenshots.
   - A tested upgrade path from every pre-release, then **1.0.0**.
   - The chat loose ends above are good M8 candidates.

## Working agreements with the owner

- **Releases:** when the owner says a release PR is merged, check out `main`,
  pull, tag `vX.Y.Z` (only if VERSION matches), push the tag, and check the
  release.
- **Prompts:** keep permission prompts to what's needed (pushes and PRs); build
  and test without stopping in between.
- **Git identity:** commit as
  `bradyloveland <8643792+bradyloveland@users.noreply.github.com>`, because
  GitHub refuses the private email.
- **Text in the app:**
  - The words are admin, board, mentor and scholar, never teacher or student.
  - Text is plain and specific: say what happened and what to do next.
  - The tagline appears on sign-in and setup, in the footer and in the docs.
