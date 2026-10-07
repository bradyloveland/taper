#!/usr/bin/env bash
# Taper installer for Debian 12 and 13 (and other systemd Linux distributions)
#
# Usage: sudo ./install.sh [options]
#   --domain NAME        Serve HTTPS at NAME with a free Let's Encrypt certificate.
#                        NAME must already point at this server, and ports 80 and 443
#                        must be reachable from the internet.
#   --email ADDRESS      Contact address for Let's Encrypt expiry notices (optional)
#   --behind-proxy       A reverse proxy on this machine handles HTTPS:
#                        listen on 127.0.0.1 only and trust its X-Forwarded-* headers
#   --proxy-ip IP[,IP]   A reverse proxy on another machine handles HTTPS:
#                        listen on every interface and trust X-Forwarded-* from these
#   --http               Plain HTTP on every interface (the default for a new install)
#   --port N             Port for plain HTTP or the reverse proxy (default 8088)
#   --version X.Y.Z      Install this release instead of the newest one
#
# Run it from an extracted release folder to install that copy, or on its own to
# download the newest release from GitHub:
#   curl -fsSL https://raw.githubusercontent.com/bradyloveland/taper/main/install.sh | sudo bash
# Re-running it upgrades Taper in place and keeps your data and settings; only the
# options you pass are changed. After the first install, these settings can also be
# changed in the web interface (Settings > Network & HTTPS).
set -euo pipefail

