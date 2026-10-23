#!/usr/bin/env bash
# DCO check (CONTRIBUTING.md "Developer Certificate of Origin"): every non-merge commit in
# BASE..HEAD carries a Signed-off-by trailer naming the commit author's email address.
# Commits by GitHub bots (Dependabot) are exempt: they cannot sign off.
#
#   scripts/ci/check-dco.sh <base> <head>
set -euo pipefail

base=${1:?usage: check-dco.sh <base> <head>}
head=${2:?usage: check-dco.sh <base> <head>}

bad=0
checked=0
while IFS= read -r sha; do
  email=$(git log -1 --format=%ae "$sha")
  subject=$(git log -1 --format=%s "$sha")
  case $email in *'[bot]@users.noreply.github.com') continue ;; esac
  checked=$((checked + 1))
  signoffs=$(git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$sha")
  if ! grep -qiF "<$email>" <<<"$signoffs"; then
    echo "::error::${sha:0:12} \"$subject\" has no Signed-off-by line for its author <$email>"
    bad=$((bad + 1))
  fi
done < <(git rev-list --no-merges "$base..$head")

if [ "$bad" -gt 0 ]; then
  echo
  echo "$bad commit(s) are not signed off. Sign them off and force-push the branch:"
  echo "  git rebase --signoff $base"
  echo "  git push --force-with-lease"
  echo "See CONTRIBUTING.md, \"Developer Certificate of Origin\"."
  exit 1
fi
echo "dco: $checked commit(s) signed off"
