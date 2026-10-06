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
| `cmd/taper/` | The program: `serve`, `setup-code`, `passwd`, `backup`, `version` |
| `internal/config/` | Settings from `TAPER_*` environment variables (`/etc/taper/taper.conf`) |
| `internal/store/` | SQLite access and numbered migrations (`migrations/NNNN_name.sql`) |
| `internal/auth/` | Password hashing, tokens, sign-in throttling |
| `internal/server/` | HTTP handlers, middleware, rendering |
| `internal/markdown/` | Safe Markdown rendering (guide, later assignment text) |
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
3. The Release workflow tests, builds the archives and publishes the GitHub
   release with the changelog section as its notes.
