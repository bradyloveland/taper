# CLAUDE.md

Context for Claude Code sessions working on this repository.

## What this is

eduk8 is a self-hosted learning platform for a Thomas Jefferson Education
(TJEd) commonwealth school. One school runs one copy on a Debian server. The roles are **admin**,
**mentor** (teacher) and **scholar** (student). Use those words in the UI, never
"teacher" or "student". There are no letter grades.

The plan and milestones are in `docs/plan.md`. User documentation is in
`docs/guide/`, and the same files are built into the app as its Guide.

## Hard rules

- **One self-contained program.** Go, SQLite (`modernc.org/sqlite`), built with
  `CGO_ENABLED=0`. Templates, CSS, JS, icons and the guide are embedded.
- **Small dependency footprint.** Allowed: `modernc.org/sqlite`, `golang.org/x/crypto`,
  `github.com/yuin/goldmark`. Anything else needs a reason in the pull request.
- **No front-end framework, no CDNs, no build step.** Pages are rendered on the server
  with `html/template`; plain JavaScript in `web/static/` adds the live parts.
- **Strict CSP:** no inline scripts, no inline event handlers, no `style=""` attributes.
- **Schema changes are new numbered files** in `internal/store/migrations/`. Never edit
  a migration that has been released. Upgrades must keep every school's data.
- **No terminal needed after install**, except the first install and the account-recovery
  commands (`eduk8 passwd`, `eduk8 setup-code`).
- **Privacy:** scholars are often minors. Never send personal data off the server
  except where the user chooses to (a bug report they write, a calendar link they share).
- **User-facing text is plain and specific:** say what happened and what to do next.

## Commands

```bash
make dev      # run locally on http://127.0.0.1:8088 with throwaway data in tmp/dev
make test     # go test -race ./...
make lint     # gofmt, go vet, staticcheck, shellcheck
make check    # both
make dist     # linux amd64/arm64 release archives in dist/
```

## How to make changes

1. Work on a branch and open a pull request against `main`. Never push to `main` directly.
2. Add or update tests with every behaviour change. Handlers get coverage in
   `internal/server/*_test.go`, which runs the real server against a temporary database.
3. Keep `make check` green, and check the change in a real browser (desktop and phone width).
4. Update the relevant page in `docs/guide/` (and `docs/plan.md` if the plan changed), and
   add an entry under `## [Unreleased]` in `CHANGELOG.md`.

## Versions and releases

- Before 1.0, each milestone is a minor pre-release (0.1.0 = M1, 0.2.0 = M2, ...).
- The version lives in `internal/version/VERSION`.
- To release: in a pull request, bump `VERSION` and rename `## [Unreleased]` to
  `## [X.Y.Z] - YYYY-MM-DD`. After merge, the owner tags `vX.Y.Z` and pushes it.
