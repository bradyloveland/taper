# Handoff

*Light your taper at mine.*

A snapshot of where Taper stands, for whoever picks it up next. Last updated
2026-10-19. This file isn't part of the in-app guide. The plan is
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

All eight milestones are done, plus the Parent role. Releases v0.3.0 to v0.8.0
are tagged; 0.9.0 (the rest of M8) is in a release pull request. The owner is
running 0.9.0 on the beta copy for a while, and expects a few additions before
1.0.0.

| Milestone | What it added |
|-----------|---------------|
| **M1 Foundation** | One Go program with SQLite. Installer for Debian. First-time setup in the browser with a one-time setup code. Sign-in, sessions (remember-me 30 days, otherwise 12 hours), people management, temporary passwords, spreadsheet import. PWA (home-screen app). |
| **M2 Updates and bug reports** | Settings → Updates checks GitHub, downloads signed releases, installs, and rolls back by itself if the new version fails. A database backup is taken before each update. "Report a problem" files GitHub issues through a token stored encrypted. |
| **M3 Security and server settings** | Two-step sign-in (TOTP, recovery codes; can be required per role). Network & HTTPS settings in the web interface: plain HTTP, Let's Encrypt, or behind a reverse proxy. Changes are tried first and undone unless confirmed from the new address. Public URL. SMTP email for password resets. |
| **M4 Classes** | Classes with terms, colors, descriptions, mentors and scholars. Archive, then delete. |
| **M5 Calendars** | School and class calendars, repeating events, school time zone. Month, week and list views. Private iCal subscription links per calendar, each resettable on its own. |
| **M6 Assignments** | Formatted instructions and attachments, due dates (on the calendars and feeds), publish now, later or as a draft. Scholar work saves itself as they type; files; turn in or take back. Mentor review with feedback, plus Undo complete and Return to scholar. Backups that include uploaded files. |
| **M7 Chat** | Community, class and group chats, live over server-sent events, with unread counts. Photos and files. Moderation (remove messages, mute people). Admins and the board can turn chats off. Plus the **Board** role. |
| **M8** | **Notifications**: the bell, five kinds (new assignments, work and feedback, due tomorrow, chat, calendar), combined while unread, chosen per person; email each or a daily summary (off by default, no content in emails). **Web push** per device, tied to the session, encrypted (`internal/webpush`, stdlib only). The **Taper DEV / BETA label** in the header (`TAPER_LABEL`). **Passkeys** (`internal/passkey`, stdlib only; count as two-step sign-in). **Accessibility** and **contrast** tests over every page. A **large-school performance test** (`TAPER_PERF=1`) and compressed static files. **Guide pictures** (`make screenshots`). **Upgrade testing** from every release (`make upgrade-test`, CI job `upgrade`). On phones, one menu under the person's initials. |
| **Parents** (after M7) | The **Parent** role and a `parents` link table: anyone who isn't a scholar can be linked to scholars. Parents see their scholars' classes, calendars and work, turn work in for them (history says "(parent)"), and talk in class chats. Linked on the person page or by a "Parent of" import column. |

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
- **The hierarchy is admin, board, mentor, parent, scholar.** Board members
  manage mentors, parents and scholars, but not admin or board accounts, so a
  board member can't take over the server (confirmed by the owner).
