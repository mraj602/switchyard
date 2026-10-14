#!/usr/bin/env bash
# Grep testdata/ for anything that must not be there. Prints file names and counts only, never the match.
# Patterns = fixed token words + the private words in scripts/fixtures/private-words.txt (gitignored,
# one per line: your handle, mail domain, company/area names) + the real identities found in this machine's ~/.claude*/.claude.json (oauthAccount) + hostname.
set -u
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"; PAT="$(mktemp)"; trap 'rm -f "$PAT"' EXIT
printf '%s\n' sk-ant gho_ ghp_ github_pat_ sessionKey 'Bearer ey' >"$PAT"
WORDS="$ROOT/scripts/fixtures/private-words.txt"
[ -f "$WORDS" ] && grep -v -e '^#' -e '^$' "$WORDS" >>"$PAT"
python3 -I - >>"$PAT" <<'PY'
import glob, json, os, socket
h = os.path.expanduser("~")
for f in [h + "/.claude.json"] + glob.glob(h + "/.claude-[0-9]*/.claude.json"):
    try: o = json.load(open(f)).get("oauthAccount") or {}
    except Exception: continue
    for k in ("emailAddress", "accountUuid", "organizationUuid", "displayName", "fullName", "organizationName"):
        if isinstance(o.get(k), str) and len(o[k]) >= 4: print(o[k])
print(socket.gethostname())
PY
cd "$ROOT" || exit 1
echo "patterns: $(wc -l <"$PAT")  (word list + real identities; matches are never printed)"
hits="$(grep -rIiF -c -f "$PAT" testdata --exclude=README.md | grep -v ':0$' || true)"
if [ -n "$hits" ]; then echo "FAIL, files with hits:"; echo "$hits"; exit 1; fi
echo "OK: zero hits for all patterns in testdata/ (README.md excluded: it names the words it checks)"
