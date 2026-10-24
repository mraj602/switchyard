#!/bin/sh
# S1 fresh install and S1b (undo keeps a folder with a login).
# Home: only ~/.claude, logged in. install.sh from the local server, then
# wy setup --yes --json --add work:amber,client --hub later; undo puts every file back.
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
[ -x "$TOOLS/claude" ] || skip_scenario "no fake claude in $TOOLS"
GOLD=$WY_E2E_SRC/internal/setup/launcher/testdata/claude-2.sh.golden

serve main "$RELEASE"
VER=$(sed -n 's/^[0-9a-f]*  [a-z]*_\([^_]*\)_.*/\1/p' "$RELEASE/SHA256SUMS" | head -n 1)

# apply_s1 <name>: fresh home, install, snapshot, setup. Leaves $H and the snapshot in place.
apply_s1() {
	H=$(new_home "$1" fresh)
	snap "$H" "$WY_E2E_TMP/$1.before.json" -claude-json
	run_install "$H" --no-setup
	assert_rc "install.sh" 0 $rc
	: > "$FAKE_LOG"
	wy setup --yes --json --add work:amber,client --hub later
	assert_rc "wy setup" 0 $rc
	cp "$OUT" "$WY_E2E_TMP/$1.setup.json"
}

# ---- S1 ----
apply_s1 s1
J=$WY_E2E_TMP/s1.setup.json
assert_eq "mode applied" applied "$(jget "$J" 'd["mode"]')"

for f in .claude-2 .claude-3; do
	assert_eq "$f mode" 700 "$(mode_of "$H/$f")"
	for item in settings.json CLAUDE.md commands skills projects history.jsonl plans; do
		if [ -L "$H/$f/$item" ]; then
			assert_eq "$f/$item links to the primary" "$H/.claude/$item" "$(readlink "$H/$f/$item")"
		else
			fail "$f/$item is not a link"
		fi
	done
	for item in agents rules keybindings.json; do
		assert_no_file "$H/$f/$item" "$f/$item (the primary has none)"
	done
done

S=$H/.claude/settings.json
assert_eq "settings.json is still a regular file" regular "$([ -f "$S" ] && [ ! -L "$S" ] && echo regular)"
assert_eq "env.CLAUDE_CODE_PLUGIN_CACHE_DIR value" "$H/.claude/plugins" "$(jget "$S" 'd["env"]["CLAUDE_CODE_PLUGIN_CACHE_DIR"]')"
assert_eq "env key appears once" 1 "$(grep -c CLAUDE_CODE_PLUGIN_CACHE_DIR "$S")"
assert_eq "theme kept" dark "$(jget "$S" 'd["theme"]')"
assert_eq "status line has no wrap (there was none)" "$H/.local/bin/wy statusline" "$(jget "$S" 'd["statusLine"]["command"]')"
assert_eq "plugin enabled" true "$(jget "$S" 'str(d["enabledPlugins"]["watchyard@wy-local"]).lower()')"
assert_eq "marketplace wy-local registered" directory "$(jget "$S" 'd["extraKnownMarketplaces"]["wy-local"]["source"]["source"]')"

assert_eq "one marketplace add" 1 "$(fake_calls '"marketplace","add"')"
assert_eq "one plugin install" 1 "$(fake_calls '"plugin","install"')"
assert_eq "no enable (settings are shared)" 0 "$(fake_calls '"plugin","enable"')"
adds=$(grep -F '"marketplace","add"' "$FAKE_LOG" | grep -cF '"unset":true' || true)
inst=$(grep -F '"plugin","install"' "$FAKE_LOG" | grep -cF '"unset":true' || true)
assert_eq "marketplace add ran as ~/.claude (CLAUDE_CONFIG_DIR unset)" 1 "$adds"
assert_eq "install ran as ~/.claude (CLAUDE_CONFIG_DIR unset)" 1 "$inst"
assert_eq "nothing ran in another folder" 0 "$(grep -F '"plugin"' "$FAKE_LOG" | grep -cF '"unset":false' || true)"

sed 's/claude-2/claude-3/g; s/label=work/label=client/' "$GOLD" > "$WY_E2E_TMP/claude-3.golden"
cmp -s "$GOLD" "$H/.local/bin/claude-2" && pass "claude-2 launcher equals the golden" || fail "claude-2 launcher differs from the golden"
cmp -s "$WY_E2E_TMP/claude-3.golden" "$H/.local/bin/claude-3" && pass "claude-3 launcher equals the golden" || fail "claude-3 launcher differs from the golden"
assert_eq "claude-2 is executable" 755 "$(mode_of "$H/.local/bin/claude-2")"

assert_eq "needs: two logins" 2 "$(jget "$J" 'len([n for n in d["needs"] if n["kind"] == "login"])')"
assert_eq "needs: no login for the primary" 0 "$(jget "$J" 'len([n for n in d["needs"] if n["folder"] == "~/.claude"])')"
assert_eq "links_changed is 0" 0 "$(jget "$J" 'd["summary"]["links_changed"]')"
assert_eq "logins_touched is 0" 0 "$(jget "$J" 'd["summary"]["logins_touched"]')"
BAK=$(jget "$J" 'd["backup"]')

# A second run (without --add: that would add two more folders) changes nothing.
wy setup --yes --json --hub later
assert_rc "second run" 0 $rc
assert_eq "second run: no changes" 0 "$(jget "$OUT" 'd["summary"]["changes"]')"

wy setup --undo --yes
assert_rc "undo" 0 $rc
snap_equal "S1 after undo" "$H" "$WY_E2E_TMP/s1.before.json" -claude-json
assert_no_file "$H/.claude-2" "~/.claude-2 after undo"
assert_no_file "$H/.claude-3" "~/.claude-3 after undo"
assert_no_file "$H/.local/bin/claude-2" "launcher after undo"
wy setup --undo-list --json
assert_eq "set $BAK is undone" undone "$(jget "$OUT" 'd["sets"][0]["status"]')"

# ---- S1b: a login in ~/.claude-2 before undo ----
apply_s1 s1b
printf 'b@example.com\n' > "$WY_E2E_TMP/email"
sy_in "$WY_E2E_TMP/email" account login "$H/.claude-2"
assert_rc "account login ~/.claude-2" 0 $rc
wy setup --undo --yes
assert_rc "undo with a login in ~/.claude-2" 0 $rc
assert_file "$H/.claude-2/.credentials.json" "~/.claude-2 keeps its login"
[ -d "$H/.claude-2" ] && pass "~/.claude-2 kept" || fail "~/.claude-2 was removed"
assert_no_file "$H/.claude-3" "~/.claude-3 removed (no login)"
assert_contains "undo says it kept ~/.claude-2" ".claude-2" "$(cat "$OUT" "$ERR")"
assert_eq "primary settings are back (no env key)" 0 "$(grep -c CLAUDE_CODE_PLUGIN_CACHE_DIR "$H/.claude/settings.json")"

finish
