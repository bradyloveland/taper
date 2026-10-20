# Developing Taper

## Tools

- Go (the version in `go.mod` or newer)
- `staticcheck` (`go install honnef.co/go/tools/cmd/staticcheck@latest`) and `shellcheck` for `make lint`
- Firefox and ImageMagick, only to regenerate icons (`make icons`)

## Run it

```bash
make dev
```

This serves http://127.0.0.1:8088 with throwaway data in `tmp/dev`. The setup
code is printed in the log (and saved in `tmp/dev/setup-code`). Use
`make dev DEV_BIND=0.0.0.0` to try it from a phone on the same network. Service
workers and "Add to Home Screen" need HTTPS everywhere except `localhost`.

## Check it

```bash
make check   # gofmt, go vet, staticcheck, shellcheck, and go test -race ./...
```

`internal/server/*_test.go` runs the real server against a temporary database and
drives it like a browser (cookies, forms, CSRF tokens). Add tests there for every
new page or form.

`TestAccessibility` signs in as each kind of person, follows every link, and
checks each page for the WCAG problems the HTML shows (labels, names, alt text,
headings, IDs, landmarks). `TestContrast` checks the colours in `app.css`, light
and dark, including any rule that sets both a text colour and a background. A
new page or colour is covered automatically.

`make upgrade-test` (also a CI job) downloads every published release,
checking its checksum. For each one it fills a fresh school through that
version's own pages with what it could do: people, classes, events,
assignments with work and feedback, chat, parents, notification choices. Then
it starts this build on the same data and checks it's all there and still
works. It also upgrades one school through every release in turn. A new
release is included automatically; add a step to `tools/upgradetest` when a
release adds something new to keep.

The pictures in the guide (`docs/guide/images`) come from `make screenshots`
(Firefox and ImageMagick needed). It builds a made-up school in a throwaway
database, runs Taper on it, and photographs pages as different people at
desktop and phone sizes, as a release would look. Run it after changing what
pages look like, and check the pictures before committing them. `TestGuide`
fails if the guide shows a missing picture, one without alt text, or if a
picture isn't used. Pictures of phones need "phone" in their name.

`TestPerformance` fills a school of 641 people (60 classes, 1,800 assignments,
31,000 pieces of work, 20,000 messages) and times the main pages for each kind
of person, and writes that tell many people at once. It's slow to set up, so
it only runs when asked:

```bash
TAPER_PERF=1 go test ./internal/server -run TestPerformance -v
```

On the dev VM every page answers in under 15 ms, and a Community post to
everyone takes about 25 ms. The test fails if a page takes over 150 ms.

CI also runs `scripts/ci-network-test.sh` (network changes from the web interface:
confirm, restart, automatic undo, installer override) and `scripts/ci-update-test.sh`.
It builds the release archives and runs `scripts/ci-install-test.sh` on a
real systemd machine: fresh install, first-time setup, re-running with new
options, uninstall and reinstall, and purge. To run that locally without touching
your machine, use a systemd container:

```bash
make dist
docker run -d --name taper-it --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw -v "$PWD/dist:/dist:ro" -v "$PWD/scripts:/scripts:ro" \
  <an image of Debian with systemd and curl, running /sbin/init>
docker exec taper-it bash /scripts/ci-install-test.sh /dist
```

## Layout

| Path | What |
|------|------|
| `cmd/taper/` | The program: `serve`, `setup-code`, `passwd`, `backup`, `rollback`, `version` |
| `internal/config/` | Settings from `TAPER_*` environment variables (`/etc/taper/taper.conf`), with `network.json` on top |
| `internal/store/` | SQLite access and numbered migrations (`migrations/NNNN_name.sql`) |
| `internal/auth/` | Password hashing, tokens, sign-in throttling |
| `internal/server/` | HTTP handlers, middleware, rendering |
| `internal/markdown/` | Safe Markdown rendering (guide, release notes, later assignment text) |
| `internal/release/` | Release manifests, signatures and version numbers |
| `internal/update/` | Checking GitHub, staging, installing and rolling back releases |
| `internal/secret/` | Encrypting saved secrets (GitHub token) with `secret.key` |
| `internal/github/` | Filing problem reports as GitHub issues |
| `internal/mail/` | Sending email over SMTP; `mailtest` is a fake server for tests |
| `internal/qr/` | QR codes (SVG) for setting up two-step sign-in |
| `internal/cal/` | Repeating events, dates in the school's time zone, and iCalendar feeds |
| `tools/taper-sign/` | Signs release folders; makes signing keys |
| `web/templates/` | Page templates (`layout.html` wraps every page) |
| `web/static/` | CSS, JS, icons, the service worker |
| `docs/guide/` | The user guide, shown in the app and on GitHub |

## Conventions

- Pages are rendered on the server. Forms post normally and redirect (POST,
  redirect, GET), and show a one-time message with `s.redirect(w, r, path, msg)`.
- Every signed-in form includes `<input type="hidden" name="csrf" value="{{.CSRF}}">`.
- Strict CSP: no inline `<script>`, `style=""` or `on*=` attributes. Add classes
  to `app.css` and behaviour to `app.js` (with `data-*` hooks).
- Templates get a `pageData`; page-specific values are in `.D`.
- New tables or columns go in a new migration file. Never edit a released one.

## Releasing

1. In a pull request, set `internal/version/VERSION` to the new version and rename
   `## [Unreleased]` in `CHANGELOG.md` to `## [X.Y.Z] - YYYY-MM-DD`.
2. After it's merged, tag it: `git tag vX.Y.Z && git push origin vX.Y.Z`.
3. The Release workflow tests, builds and **signs** the archives, and publishes the
   GitHub release with the changelog section as its notes.

### Release signing

Each archive has a `MANIFEST` (SHA-256 of every file) and `MANIFEST.sig`, an
ed25519 signature. Taper only installs releases from the Updates page if
they're signed by a key built into `internal/release/keys.go`.

- The private key is the `TAPER_SIGNING_KEY` Actions secret (`id:base64-seed`), and
  the owner keeps an offline copy. `make dist` signs when it's set.
- To replace the key: `go run ./tools/taper-sign genkey taper-YYYY`, **add** the
  new public key to `keys.go` (keep the old one), release that version signed with
  the old key, then switch the secret to the new key. Versions that know both keys
  accept either.

### Testing updates

`scripts/ci-update-test.sh` (run in CI) builds versions A, B and C signed with a
throwaway key, installs A, updates to B from the web interface, goes back, updates
again, then installs C, which is built not to start (`-X main.brokenOnPurpose=yes`).
It checks that B is put back on its own and that unsigned releases are refused.
It needs root and systemd. Locally, run it in a systemd container that has Go,
`make` and `sudo`.
