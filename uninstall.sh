#!/usr/bin/env bash
# Taper uninstaller
#
# Usage: sudo ./uninstall.sh [--purge]
#   --purge   Also delete all of Taper's data (the database, uploaded files,
#             certificates), its settings and the taper service account.
#             Without it, the data in /var/lib/taper is kept, so installing
#             again picks up where you left off.
set -euo pipefail

PURGE=0
case "${1:-}" in
  --purge) PURGE=1 ;;
  "") ;;
  -h|--help) sed -n '2,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *) echo "Unknown option: $1 (see --help)" >&2; exit 1 ;;
esac

[[ $EUID -eq 0 ]] || { echo "Run this as root: sudo ./uninstall.sh" >&2; exit 1; }

if [[ $PURGE -eq 1 ]]; then
  echo "This deletes every class, assignment, message and account in Taper, for good."
  if [[ -t 0 ]]; then
    read -r -p "Type DELETE to continue: " answer
    [[ "$answer" == "DELETE" ]] || { echo "Nothing was removed."; exit 1; }
  fi
fi

systemctl disable --now taper 2>/dev/null || true
rm -f /etc/systemd/system/taper.service
rm -rf /etc/systemd/system/taper.service.d
systemctl daemon-reload
rm -f /usr/local/bin/taper
rm -rf /opt/taper

if [[ $PURGE -eq 1 ]]; then
  rm -rf /var/lib/taper /etc/taper
  if id -u taper >/dev/null 2>&1; then
    userdel taper 2>/dev/null || true
  fi
  echo "Taper and all of its data are removed."
else
  echo "Taper is removed. Its data and settings are kept in /var/lib/taper and /etc/taper."
  echo "To delete them too: sudo ./uninstall.sh --purge"
fi
