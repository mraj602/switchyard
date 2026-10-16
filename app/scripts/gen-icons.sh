#!/usr/bin/env bash
# Renders the desktop app's and the board's icons from app/build/icons-src/*.svg. The PNGs are
# committed, so nothing here runs at build time; run it only when the art changes:
#
#   app/scripts/gen-icons.sh
#
# Two masters of one mark (gold double chevron on the ink tile): icon.svg (stroke 2 on the 20 grid)
# for 48 px and up, icon-small.svg (stroke 2.6) for 32 px and below.
#
#   app/build/icon.png               1024 (mac / win builds)
#   app/build/icons/NxN.png          16 24 32 48 64 128 256 512 (electron-builder linux)
#   app/assets/icon.png              512 window and notification icon
#   app/assets/tray/                 monochrome tray glyphs, no tile:
#     dark/  light/                    tray[-dot]-{16,22,24,32}[@2x].png; dark ink for light bars,
#                                      light ink for dark bars
#     trayTemplate[@2x].png, trayDotTemplate[@2x].png   macOS template images (16 px)
#     overlay-dot[@2x].png             Windows taskbar overlay for "needs you"
#   webui/static/icons/icon.svg      the board favicon: the large tile
#   webui/static/icons/favicon-32.png, favicon.ico (16 + 32 + 48)
#
# The phone's icons (phone/scripts/gen-icons.sh) are a separate set; the notification icon is a
# 96 px alpha mask and does not use the small master. Renderer, first one found: rsvg-convert, or
# ImageMagick (`magick` or `convert`) with its RSVG delegate.
set -euo pipefail

here="$(cd "$(dirname "$0")/.." && pwd -P)"
root="$(cd "$here/.." && pwd -P)"
src="$here/build/icons-src"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp:?}"' EXIT

im=""
if command -v magick >/dev/null 2>&1; then im=magick; elif command -v convert >/dev/null 2>&1; then im=convert; fi

render() { # render <svg> <size> <png>
  if command -v rsvg-convert >/dev/null 2>&1; then
    rsvg-convert -w "$2" -h "$2" "$1" -o "$3"
  elif [ -n "$im" ]; then
    "$im" -background none -density 1200 "$1" -resize "${2}x${2}" "PNG32:$3"
  else
    echo "gen-icons.sh: need rsvg-convert or ImageMagick with RSVG" >&2
    exit 1
  fi
}

master() { if [ "$1" -le 32 ]; then echo "$src/icon-small.svg"; else echo "$src/icon.svg"; fi; }

# --- app icons
mkdir -p "$here/build/icons" "$here/assets/tray/dark" "$here/assets/tray/light"
for n in 16 24 32 48 64 128 256 512; do render "$(master $n)" $n "$here/build/icons/${n}x${n}.png"; done
render "$src/icon.svg" 1024 "$here/build/icon.png"
render "$src/icon.svg" 512 "$here/assets/icon.png"

# --- tray
glyph() { # glyph <name> <color> <dot yes|no> <dir> <sizes...>  (1x and @2x for each size)
  local name=$1 color=$2 dot=$3 dir=$4 s
  shift 4
  local svg="$tmp/$name.svg"
  if [ "$dot" = yes ]; then
    sed "s/@COLOR@/$color/g" "$src/tray.svg" >"$svg"
  else
    sed -e '/<!--DOT-->/,/<!--\/DOT-->/d' -e "s/@COLOR@/$color/g" "$src/tray.svg" >"$svg"
  fi
  for s in "$@"; do
    render "$svg" "$s" "$dir/$name-$s.png"
    render "$svg" $((s * 2)) "$dir/$name-$s@2x.png"
  done
}
for v in dark:#1F2328 light:#F2F3F4; do
  d="$here/assets/tray/${v%%:*}"
  c="${v#*:}"
  glyph tray "$c" no "$d" 16 22 24 32
  glyph tray-dot "$c" yes "$d" 16 22 24 32
done
# macOS: black on alpha; the name ending in Template makes it a template image
glyph trayTemplate '#000000' no "$here/assets/tray" 16
glyph trayDotTemplate '#000000' yes "$here/assets/tray" 16
for f in trayTemplate trayDotTemplate; do
  mv "$here/assets/tray/$f-16.png" "$here/assets/tray/$f.png"
  mv "$here/assets/tray/$f-16@2x.png" "$here/assets/tray/$f@2x.png"
done
# Windows overlay: a gold dot with an ink rim, readable on any taskbar
cat >"$tmp/overlay.svg" <<'S'
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="#17191B"/><circle cx="8" cy="8" r="5.2" fill="#E8B04B"/></svg>
S
render "$tmp/overlay.svg" 16 "$here/assets/tray/overlay-dot.png"
render "$tmp/overlay.svg" 32 "$here/assets/tray/overlay-dot@2x.png"

# --- board favicon
cp "$src/icon.svg" "$root/webui/static/icons/icon.svg"
render "$src/icon-small.svg" 32 "$root/webui/static/icons/favicon-32.png"
render "$src/icon-small.svg" 16 "$tmp/f16.png"
render "$src/icon-small.svg" 32 "$tmp/f32.png"
render "$src/icon.svg" 48 "$tmp/f48.png"
if [ -n "$im" ]; then
  "$im" "$tmp/f16.png" "$tmp/f32.png" "$tmp/f48.png" "$root/webui/static/icons/favicon.ico"
else
  echo "gen-icons.sh: favicon.ico needs ImageMagick; skipped" >&2
fi
echo done
