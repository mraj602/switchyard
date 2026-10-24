#!/bin/sh
# S7 uninstall: after S2 (adopted laptop) and after S1 (fresh install), then --purge.
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
[ -x "$TOOLS/claude" ] || skip_scenario "no fake claude in $TOOLS"

# ---- after S2 ----
H=$(new_home laptop laptop)
install_sy "$H"
P=$H/.claude
cp "$P/settings.json" "$WY_E2E_TMP/settings.before.json"
links_of "$H" > "$WY_E2E_TMP/links.before"
wy setup --yes --hub later
assert_rc "S2 setup" 0 $rc
assert_contains "setup wrapped the status line" "wy statusline --wrap" "$(jget "$P/settings.json" 'd["statusLine"]["command"]')"

wy uninstall --dry-run --json
assert_rc "uninstall dry run" 0 $rc
assert_contains "dry run changes nothing" watchyard@wy-local "$(cat "$P/settings.json")"

wy uninstall --yes --json
assert_rc "uninstall" 0 $rc
S=$P/settings.json
assert_eq "status line is the original string" "$(jget "$WY_E2E_TMP/settings.before.json" 'd["statusLine"]["command"]')" "$(jget "$S" 'd["statusLine"]["command"]')"
assert_eq "no watchyard plugin key" 0 "$(grep -c 'watchyard@wy-local' "$S")"
assert_eq "no wy-local marketplace" 0 "$(grep -c '"wy-local"' "$S")"
assert_eq "the other plugin keys and marketplaces are intact" "$(jget "$WY_E2E_TMP/settings.before.json" '[d["enabledPlugins"], d["extraKnownMarketplaces"]]')" "$(jget "$S" '[d["enabledPlugins"], d["extraKnownMarketplaces"]]')"
assert_eq "other hooks intact" "$(jget "$WY_E2E_TMP/settings.before.json" 'd["hooks"]')" "$(jget "$S" 'd["hooks"]')"
assert_eq "settings.json equals the original (as JSON)" "$(jget "$WY_E2E_TMP/settings.before.json" 'd')" "$(jget "$S" 'd')"
links_of "$H" > "$WY_E2E_TMP/links.after"
cmp -s "$WY_E2E_TMP/links.before" "$WY_E2E_TMP/links.after" && pass "all 48 links intact" || fail "links changed by uninstall"
assert_eq "the plugin records are gone" 0 "$(grep -c 'watchyard@wy-local' "$P/plugins/installed_plugins.json")"
assert_eq "no wy-local marketplace record" 0 "$(grep -c 'wy-local' "$P/plugins/known_marketplaces.json")"
assert_eq "ledger empty" "{}" "$(jget "$H/.local/state/watchyard/setup/ledger.json" '{k: v for k, v in d.items() if v and k != "v"}')"
assert_eq "~/.local/bin holds only wy" wy "$(ls -A "$H/.local/bin")"
assert_eq "claude-launch untouched" claude-launch "$(ls -A "$H/bin")"
assert_file "$H/.config/watchyard/config.toml" "config.toml kept without --purge"
wy uninstall --yes
assert_rc "second uninstall" 0 $rc

# ---- after S1 ----
H=$(new_home fresh fresh)
install_sy "$H"
mkdir -p "$H/.config/watchyard"
printf '{"theme": "electron"}\n' > "$H/.config/watchyard/config.json"
EJ=$(sha "$H/.config/watchyard/config.json")
wy setup --yes --add work:amber,client --hub later
assert_rc "S1 setup" 0 $rc
assert_file "$H/.local/bin/claude-2" "launcher claude-2 written"
assert_eq "env key set" 1 "$(grep -c CLAUDE_CODE_PLUGIN_CACHE_DIR "$H/.claude/settings.json")"

wy uninstall --yes
assert_rc "uninstall" 0 $rc
assert_no_file "$H/.local/bin/claude-2" "launcher claude-2"
assert_no_file "$H/.local/bin/claude-3" "launcher claude-3"
assert_eq "env key gone" 0 "$(grep -c CLAUDE_CODE_PLUGIN_CACHE_DIR "$H/.claude/settings.json")"
assert_eq "status line gone (wy created it)" 0 "$(grep -c statusLine "$H/.claude/settings.json")"
assert_eq "no plugin key" 0 "$(grep -c 'wy-local' "$H/.claude/settings.json")"
assert_eq "theme kept" dark "$(jget "$H/.claude/settings.json" 'd["theme"]')"
[ -d "$H/.claude-2" ] && [ -d "$H/.claude-3" ] && pass "the folders stay" || fail "a folder was removed"
assert_eq "~/.claude-2/skills is still a link" "$H/.claude/skills" "$(readlink "$H/.claude-2/skills")"
assert_file "$H/.config/watchyard/config.toml" "config.toml kept without --purge"
assert_eq "ledger empty" "{}" "$(jget "$H/.local/state/watchyard/setup/ledger.json" '{k: v for k, v in d.items() if v and k != "v"}')"

# ---- --purge ----
wy uninstall --yes --purge
assert_rc "uninstall --purge" 0 $rc
assert_no_file "$H/.config/watchyard/config.toml" "config.toml"
assert_no_file "$H/.config/watchyard/config.toml.bak" "config.toml.bak"
assert_file "$H/.config/watchyard/config.json" "the Electron config.json"
assert_eq "config.json untouched" "$EJ" "$(sha "$H/.config/watchyard/config.json")"
assert_no_file "$H/.local/state/watchyard" "state folder after --purge"
assert_file "$H/.local/bin/wy" "the wy binary stays"
assert_contains "last line says how to remove wy" "rm " "$(tail -n 3 "$OUT" "$ERR")"

finish
