#!/usr/bin/env bash
# Installs a built release on this (throwaway, systemd) machine and checks
# that install, first-time setup, upgrade in place, settings changes and
# uninstall all work. Used by CI; needs root.
#
# Usage: sudo scripts/ci-install-test.sh DIST_DIR
set -euo pipefail

DIST="$(cd "${1:?usage: ci-install-test.sh DIST_DIR}" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

step() { printf '\n==> %s\n' "$*"; }
die()  { echo "FAILED: $*" >&2; journalctl -u taper -n 40 --no-pager >&2 || true; exit 1; }

arch=amd64
[[ "$(uname -m)" == aarch64 ]] && arch=arm64
archive="$(ls "$DIST"/taper-*-linux-$arch.tar.gz)"
(cd "$DIST" && sha256sum -c --quiet --ignore-missing SHA256SUMS) || die "checksums don't match"
tar -xzf "$archive" -C "$WORK"
rel="$(ls -d "$WORK"/taper-*)"

step "Fresh install"
out="$("$rel/install.sh")" || die "install.sh failed"
echo "$out"
code="$(echo "$out" | grep -o 'Setup code: .*' | grep -oE '[A-Z0-9]{4}-[A-Z0-9]{4}')" || die "no setup code printed"
systemctl is-active --quiet taper || die "service not running"
curl -fsS http://127.0.0.1:8088/healthz | grep -q ok || die "healthz"
[[ "$(stat -c %U:%a /var/lib/taper)" == "taper:700" ]] || die "data folder owner or mode wrong"
[[ "$(stat -c %U:%G:%a /etc/taper/taper.conf)" == "root:taper:640" ]] || die "settings file owner or mode wrong"
[[ "$(taper setup-code)" == "$code" ]] || die "taper setup-code shows a different code"

step "First-time setup in the browser"
status="$(curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:8088/setup \
  --data-urlencode "code=$code" --data-urlencode "school=CI Commonwealth" \
  --data-urlencode "display_name=CI Admin" --data-urlencode "username=ciadmin" \
  --data-urlencode "password=ci-password-1" --data-urlencode "confirm=ci-password-1")"
[[ "$status" == 303 ]] || die "setup answered $status"
taper setup-code | grep -q "already done" || die "setup code still offered after setup"
status="$(curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:8088/login \
  --data-urlencode "username=ciadmin" --data-urlencode "password=ci-password-1")"
[[ "$status" == 303 ]] || die "sign-in answered $status"

step "Uploading a file works under the service's sandbox"
jar="$WORK/cookies"
base=http://127.0.0.1:8088
curl -fsS -c "$jar" -o /dev/null -X POST "$base/login" \
  --data-urlencode "username=ciadmin" --data-urlencode "password=ci-password-1"
csrf() { curl -fsS -b "$jar" "$base/account" | grep -o 'name="csrf" value="[^"]*"' | head -1 | cut -d'"' -f4; }
token="$(csrf)"
[[ -n "$token" ]] || die "no CSRF token"
status="$(curl -s -o /dev/null -w '%{http_code}' -b "$jar" -X POST "$base/classes/new" \
  --data-urlencode "csrf=$token" --data-urlencode "name=CI Class")"
[[ "$status" == 303 ]] || die "creating a class answered $status"
echo "Light your taper at mine." > "$WORK/reading.txt"
loc="$(curl -s -o /dev/null -w '%{redirect_url}' -b "$jar" -X POST "$base/classes/1/assignments/new" \
  -F "csrf=$token" -F "title=CI reading" -F "publish=now" -F "files=@$WORK/reading.txt")"
[[ "$loc" == */assignments/1 ]] || die "creating an assignment went to '$loc'"
stored="$(find /var/lib/taper/files -type f | head -1)"
[[ -n "$stored" && "$(stat -c %U "$stored")" == taper ]] || die "uploaded file not stored under /var/lib/taper/files"
link="$(curl -fsS -b "$jar" "$base/assignments/1" | grep -o 'href="/files/[^"]*"' | head -1 | cut -d'"' -f2)"
curl -fsS -b "$jar" "$base$link" | grep -q "Light your taper" || die "the uploaded file didn't download"
curl -fsS -b "$jar" -o "$WORK/backup.tar.gz" "$base/admin/backup?files=1"
tar -tzf "$WORK/backup.tar.gz" | grep -q '^files/' || die "the backup lacks the uploaded files"

step "Re-run with a new port: settings change, data kept"
"$rel/install.sh" --port 8090 | tee "$WORK/out2"
grep -q "reinstalled" "$WORK/out2" || die "re-run didn't say it reinstalled"
grep -q '^TAPER_PORT=8090$' /etc/taper/taper.conf || die "port not saved"
curl -fsS http://127.0.0.1:8090/healthz >/dev/null || die "not answering on the new port"
taper passwd ciadmin | grep -q "Temporary password" || die "admin account lost in upgrade"

step "Re-run without options keeps the port"
/opt/taper/install.sh >/dev/null
grep -q '^TAPER_PORT=8090$' /etc/taper/taper.conf || die "port reset by a plain re-run"

step "Behind a reverse proxy"
"$rel/install.sh" --behind-proxy >/dev/null
grep -q '^TAPER_MODE=proxy$' /etc/taper/taper.conf || die "proxy mode not saved"
grep -q '^TAPER_BIND=127.0.0.1$' /etc/taper/taper.conf || die "proxy bind not saved"
curl -fsS http://127.0.0.1:8090/healthz >/dev/null || die "not answering in proxy mode"
ip="$(hostname -I | awk '{print $1}')"
curl -fsS -m 3 "http://$ip:8090/healthz" >/dev/null 2>&1 && die "--behind-proxy should only listen on 127.0.0.1"
"$rel/install.sh" --proxy-ip 192.0.2.10 >/dev/null
grep -q '^TAPER_BIND=$' /etc/taper/taper.conf || die "--proxy-ip should listen everywhere"
grep -q '^TAPER_TRUSTED_PROXIES=127.0.0.1,::1,192.0.2.10$' /etc/taper/taper.conf || die "proxy address not trusted"
curl -fsS "http://$ip:8090/healthz" >/dev/null || die "--proxy-ip isn't reachable from other machines"
"$rel/install.sh" --http --port 8088 >/dev/null
grep -q '^TAPER_BIND=$' /etc/taper/taper.conf || die "--http should listen everywhere"

step "Uninstall keeps data; reinstall picks it up"
/opt/taper/uninstall.sh
[[ ! -e /opt/taper && ! -e /etc/systemd/system/taper.service ]] || die "program not removed"
[[ -f /var/lib/taper/taper.db ]] || die "data removed without --purge"
"$rel/install.sh" | tee "$WORK/out3"
grep -q "Setup code" "$WORK/out3" && die "reinstall asked for setup again"
taper passwd ciadmin >/dev/null || die "data not picked up after reinstall"

step "Purge"
/opt/taper/uninstall.sh --purge </dev/null
[[ ! -e /var/lib/taper && ! -e /etc/taper ]] || die "purge left data"
id -u taper >/dev/null 2>&1 && die "purge left the service account"

echo
echo "All install checks passed."
