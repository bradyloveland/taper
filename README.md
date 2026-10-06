# Taper

A self-hosted learning platform for Thomas Jefferson Education (TJEd) commonwealth schools.

Taper gives a school one place for its classes, school and class calendars,
assignments and chat. Admins, mentors and scholars each get their own view. The
school runs it on its own Debian server, so its data stays with the school.

> **Status:** in early development. See the [plan and milestones](docs/plan.md).

## Features (planned for 1.0)

- **People and roles:** admins, mentors (teachers) and scholars (students).
- **Classes:** mentors and enrolled scholars for each class.
- **Calendars:** a school calendar and class calendars, with iCal subscription links
  for Google, Apple and Outlook calendars.
- **Assignments:** mentors post work with attachments; scholars download it or work
  online and turn it in; mentors give written feedback. No letter grades.
- **Chat:** a community channel and a channel for each class.
- **Works on phones:** add it to the home screen on Android and iOS (PWA).
- **Easy to run:** one program and one database file, a one-line installer,
  updates from GitHub in the web interface, and in-app bug reports that go to
  GitHub issues.
- **Built-in guide:** the [user guide](docs/guide/) is also in the app.

## Install

Installation instructions arrive with milestone 1. They will be in
[docs/guide/install.md](docs/guide/install.md).

## Development

Taper is written in Go with SQLite. See [docs/development.md](docs/development.md)
once milestone 1 lands, and [CLAUDE.md](CLAUDE.md) for the project's working rules.

## License

[MIT](LICENSE)
