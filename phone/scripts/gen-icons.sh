#!/usr/bin/env bash
# Renders the phone app's icons from phone/assets/src/*.svg into phone/assets/ (app.config.ts
# reads them). The PNGs are committed, so nothing here runs at build time; run it only when the
# art changes:
#
#   phone/scripts/gen-icons.sh
#
# icon.png                 1024 launcher icon (iOS, Android legacy): the mark on ink, full bleed
# adaptive-foreground.png  Android adaptive icon foreground, mark inside the safe zone, transparent
# adaptive-monochrome.png  Android 13 themed icon layer, white on transparent
# notification-icon.png    96 x 96 white mark on transparent: the status bar / notification small
#                          icon (Android uses only the alpha channel)
# mark.png                 192 x 192 gold mark on transparent, tightly cropped: the logo on the
#                          app's own screens (src/components/logo.tsx)
#
# The Android splash screen reuses the adaptive foreground on the ink colour (with-wy-native.js).
#
# The same art as scripts/icons/icon.svg (the board and desktop app). Renderer, first one found:
# rsvg-convert, or ImageMagick (`magick` / `convert`) with its RSVG delegate.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd -P)"
src="$here/assets/src"
out="$here/assets"

render() { # render <svg> <size> <png>
  if command -v rsvg-convert >/dev/null 2>&1; then
    rsvg-convert -w "$2" -h "$2" "$1" -o "$3"
  elif command -v magick >/dev/null 2>&1; then
    magick -background none -density 600 "$1" -resize "${2}x${2}" "PNG32:$3"
  elif command -v convert >/dev/null 2>&1; then
    convert -background none -density 600 "$1" -resize "${2}x${2}" "PNG32:$3"
  else
    echo "gen-icons.sh: need rsvg-convert or ImageMagick with RSVG" >&2
    exit 1
  fi
}

render "$src/icon.svg" 1024 "$out/icon.png"
render "$src/adaptive-foreground.svg" 1024 "$out/adaptive-foreground.png"
render "$src/adaptive-monochrome.svg" 1024 "$out/adaptive-monochrome.png"
render "$src/notification-icon.svg" 96 "$out/notification-icon.png"
render "$src/mark.svg" 192 "$out/mark.png"
ls -l "$out"/*.png
