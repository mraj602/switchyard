#!/bin/sh
# S4 failure and rollback: an injected plugin failure rolls everything back; a Claude that
# replaces the settings link with a file is caught by the link guard; a kill -9 mid-apply is
# undone by the next run, which then ends in the state of an uninterrupted run.
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
[ -x "$TOOLS/claude" ] || skip_scenario "no fake claude in $TOOLS"

ARGS="--yes --json --add work:amber,client --hub later"

# ---- a. FAKE_CLAUDE_FAIL=install ----
H=$(new_home fail fresh)
install_sy "$H"
snap "$H" "$SY_E2E_TMP/a.before.json" -claude-json
SY_E2E_ENV="FAKE_CLAUDE_FAIL=install"
# shellcheck disable=SC2086
sy setup $ARGS
assert_rc "plugin install fails" 1 $rc
SY_E2E_ENV=
snap_equal "after the failed install" "$H" "$SY_E2E_TMP/a.before.json" -claude-json
sy setup --undo-list --json
assert_eq "set is rolled-back" rolled-back "$(jget "$OUT" 'd["sets"][0]["status"]')"
assert_no_file "$H/.claude-2" "~/.claude-2 after the rollback"
assert_no_file "$H/.local/bin/claude-2" "launcher after the rollback"

# ---- b. FAKE_CLAUDE_BREAK_LINK=1: the settings link is replaced by a file ----
# The primary's settings.json is a relative link into a dotfiles folder; the fake replaces
# it by a regular file when it writes. The link guard must notice, restore the link with
# its raw target, and roll back.
H=$(new_home break fresh)
install_sy "$H"
mkdir -p "$H/dotfiles"
mv "$H/.claude/settings.json" "$H/dotfiles/settings.json"
ln -s ../dotfiles/settings.json "$H/.claude/settings.json"
snap "$H" "$SY_E2E_TMP/b.before.json" -claude-json
SY_E2E_ENV="FAKE_CLAUDE_BREAK_LINK=1"
sy setup --yes --json --hub later
assert_rc "broken link" 1 $rc
SY_E2E_ENV=
assert_contains "the error names the link guard and the file" "link guard: 1 links changed during the run (restored): $H/.claude/settings.json" "$(cat "$ERR")"
assert_eq "settings.json link restored with its raw target" "../dotfiles/settings.json" "$(readlink "$H/.claude/settings.json")"
snap_equal "after the broken link" "$H" "$SY_E2E_TMP/b.before.json" -claude-json
sy setup --undo-list --json
assert_eq "set is rolled-back" rolled-back "$(jget "$OUT" 'd["sets"][0]["status"]')"

# ---- c. kill -9 mid-apply ----
# uninterrupted reference first, same home path, so the two final states can be compared.
H=$(new_home kill fresh)
install_sy "$H"
snap "$H" "$SY_E2E_TMP/c.before.json" -claude-json
# shellcheck disable=SC2086
sy setup $ARGS
assert_rc "uninterrupted run" 0 $rc
snap "$H" "$SY_E2E_TMP/c.ref.json" -claude-json -no-mtime -ignore .local/state,.local/bin/sy,.claude/plugins,.claude/settings.json
cp "$H/.claude/settings.json" "$SY_E2E_TMP/c.ref.settings.json"
rm -rf "$SY_E2E_TMP/kill"

H=$(new_home kill fresh)
install_sy "$H"
: > "$FAKE_LOG"
mkdir -p "$SY_E2E_TMP/tmp"
SY_E2E_ENV="FAKE_CLAUDE_SLOW=3000"
# shellcheck disable=SC2086
sy_bg "$SY_E2E_TMP/sy.pid" "$SY_E2E_TMP/killed.out" setup $ARGS
SY_E2E_ENV=
i=0
until grep -q '"marketplace","add"' "$FAKE_LOG" 2>/dev/null; do
	i=$((i + 1)); [ "$i" -lt 100 ] || break
	sleep 0.1
done
kill -9 "$(cat "$SY_E2E_TMP/sy.pid")" 2>/dev/null || true
wait "$(cat "$SY_E2E_TMP/sy.pid")" 2>/dev/null || true
pass "sy killed while the fake claude was running"
# The orphaned fake finishes its sleep and its write; wait for it.
sleep 5
sy setup --undo-list --json
assert_eq "set left in-progress" in-progress "$(jget "$OUT" 'd["sets"][0]["status"]')"

# shellcheck disable=SC2086
sy setup $ARGS
assert_rc "next run" 0 $rc
assert_contains "says it undid the interrupted set" "Undid the interrupted run" "$(cat "$ERR" "$OUT")"
assert_eq "applied" applied "$(jget "$OUT" 'd["mode"]')"
sy setup --undo-list --json
assert_eq "two sets" 2 "$(jget "$OUT" 'len(d["sets"])')"
assert_eq "the interrupted set was undone or rolled back" 1 "$(jget "$OUT" 'len([s for s in d["sets"] if s["status"] in ("undone", "rolled-back")])')"
snap_equal "final state equals an uninterrupted run" "$H" "$SY_E2E_TMP/c.ref.json" -claude-json -no-mtime -ignore .local/state,.local/bin/sy,.claude/plugins,.claude/settings.json
assert_eq "settings.json equals the uninterrupted run's" "$(sha "$SY_E2E_TMP/c.ref.settings.json")" "$(sha "$H/.claude/settings.json")"
assert_eq "plugin enabled once" 1 "$(grep -c 'switchyard@sy-local' "$H/.claude/settings.json")"
assert_eq "plugin recorded" 1 "$(grep -c 'switchyard@sy-local' "$H/.claude/plugins/installed_plugins.json")"

finish
