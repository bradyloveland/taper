#!/usr/bin/env bash
# Tests changing network settings from the web interface on a real
# (throwaway) systemd machine:
#   1. change the port; both addresses answer; confirm from the new one
#   2. the change survives a restart
#   3. a change that isn't confirmed is undone by itself
#   4. the installer's options replace settings changed in the web interface
# Usage: scripts/ci-network-test.sh DIST_DIR   (uses sudo)
set -euo pipefail

DIST="$(cd "${1:?usage: ci-network-test.sh DIST_DIR}" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

say()  { printf '\n==> %s\n' "$*"; }
fail() { echo "FAILED: $*" >&2; sudo journalctl -u taper -n 40 --no-pager >&2 || true; exit 1; }

arch=amd64
[[ "$(uname -m)" == aarch64 ]] && arch=arm64
tar -xzf "$(ls "$DIST"/taper-*-linux-$arch.tar.gz)" -C "$WORK"
rel="$(ls -d "$WORK"/taper-*/)"

jar="$WORK/cookies"
page() { curl -sS -b "$jar" -c "$jar" "$@"; }
csrf() { page "$1/admin/network" | grep -o 'name="csrf" value="[^"]*"' | head -n1 | sed 's/.*value="\([^"]*\)"/\1/'; }
post() { # base path [curl args]: POST with the CSRF token, print the status
  local base="$1" path="$2"; shift 2
  page -o /dev/null -w '%{http_code}' -X POST "$@" --data-urlencode "csrf=$(csrf "$base")" "$base$path"
}
answers() { curl -fsS -m 2 "http://127.0.0.1:$1/healthz" >/dev/null 2>&1; }
wait_for() { # description seconds command...
  local what="$1" secs="$2"; shift 2
  for _ in $(seq "$secs"); do "$@" && return 0; sleep 1; done
  fail "timed out waiting for $what"
}
not() { ! "$@"; }

say "Installing"
sudo "$rel/install.sh" --port 8088 >/dev/null
echo "TAPER_CONFIRM_SECONDS=15" | sudo tee -a /etc/taper/taper.conf >/dev/null
sudo systemctl restart taper
wait_for "Taper to start" 30 answers 8088
code="$(sudo taper setup-code)"
status="$(page -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:8088/setup --data-urlencode "code=$code" \
  --data-urlencode "school=CI School" --data-urlencode "display_name=CI Admin" --data-urlencode "username=ciadmin" \
  --data-urlencode "password=ci-password-1" --data-urlencode "confirm=ci-password-1")"
[[ "$status" == 303 ]] || fail "setup answered $status"

say "Changing the port to 8090 and confirming from the new address"
[[ "$(post http://127.0.0.1:8088 /admin/network --data-urlencode mode=http --data-urlencode port=8090 --data-urlencode bind=)" == 303 ]] \
  || fail "the change wasn't accepted"
answers 8088 || fail "the old address should keep working while trying"
answers 8090 || fail "the new address should answer"
page http://127.0.0.1:8090/admin/network | grep -q "The new settings work. Keep them?" || fail "the new address should offer Keep"
[[ "$(post http://127.0.0.1:8090 /admin/network/confirm)" == 303 ]] || fail "confirming failed"
wait_for "the old address to close" 15 not answers 8088
sudo grep -q '"TAPER_PORT": "8090"' /var/lib/taper/network.json || fail "the change wasn't saved"
[[ "$(sudo taper network)" == "http 8090 - -" ]] || fail "taper network shows $(sudo taper network)"

say "The change survives a restart"
sudo systemctl restart taper
wait_for "Taper on 8090 after a restart" 30 answers 8090
answers 8088 && fail "8088 shouldn't answer after the restart"

say "An unconfirmed change is undone"
[[ "$(post http://127.0.0.1:8090 /admin/network --data-urlencode mode=http --data-urlencode port=8092 --data-urlencode bind=)" == 303 ]] \
  || fail "the second change wasn't accepted"
answers 8092 || fail "8092 should answer while trying"
wait_for "the unconfirmed change to be undone" 40 not answers 8092
answers 8090 || fail "8090 should still work"
page http://127.0.0.1:8090/admin/network | grep -q "confirmed in time" || fail "the page should say the change was undone"

say "The installer's options replace web settings"
out="$(sudo "$rel/install.sh" --port 8088)"
grep -q "replaced by the options given here" <<<"$out" || fail "the installer should say it replaced web settings"
grep -q "Open http://.*:8088/" <<<"$out" || fail "the installer should show the port in use"
wait_for "Taper on 8088 again" 30 answers 8088
sudo test ! -e /var/lib/taper/network.json || fail "network.json should be removed"

say "Recovery commands"
sudo taper mfa-reset ciadmin | grep -q "Two-step sign-in is off" || fail "taper mfa-reset"
sudo taper network --reset | grep -q "removed" || fail "taper network --reset"

sudo /opt/taper/uninstall.sh --purge </dev/null >/dev/null
echo
echo "All network checks passed."
