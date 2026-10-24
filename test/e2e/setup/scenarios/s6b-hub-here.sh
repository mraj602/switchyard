#!/bin/sh
# S6 hub, "here" variant: `wy setup --hub here` installs the hub as a systemd user service and
# connects this machine to it. Needs a user systemd: run.sh starts the Fedora image with systemd
# as PID 1 (`podman run --systemd=always`), starts user@1000 and sets WY_E2E_SYSTEMD=1 with
# XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS. Everywhere else this scenario is skipped.
. "$(dirname "$0")/../lib/common.sh"

[ "${WY_E2E_SYSTEMD:-}" = 1 ] || skip_scenario "needs the systemd container (run.sh fedora podman: podman run --systemd=always)"
[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
[ -x "$TOOLS/claude" ] || skip_scenario "no fake claude in $TOOLS"

[ -n "$SD_HOME" ] || skip_scenario "not in a container: the fixture must live in the container user's real HOME"
# The systemd user manager reads units from the real HOME of its user, so the fixture is built
# there (the container's throwaway home), not in a temp folder.
H=$SD_HOME
[ -z "$(ls -A "$H/.claude" 2>/dev/null)" ] || skip_scenario "$H/.claude exists: not a throwaway container home"
"$E2E/homes/fresh.sh" "$H"
install_sy "$H"
LINGER=$(loginctl show-user "$(id -un)" -p Linger 2>/dev/null | cut -d= -f2)
note "linger is '$LINGER' for $(id -un)"

wy setup --yes --json --hub here
assert_rc "setup --hub here" 0 $rc
assert_eq "applied" applied "$(jget "$OUT" 'd["mode"]')"
UNIT=$H/.config/systemd/user/watchyard-hub.service
assert_file "$UNIT" "hub user unit"
assert_contains "the unit serves on 127.0.0.1:8787" "127.0.0.1:8787" "$(cat "$UNIT")"
in_home systemctl --user is-active watchyard-hub.service >"$WY_E2E_TMP/active" 2>&1 || true
assert_eq "hub unit active" active "$(cat "$WY_E2E_TMP/active")"
in_home systemctl --user is-active watchyard-flush.timer >"$WY_E2E_TMP/active" 2>&1 || true
assert_eq "flush timer active" active "$(cat "$WY_E2E_TMP/active")"
assert_contains "/health ok" '"ok":true' "$(curl -fs http://127.0.0.1:8787/health)"
assert_file "$H/.local/state/watchyard/token" "device token"
assert_contains "hub.url set" "url = 'http://127.0.0.1:8787'" "$(cat "$H/.config/watchyard/config.toml")"
if [ "$LINGER" = no ]; then
	assert_contains "linger warning when it is off" "enable-linger" "$(cat "$OUT" "$ERR")"
fi

# A rerun changes nothing.
wy setup --yes --json --hub keep
assert_rc "rerun" 0 $rc
assert_eq "rerun: 0 changes" 0 "$(jget "$OUT" 'd["summary"]["changes"]')"

# Undo stops and removes the services; the hub database stays (it holds data).
wy setup --undo --yes
assert_rc "undo" 0 $rc
in_home systemctl --user is-active watchyard-hub.service >"$WY_E2E_TMP/active" 2>&1 || true
assert_eq "hub unit no longer active" inactive "$(cat "$WY_E2E_TMP/active")"
assert_no_file "$UNIT" "hub user unit after undo"
if curl -fs --max-time 2 http://127.0.0.1:8787/health >/dev/null 2>&1; then fail "hub still answering after undo"; else pass "hub stopped after undo"; fi

finish
