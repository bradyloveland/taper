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

CI also builds the release archives and runs `scripts/ci-install-test.sh` on a
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
| `internal/config/` | Settings from `TAPER_*` environment variables (`/etc/taper/taper.conf`) |
| `internal/store/` | SQLite access and numbered migrations (`migrations/NNNN_name.sql`) |
| `internal/auth/` | Password hashing, tokens, sign-in throttling |
| `internal/server/` | HTTP handlers, middleware, rendering |
| `internal/markdown/` | Safe Markdown rendering (guide, release notes, later assignment text) |
| `internal/release/` | Release manifests, signatures and version numbers |
| `internal/update/` | Checking GitHub, staging, installing and rolling back releases |
| `internal/secret/` | Encrypting saved secrets (GitHub token) with `secret.key` |
| `internal/github/` | Filing problem reports as GitHub issues |
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
