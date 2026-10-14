#!/bin/sh
# S1: a new machine. Only ~/.claude exists, logged in, with a little content, no settings
# links, no plugins, no launchers, no status line.
. "$(dirname "$0")/_lib.sh"
init_home "$1"
primary_content
put "$H/.claude/settings.json" <<'JSON'
{
  "theme": "dark"
}
JSON
login_folder "$H/.claude" "$H/.claude.json" a@example.com "Example Org" claude_max
mkd "$H/.local/bin" 0755
fix_mtimes
