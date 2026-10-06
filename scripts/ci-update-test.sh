#!/usr/bin/env bash
# Tests updating from the web interface on a real (throwaway) systemd machine:
#   1. install version A with install.sh, and finish setup
#   2. upload and install B; it starts and is confirmed after a minute
#   3. go back to A by hand, then install B again
#   4. install C, which is built not to start; B is put back on its own
#   5. an unsigned release is refused
# The builds trust a throwaway key made here, not the project's release key.
# Run from the repository root; it uses sudo.
set -euo pipefail

say()  { printf '\n==> %s\n' "$*"; }
fail() { echo "FAILED: $*" >&2; sudo journalctl -u taper -n 60 --no-pager >&2 || true; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

keys="$(go run ./tools/taper-sign genkey ci-test)"
pub="$(sed -n 's/^public:  //p' <<<"$keys")"
export TAPER_SIGNING_KEY
TAPER_SIGNING_KEY="$(sed -n 's/^private: //p' <<<"$keys")"
key_flag="-X github.com/bradyloveland/taper/internal/release.ExtraKey=$pub"
arch=amd64
[[ "$(uname -m)" == aarch64 ]] && arch=arm64

build() { # version name [extra ldflags]
  make dist VERSION="$1" EXTRA_LDFLAGS="$key_flag ${3:-}" >/dev/null 2>&1 || fail "building $1 failed"
  mv "dist/taper-$1-linux-$arch.tar.gz" "$WORK/$2.tar.gz"
}
say "Building versions A, B and C"
build 0.0.1-ci.1 a
build 0.0.1-ci.2 b
build 0.0.1-ci.3 c "-X main.brokenOnPurpose=yes"

say "Installing A"
tar -xzf "$WORK/a.tar.gz" -C "$WORK"
sudo "$WORK/taper-0.0.1-ci.1-linux-$arch/install.sh" --port 8088 >/dev/null
base=http://127.0.0.1:8088
jar="$WORK/cookies"

page() { curl -sS -b "$jar" -c "$jar" "$@"; }
csrf() { page "$base/admin/updates" | grep -o 'name="csrf" value="[^"]*"' | head -n1 | sed 's/.*value="\([^"]*\)"/\1/'; }
# has TEXT URL [curl args]: the page contains TEXT (HTML entities aside).
has() { local text="$1" out; shift; out="$(page "$@" | sed -e "s/&#39;/'/g" -e 's/&amp;/\&/g')" || return 1; grep -qF -- "$text" <<<"$out"; }
running() { curl -fsS "$base/healthz" 2>/dev/null | sed -n 's/^ok //p' || true; }
is_version() { [[ "$(running)" == "$1" ]]; }
wait_for() { # description seconds command...
  local what="$1" secs="$2"; shift 2
  for _ in $(seq "$secs"); do "$@" && return 0; sleep 1; done
  fail "timed out waiting for $what"
}
post() { # path [curl args]: POST with the CSRF token, print the status
  local path="$1"; shift
  page -o /dev/null -w '%{http_code}' -X POST "$@" -F "csrf=$(csrf)" "$base$path"
}
upload_install() { # name version
  [[ "$(post /admin/updates/upload -F "release=@$WORK/$1.tar.gz")" == 303 ]] || fail "upload of $2 wasn't accepted"
  has "Ready to install: Taper $2" "$base/admin/updates" || fail "$2 isn't ready to install"
  [[ "$(post /admin/updates/install)" == 200 ]] || fail "install of $2 didn't start"
}

code="$(sudo taper setup-code)"
status="$(page -o /dev/null -w '%{http_code}' -X POST "$base/setup" --data-urlencode "code=$code" \
  --data-urlencode "school=CI School" --data-urlencode "display_name=CI Admin" --data-urlencode "username=ciadmin" \
  --data-urlencode "password=ci-password-1" --data-urlencode "confirm=ci-password-1")"
[[ "$status" == 303 ]] || fail "setup answered $status"
has "This server runs Taper 0.0.1-ci.1" "$base/admin/updates" || fail "the Updates page doesn't show A"
if has "isn't running as the taper system service" "$base/admin/updates"; then fail "updates should be possible"; fi

say "Updating to B"
upload_install b 0.0.1-ci.2
wait_for "B to run" 60 is_version 0.0.1-ci.2
test "$(sudo /opt/taper/taper.prev version)" = 0.0.1-ci.1 || fail "A should be kept as taper.prev"
wait_for "B to be confirmed" 90 has "Updated from 0.0.1-ci.1 to 0.0.1-ci.2. Everything went well." "$base/admin/updates"
sudo sh -c "ls /var/lib/taper/backups/taper-*-0.0.1-ci.1.db" >/dev/null 2>&1 || fail "the database wasn't backed up before the update"

say "Going back to A by hand"
has "Go back to 0.0.1-ci.1" "$base/admin/updates" || fail "going back should be offered"
[[ "$(post /admin/updates/rollback)" == 200 ]] || fail "going back didn't start"
wait_for "A to run again" 60 is_version 0.0.1-ci.1
has "Went back from 0.0.1-ci.2 to 0.0.1-ci.1." "$base/admin/updates" || fail "going back should be recorded"

say "Updating to B again"
upload_install b 0.0.1-ci.2
wait_for "B to run" 60 is_version 0.0.1-ci.2
wait_for "B to be confirmed" 90 has "Everything went well." "$base/admin/updates"

say "Installing C, which doesn't start"
upload_install c 0.0.1-ci.3
# B answers for a moment before it restarts, so wait for the rollback itself.
wait_for "C to be undone" 120 has "didn't start: this test build doesn't start, on purpose" "$base/admin/updates"
is_version 0.0.1-ci.2 || fail "B should be running again"
test "$(sudo /opt/taper/taper version)" = 0.0.1-ci.2 || fail "B should be back in the program folder"
systemctl is-active --quiet taper || fail "the service should be running"

say "An unsigned release is refused"
env -u TAPER_SIGNING_KEY make dist VERSION=0.0.1-ci.4 >/dev/null 2>&1
mv "dist/taper-0.0.1-ci.4-linux-$arch.tar.gz" "$WORK/d.tar.gz"
[[ "$(post /admin/updates/upload -F "release=@$WORK/d.tar.gz")" == 422 ]] || fail "an unsigned release was accepted"

say "Cleaning up"
sudo /opt/taper/uninstall.sh --purge </dev/null >/dev/null
echo
echo "All update checks passed."
