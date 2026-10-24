#!/bin/sh
# The e2e build (docs/spec/desktop.md C11): the Linux AppImage only, in
# dist/e2e/, with `watchyard.allowCdp: true` baked into the asar's package.json, so the e2e
# test may drive it over --remote-debugging-port (src/main/cdp.js, desktop.md C2). Release
# builds (npm run dist, CI's package job) never pass this flag, and their app exits 1 when
# started with a remote debugging switch. Never signed, never published.
#
# Run from app/ after `node scripts/fetch-wy.mjs auto auto` (the bundled wy).
set -eu
cd "$(dirname "$0")/.."
export CSC_IDENTITY_AUTO_DISCOVERY=false
exec npx --no-install electron-builder --linux AppImage --publish never \
  -c.directories.output=dist/e2e \
  -c.extraMetadata.watchyard.allowCdp=true
