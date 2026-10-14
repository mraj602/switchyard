# Shared helpers for the fixture home builders. Source it; do not run it.
#   . "$(dirname "$0")/_lib.sh"; init_home "$1"
# Every builder takes one argument: the HOME folder to fill (created if missing). Builders are
# POSIX sh, use no Go and no network, and fix every mtime so snapshots are reproducible.
# Fake logins: a folder is "logged in" when its .claude.json has an oauthAccount and a
# .credentials.json exists (mode 0600, fake content). Adjust login_folder if the fake claude
# (test/e2e/setup/fakeclaude) expects another shape.
set -eu

FIXTURE_MTIME=202601020304.05

init_home() {
	[ $# -eq 1 ] && [ -n "$1" ] || { echo "usage: $0 <home>" >&2; exit 2; }
	mkdir -p "$1"
	H=$(cd "$1" && pwd)
	export H
}

# mkd <path> [mode]
mkd() { mkdir -p "$1"; chmod "${2:-0755}" "$1"; }

# put <path> [mode]: write stdin to the file (parents created).
put() {
	mkdir -p "$(dirname "$1")"
	cat > "$1"
	chmod "${2:-0644}" "$1"
}

# login_folder <config folder> <state file> <email> <org> <org type> [mcpServers json]
# The state file is ~/.claude.json for ~/.claude and <folder>/.claude.json for the others.
login_folder() {
	mkd "$1" 0700
	# not ${6:-{\}}: macOS /bin/sh (bash 3.2) keeps the backslash and writes invalid JSON
	mcp=${6-}
	[ -n "$mcp" ] || mcp='{}'
	uuid=$(printf '%012d' "$(printf '%s' "$3" | cksum | cut -d' ' -f1)")
	put "$2" 0600 <<JSON
{
  "numStartups": 12,
  "installMethod": "native",
  "oauthAccount": {
    "emailAddress": "$3",
    "organizationName": "$4",
    "organizationType": "$5",
    "accountUuid": "00000000-0000-4000-8000-$uuid"
  },
  "mcpServers": $mcp,
  "projects": {}
}
JSON
	put "$1/.credentials.json" 0600 <<JSON
{"claudeAiOauth": {"accessToken": "fake-access-token-$3", "refreshToken": "fake-refresh-token-$3", "expiresAt": 4102444800000}}
JSON
}

# primary_content: what a lived-in ~/.claude holds (no plugins folder; add it per fixture).
primary_content() {
	P=$H/.claude
	mkd "$P" 0700
	put "$P/CLAUDE.md" <<'MD'
# Instructions
Be concise.
MD
	put "$P/commands/hello.md" <<'MD'
Say hello.
MD
	put "$P/skills/alpha/SKILL.md" <<'MD'
---
name: alpha
description: Alpha test skill.
---
Alpha.
MD
	put "$P/skills/beta/SKILL.md" <<'MD'
---
name: beta
description: Beta test skill.
---
Beta.
MD
	put "$P/projects/-home-me-proj/s1.jsonl" <<'JSONL'
{"type":"user","sessionId":"s1","message":"hi"}
JSONL
	put "$P/history.jsonl" <<'JSONL'
{"display":"first prompt","timestamp":1767324245000,"project":"/home/me/proj"}
JSONL
	put "$P/plans/p1.md" <<'MD'
# Plan one
MD
}

fix_mtimes() {
	find "$H" ! -type l -exec touch -t "$FIXTURE_MTIME" {} + 2>/dev/null || true
}
