#!/usr/bin/env bash
# Run Claude Code headless a handful of times with capture-hook.sh registered for every hook event,
# via a per-invocation --settings file and --setting-sources local (no real settings file is edited,
# and the user's own hooks do not fire). Costs a few tiny turns of real usage.
# usage: capture-runs.sh <work dir>      -> <work dir>/cap1.jsonl ... cap8.jsonl
set -u
W="${1:?work dir}"; HERE="$(cd "$(dirname "$0")" && pwd)"
CLAUDE="${CLAUDE_BIN:-$HOME/.local/bin/claude}"   # the real binary, not a shell wrapper
mkdir -p "$W/proj1"
python3 -I - "$W/settings.json" "$HERE/capture-hook.sh" <<'PY'
import json, sys
evs = "SessionStart UserPromptSubmit UserPromptExpansion PreToolUse PostToolUse PostToolUseFailure Notification PermissionRequest PermissionDenied Stop StopFailure SubagentStart SubagentStop PreCompact PostCompact SessionEnd TaskCreated TaskCompleted Setup".split()
tool = {"PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest", "PermissionDenied"}
h = {}
for e in evs:
    g = {"hooks": [{"type": "command", "command": f"{sys.argv[2]} {e}"}]}
    if e in tool: g["matcher"] = "*"
    h[e] = [g]
json.dump({"hooks": h}, open(sys.argv[1], "w"), indent=1)
PY
cd "$W/proj1"
run() { n=$1; shift; export WY_CAPTURE_FILE="$W/cap$n.jsonl"; rm -f "$WY_CAPTURE_FILE"
  timeout 300 "$CLAUDE" -p "$@" --settings "$W/settings.json" --setting-sources local </dev/null >/dev/null 2>&1; }
run 1 "Create a file hello.txt containing hi, then run 'cat hello.txt' with Bash, then reply DONE" --permission-mode acceptEdits --allowedTools "Write,Bash(cat:*)" --max-turns 6
run 2 "Run 'echo hi > out.txt' using the Bash tool, then reply DONE" --max-turns 4          # not allowed -> PermissionRequest, auto-denied in -p
run 3 "Ask me which colour I prefer (red, green or blue) and stop. Do not use tools." --max-turns 3
run 4 "Use the Agent tool with a general-purpose subagent to reply with the word OK, then reply DONE" --max-turns 5 --allowedTools Agent
run 5 "Run 'cat nonexistent.txt' with Bash, then reply DONE" --allowedTools "Bash(cat:*)" --max-turns 4
run 6 "hi" --model claude-bogus-9 --max-turns 2                                              # -> StopFailure
run 7 "/compact" --max-turns 2                                                               # -> PreCompact
run 8 "Reply DONE" --max-turns 2                                                             # env sample (needs the capture-hook.sh env allowlist)
