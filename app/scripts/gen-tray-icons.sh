#!/usr/bin/env bash
# Renders assets/tray/tray-{0..9,9plus}.png (22 px) and tray-N@2x.png (44 px) from the one SVG
# scripts/tray-icon.svg. The PNGs are committed, so nothing here runs at build time; run it
# only when the art changes:
#
#   scripts/gen-tray-icons.sh
#
# The badge (the text between the BADGE markers) is cut for count 0 and filled in with the
# label for the others. Renderer, first one found, as in scripts/icons/gen.sh (the repo root):
# rsvg-convert, or ImageMagick (`magick` or `convert`) with its RSVG delegate.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
svg="$here/tray-icon.svg"
out="$here/../assets/tray"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$out"

render() { # render <svg> <size> <png>
  local src=$1 size=$2 png=$3
  if command -v rsvg-convert >/dev/null 2>&1; then
    rsvg-convert -w "$size" -h "$size" "$src" -o "$png"
  elif command -v magick >/dev/null 2>&1; then
    magick -background none -density 1200 "$src" -resize "${size}x${size}" "PNG32:$png"
  elif command -v convert >/dev/null 2>&1; then
    convert -background none -density 1200 "$src" -resize "${size}x${size}" "PNG32:$png"
  else
    echo "gen-tray-icons.sh: need rsvg-convert or ImageMagick with RSVG" >&2
    exit 1
  fi
}

# variant <key> <label> <font size> <baseline y>; an empty label means no badge
variant() {
  local key=$1 label=$2 fsize=$3 ly=$4 src="$tmp/tray-$1.svg"
  if [ -z "$label" ]; then
    sed '/<!--BADGE-->/,/<!--\/BADGE-->/d' "$svg" >"$src"
  else
    sed -e "s/@LABEL_SIZE@/$fsize/" -e "s/@LABEL_Y@/$ly/" -e "s/@LABEL@/$label/" "$svg" >"$src"
  fi
  render "$src" 22 "$out/tray-$key.png"
  render "$src" 44 "$out/tray-$key@2x.png"
}

variant 0 "" 0 0
for n in 1 2 3 4 5 6 7 8 9; do variant "$n" "$n" 9 9.8; done
variant 9plus "9+" 7 9.2
ls -l "$out"
