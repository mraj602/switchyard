#!/bin/sh
# S5 MCP sync: three logged-in folders with different mcpServers (fake secrets in the values).
# First sync = union with the primary winning same-name conflicts; an edit in one folder
# reaches the others on `wy tick`; a held lock and a live session each cause a skip; bytes
# outside the mcpServers span never change; no secret value is printed or logged.
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
[ -x "$TOOLS/claude" ] || skip_scenario "no fake claude in $TOOLS"

MS="python3 -I $E2E/lib/mcpspan.py"
H=$(new_home mcp mcp)
install_wy "$H"
STATE=$H/.local/state/watchyard
F1=$H/.claude.json F2=$H/.claude-2/.claude.json F3=$H/.claude-3/.claude.json
SECRETS=$WY_E2E_TMP/secrets
cat "$E2E/homes/mcp.secrets" > "$SECRETS"
echo "sk-FAKE-SECRET-edit-0006" >> "$SECRETS"
echo "sk-FAKE-SECRET-edit-0007" >> "$SECRETS"
echo "sk-FAKE-SECRET-conflict-primary-0008" >> "$SECRETS"
echo "sk-FAKE-SECRET-conflict-two-0009" >> "$SECRETS"
echo "sk-FAKE-SECRET-live-0010" >> "$SECRETS"
echo "sk-FAKE-SECRET-lock-0011" >> "$SECRETS"
: > "$WY_E2E_TMP/all-output.txt"
collect() { cat "$OUT" "$ERR" >> "$WY_E2E_TMP/all-output.txt"; }

servers_equal() { # <what>: the three files hold the same mcpServers
	a=$($MS servers "$F1"); b=$($MS servers "$F2"); c=$($MS servers "$F3")
	if [ "$a" = "$b" ] && [ "$b" = "$c" ]; then pass "$1: the three lists are equal"; else fail "$1: lists differ"; printf '%s\n%s\n%s\n' "$a" "$b" "$c" | sed 's/sk-FAKE[A-Za-z0-9-]*/<secret>/g' >&2; fi
}
names() { python3 -I -c 'import json, sys; print(" ".join(sorted(json.loads(sys.argv[1]))))' "$($MS servers "$1")"; }
has_server() { python3 -I -c 'import json, sys; sys.exit(0 if sys.argv[2] in json.loads(sys.argv[1]) else 1)' "$($MS servers "$1")" "$2"; }

# ---- 1. first sync (setup) ----
wy setup --yes --json --hub later
assert_rc "setup" 0 $rc
collect
servers_equal "after the first sync"
assert_eq "union of the three folders" "only-primary only-three only-two shared-name" "$(names "$F1")"
assert_eq "same-name conflict: the primary's version wins" primary-cmd "$(python3 -I -c 'import json, sys; print(json.loads(sys.argv[1])["shared-name"]["command"])' "$($MS servers "$F2")")"
CONF=$STATE/share/mcp-conflicts.jsonl
assert_file "$CONF" "conflict log"
assert_contains "conflict log names the server" shared-name "$(cat "$CONF")"
assert_eq "base file mode" 600 "$(mode_of "$STATE/share/mcp-base.json")"

# ---- 2. an edit in one folder reaches the others on tick ----
$MS set "$F3" edited-three '{"type": "stdio", "command": "edited", "env": {"TOKEN": "sk-FAKE-SECRET-edit-0006"}}'
for f in $F1 $F2 $F3; do $MS outside "$f" > "$WY_E2E_TMP/outside.$(echo "$f" | tr / _)"; done
wy tick
assert_rc "tick" 0 $rc
collect
servers_equal "after tick"
has_server "$F1" edited-three && pass "edited-three reached the primary" || fail "edited-three missing in the primary"
has_server "$F2" edited-three && pass "edited-three reached ~/.claude-2" || fail "edited-three missing in ~/.claude-2"
for f in $F1 $F2 $F3; do
	$MS outside "$f" > "$WY_E2E_TMP/outside.after"
	if cmp -s "$WY_E2E_TMP/outside.after" "$WY_E2E_TMP/outside.$(echo "$f" | tr / _)"; then pass "bytes outside mcpServers unchanged: ${f#$H/}"; else fail "bytes outside mcpServers changed: ${f#$H/}"; diff "$WY_E2E_TMP/outside.$(echo "$f" | tr / _)" "$WY_E2E_TMP/outside.after" | head -10 >&2; fi
