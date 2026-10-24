#!/bin/sh
# S3 conflicts: ~/.claude-3/CLAUDE.md differs, ~/.claude-2/skills/ has one extra and one
# differing skill, ~/.claude-2/history.jsonl differs. Defaults keep everything separate;
# --resolve merges what can be merged; two undos restore the tree.
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
[ -x "$TOOLS/claude" ] || skip_scenario "no fake claude in $TOOLS"

H=$(new_home conflicts conflicts)
install_sy "$H"
P=$H/.claude
snap "$H" "$WY_E2E_TMP/before.json" -claude-json
cp "$P/CLAUDE.md" "$WY_E2E_TMP/primary.CLAUDE.md"
cp "$H/.claude-3/CLAUDE.md" "$WY_E2E_TMP/three.CLAUDE.md"
cp "$H/.claude-2/skills/beta/SKILL.md" "$WY_E2E_TMP/two.beta.md"

# 1. Defaults (--conflicts keep): the three conflicts are reported, nothing of them linked.
wy setup --yes --hub later
assert_rc "defaults" 0 $rc
bang=$(grep -c '^  ! ' "$OUT" || true)
assert_eq "three ! lines" 3 "$bang"
assert_contains "! CLAUDE.md" "! ~/.claude-3 CLAUDE.md" "$(cat "$OUT")"
assert_contains "! skills" "! ~/.claude-2 skills/" "$(cat "$OUT")"
assert_contains "! history.jsonl" "! ~/.claude-2 history.jsonl" "$(cat "$OUT")"
for x in .claude-3/CLAUDE.md .claude-2/skills .claude-2/history.jsonl; do
	if [ -L "$H/$x" ]; then fail "$x was linked under the default"; else pass "$x left separate"; fi
done
assert_eq "~/.claude-3/CLAUDE.md unchanged" "$(sha "$WY_E2E_TMP/three.CLAUDE.md")" "$(sha "$H/.claude-3/CLAUDE.md")"
assert_eq "~/.claude-3 skills linked (no conflict there)" "$P/skills" "$(readlink "$H/.claude-3/skills")"
assert_eq "~/.claude-2 CLAUDE.md linked (no conflict there)" "$P/CLAUDE.md" "$(readlink "$H/.claude-2/CLAUDE.md")"

# 2. Resolve.
wy setup --yes --hub later --json --resolve '~/.claude-3:CLAUDE.md=primary,~/.claude-2:skills=merge,~/.claude-2:history.jsonl=merge'
assert_rc "resolve" 0 $rc
assert_eq "~/.claude-3/CLAUDE.md is a link to the primary" "$P/CLAUDE.md" "$(readlink "$H/.claude-3/CLAUDE.md")"
assert_eq "the primary CLAUDE.md is unchanged" "$(sha "$WY_E2E_TMP/primary.CLAUDE.md")" "$(sha "$P/CLAUDE.md")"
assert_eq "~/.claude-2/history.jsonl is a link to the primary" "$P/history.jsonl" "$(readlink "$H/.claude-2/history.jsonl")"
assert_eq "history.jsonl: the union of both files" 2 "$(wc -l < "$P/history.jsonl" | tr -d ' ')"
assert_contains "history.jsonl has the line from ~/.claude-2" "second prompt from two" "$(cat "$P/history.jsonl")"
assert_eq "history.jsonl ordered by timestamp" "first prompt" "$(sed -n 1p "$P/history.jsonl" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["display"])')"
# skills: gamma moved to the primary; beta still differs so
# the folder is not linked and keeps its own beta.
if [ -L "$H/.claude-2/skills" ]; then fail "~/.claude-2/skills was linked although beta differs"; else pass "~/.claude-2/skills not linked (beta differs)"; fi
assert_file "$P/skills/gamma/SKILL.md" "gamma moved to the primary"
assert_no_file "$H/.claude-2/skills/gamma" "gamma gone from ~/.claude-2"
assert_eq "~/.claude-2 keeps its own beta" "$(sha "$WY_E2E_TMP/two.beta.md")" "$(sha "$H/.claude-2/skills/beta/SKILL.md")"
assert_contains "the primary's beta is its own" "Beta test skill." "$(cat "$P/skills/beta/SKILL.md")"
assert_contains "report lists the entry that still differs" "beta" "$(cat "$OUT" "$ERR")"

# 3. Undo both sets: the tree is as at the start.
wy setup --undo --yes
assert_rc "undo (resolve set)" 0 $rc
wy setup --undo --yes
assert_rc "undo (defaults set)" 0 $rc
snap_equal "S3 after undo" "$H" "$WY_E2E_TMP/before.json" -claude-json

finish
