#!/usr/bin/env bash
# check-release-seams.sh: a release build of sy contains none of the test seams.
#
# Builds sy the way `make build` / scripts/release/build.sh do (CGO_ENABLED=0, -trimpath,
# -ldflags '-s -w'), then checks that the strings of the `fakeclock` and `fakeopen` build tags
# are absent. A positive control builds with `-tags fakeclock,fakeopen` and requires the same
# strings to be present, so the check cannot pass just because the strings stopped existing.
# It only builds; it never runs sy.
set -euo pipefail

GO=${GO:-go}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

# Strings owned by the test-seam build tags (fakeclock: internal/hub/cli/hooks_fakeclock.go;
# fakeopen: internal/termctl/fakeopen.go).
markers=(SY_FAKE_CLOCK SY_TEST_WEBPUSH_DIAL SY_TEST_EXPO_DIAL SY_TEST_OPENER)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 1. The release script and the Makefile's build must not pass build tags.
if grep -nE -- '(^|[^a-z])-tags|GOFLAGS' scripts/release/build.sh; then
  echo "release-check: FAIL: scripts/release/build.sh passes build tags or GOFLAGS" >&2
  exit 1
fi
if grep -nE -- 'build .*-tags' Makefile | grep -v '^[0-9]*:#'; then
  echo "release-check: FAIL: a Makefile build passes build tags" >&2
  exit 1
fi

build() { # build <out> [tags]
  local out=$1 tags=${2:-}
  if [ -n "$tags" ]; then
    CGO_ENABLED=0 "$GO" build -trimpath -tags "$tags" -ldflags '-s -w' -o "$out" ./cmd/sy
  else
    CGO_ENABLED=0 "$GO" build -trimpath -ldflags '-s -w' -o "$out" ./cmd/sy
  fi
}

has() { grep -aqF -- "$2" "$1"; }

echo "release-check: building the test-seam binary (positive control)"
build "$tmp/sy-seams" fakeclock,fakeopen
for m in "${markers[@]}"; do
  if ! has "$tmp/sy-seams" "$m"; then
    echo "release-check: FAIL: the control build lacks '$m'; the check would prove nothing" >&2
    exit 1
  fi
done

echo "release-check: building the release binary"
build "$tmp/sy-release"
bad=0
for m in "${markers[@]}"; do
  if has "$tmp/sy-release" "$m"; then
    echo "release-check: FAIL: the release binary contains '$m'" >&2
    bad=1
  fi
done
[ "$bad" -eq 0 ] || exit 1
echo "release-check: ok: no fakeclock or fakeopen seams in a release build"