done
assert_eq "oauthAccount of ~/.claude-2 intact" b@example.com "$(jget "$F2" 'd["oauthAccount"]["emailAddress"]')"

# ---- 3. a same-name conflict: primary wins, logged without values ----
before=$(wc -l < "$CONF" | tr -d ' ')
$MS set "$F1" shared-name '{"type": "stdio", "command": "primary-cmd-2", "env": {"API_KEY": "sk-FAKE-SECRET-conflict-primary-0008"}}'
$MS set "$F2" shared-name '{"type": "stdio", "command": "two-cmd-2", "env": {"API_KEY": "sk-FAKE-SECRET-conflict-two-0009"}}'
wy tick
assert_rc "tick (conflict)" 0 $rc
collect
servers_equal "after the conflict tick"
assert_eq "primary version won" primary-cmd-2 "$(python3 -I -c 'import json, sys; print(json.loads(sys.argv[1])["shared-name"]["command"])' "$($MS servers "$F3")")"
after=$(wc -l < "$CONF" | tr -d ' ')
[ "$after" -gt "$before" ] && pass "the conflict was logged ($before -> $after lines)" || fail "no new conflict line ($before -> $after)"

# ---- 4. a live session in ~/.claude-2 skips that file ----
sleep 120 &
LIVE=$!
mkdir -p "$H/.claude-2/sessions"
printf '{"pid": %s, "sessionId": "fake"}\n' "$LIVE" > "$H/.claude-2/sessions/$LIVE.json"
$MS set "$F3" live-test '{"type": "stdio", "command": "live", "env": {"TOKEN": "sk-FAKE-SECRET-live-0010"}}'
wy tick
assert_rc "tick (live session)" 0 $rc
collect
has_server "$F1" live-test && pass "live-test reached the primary" || fail "live-test missing in the primary"
has_server "$F2" live-test && fail "~/.claude-2 was written while a session was live" || pass "~/.claude-2 skipped (live session)"
assert_contains "tick says one file was skipped" "1 skipped" "$(cat "$OUT")"
kill "$LIVE" 2>/dev/null || true
wait "$LIVE" 2>/dev/null || true
wy tick
assert_rc "tick (session gone)" 0 $rc
collect
has_server "$F2" live-test && pass "~/.claude-2 caught up once the session ended" || fail "~/.claude-2 still lacks live-test"

# ---- 5. a held fresh lock skips that file ----
mkdir "$H/.claude-3/.claude.json.lock"
$MS set "$F2" lock-test '{"type": "stdio", "command": "lock", "env": {"TOKEN": "sk-FAKE-SECRET-lock-0011"}}'
wy tick
assert_rc "tick (lock held)" 0 $rc
collect
has_server "$F1" lock-test && pass "lock-test reached the primary" || fail "lock-test missing in the primary"
has_server "$F3" lock-test && fail "~/.claude-3 was written while its lock was held" || pass "~/.claude-3 skipped (lock held)"
rmdir "$H/.claude-3/.claude.json.lock"
wy tick
assert_rc "tick (lock released)" 0 $rc
collect
has_server "$F3" lock-test && pass "~/.claude-3 caught up once the lock was released" || fail "~/.claude-3 still lacks lock-test"
servers_equal "at the end"
assert_no_file "$H/.claude-3/.claude.json.lock" "lock left behind"
assert_no_file "$H/.claude-2/.claude.json.lock" "lock left behind"

# ---- 6. no secret anywhere it must not be ----
wy doctor --json; collect
wy account list --json; collect
wy setup --dry-run --json; collect
leaks=0
while read -r sec; do
	[ -n "$sec" ] || continue
	if grep -qF "$sec" "$WY_E2E_TMP/all-output.txt"; then leaks=$((leaks + 1)); fail "secret $sec printed on stdout/stderr"; fi
	if [ -d "$STATE/logs" ] && grep -rqF "$sec" "$STATE/logs"; then leaks=$((leaks + 1)); fail "secret $sec in <state>/logs"; fi
	if grep -qF "$sec" "$CONF"; then leaks=$((leaks + 1)); fail "secret $sec in mcp-conflicts.jsonl"; fi
done < "$SECRETS"
[ "$leaks" -eq 0 ] && pass "no secret value in any output, in the logs or in the conflict log"

finish
