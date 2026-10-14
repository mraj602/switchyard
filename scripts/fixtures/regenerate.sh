#!/usr/bin/env bash
# Rebuild testdata/ from this machine's real data. Redacted output only ever lands in testdata/.
# usage: scripts/fixtures/regenerate.sh [capdir]   (capdir = dir holding cap1..8.jsonl from capture-runs.sh;
#        default: runs capture-runs.sh into a temp dir, which spends a little real Claude usage)
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"; ROOT="$(cd "$HERE/../.." && pwd)"
CLAUDE="${CLAUDE_BIN:-$HOME/.local/bin/claude}"
STAGE="$(mktemp -d)"; trap 'rm -rf "$STAGE"' EXIT
CAP="${1:-}"; if [ -z "$CAP" ]; then CAP="$STAGE/cap"; mkdir -p "$CAP"; "$HERE/capture-runs.sh" "$CAP"; fi

# 1. auth: `claude auth status --json` per config folder + ONLY the oauthAccount key of its .claude.json
mkdir -p "$STAGE/raw/auth"
for i in "" -2 -3 -4 -5 -6 -7; do
  n=${i#-}; n=${n:-1}; dir="$HOME/.claude$i"; cj="$dir/.claude.json"; [ -z "$i" ] && cj="$HOME/.claude.json"
  # slot 1 must run WITHOUT CLAUDE_CONFIG_DIR: with it set to ~/.claude, claude looks for ~/.claude/.claude.json
  if [ -z "$i" ]; then env -u CLAUDE_CONFIG_DIR "$CLAUDE" auth status --json </dev/null >"$STAGE/raw/auth/status-$n.json" 2>/dev/null || true
  else CLAUDE_CONFIG_DIR="$dir" "$CLAUDE" auth status --json </dev/null >"$STAGE/raw/auth/status-$n.json" 2>/dev/null || true; fi
  python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); o=d.get("oauthAccount"); print(json.dumps({"oauthAccount":o},indent=2)) if o else sys.exit(1)' "$cj" >"$STAGE/raw/auth/oauth-$n.json" 2>/dev/null || rm -f "$STAGE/raw/auth/oauth-$n.json"
done

# 2. prototype db (copy first; the desktop app keeps writing the original)
mkdir -p "$STAGE/db"; cp ~/.config/switchyard/usage.db* "$STAGE/db/"
python3 -I "$HERE/extract-prototype.py" "$STAGE/db/usage.db" "$STAGE/raw"

# 3. hooks + transcripts from the capture runs
python3 -I "$HERE/collect-hooks.py" "$STAGE/raw" 01-write-and-bash="$CAP/cap1.jsonl" 02-permission-needed="$CAP/cap2.jsonl" \
  03-question="$CAP/cap3.jsonl" 04-subagent="$CAP/cap4.jsonl" 05-tool-failure="$CAP/cap5.jsonl" \
  06-stop-failure="$CAP/cap6.jsonl" 07-compact="$CAP/cap7.jsonl" 08-plain-with-env="$CAP/cap8.jsonl" >/dev/null
mkdir -p "$STAGE/raw/transcripts"
for pair in 01-write-and-bash:1 03-question:3 07-compact:7; do
  tp="$(jq -r 'select(.event=="SessionStart")|.payload.transcript_path' "$CAP/cap${pair##*:}.jsonl" | head -1)"
  cp "$tp" "$STAGE/raw/transcripts/${pair%%:*}.jsonl"
done

# 4. redact into testdata/ (keeps hand-written README.md and the synthetic statusline files)
rm -rf "$STAGE/out"; python3 -I "$HERE/redact.py" "$STAGE/raw" "$STAGE/out"
for d in auth hooks usage prototype transcripts; do rm -rf "$ROOT/testdata/$d"; cp -r "$STAGE/out/$d" "$ROOT/testdata/$d"; done
python3 -I "$HERE/make-statusline.py" "$ROOT/testdata/statusline"
"$HERE/check-redaction.sh"
