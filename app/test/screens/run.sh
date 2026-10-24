#!/bin/bash
# Runs the screenshot audit (screens.mjs) under xvfb on a private session bus.
#   app/test/screens/run.sh <out dir> [phase ...]
# WY_BIN defaults to the smoke build (make app-smoke builds it); temp files go to
# ~/.cache/wy-audit/tmp so a small /tmp quota is not filled.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
app="$(cd "$here/../.." && pwd)"
out="${1:?usage: run.sh <out dir> [phase ...]}"
shift
export WY_BIN="${WY_BIN:-$app/node_modules/.cache/wy-smoke/wy}"
export TMPDIR="${TMPDIR_AUDIT:-$HOME/.cache/wy-audit/tmp}"
mkdir -p "$TMPDIR" "$out"
cd "$app"
exec dbus-run-session -- env -u WAYLAND_DISPLAY -u XDG_SESSION_TYPE \
  xvfb-run -a -s "-screen 0 1440x900x24" node test/screens/screens.mjs "$out" "$@"
