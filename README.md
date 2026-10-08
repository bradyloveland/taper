# Taper

*Light your taper at mine.*

A self-hosted learning platform for Thomas Jefferson Education (TJEd) commonwealth schools.

Taper gives a school one place for its classes, school and class calendars,
assignments and chat. Admins, mentors and scholars each get their own view. The
school runs it on its own Debian server, so its data stays with the school.

> **Status:** in early development (milestones 1 to 7 of 8). See the [plan and milestones](docs/plan.md).

## Features (planned for 1.0)

- **People and roles:** admins, mentors (teachers) and scholars (students).
- **Classes:** mentors and enrolled scholars for each class.
- **Calendars:** a school calendar and class calendars, with iCal subscription links
  for Google, Apple and Outlook calendars.
- **Assignments:** mentors post work with attachments; scholars download it or work
  online and turn it in; mentors give written feedback. No letter grades.
- **Chat:** a community channel and a channel for each class.
- **Works on phones:** add it to the home screen on Android and iOS (PWA).
- **Easy to run:** one program, one database file and a folder of uploads, a one-line installer,
  updates from GitHub in the web interface, and in-app bug reports that go to
  GitHub issues.
- **Built-in guide:** the [user guide](docs/guide/) is also in the app.

## Install

On a Debian 12 or 13 server:

```bash
curl -fsSL https://raw.githubusercontent.com/bradyloveland/taper/main/install.sh | sudo bash
```

Then open the address it prints and enter the setup code. HTTPS with a free
Let's Encrypt certificate is one option away (`--domain learn.yourschool.org`).
See the [install guide](docs/guide/install.md).

## Documentation

- [User guide](docs/guide/README.md), which is also built into the app
- [Plan and milestones](docs/plan.md)
- [Developing Taper](docs/development.md)

## Development

Taper is written in Go with SQLite: one self-contained program, no build step
for the web pages. `make dev` runs it locally, and `make check` runs the linters and
tests. See [docs/development.md](docs/development.md) and [CLAUDE.md](CLAUDE.md).

## License

[MIT](LICENSE)
