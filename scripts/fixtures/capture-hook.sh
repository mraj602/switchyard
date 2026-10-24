#!/usr/bin/env bash
# Capture a Claude Code hook payload. Usage: capture-hook.sh <EventName>
# Appends one JSON line {event, env{...}, payload} to $WY_CAPTURE_FILE (default /tmp/sy-capture.jsonl).
# Always exits 0 and prints nothing, so it never affects the session.
out="${WY_CAPTURE_FILE:-/tmp/sy-capture.jsonl}"
ev="${1:-unknown}"
payload="$(cat)"
envjson="$(jq -n 'env|with_entries(select(.key|test("^(CLAUDE_CONFIG_DIR|CLAUDE_PROJECT_DIR|CLAUDE_CODE_ENTRYPOINT|CLAUDE_ACCOUNT|CLAUDE_AREA|TERM_PROGRAM|TERM_PROGRAM_VERSION|WARP_[A-Z_]+)$")))' 2>/dev/null)"
[ -n "$envjson" ] || envjson='{}'
jq -cn --arg ev "$ev" --argjson env "$envjson" --arg raw "$payload" \
  '{event:$ev, env:$env, payload:($raw|try fromjson catch $raw)}' >> "$out" 2>/dev/null
exit 0
