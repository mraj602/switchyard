#!/usr/bin/env bash
# Rebuild testdata/agents/ from this machine: the output of `claude agents --json` per config folder and the
# sessions/*.json files (never the *.key files next to them). Read-only; redacted output only lands in testdata/.
# usage: scripts/fixtures/collect-agents.sh
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"; ROOT="$(cd "$HERE/../.." && pwd)"
CLAUDE="${CLAUDE_BIN:-$HOME/.local/bin/claude}"
STAGE="$(mktemp -d)"; trap 'rm -rf "$STAGE"' EXIT
RAW="$STAGE/raw/agents"; mkdir -p "$RAW/sessions-dir"

# ~/.claude is special: run it with CLAUDE_CONFIG_DIR unset (-default) and set to itself (-explicit); the two differ.
env -u CLAUDE_CONFIG_DIR "$CLAUDE" agents --json </dev/null >"$RAW/claude-agents-1-default.json"
CLAUDE_CONFIG_DIR="$HOME/.claude" "$CLAUDE" agents --json </dev/null >"$RAW/claude-agents-1-explicit.json"
for n in 2 3; do
  CLAUDE_CONFIG_DIR="$HOME/.claude-$n" "$CLAUDE" agents --json </dev/null >"$RAW/claude-agents-$n.json"
done
# the fallback files: *.json only, so a *.key is never opened
for f in "$HOME"/.claude-2/sessions/*.json; do [ -e "$f" ] && cp "$f" "$RAW/sessions-dir/$(basename "$f")"; done

# free-text names and real session ids become fakes (stable across files); pid and pidDomain hash too
python3 -I - "$RAW" <<'PY'
import glob, json, os, sys
raw = sys.argv[1]; ids = {}; n = [0]
def fake(v):
    if v not in ids:
        n[0] += 1; ids[v] = "00000000-0000-4000-8000-%012d" % n[0]
    return ids[v]
def walk(o, name_n):
    if isinstance(o, list):
        for i, x in enumerate(o): walk(x, name_n)
    elif isinstance(o, dict):
        if "sessionId" in o: o["sessionId"] = fake(o["sessionId"])
        if "id" in o and isinstance(o["id"], str): o["id"] = "bg%06d" % int(fake(o["id"])[-12:])
        if "name" in o: o["name"] = "session %d" % (name_n.setdefault(o["name"], len(name_n) + 1))
        if "pidDomain" in o: o["pidDomain"] = "linux:" + "0" * 32 + ":pid:[4026531836]"
files = sorted(glob.glob(raw + "/*.json") + glob.glob(raw + "/sessions-dir/*.json"))
names = {}
for f in files:
    d = json.load(open(f)); walk(d, names)
    json.dump(d, open(f, "w"), indent=2); open(f, "a").write("\n")
# sessions-dir files are named by pid: keep, they are numbers
PY

python3 -I "$HERE/redact.py" "$STAGE/raw" "$STAGE/out"
rm -rf "$ROOT/testdata/agents"; cp -r "$STAGE/out/agents" "$ROOT/testdata/agents"
"$HERE/check-redaction.sh"
