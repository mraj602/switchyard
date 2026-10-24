#!/bin/sh
# S5: three logged-in folders with different mcpServers. Server values hold fake secrets
# (homes/mcp.secrets, one per line): no secret may ever appear in wy output or logs.
#   ~/.claude    only-primary, shared-name (primary version)
#   ~/.claude-2  only-two,     shared-name (different command: a same-name conflict)
#   ~/.claude-3  only-three
. "$(dirname "$0")/_lib.sh"
init_home "$1"
here=$(cd "$(dirname "$0")" && pwd)
s1=$(sed -n 1p "$here/mcp.secrets")
s2=$(sed -n 2p "$here/mcp.secrets")
s3=$(sed -n 3p "$here/mcp.secrets")
s4=$(sed -n 4p "$here/mcp.secrets")
s5=$(sed -n 5p "$here/mcp.secrets")
primary_content
put "$H/.claude/settings.json" <<'JSON'
{}
JSON
login_folder "$H/.claude" "$H/.claude.json" a@example.com "Example Org" claude_max \
	"{\"only-primary\": {\"type\": \"stdio\", \"command\": \"npx\", \"args\": [\"p-server\"], \"env\": {\"API_KEY\": \"$s1\"}}, \"shared-name\": {\"type\": \"stdio\", \"command\": \"primary-cmd\", \"env\": {\"API_KEY\": \"$s4\"}}}"
login_folder "$H/.claude-2" "$H/.claude-2/.claude.json" b@example.com "Example Org" claude_max \
	"{\"only-two\": {\"type\": \"http\", \"url\": \"https://two.example.com/mcp\", \"headers\": {\"Authorization\": \"Bearer $s2\"}}, \"shared-name\": {\"type\": \"stdio\", \"command\": \"two-cmd\", \"env\": {\"API_KEY\": \"$s5\"}}}"
login_folder "$H/.claude-3" "$H/.claude-3/.claude.json" c@example.com "Team Org" claude_team \
	"{\"only-three\": {\"type\": \"stdio\", \"command\": \"three\", \"env\": {\"TOKEN\": \"$s3\"}}}"
mkd "$H/.local/bin" 0755
fix_mtimes
