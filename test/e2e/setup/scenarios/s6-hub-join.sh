#!/bin/sh
# S6 hub, join variant: the hub runs as a background process on 127.0.0.1 (own HOME, own
# database); `sy hub token create` there; `--hub join:<url> --token-stdin` here; the timer
# unit is written; `--prototype remove` removes exactly the three fixture hook entries; undo
# restores them. (The `--hub here` systemd variant is s6b-hub-here.sh.)
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
[ -x "$TOOLS/claude" ] || skip_scenario "no fake claude in $TOOLS"
# The timer would be a launchd agent here, and writing one is not the point of this scenario
# (CI has the plist goldens): do not start launchd things from a test.
[ "$(go_os)" = linux ] || skip_scenario "the timer unit is a launchd agent on macOS (not bootstrapped from tests)"

# ---- the hub, in its own home ----
HUBH=$SY_E2E_TMP/hub/home
mkdir -p "$HUBH"
install_sy "$HUBH"
H=$HUBH
PORT=$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
URL=http://127.0.0.1:$PORT
sy_bg "$SY_E2E_TMP/hub.pid" "$SY_E2E_TMP/hub.out" hub serve --listen "127.0.0.1:$PORT"
trap 'kill "$(cat "$SY_E2E_TMP/hub.pid")" 2>/dev/null; cleanup' EXIT
i=0
until curl -fs "$URL/health" >/dev/null 2>&1; do
	i=$((i + 1)); [ "$i" -lt 100 ] || { fail "hub did not start"; cat "$SY_E2E_TMP/hub.out.err" >&2; finish; exit 1; }
	sleep 0.1
done
pass "hub serving on $URL"

# ---- the machine ----
H=$(new_home join laptop)
install_sy "$H"
P=$H/.claude
snap "$H" "$SY_E2E_TMP/before.json" -claude-json
cp "$P/settings.json" "$SY_E2E_TMP/settings.before.json"
assert_eq "three prototype hooks before" 3 "$(grep -c 'switchyard-hook.sh' "$P/settings.json")"
sy machine --json
MID=$(jget "$OUT" 'd["id"]')
H=$HUBH
sy hub token create --kind machine --name e2e-laptop --machine "$MID" --json
assert_rc "token create" 0 $rc
TOKEN=$(jget "$OUT" 'd["token"]')
printf '%s\n' "$TOKEN" > "$SY_E2E_TMP/token"
H=$SY_E2E_TMP/join/home

# --prototype remove is refused while no hub is connected (setup.md 4, 10).
sy setup --yes --hub later --prototype remove
[ "$rc" -ne 0 ] && pass "--prototype remove refused without a live hub (exit $rc)" || fail "--prototype remove accepted without a hub"
assert_eq "hooks still there after the refusal" 3 "$(grep -c 'switchyard-hook.sh' "$P/settings.json")"
snap_equal "after the refused run" "$H" "$SY_E2E_TMP/before.json" -claude-json

# Join with the token from stdin; remove the prototype hooks.
sy_in "$SY_E2E_TMP/token" setup --yes --json --hub "join:$URL" --token-stdin --prototype remove
assert_rc "setup --hub join" 0 $rc
assert_eq "applied" applied "$(jget "$OUT" 'd["mode"]')"
S=$H/.local/state/switchyard
assert_file "$S/token" "device token"
assert_eq "token mode" 600 "$(mode_of "$S/token")"
assert_eq "token stored as given" "$TOKEN" "$(head -n 1 "$S/token")"
assert_contains "hub.url in config.toml" "url = '$URL'" "$(cat "$H/.config/switchyard/config.toml")"
assert_file "$H/.config/systemd/user/switchyard-flush.service" "timer service unit"
assert_file "$H/.config/systemd/user/switchyard-flush.timer" "timer unit"
assert_contains "the unit runs sy tick" "tick" "$(cat "$H/.config/systemd/user/switchyard-flush.service")"
assert_eq "no token in the setup output" 0 "$(grep -cF "$TOKEN" "$OUT" "$ERR" | awk -F: '{n += $2} END {print n + 0}')"
assert_eq "no prototype hook entry left" 0 "$(grep -c 'switchyard-hook' "$P/settings.json")"
assert_file "$P/switchyard-hook.sh" "the prototype script itself is left alone"
# Exactly the three entries went: everything else in settings.json is as before.
python3 -I - "$SY_E2E_TMP/settings.before.json" "$P/settings.json" <<'PYEOF' && pass "exactly the three hook entries were removed" || fail "settings.json differs beyond the hooks"
import json, sys
a, b = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
assert set(a["hooks"]) == {"SessionStart", "Stop", "Notification"}, a["hooks"]
assert not b.get("hooks"), b.get("hooks")
for k in ("statusLine", "enabledPlugins", "extraKnownMarketplaces"):
    a.pop(k), b.pop(k)
a.pop("hooks")
b.pop("hooks", None)
assert a == b, (a, b)
PYEOF
sy doctor --json
assert_eq "doctor: no prototype warning, no hub failure" 0 "$(grep -cE '"name": "(prototype|hub)"' "$OUT")"

# A second run is idempotent.
sy_in "$SY_E2E_TMP/token" setup --yes --json --hub "join:$URL" --token-stdin --prototype remove
assert_rc "rerun" 0 $rc
assert_eq "rerun: 0 changes" 0 "$(jget "$OUT" 'd["summary"]["changes"]')"

# Undo: the hooks are back, the connection is gone, the tree is as before.
sy setup --undo --yes
assert_rc "undo" 0 $rc
assert_eq "three prototype hooks restored" 3 "$(grep -c 'switchyard-hook.sh' "$P/settings.json")"
assert_eq "settings.json equals the original (as JSON)" "$(jget "$SY_E2E_TMP/settings.before.json" 'd')" "$(jget "$P/settings.json" 'd')"
assert_no_file "$S/token" "device token after undo"
assert_no_file "$H/.config/systemd/user/switchyard-flush.timer" "timer unit after undo"
assert_no_file "$H/.config/switchyard/config.toml" "config.toml after undo"
snap_equal "S6 after undo" "$H" "$SY_E2E_TMP/before.json" -claude-json
assert_contains "undo says the device token is not revoked" "revoke" "$(cat "$OUT" "$ERR")"

finish
