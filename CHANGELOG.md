# Changelog

All notable changes to Taper are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **Passkeys**: sign in with your fingerprint, face or device PIN instead of a password.
  Add them under **My account → Passkeys**; sign in with **Sign in with a passkey** (or the
  suggestion in the username box). A passkey counts as two-step sign-in: where the school
  requires it, a passkey is enough, and a password alone isn't. Admins and the board can
  remove someone's passkeys from their page, and `taper mfa-reset` removes them too.
  Standard library only (ES256, EdDSA and RS256 keys, via `getPublicKey()`, so no CBOR).

### Changed
- **The guide has pictures** (milestone 8): 15 screenshots of a made-up school, at desktop
  and phone sizes, in the pages they explain, in the app and on GitHub. `make screenshots`
  retakes them all.
- **Accessibility** (milestone 8): an automated check now opens every page as an admin, a
  mentor, a scholar, a parent and someone signed out, and checks it against WCAG 2.2 AA
  (labels, names, alt text, headings, IDs and references, language, landmarks); another
  checks colour contrast in both themes. Fixes from it: a stronger focus outline on light
  backgrounds, a darker blue for buttons in the dark theme, the chat moderation panel's
  heading order, new chat messages announced to screen readers, focused items no longer
  hidden under the top bar, and the phone menu saying when something's new.
- **Performance** (milestone 8): a new test times every main page against a school of 641
  people with 1,800 assignments, 31,000 pieces of work and 20,000 messages. Every page
  answers in under 15 ms on the test server, and a Community post that notifies everyone in
  about 25 ms. Static files (styles, scripts, icons) are now sent compressed, about a quarter
  of their size; pages aren't, so their security tokens can't be guessed through compression.
- The top left now always says **Taper**, with the label after it on copies that aren't a
  school's real one (**Taper BETA**, **Taper DEV**). The school's name is on the sign-in page,
  in the account menu, in the footer, and under the home page's greeting, which now uses your
  first name ("Good evening, Pam").
- On phones, the top bar shows only your initials: their menu has Home, Calendar, Classes,
  Chat and Notifications (with their counts) as well as your own links, and a dot on them
  says something's new. The separate menu button and bell are gone on phones.

## [0.8.0] - 2026-10-19

### Added
- **Notifications on your lock screen** (web push). Under **My account → Notifications**, turn
  them on for each phone, tablet or computer, and send a test. They follow the same choices
  as the bell; messages in one chat replace each other; the app's icon shows the unread
  count. They stop on a device when you sign out there. On iPhone and iPad, Taper must be on
  the Home Screen. Messages are encrypted for the device (RFC 8291) and signed with the
  server's own key (VAPID, made on first use and stored encrypted), with no new dependencies.
- **Notifications** (the start of milestone 8). A bell in the top bar shows what's new:
  - new assignments (when they're published, including ones set to publish later);
  - work turned in (for mentors), and feedback, Complete and Return (for scholars and their
    parents), combined per assignment until looked at;
  - work due tomorrow and not turned in, from 3 PM the day before;
  - chat messages, combined per chat until it's opened;
  - new, changed and cancelled calendar events.
  Each person chooses the kinds they want under **My account → Notifications**, and can turn
  on **email**: one for each notification, or a summary each evening. Emails link to Taper
  and don't include what anyone wrote. Notifications are kept for 60 days.
- A **label under the logo** for copies of Taper that aren't a school's real one: **Dev** on
  development builds, **Beta** on pre-releases, or any word set with the installer's
  `--label` option (`TAPER_LABEL`). It's also at the start of the browser tab's title. A
  school's copy running a release shows none.

## [0.7.0] - 2026-10-19

### Added
- Milestone 7, chat:
  - **Chat** in the top bar: **Community** for the whole school (everyone reads; admins and
    the board post), a chat for each class, and **group chats** that admins and the board
    make with any name and anyone in them. A count of unread messages next to Chat, a dot on
    the phone menu, and a **Class chat** card on each class's page.
  - **Photos and files** in chat (up to 10 at a time; photos show in the chat; paste a
    picture to attach it). Links become clickable.
  - Messages arrive **live**, without reloading (server-sent events; no new dependencies).
    After a lost connection, missed messages are filled in. Pages still work without
    JavaScript.
  - Remove your own messages. Mentors moderate their class chats, admins and the board every chat:
    remove messages and **mute** people for an hour, a day or until unmuted.
  - Admins and the board can **turn a chat off** (messages are kept). Archived classes'
    chats are read-only.
- The **Board** role: board members see and run every class, calendar, assignment and chat,
  and manage mentors and scholars, but not admin or board accounts or the server (settings,
  updates, network, email, backups). Two-step sign-in can be required for board members.

- The **Parent** role. Admins and the board link parents to their scholars on either one's
  page, or with a **Parent of** column when importing a spreadsheet. Anyone who isn't a
  scholar can be a parent, so mentors and board members with children at the school don't
  need a second account. Parents:
  - see a card for each scholar on their home page, with work to do, sent back, or waiting
    for a mentor;
  - see their scholars' classes (without the other scholars' names), calendars and
    calendar feeds, assignments, work and feedback;
  - can write, attach files, turn in and take back work **for** their scholar. The work's
    history says the parent did it;
  - read Community and talk in their scholars' class chats.
  Two-step sign-in can be required for parents.

## [0.6.0] - 2026-10-07

### Changed
- A simpler header: **Home**, **Calendar** and **Classes** across the top, and a menu under
  your initials at the far right with **My account**, **Guide**, **Settings** (admins) and
  **Sign out**. People, Updates, Network & HTTPS, Email and Problem reports are now tabs
  under Settings.

### Added
- Milestone 6, assignments:
  - **Assignments** for each class: a title, formatted instructions, attached files (up to
    25 MB each), an optional due date and time, and how work is turned in (written in Taper,
    as files, both, or on paper). Publish now, from a day and time, or keep a draft.
  - **Scholars** download the files, write in a box that saves as they type, attach files,
    and **turn in** their work. They can take it back until their mentor looks at it.
  - **Mentors** see every scholar's work and status, write feedback, and mark it
    **Complete** or **Needs another look**; the scholar can then turn it in again. Mentors
    can also **Undo complete** or **Return to scholar** without feedback. Each piece of
    work keeps a history. No grades.
  - Assignments live in their class: an **Assignments** card on each class's page, and a
    list of all of them. The home page's **Assignments** card brings together all your
    classes: work still to do for scholars; work to review, then what's due soon, for
    mentors.
  - **Due dates** on the class calendar, My calendar, the Coming up cards and calendar
    subscriptions.
  - **Settings → Download database and files**: a `.tar.gz` with the database and every
    uploaded file. Settings shows how much space uploads use.

## [0.5.0] - 2026-10-07

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
