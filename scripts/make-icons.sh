#!/usr/bin/env bash
# Regenerates the PNG icons from web/static/icons/icon.svg.
# Needs Firefox (to draw the SVG) and ImageMagick (to resize and round it).
set -euo pipefail

ICONS="$(cd "$(dirname "${BASH_SOURCE[0]}")/../web/static/icons" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

cp "$ICONS/icon.svg" "$WORK/icon.svg"
# Maskable: full-bleed background, artwork inside the central 80% safe zone.
sed -e 's|<rect width="512" height="512" rx="112" fill="#1f3a5f"/>|<rect width="512" height="512" fill="#1f3a5f"/><g transform="translate(51.2 51.2) scale(.8)">|' \
    -e 's|</svg>|</g></svg>|' "$WORK/icon.svg" > "$WORK/maskable.svg"
# Square: iOS rounds the corners itself.
sed -e 's| rx="112"||' "$WORK/icon.svg" > "$WORK/square.svg"

for n in icon maskable square; do
  printf '<!doctype html><html><body style="margin:0"><img src="%s.svg" width="1024" height="1024" style="display:block"></body></html>' "$n" > "$WORK/$n.html"
  firefox --headless --no-remote --profile "$(mktemp -d -p "$WORK")" --window-size=1024,1024 \
    --screenshot "$WORK/$n.png" "file://$WORK/$n.html" >/dev/null 2>&1
done

convert -size 1024x1024 xc:none -fill white -draw "roundrectangle 0,0,1023,1023,224,224" "$WORK/mask.png"
convert "$WORK/icon.png" "$WORK/mask.png" -alpha off -compose CopyOpacity -composite "$WORK/rounded.png"
convert "$WORK/rounded.png" -resize 512x512 -strip "$ICONS/icon-512.png"
convert "$WORK/rounded.png" -resize 192x192 -strip "$ICONS/icon-192.png"
convert "$WORK/rounded.png" -resize 32x32 -strip "$ICONS/favicon-32.png"
convert "$WORK/maskable.png" -resize 512x512 -strip "$ICONS/icon-maskable-512.png"
convert "$WORK/square.png" -resize 180x180 -strip "$ICONS/apple-touch-icon.png"
echo "Icons written to $ICONS"