REPO="bradyloveland/taper"
APP_DIR=/opt/taper
CONF_DIR=/etc/taper
CONF="$CONF_DIR/taper.conf"
DATA_DIR=/var/lib/taper
USER_NAME=taper
UNIT=/etc/systemd/system/taper.service

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
fail() { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

NEW_MODE="" NEW_PORT="" NEW_DOMAIN="" NEW_EMAIL="" NEW_BIND="" NEW_PROXIES="" SET_BIND=0 SET_PROXIES=0
WANT_VERSION=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) [[ $# -ge 2 ]] || fail "--domain needs a name"; NEW_MODE=https; NEW_DOMAIN="$2"; SET_BIND=1; SET_PROXIES=1; shift 2 ;;
    --email) [[ $# -ge 2 ]] || fail "--email needs an address"; NEW_EMAIL="$2"; shift 2 ;;
    --behind-proxy) NEW_MODE=proxy; NEW_BIND=127.0.0.1; NEW_PROXIES="127.0.0.1,::1"; SET_BIND=1; SET_PROXIES=1; shift ;;
    --proxy-ip) [[ $# -ge 2 ]] || fail "--proxy-ip needs an address"; NEW_MODE=proxy; NEW_BIND=""; NEW_PROXIES="127.0.0.1,::1,$2"; SET_BIND=1; SET_PROXIES=1; shift 2 ;;
    --http) NEW_MODE=http; SET_BIND=1; SET_PROXIES=1; shift ;;
    --port) [[ $# -ge 2 ]] || fail "--port needs a number"; NEW_PORT="$2"; shift 2 ;;
    --version) [[ $# -ge 2 ]] || fail "--version needs a version"; WANT_VERSION="${2#v}"; shift 2 ;;
    -h|--help) sed -n '2,22p' "${BASH_SOURCE[0]}" 2>/dev/null | sed 's/^# \{0,1\}//' || true; exit 0 ;;
    *) fail "Unknown option: $1 (see --help)" ;;
  esac
done

[[ $EUID -eq 0 ]] || fail "Run this as root: sudo ./install.sh"
command -v systemctl >/dev/null || fail "This installer needs systemd."
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "Taper runs on x86-64 or ARM64 Linux; this machine is $(uname -m)." ;;
esac
if [[ -n "$NEW_PORT" ]] && ! [[ "$NEW_PORT" =~ ^[0-9]+$ && "$NEW_PORT" -ge 1 && "$NEW_PORT" -le 65535 ]]; then
  fail "--port must be a number from 1 to 65535."
fi
if [[ "$NEW_MODE" == https ]] && ! [[ "$NEW_DOMAIN" =~ ^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; then
  fail "--domain must be a public name like learn.example.org."
fi

SRC_DIR=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fetch() { # url dest
  if command -v curl >/dev/null; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null; then wget -qO "$2" "$1"
  else fail "Install curl or wget first: apt-get install -y curl"
  fi
}

# ---- the program --------------------------------------------------------------
if [[ -n "$SRC_DIR" && -x "$SRC_DIR/taper" && -z "$WANT_VERSION" ]]; then
  BIN="$SRC_DIR/taper"
  REL_DIR="$SRC_DIR"
  say "Installing from $SRC_DIR"
else
  if [[ -z "$WANT_VERSION" ]]; then
    say "Looking up the newest release"
    fetch "https://api.github.com/repos/$REPO/releases?per_page=20" "$WORK/releases.json" \
      || fail "Couldn't reach GitHub. Check this server's internet connection."
    WANT_VERSION="$(grep -o '"tag_name": *"v[0-9][^"]*"' "$WORK/releases.json" | head -n1 | sed 's/.*"v\(.*\)"/\1/' || true)"
    [[ -n "$WANT_VERSION" ]] || fail "There's no Taper release on GitHub yet. Download a release from https://github.com/$REPO/releases and run its install.sh."
  fi
  NAME="taper-$WANT_VERSION-linux-$ARCH"
  say "Downloading Taper $WANT_VERSION"
  BASE_URL="https://github.com/$REPO/releases/download/v$WANT_VERSION"
  fetch "$BASE_URL/$NAME.tar.gz" "$WORK/$NAME.tar.gz" || fail "Couldn't download $NAME.tar.gz."
  fetch "$BASE_URL/SHA256SUMS" "$WORK/SHA256SUMS" || fail "Couldn't download the checksums."
  (cd "$WORK" && grep " $NAME.tar.gz\$" SHA256SUMS | sha256sum -c --quiet -) \
    || fail "The download doesn't match its checksum. Try again."
  tar -xzf "$WORK/$NAME.tar.gz" -C "$WORK"
  BIN="$WORK/$NAME/taper"
  REL_DIR="$WORK/$NAME"
fi
"$BIN" version >/dev/null 2>&1 || fail "The taper program won't run on this machine."
NEW_VERSION="$("$BIN" version)"

# ---- account and folders -------------------------------------------------------
if ! id -u "$USER_NAME" >/dev/null 2>&1; then
  say "Creating the $USER_NAME service account"
  useradd --system --user-group --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$USER_NAME"
fi
install -d -o root -g "$USER_NAME" -m 750 "$CONF_DIR"
install -d -o "$USER_NAME" -g "$USER_NAME" -m 700 "$DATA_DIR"
# The service owns its program folder so it can update itself from the web interface.
install -d -o "$USER_NAME" -g "$USER_NAME" -m 755 "$APP_DIR"

FRESH=1
[[ -f "$DATA_DIR/taper.db" ]] && FRESH=0
OLD_VERSION=""
[[ -x "$APP_DIR/taper" ]] && OLD_VERSION="$("$APP_DIR/taper" version 2>/dev/null || true)"

# ---- settings: keep what's there, change only what was asked ---------------------
conf_get() { # key -> value from the existing file, without running it
  [[ -f "$CONF" ]] || return 0
  grep -E "^$1=" "$CONF" | tail -n1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//'
}
MODE="$(conf_get TAPER_MODE)";   MODE="${MODE:-http}"
PORT="$(conf_get TAPER_PORT)";   PORT="${PORT:-8088}"
BIND="$(conf_get TAPER_BIND)"
DOMAIN="$(conf_get TAPER_DOMAIN)"
EMAIL="$(conf_get TAPER_EMAIL)"
PROXIES="$(conf_get TAPER_TRUSTED_PROXIES)"
[[ -n "$NEW_MODE" ]] && MODE="$NEW_MODE"
[[ -n "$NEW_PORT" ]] && PORT="$NEW_PORT"
[[ $SET_BIND -eq 1 ]] && BIND="$NEW_BIND"
[[ $SET_PROXIES -eq 1 ]] && PROXIES="$NEW_PROXIES"
[[ -n "$NEW_DOMAIN" ]] && DOMAIN="$NEW_DOMAIN"
[[ -n "$NEW_EMAIL" ]] && EMAIL="$NEW_EMAIL"
[[ "$MODE" != https ]] && DOMAIN="" && EMAIL=""

cat > "$CONF.new" <<EOF
# Taper settings, read by the taper service (systemd EnvironmentFile).
# Change them by running the installer again, for example:
#   sudo $APP_DIR/install.sh --domain learn.example.org
# or edit this file and run: sudo systemctl restart taper
# Everything else is set in the web interface.

# http (plain HTTP), https (Let's Encrypt for TAPER_DOMAIN), or proxy (behind a reverse proxy)
TAPER_MODE=$MODE
# Port for http and proxy modes. https mode always uses 443 and 80.
TAPER_PORT=$PORT
# Address to listen on; empty means every interface.
TAPER_BIND=$BIND
TAPER_DOMAIN=$DOMAIN
TAPER_EMAIL=$EMAIL
# Reverse proxies whose X-Forwarded-For and X-Forwarded-Proto headers are believed.
TAPER_TRUSTED_PROXIES=$PROXIES
TAPER_DATA_DIR=$DATA_DIR
TAPER_APP_DIR=$APP_DIR
EOF
chown root:"$USER_NAME" "$CONF.new"
chmod 640 "$CONF.new"
mv -f "$CONF.new" "$CONF"
# Network options given here replace settings changed in the web interface.
if [[ -n "$NEW_MODE" || -n "$NEW_PORT" || -n "$NEW_EMAIL" ]] && [[ -f "$DATA_DIR/network.json" ]]; then
  rm -f "$DATA_DIR/network.json"
  echo "The network settings changed in the web interface are replaced by the options given here."
fi

# ---- install the files ------------------------------------------------------------
systemctl stop taper 2>/dev/null || true
if ! [[ "$REL_DIR" -ef "$APP_DIR" ]]; then
  install -o "$USER_NAME" -g "$USER_NAME" -m 755 "$BIN" "$APP_DIR/taper.new"
  mv -f "$APP_DIR/taper.new" "$APP_DIR/taper"
  # Kept so the installer can be run again later without downloading it.
  for f in install.sh uninstall.sh MANIFEST MANIFEST.sig README.md CHANGELOG.md LICENSE; do
    if [[ -f "$REL_DIR/$f" ]]; then
      install -o "$USER_NAME" -g "$USER_NAME" -m 644 "$REL_DIR/$f" "$APP_DIR/$f"
    elif [[ "$f" == MANIFEST* ]]; then
      rm -f "$APP_DIR/$f" # an unsigned build: don't keep a stale manifest
    fi
  done
  # A version installed this way can't be undone from the Updates page.
  rm -f "$APP_DIR"/*.prev
  chmod 755 "$APP_DIR/install.sh" "$APP_DIR/uninstall.sh" 2>/dev/null || true
fi

cat > /usr/local/bin/taper <<EOF
#!/bin/sh
# Runs taper commands with the service's settings, as the service account, so
# its files stay owned by it.
set -a
. $CONF
set +a
if [ "\$(id -u)" = 0 ]; then exec runuser -u $USER_NAME -- $APP_DIR/taper "\$@"; fi
exec $APP_DIR/taper "\$@"
EOF
chmod 755 /usr/local/bin/taper

# ---- service ---------------------------------------------------------------------------
say "Installing the systemd service"
cat > "$UNIT" <<EOF
[Unit]
Description=Taper learning platform
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
User=$USER_NAME
Group=$USER_NAME
EnvironmentFile=$CONF
Environment=HOME=$DATA_DIR
ExecStart=$APP_DIR/taper serve
# After an update from the web interface, the previous version (taper.prev)
# counts failed runs of the new one and puts itself back after three.
# Otherwise it does nothing; the "-" ignores it being missing.
ExecStopPost=-$APP_DIR/taper.prev rollback --after-failure
Restart=always
RestartSec=3
UMask=0077
# Ports 80 and 443 (https mode) without running as root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=$DATA_DIR $APP_DIR
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=yes

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable taper >/dev/null 2>&1

started() { systemctl restart taper && sleep 2 && systemctl is-active --quiet taper; }
if ! started; then
  # Containers without nesting can't use systemd's sandboxing (exit status 226).
  if [[ "$(systemctl show -p ExecMainStatus --value taper)" == "226" ]]; then
    echo "This container doesn't allow systemd's sandboxing, so it's turned off for this service."
    echo "Turn on the container's \"nesting\" feature to use it."
    mkdir -p /etc/systemd/system/taper.service.d
    cat > /etc/systemd/system/taper.service.d/no-sandbox.conf <<'EOF'
[Service]
ProtectSystem=no
ProtectHome=no
PrivateTmp=no
PrivateDevices=no
ProtectKernelTunables=no
ProtectKernelModules=no
ProtectControlGroups=no
EOF
    systemctl daemon-reload
    started || fail "The service didn't start. See: journalctl -u taper -n 50"
  else
    fail "The service didn't start. See: journalctl -u taper -n 50"
  fi
fi

# ---- check it answers -------------------------------------------------------------------
# The settings in use: the web interface may have changed them (network.json).
if read -r MODE PORT BIND DOMAIN < <(/usr/local/bin/taper network 2>/dev/null); then
  [[ "$BIND" == "-" ]] && BIND=""
  [[ "$DOMAIN" == "-" ]] && DOMAIN=""
fi
if [[ "$MODE" != https ]] && command -v curl >/dev/null; then
  CHECK_HOST="${BIND:-127.0.0.1}"
  [[ "$CHECK_HOST" == *:* ]] && CHECK_HOST="[$CHECK_HOST]"
  ok=0
  for _ in 1 2 3 4 5; do
    if curl -fsS -o /dev/null "http://$CHECK_HOST:$PORT/healthz"; then ok=1; break; fi
    sleep 1
  done
  [[ $ok -eq 1 ]] || fail "Taper started but isn't answering on port $PORT. See: journalctl -u taper -n 50"
fi

# ---- done ------------------------------------------------------------------------------
say "Done."
if [[ -n "$OLD_VERSION" && "$OLD_VERSION" != "$NEW_VERSION" ]]; then
  # "Upgraded" only when it's clearly newer; development builds (with a
  # suffix like -dev) don't compare cleanly, so they just say "Changed".
  if [[ "$OLD_VERSION$NEW_VERSION" != *-* ]] &&
     [[ "$(printf '%s\n%s\n' "$OLD_VERSION" "$NEW_VERSION" | sort -V | tail -n1)" == "$NEW_VERSION" ]]; then
    echo "Upgraded from $OLD_VERSION to $NEW_VERSION. Your data and settings were kept."
  else
    echo "Changed from version $OLD_VERSION to $NEW_VERSION. Your data and settings were kept."
  fi
elif [[ $FRESH -eq 0 ]]; then
  echo "Taper $NEW_VERSION reinstalled. Your data and settings were kept."
else
  echo "Taper $NEW_VERSION installed."
fi
echo
case "$MODE" in
  https)
    echo "Open https://$DOMAIN/"
    echo "The first visit can take a few seconds while Let's Encrypt issues the certificate."
    ;;
  proxy)
    echo "Taper listens on http://${BIND:-0.0.0.0}:$PORT/ for your reverse proxy."
    echo "Point the proxy at that address, and keep the Host header (see the install guide)."
    ;;
  *)
    HOST="$(hostname -I 2>/dev/null | awk '{print $1}')"
    HOST="${HOST:-$(hostname)}"
    echo "Open http://$HOST:$PORT/"
    echo "Plain HTTP is fine for trying Taper out. To install it on phones, use HTTPS:"
    echo "Settings > Network & HTTPS in the web interface, or this installer's --domain or"
    echo "--behind-proxy options (see the install guide)."
    ;;
esac
SETUP_CODE="$(/usr/local/bin/taper setup-code 2>/dev/null | grep -E '^[A-Z0-9]{4}-[A-Z0-9]{4}$' || true)"
if [[ -n "$SETUP_CODE" ]]; then
  echo
  printf 'Setup code: \033[1m%s\033[0m\n' "$SETUP_CODE"
  echo "Enter it in the browser to name your school and create the first admin."
  echo "To see it again: sudo taper setup-code"
fi
echo
echo "Logs: journalctl -u taper -f"
