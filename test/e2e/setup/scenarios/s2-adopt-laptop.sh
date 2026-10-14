#!/bin/sh
# S2 adopt this laptop's layout (docs/spec/setup.md section 11): seven folders, 48 hand-made
# links, shell-function launchers, prototype hooks, a status line to wrap, three plugins.
# BLESS=1 rewrites golden/s2-dry-run.json.
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
[ -x "$TOOLS/claude" ] || skip_scenario "no fake claude in $TOOLS"

H=$(new_home laptop laptop)
install_sy "$H"
snap "$H" "$SY_E2E_TMP/before.json" -claude-json
P=$H/.claude
for f in $P/settings.json $P/plugins/installed_plugins.json $P/plugins/known_marketplaces.json $H/bin/claude-launch $H/.bashrc; do
	cp "$f" "$SY_E2E_TMP/before.$(basename "$f")"
done
links_of "$H" > "$SY_E2E_TMP/links.before"
assert_eq "48 hand-made links" 48 "$(wc -l < "$SY_E2E_TMP/links.before" | tr -d ' ')"

# Dry run: the golden, nothing written.
sy setup --dry-run --json
assert_rc "dry run" 0 $rc
python3 -I "$E2E/lib/normplan.py" "$H" "$OUT" > "$SY_E2E_TMP/dry.json"
golden_check "dry run plan" "$E2E/golden/s2-dry-run.json" "$SY_E2E_TMP/dry.json"
assert_eq "dry run: 5 actions" 5 "$(jget "$OUT" 'len(d["actions"])')"
assert_eq "dry run: links_changed 0" 0 "$(jget "$OUT" 'd["summary"]["links_changed"]')"
assert_eq "dry run: logins_touched 0" 0 "$(jget "$OUT" 'd["summary"]["logins_touched"]')"
PLAN=$(jget "$OUT" 'd["plan_id"]')
assert_no_file "$H/.local/state/switchyard/backup" "backup folder after a dry run"
assert_no_file "$H/.config/switchyard" "config folder after a dry run"
snap_equal "dry run" "$H" "$SY_E2E_TMP/before.json" -claude-json

# Apply exactly that plan.
sy setup --yes --hub later --json --expect-plan "$PLAN"
assert_rc "apply" 0 $rc
assert_eq "applied" applied "$(jget "$OUT" 'd["mode"]')"
links_of "$H" > "$SY_E2E_TMP/links.after"
cmp -s "$SY_E2E_TMP/links.before" "$SY_E2E_TMP/links.after" && pass "the 48 links are unchanged" || { fail "links changed"; diff "$SY_E2E_TMP/links.before" "$SY_E2E_TMP/links.after" >&2 || true; }
assert_eq "~/.local/bin holds only sy" sy "$(ls -A "$H/.local/bin")"
assert_eq "claude-launch unchanged" "$(sha "$SY_E2E_TMP/before.claude-launch")" "$(sha "$H/bin/claude-launch")"
assert_eq ".bashrc unchanged" "$(sha "$SY_E2E_TMP/before..bashrc")" "$(sha "$H/.bashrc")"
assert_eq "no other file in ~/bin" claude-launch "$(ls -A "$H/bin")"

# settings.json differs only at the three pointers of setup.md 11 step 3.
python3 -I - "$SY_E2E_TMP/before.settings.json" "$P/settings.json" "$H" <<'EOF' && pass "settings.json differs only at the three pointers" || fail "settings.json differs elsewhere"
import json, sys
a, b = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
home = sys.argv[3]
assert b["statusLine"]["command"] == home + "/.local/bin/sy statusline --wrap 'bash " + home + "/.claude/statusline-command.sh'", b["statusLine"]
assert b["enabledPlugins"].pop("switchyard@sy-local") is True
assert b["extraKnownMarketplaces"].pop("sy-local")["source"]["source"] == "directory"
b["statusLine"]["command"] = a["statusLine"]["command"]
assert a == b, (a, b)
EOF
assert_eq "settings.json is still a link in ~/.claude-2" "$P/settings.json" "$(readlink "$H/.claude-2/settings.json")"
assert_eq "no env key (plugins/ is linked by hand)" 0 "$(grep -c CLAUDE_CODE_PLUGIN_CACHE_DIR "$P/settings.json")"
assert_eq "prototype hooks kept" 3 "$(grep -c switchyard-hook.sh "$P/settings.json")"
assert_eq "one marketplace add" 1 "$(fake_calls '"marketplace","add"')"
assert_eq "one install" 1 "$(fake_calls '"plugin","install"')"
assert_eq "no plugin command ran in a folder other than ~/.claude" 0 "$(grep -F '"plugin"' "$FAKE_LOG" | grep -cF '"unset":false' || true)"
assert_eq "config.toml written" 1 "$(grep -c 'primary = ' "$H/.config/switchyard/config.toml")"
assert_eq "share.mcp = external (hand-made sync)" 1 "$(grep -c "mcp = 'external'" "$H/.config/switchyard/config.toml")"

# Rerun: nothing to do, and no new backup set.
sy setup --yes --hub later --json
assert_rc "rerun" 0 $rc
assert_eq "rerun: 0 changes" 0 "$(jget "$OUT" 'd["summary"]["changes"]')"
assert_eq "rerun: no actions" 0 "$(jget "$OUT" 'len(d["actions"])')"
sy setup --undo-list --json
assert_eq "one backup set" 1 "$(jget "$OUT" 'len(d["sets"])')"
sy setup --yes --hub later
assert_rc "rerun, text" 0 $rc
assert_contains "text output says 0 changes" "0 changes" "$(cat "$OUT" "$ERR")"

# Undo: the tree and the three plugin files are byte-equal again.
sy setup --undo --yes
assert_rc "undo" 0 $rc
snap_equal "S2 after undo" "$H" "$SY_E2E_TMP/before.json" -claude-json
for f in settings.json installed_plugins.json known_marketplaces.json; do
	case $f in settings.json) cur=$P/$f ;; *) cur=$P/plugins/$f ;; esac
	assert_eq "$f byte-equal after undo" "$(sha "$SY_E2E_TMP/before.$f")" "$(sha "$cur")"
done
links_of "$H" > "$SY_E2E_TMP/links.undone"
cmp -s "$SY_E2E_TMP/links.before" "$SY_E2E_TMP/links.undone" && pass "links unchanged after undo" || fail "links changed by undo"
assert_no_file "$P/plugins/cache/sy-local" "plugins/cache/sy-local"
assert_no_file "$H/.config/switchyard/config.toml" "config.toml"

finish
