#!/usr/bin/env bash
# CI guard (docs/phases/6.md "CI job design"): the root Electron prototype stays live until the
# person retires it, so no commit on this branch may change it. Lists
# `git diff --name-only <base>...HEAD` and fails on any prototype path.
#
#   app/scripts/check-prototype-untouched.sh [<base>]     (default base: origin/main)
#
# Skipped when the head commit's message carries [prototype-retire], which is reserved for the
# person's own manual retirement commit.
set -euo pipefail

base="${1:-origin/main}"
[ -n "$base" ] || base=origin/main

if git log -1 --format=%B HEAD | grep -qF '[prototype-retire]'; then
  echo "check-prototype-untouched: [prototype-retire] in the head commit, skipped"
  exit 0
fi

if ! git rev-parse --verify --quiet "${base}^{commit}" >/dev/null; then
  echo "check-prototype-untouched: base ${base} not found (fetch with fetch-depth: 0)" >&2
  exit 2
fi

# Prototype paths: directories end in /, everything else is an exact file at the repo root.
protected_dirs='^(core|server|shell|web|bin|assets)/'
protected_files='^(main\.js|preload\.js|package\.json|package-lock\.json|test/e2e\.js|test/server\.js|test/preview\.js|test/web-preview\.js|Dockerfile|docker-compose\.yml)$'

changed="$(git diff --name-only "${base}...HEAD")"
bad="$(printf '%s\n' "$changed" | grep -E "${protected_dirs}|${protected_files}" || true)"

if [ -n "$bad" ]; then
  echo "check-prototype-untouched: these prototype files changed since ${base}:" >&2
  printf '  %s\n' $bad >&2
  echo "The prototype stays as it is until it is retired (docs/phases/6.md)." >&2
  exit 1
fi
echo "check-prototype-untouched: no prototype file changed since ${base}"