- **Parents are links, not only a role** (owner's choice): mentors and board
  members with children don't need a second account. In their scholars'
  classes they have the class role `parent` (`store.ClassRelation`); in a class
  they mentor they're its mentor. They see class pages without the other
  scholars' names, and can post in class chats.
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

- **`main`** has all of M8 (through PR #25).
- **`release-0.9.0`** sets VERSION to `0.9.0`. After it merges, tag `v0.9.0`.
- **Dev VM** (hostname `dev`, 10.10.10.34) runs two copies of Taper:

  | | **Beta** | **Dev** |
  |---|---|---|
  | What | Official releases only, installed with the installer or Settings → Updates, as a school would | Builds of open pull requests, deployed by Claude for the owner to try before approving |
  | Service | `taper` | `taper-dev` |
  | Port | 8088 (https://taper-dev.ltekk.com through Nginx Proxy Manager, for now) | 8090 (needs its own proxy host) |
  | Folders | `/opt/taper`, `/var/lib/taper`, `/etc/taper/taper.conf` | `/opt/taper-dev`, `/var/lib/taper-dev`, `/etc/taper-dev/taper.conf` |
  | Label | `TAPER_LABEL=beta` (shown from 0.8.0) | `TAPER_LABEL=dev` |
  | Commands | `sudo taper …` | `sudo taper-dev …` (wrapper with the dev settings) |

  - Dev was cloned from beta on 2026-10-19 (database, `secret.key`, files), so it has
    the same people and settings, including email and the public URL. Treat its data
    as test data.
  - **Deploying to dev:** build (`make build`; VERSION gives the version), then
    `sudo systemctl stop taper-dev`, copy `taper.db` to
    `/var/lib/taper-dev/before-<version>-<time>.db`,
    `sudo install -o taper -g taper -m 755 bin/taper /opt/taper-dev/taper`, and
    `sudo systemctl start taper-dev`. When several PRs are open, deploy a local merge
    of them (branch `deploy-dev`, not pushed).
  - `taper-dev rollback` checks whether the `taper` service is running, not
    `taper-dev`; stop both before a manual rollback on dev. The automatic rollback
    after a failed update works for both.
  - Port **8099 is the owner's `pbcm` program**; 8097 is free for throwaway servers.
- **Testing:**
  - `make check`: gofmt, vet, staticcheck, shellcheck, and tests with the race
    detector.
  - Server tests run the real server against a temporary database.
  - Browser walkthroughs used geckodriver and headless Firefox with throwaway
    Python scripts (not in the repo).
  - Headless Firefox reports no pointer at all, which is why Enter-to-send checks
    `(pointer: coarse)` rather than `(hover: hover)`.

## Open bugs and loose ends

- **Live chat through the reverse proxy** works, according to the owner. If
  messages only appear after reloading, turn on *Websockets Support* in Nginx
  Proxy Manager; uploads need `client_max_body_size 110m;` under Advanced.
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

1. **Release 0.9.0:** merge `release-0.9.0`, tag `v0.9.0`, and check the
   archives hold `MANIFEST` and `MANIFEST.sig`. The owner updates the beta copy.
2. **Before 1.0.0:** the owner runs 0.9.0 for a while and will bring a few
   additions. Then set VERSION to `1.0.0` and release it the same way.
3. **Loose ends worth a look** (from "Open bugs and loose ends" above): chat
   removals aren't replayed after a reconnect, muting doesn't take away the
   message box until a reload, no message editing, no photo thumbnails, and the
   People page still links admin and board rows for board members.
4. Later ideas from the plan: portfolios, direct messages, attendance, a school
   library or reading lists.

## Working agreements with the owner

- **Releases:** when the owner says a release PR is merged, check out `main`,
  pull, tag `vX.Y.Z` (only if VERSION matches), push the tag, and check the
  release.
- **Prompts:** keep permission prompts to what's needed (pushes and PRs); build
  and test without stopping in between.
- **GitHub sign-in on the VM:** if `gh` says its token is invalid, the owner
  runs `GH_BROWSER=true gh auth login --hostname github.com --git-protocol https --web`
  and approves the code at github.com/login/device.
- **Git identity:** commit as
  `bradyloveland <8643792+bradyloveland@users.noreply.github.com>`, because
  GitHub refuses the private email.
- **Text in the app:**
  - The words are admin, board, mentor and scholar, never teacher or student.
  - Text is plain and specific: say what happened and what to do next.
  - The tagline appears on sign-in and setup, in the footer and in the docs.
