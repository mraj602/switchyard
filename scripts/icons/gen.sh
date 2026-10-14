#!/usr/bin/env bash
# Renders the app icons from scripts/icons/icon.svg (and badge.svg) into webui/static/icons/.
# The PNGs are committed, so nothing here runs at build time; run it only when the art changes:
#
#   scripts/icons/gen.sh
#
# Renderer, first one found: rsvg-convert (librsvg); ImageMagick (`magick` or `convert`)
# with its RSVG delegate (`magick -list format | grep -i svg` shows RSVG); or headless
# Chrome through puppeteer-core (render.cjs; set CHROME and NODE_PATH like webui/smoke).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
out="$here/../../webui/static/icons"
mkdir -p "$out"

render() { # render <svg> <size> <png>
  local svg=$1 size=$2 png=$3
  if command -v rsvg-convert >/dev/null 2>&1; then
    rsvg-convert -w "$size" -h "$size" "$svg" -o "$png"
  elif command -v magick >/dev/null 2>&1; then
    magick -background none -density 1200 "$svg" -resize "${size}x${size}" "PNG32:$png"
  elif command -v convert >/dev/null 2>&1; then
    convert -background none -density 1200 "$svg" -resize "${size}x${size}" "PNG32:$png"
  elif command -v node >/dev/null 2>&1 && node -e "require('puppeteer-core')" 2>/dev/null; then
    node "$here/render.cjs" "$svg" "$size" "$png"
  else
    echo "gen.sh: need rsvg-convert, ImageMagick with RSVG, or node with puppeteer-core" >&2
    exit 1
  fi
}

cp "$here/icon.svg" "$out/icon.svg"
render "$here/icon.svg" 192 "$out/icon-192.png"
render "$here/icon.svg" 512 "$out/icon-512.png"
render "$here/icon.svg" 512 "$out/maskable-512.png"        # same art: it fits the 80% safe zone
render "$here/icon.svg" 180 "$out/apple-touch-icon-180.png" # opaque and square: iOS rounds it
render "$here/badge.svg" 96 "$out/badge-96.png"            # white on transparent, status-bar badge
ls -l "$out"
