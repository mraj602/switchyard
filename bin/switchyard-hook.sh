#!/usr/bin/env bash
# Forwards a Claude Code hook event (JSON on stdin) to Switchyard's local receiver.
# Installed once as ~/.claude/switchyard-hook.sh by bin/connect.sh and shared by every account.
#
# The account is resolved at run time from CLAUDE_CONFIG_DIR (the login email in that dir's
# .claude.json), so ~/.claude-N/settings.json can stay a symlink to one shared settings.json.
# Token and port come from Switchyard's own config.json, so neither lives in settings.json.
# Always exits 0 and never blocks a session if Switchyard isn't running.

data="${SWITCHYARD_DATA:-$HOME/.config/switchyard}"
cfg="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
state="$cfg/.claude.json"
[ "$(cd "$cfg" 2>/dev/null && pwd -P)" = "$(cd "$HOME/.claude" 2>/dev/null && pwd -P)" ] && state="$HOME/.claude.json"

token="$(jq -r '.hookToken // empty' "$data/config.json" 2>/dev/null)"
[ -n "$token" ] || exit 0
port="${SWITCHYARD_PORT:-$(jq -r '.hookPort // empty' "$data/config.json" 2>/dev/null)}"
case "$port" in ''|*[!0-9]*) port=47821 ;; esac
email="$(jq -r '.oauthAccount.emailAddress // empty' "$state" 2>/dev/null)"

uri() { jq -rn --arg v "$1" '$v|@uri'; }
q="account=$(uri "${CLAUDE_ACCOUNT:-$(basename "$cfg")}")"
[ -n "$email" ] && q="$q&email=$(uri "$email")"
[ -n "${CLAUDE_AREA:-}" ] && q="$q&area=$(uri "$CLAUDE_AREA")"

curl -s -m 2 -o /dev/null -X POST "http://127.0.0.1:$port/hook?$q" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' --data-binary @- || true
exit 0
