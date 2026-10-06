#!/usr/bin/env bash
# Prints the CHANGELOG.md section for a version: scripts/release-notes.sh 0.1.0
set -euo pipefail
version="${1:?usage: release-notes.sh VERSION}"
awk -v v="$version" '
  /^## \[/ { if (found) exit; found = index($0, "## [" v "]") == 1; next }
  found { print }
' "$(dirname "${BASH_SOURCE[0]}")/../CHANGELOG.md" | sed -e '/./,$!d'
