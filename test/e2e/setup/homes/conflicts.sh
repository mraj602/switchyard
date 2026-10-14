#!/bin/sh
# S3: a fresh primary plus ~/.claude-2 and ~/.claude-3 (logged in, nothing linked) whose
# content differs from the primary:
#   ~/.claude-3/CLAUDE.md             differs
#   ~/.claude-2/skills/               alpha same, beta differs, gamma only here
#   ~/.claude-2/history.jsonl         differs (an extra line)
. "$(dirname "$0")/_lib.sh"
init_home "$1"
sh "$(dirname "$0")/fresh.sh" "$H"
login_folder "$H/.claude-2" "$H/.claude-2/.claude.json" b@example.com "Example Org" claude_max
login_folder "$H/.claude-3" "$H/.claude-3/.claude.json" c@example.com "Team Org" claude_team
put "$H/.claude-3/CLAUDE.md" <<'MD'
# Instructions
Be verbose.
MD
put "$H/.claude-2/skills/alpha/SKILL.md" <<'MD'
---
name: alpha
description: Alpha test skill.
---
Alpha.
MD
put "$H/.claude-2/skills/beta/SKILL.md" <<'MD'
---
name: beta
description: Beta test skill, changed in claude-2.
---
Beta two.
MD
put "$H/.claude-2/skills/gamma/SKILL.md" <<'MD'
---
name: gamma
description: Gamma only in claude-2.
---
Gamma.
MD
put "$H/.claude-2/history.jsonl" <<'JSONL'
{"display":"first prompt","timestamp":1767324245000,"project":"/home/me/proj"}
{"display":"second prompt from two","timestamp":1767324300000,"project":"/home/me/other"}
JSONL
fix_mtimes
