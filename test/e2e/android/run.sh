#!/usr/bin/env bash
# Switchyard Android app against a real local hub, on the Android emulator (make e2e-android).
#
# Never part of `make check`. It skips cleanly (exit 0, "SKIP: ...") without the SDK, the AVD,
# KVM, python3 or an APK it can build. Details and the findings it was written from:
# test/e2e/android/README.md.
#
# What runs, in order (each line below is one or more PASS/FAIL checks):
#   1  install + grant POST_NOTIFICATIONS; the app starts (bundled JS, no Metro)
#   2  pairing through the switchyard://pair deep link with the QR's h and k (the QR path, the seal)
#   3  sy-link service: foreground service up, hub sees the link, persistent notification
#   4  an "asked" alert (fake machine -> hub ingest -> alert engine -> link -> notification) with
#      Open / Snooze / Seen; Seen; the hub's "send test notification"; Snooze
#   5  hub restart: reconnect, then a new alert
#   6  airplane mode: an alert queued while offline, plus a hub restart, replays on reconnect
#   7  force-stop stays stopped; opening the app restarts it and replays what was queued
#   8  task removed (swipe away from Recents): the service keeps running
#   9  adb reboot: BOOT_COMPLETED restarts the service, the link reconnects
#  11  claude.ai account tabs (P17): two seats, a WebView profile each, Open in Claude, sign out
#  12  analytics (More > Analytics): a token_rows record from the fake machine shows in the totals
#  10  revoke from the hub API (DELETE /v1/devices/{id}): 4403, service stops, UI says so, and no
#      orphaned notification is left; then `sy hub token revoke` (CLI) the same way
#
# Safety: only the emulator serial it starts (or an emulator-* that is already attached) is ever
# addressed (adb -s); real devices are never touched. The hub runs with a temporary HOME and XDG
# dirs and listens on 127.0.0.1 only (the emulator reaches it at 10.0.2.2). Nothing is uploaded.
#
# Environment (all optional):
#   ANDROID_HOME        SDK (default ~/Android/Sdk)
#   SY_E2E_AVD          AVD name (default pixel_7_api35)
#   SY_E2E_EMU_PORT     console port of the emulator it starts (default 5584 -> emulator-5584)
#   SY_E2E_GPU          emulator -gpu mode (default swangle_indirect; swiftshader_indirect and
#                       guest crash the emulator's renderer on some hosts)
#   SY_E2E_APK          APK to install (default phone/dist/app-e2e.apk; built by
#                       phone/scripts/e2e-android-apk.sh when missing, ~6 min)
#   SY_E2E_NO_BUILD=1   skip instead of building a missing APK
#   SY_E2E_KEEP=1       keep the temp dir (hub state, logs) even when everything passes
#   SY_E2E_KEEP_EMU=1   leave the emulator running at the end
#   SY_E2E_PHASES       space separated list of phase numbers to run (default: all; phase 2 always)
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd "$HERE/../../.." && pwd -P)"
LIB="$HERE/lib.py"
PKG="${SY_APP_ID:-dev.switchyard.app.local}"
AVD="${SY_E2E_AVD:-pixel_7_api35}"
EMU_PORT="${SY_E2E_EMU_PORT:-5584}"
GPU="${SY_E2E_GPU:-swangle_indirect}"
APK="${SY_E2E_APK:-$ROOT/phone/dist/app-e2e.apk}"

skip() { echo "SKIP: $*"; exit 0; }
die() { echo "ERROR: $*" >&2; exit 1; }

# ---------------------------------------------------------------- prerequisites (skip cleanly)
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
[ -d "$ANDROID_HOME" ] || skip "no Android SDK at $ANDROID_HOME (set ANDROID_HOME)"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export PATH="$ANDROID_HOME/emulator:$ANDROID_HOME/platform-tools:$HOME/.local/bin:$PATH"
command -v adb >/dev/null || skip "adb is not on PATH"
command -v emulator >/dev/null || skip "emulator is not on PATH"
command -v go >/dev/null || skip "go is not on PATH"
command -v python3 >/dev/null || skip "python3 is not on PATH"
command -v curl >/dev/null || skip "curl is not on PATH"
emulator -list-avds 2>/dev/null | grep -qx "$AVD" || skip "no AVD named $AVD (set SY_E2E_AVD)"
[ -r /dev/kvm ] && [ -w /dev/kvm ] || skip "no usable /dev/kvm (the emulator would be unusably slow)"

# ---------------------------------------------------------------- state and cleanup
T="$(mktemp -d "${TMPDIR:-/tmp}/sy-e2e-android.XXXXXX")"
mkdir -p "$T"/home "$T"/cfg "$T"/data "$T"/state "$T"/cache "$T"/bin
SER=""
EMU_PID=""
HUB_PID=""
STARTED_EMU=0
NPASS=0
NFAIL=0
FAILED=()
PORT=""

adbx() { adb -s "$SER" "$@"; }

cleanup() {
  local rc=$?
  [ -n "$HUB_PID" ] && kill -INT "$HUB_PID" 2>/dev/null
  if [ "$STARTED_EMU" = 1 ] && [ -z "${SY_E2E_KEEP_EMU:-}" ] && [ -n "$SER" ]; then
    adb -s "$SER" emu kill >/dev/null 2>&1
    [ -n "$EMU_PID" ] && { sleep 2; kill "$EMU_PID" 2>/dev/null; }
  fi
  if [ "$NFAIL" -gt 0 ] || [ -n "${SY_E2E_KEEP:-}" ] || [ "$rc" -ne 0 ]; then
    echo "kept: $T (hub.log, emulator.log, logcat-*.txt, ui-*.xml)"
  else
    rm -rf "$T"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

pass() { NPASS=$((NPASS + 1)); printf 'PASS  %s%s\n' "$1" "${2:+  [$2]}"; }
fail() { NFAIL=$((NFAIL + 1)); FAILED+=("$1"); printf 'FAIL  %s%s\n' "$1" "${2:+  [$2]}"; }
note() { printf '      %s\n' "$*"; }

# wait_until SECONDS cmd...: polls once a second.
wait_until() {
  local end=$((SECONDS + $1))
  shift
  while [ "$SECONDS" -lt "$end" ]; do
    "$@" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}
# check NAME SECONDS cmd...: PASS when cmd succeeds within SECONDS.
check() {
  local name="$1" secs="$2"
  shift 2
  if wait_until "$secs" "$@"; then pass "$name"; else fail "$name"; return 1; fi
}
# check_not NAME SECONDS cmd...: PASS when cmd stays failing for the whole SECONDS.
check_not() {
  local name="$1" secs="$2"
  shift 2
  local end=$((SECONDS + secs))
  while [ "$SECONDS" -lt "$end" ]; do
    if "$@" >/dev/null 2>&1; then fail "$name"; return 1; fi
    sleep 1
  done
  pass "$name"
}

phase_on() {
  [ -z "${SY_E2E_PHASES:-}" ] && return 0
  case " $SY_E2E_PHASES " in *" $1 "*) return 0 ;; esac
  return 1
}

# ---------------------------------------------------------------- the emulator
pick_emulator() {
  # An emulator-* that is already attached is used as it is; any other device is never touched.
  local existing
  existing="$(adb devices | awk '$1 ~ /^emulator-[0-9]+$/ && $2 == "device" {print $1; exit}')"
  if [ -n "$existing" ]; then
    SER="$existing"
    note "using the running emulator $SER (left running at the end)"
    return 0
  fi
  SER="emulator-$EMU_PORT"
  STARTED_EMU=1
  note "starting $AVD as $SER (headless, -gpu $GPU)"
  setsid emulator -avd "$AVD" -port "$EMU_PORT" -no-window -no-audio -no-snapshot -no-boot-anim \
    -gpu "$GPU" -feature -Vulkan,-GLDirectMem >"$T/emulator.log" 2>&1 </dev/null &
  EMU_PID=$!
}

boot_done() { [ "$(adbx shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = 1 ]; }
wait_boot() {
  wait_until "${1:-300}" boot_done
}

# ---------------------------------------------------------------- the hub
# Every sy call runs with a scrubbed environment: a temp HOME and XDG dirs, nothing of the caller's.
HENV=(env -i PATH="$PATH" HOME="$T/home" TZ=UTC NO_COLOR=1 XDG_CONFIG_HOME="$T/cfg" XDG_DATA_HOME="$T/data"
  XDG_STATE_HOME="$T/state" XDG_CACHE_HOME="$T/cache")
hsy() { "${HENV[@]}" "$SY" "$@"; }
free_port() { python3 -I -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
hub_up() { curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; }
hub_start() {
  # not through hsy(): $! must be the hub itself (env execs sy), so SIGINT reaches it
  "${HENV[@]}" "$SY" hub serve --listen "127.0.0.1:$PORT" --no-app-mirror >>"$T/hub.log" 2>&1 &
  HUB_PID=$!
  wait_until 30 hub_up
}
hub_stop() {
  [ -n "$HUB_PID" ] || return 0
  kill -INT "$HUB_PID" 2>/dev/null
  wait "$HUB_PID" 2>/dev/null
  HUB_PID=""
}
hub_origin() { echo "http://127.0.0.1:$PORT"; }

COOKIE=""
owner_login() {
  local sec
  sec="$(hsy hub owner-link --url "$(hub_origin)" --insecure --json | python3 -I "$LIB" json secret)"
  COOKIE="$(curl -s -i -X POST "$(hub_origin)/v1/session/owner" -H 'Content-Type: application/json' \
    -H "Origin: $(hub_origin)" -d "{\"secret\":\"$sec\"}" | tr -d '\r' | sed -n 's/^[Ss]et-[Cc]ookie: \([^;]*\).*/\1/p' | head -1)"
  [ -n "$COOKIE" ]
}
# api METHOD PATH [JSON]: as the owner.
api() {
  # a cookie request that changes state must be same-origin JSON, even a DELETE without a body
  curl -s -X "$1" "$(hub_origin)$2" -H "Cookie: $COOKIE" -H "Origin: $(hub_origin)" -H 'Content-Type: application/json' ${3:+-d "$3"}
}
phone_field() { api GET /v1/devices | python3 -I "$LIB" phone "$1"; }
link_connected() { [ "$(phone_field link.connected)" = true ]; }
link_disconnected() { [ "$(phone_field link.connected)" != true ]; }
alert_field() { api GET "/v1/alerts?session=$1&limit=1" | python3 -I "$LIB" json "alerts.0.$2"; }
alert_is() { [ "$(alert_field "$1" "$2")" = "$3" ]; }
has_phone() { [ -n "$(phone_field id)" ]; }

MACHINE="m_01E2EANDR01DTEST000000000A"
MTOKEN=""
ask() { "$T/bin/fakemachine" -hub "$(hub_origin)" -token "$MTOKEN" -machine "$MACHINE" -session "$1" ask >>"$T/fakemachine.log" 2>&1; }
sid() { printf 'c41e0000-0000-4000-8000-%012d' "$1"; }

# ---------------------------------------------------------------- the phone
notes() { adbx shell dumpsys notification --noredact 2>/dev/null | python3 -I "$LIB" notes "$PKG"; }
note_posted() { notes | awk -F'\t' -v ch="$1" -v t="$2" '$1 == ch && index($3 t, t) {f=1} END {exit !f}'; }
alert_posted() { notes | awk -F'\t' '$1 == "sy_needs_you" && index($2, "asked a question") {f=1} END {exit !f}'; }
alert_count() { notes | awk -F'\t' '$1 == "sy_needs_you" && index($2, "asked a question") {n++} END {print n+0}'; }
conn_text() { notes | awk -F'\t' '$1 == "sy_connection" {print $3}'; }
svc_up() { adbx shell dumpsys activity services "$PKG" 2>/dev/null | grep -q 'SyLinkService'; }
svc_fg() { adbx shell dumpsys activity services "$PKG" 2>/dev/null | grep -q 'isForeground=true'; }
svc_down() { ! svc_up; }
alerts_are() { [ "$(alert_count)" = "$1" ]; }
conn_starts() { case "$(conn_text)" in "$1"*) return 0 ;; esac; return 1; }
no_conn_note() { [ -z "$(conn_text)" ]; }
app_pid() { adbx shell pidof "$PKG" 2>/dev/null | tr -d '\r'; }

ui_dump() {
  local i
  for i in 1 2 3; do
    if adbx shell uiautomator dump /sdcard/sy-ui.xml >/dev/null 2>&1 && adbx exec-out cat /sdcard/sy-ui.xml >"$T/ui.xml" 2>/dev/null && [ -s "$T/ui.xml" ]; then
      # A loaded emulator can raise "System UI isn't responding" over the app: wait it out.
      grep -q "isn&apos;t responding\|isn't responding" "$T/ui.xml" || return 0
      adbx shell input tap $(python3 -I "$LIB" tap Wait <"$T/ui.xml") 2>/dev/null
    fi
    sleep 1
  done
  return 1
}
ui_has() { ui_dump && python3 -I "$LIB" has "$1" <"$T/ui.xml"; }
ui_tap() {
  local xy
  ui_dump || return 1
  xy="$(python3 -I "$LIB" tap "$1" <"$T/ui.xml")" || return 1
  adbx shell input tap $xy
}
shade_open() { adbx shell cmd statusbar expand-notifications; sleep 2; }
shade_close() { adbx shell cmd statusbar collapse; }
shade_tap() { # the action button of the notification shade
  shade_open
  ui_tap "$1"
  local rc=$?
  shade_close
  return $rc
}
go_bar() { # go_bar LABEL: the bottom bar entry (not a screen's own title)
  local xy
  ui_dump || return 1
  xy="$(python3 -I "$LIB" tapbar "$1" <"$T/ui.xml")" || return 1
  adbx shell input tap $xy
}
home() { adbx shell input keyevent KEYCODE_HOME; }
launch() { adbx shell am start -n "$PKG/.MainActivity" >/dev/null 2>&1; }
save_diag() { # save_diag TAG: logcat and the screen, for a failure
  adbx logcat -d >"$T/logcat-$1.txt" 2>/dev/null
  ui_dump && cp "$T/ui.xml" "$T/ui-$1.xml"
}

# ================================================================ build what the test needs
SY="$ROOT/dist/sy"
echo "== build sy and the fake machine"
(cd "$ROOT" && make build >"$T/build.log" 2>&1) || { cat "$T/build.log"; die "make build failed"; }
(cd "$ROOT" && go build -o "$T/bin/fakemachine" ./test/e2e/android/fakemachine) || die "building fakemachine failed"

if [ ! -f "$APK" ]; then
  [ -z "${SY_E2E_NO_BUILD:-}" ] || skip "no APK at $APK and SY_E2E_NO_BUILD is set (phone/scripts/e2e-android-apk.sh builds it)"
  echo "== build the APK (no Metro, cleartext hub allowed): ~6 min"
  SY_E2E_APK="$APK" "$ROOT/phone/scripts/e2e-android-apk.sh" >"$T/apk-build.log" 2>&1 || { tail -30 "$T/apk-build.log"; die "APK build failed (log: $T/apk-build.log)"; }
fi

# ================================================================ hub
echo "== hub (temp HOME $T/home)"
PORT="$(free_port)"
hub_start || { cat "$T/hub.log"; die "the hub did not start"; }
owner_login || die "owner sign-in failed"
# The alert engine waits 60 s before an "asked" push: this test does not.
api PUT /v1/rules/rule_asked '{"delay_s":0}' >/dev/null
api PUT /v1/push/settings '{"flood":{"max":100,"per_s":600}}' >/dev/null
MTOKEN="$(hsy hub token create --kind machine --name e2e-laptop --machine "$MACHINE" --json | python3 -I "$LIB" json token)"
[ -n "$MTOKEN" ] || die "no machine token"

# ================================================================ emulator
echo "== emulator"
pick_emulator
wait_until 120 adbx get-state || { tail -5 "$T/emulator.log" 2>/dev/null; skip "the emulator did not come up (see $T/emulator.log)"; }
wait_boot 300 || skip "the emulator did not finish booting"
note "$SER booted"

# ================================================================ 1 install
echo "== 1  install and start"
adbx uninstall "$PKG" >/dev/null 2>&1
if adbx install -r "$APK" >"$T/install.log" 2>&1; then pass "1.1 APK installs"; else fail "1.1 APK installs" "$(tail -1 "$T/install.log")"; exit 1; fi
adbx shell pm grant "$PKG" android.permission.POST_NOTIFICATIONS
adbx logcat -c
launch
if check "1.2 the app starts and shows the Pair screen (bundled JS)" 60 ui_has "Pair with your hub"; then :; else save_diag start; exit 1; fi

# ================================================================ 2 pairing by deep link
echo "== 2  pairing through switchyard://pair (the QR's secret, code, h and k)"
pair_by_link() {
  local json url q
  json="$(hsy hub pair --kind phone --url "http://10.0.2.2:$PORT" --insecure --json)" || return 1
  url="$(printf '%s' "$json" | python3 -I "$LIB" json url)"
  q="${url#*#/pair?}"
  adbx shell "am start -a android.intent.action.VIEW -d 'switchyard://pair?u=http%3A%2F%2F10.0.2.2%3A$PORT&$q' $PKG" >/dev/null 2>&1
}
# The card's tap, then the hub's device list. A "Can't reach" right after the install has been
# seen once in a few runs (the claim never arrived at the hub): one fresh link is tried, and said.
tap_pair() {
  ui_tap "Pair with 10.0.2.2:$PORT"
  wait_until 25 has_phone && return 0
  if ui_has "Can't reach"; then
    note "flake: the app said Can't reach 10.0.2.2:$PORT with the hub up; trying a fresh link"
    save_diag cantreach
    pair_by_link
    wait_until 30 ui_has "Pair with 10.0.2.2:$PORT" && ui_tap "Pair with 10.0.2.2:$PORT"
    wait_until 30 has_phone
    return $?
  fi
  return 1
}
pair_by_link
check "2.1 the link opens a pre-filled card and claims nothing yet" 30 ui_has "Pair with 10.0.2.2:$PORT"
[ -z "$(phone_field id)" ] && pass "2.2 nothing was claimed before the tap" || fail "2.2 nothing was claimed before the tap"
tap_pair && pass "2.3 pairing succeeds: the hub has a phone device" || fail "2.3 pairing succeeds: the hub has a phone device"
PHONE_ID="$(phone_field id)"
check "2.4 the app shows the hub (Now screen)" 30 ui_has "Needs you"
[ "$(phone_field keys.reg)" = qr ] && pass "2.5 keys went in sealed by the QR (reg = qr)" || fail "2.5 keys went in sealed by the QR (reg = qr)" "reg=$(phone_field keys.reg)"
[ -n "$(phone_field targets.0.id)" ] && [ "$(phone_field targets.0.kind)" = link ] && pass "2.6 the hub made the phone's link push target" || fail "2.6 the hub made the phone's link push target"
TARGET="$(phone_field targets.0.id)"
[ -n "$PHONE_ID" ] || { save_diag pair; exit 1; }

# ================================================================ 3 the link
echo "== 3  the sy-link service"
check "3.1 SyLinkService is running as a foreground service" 30 svc_fg
check "3.2 the hub reports the phone's link connected" 30 link_connected
check "3.3 the persistent notification says Connected to hub" 20 conn_starts "Connected to hub"
grep -q "link login $PHONE_ID" "$T/hub.log" && pass "3.4 the hub log has the link login" || fail "3.4 the hub log has the link login"

# ================================================================ 4 alerts
if phase_on 4; then
  echo "== 4  alerts, Seen, test notification, Snooze"
  S1="$(sid 1)"
  ask "$S1"
  check "4.1 an asked alert becomes a notification on sy_needs_you" 30 alert_posted
  acts="$(notes | awk -F'\t' '$1 == "sy_needs_you" && index($2, "asked a question") {print $4; exit}')"
  [ "$acts" = "Open,Snooze 15 min,Seen" ] && pass "4.2 it carries Open, Snooze 15 min and Seen" || fail "4.2 it carries Open, Snooze 15 min and Seen" "actions=$acts"
  [ "$(alert_field "$S1" deliveries.0.kind)" = link ] && pass "4.3 the hub delivery log says kind link" || fail "4.3 the hub delivery log says kind link"
  shade_tap "Seen"
  check "4.4 Seen: the hub marks the alert seen" 20 alert_is "$S1" status seen
  check "4.5 Seen: the notification is gone" 15 alerts_are 0
  api POST "/v1/push/targets/$TARGET/test" '{}' >"$T/test-push.json"
  check "4.6 the hub's send-test-notification arrives on sy_test" 20 note_posted sy_test "Test push from your hub"
  S2="$(sid 2)"
  ask "$S2"
  check "4.7 a second alert arrives" 30 alert_posted
  shade_tap "Snooze 15 min"
  check "4.8 Snooze: the hub marks the alert snoozed" 20 alert_is "$S2" status snoozed
  check "4.9 Snooze: the notification is gone" 15 alerts_are 0
fi

# ================================================================ 5 hub restart
if phase_on 5; then
  echo "== 5  hub restart"
  hub_stop
  hub_start
  owner_login
  check "5.1 the phone reconnects to the restarted hub" 90 link_connected
  S3="$(sid 3)"
  ask "$S3"
  check "5.2 an alert after the restart is delivered" 30 alert_posted
  shade_tap "Seen" || true
fi

# ================================================================ 6 airplane mode
if phase_on 6; then
  echo "== 6  airplane mode, offline queue, hub restart"
  adbx shell cmd connectivity airplane-mode enable
  check "6.1 the phone notices it is offline (Reconnecting)" 30 conn_starts "Reconnecting"
  S4="$(sid 4)"
  ask "$S4"
  check_not "6.2 an alert raised while offline is not shown yet" 6 alert_posted
  hub_stop
  hub_start
  owner_login
  adbx shell cmd connectivity airplane-mode disable
  check "6.3 airplane off: the phone reconnects" 120 link_connected
  check "6.4 the alert queued while offline (across a hub restart) is replayed" 60 alert_posted
  shade_tap "Seen" || true
fi

# ================================================================ 7 force-stop
if phase_on 7; then
  echo "== 7  force-stop"
  adbx shell am force-stop "$PKG"
  check "7.1 after force-stop the service is down" 10 svc_down
  check_not "7.2 and stays down (Android blocks every restart of a force-stopped app)" 20 svc_up
  S5="$(sid 5)"
  ask "$S5"
  check_not "7.3 an alert raised meanwhile is not shown" 5 alert_posted
  launch
  check "7.4 opening the app restarts the service" 60 svc_fg
  check "7.5 the link reconnects" 60 link_connected
  check "7.6 the alert queued meanwhile is replayed" 60 alert_posted
  shade_tap "Seen" || true
fi

# ================================================================ 8 task removed
if phase_on 8; then
  echo "== 8  task removed (swipe the app away from Recents)"
  launch
  sleep 3
  adbx shell input keyevent KEYCODE_APP_SWITCH
  sleep 3
  gone=0
  for dur in 300 600; do
    ui_dump && xy="$(python3 -I "$LIB" tap "Switchyard" <"$T/ui.xml")" || break
    set -- $xy
    adbx shell input swipe "$1" "$2" "$1" 100 "$dur"
    sleep 4
    if ! adbx shell dumpsys activity recents 2>/dev/null | grep -q "A=[0-9]*:$PKG"; then gone=1; break; fi
  done
  if [ "$gone" = 1 ]; then
    pass "8.1 the task is gone from Recents"
    check "8.2 the service still runs (onTaskRemoved restarts it, the process stays)" 15 svc_fg
    check "8.3 and the link is connected" 30 link_connected
  else
    note "the Recents card did not go away (launcher gesture): 8 is not conclusive on this run"
  fi
  home
fi

# ================================================================ 9 reboot
if phase_on 9; then
  echo "== 9  adb reboot (BOOT_COMPLETED)"
  adbx reboot
  sleep 15
  wait_boot 300 && pass "9.1 the emulator rebooted" || fail "9.1 the emulator rebooted"
  svc_down && note "service not up yet at boot_completed (BOOT_COMPLETED comes after the user unlocks)"
  check "9.2 BOOT_COMPLETED restarts SyLinkService without opening the app" 240 svc_fg
  check "9.3 the link reconnects" 90 link_connected
  S6="$(sid 6)"
  ask "$S6"
  check "9.4 an alert after the reboot is delivered" 30 alert_posted
  shade_tap "Seen" || true
fi

# ================================================================ 10 revoke
if phase_on 10; then
  echo "== 10  revoke on the hub"
  launch
  sleep 3
  api DELETE "/v1/devices/$PHONE_ID" >/dev/null
  check "10.1 the hub revoked: the service stops (4403)" 30 svc_down
  check "10.2 the notification says the phone was removed" 20 note_posted sy_needs_you "This phone was removed from the hub" || {
    adbx shell dumpsys notification --noredact >"$T/notifications-revoke.txt" 2>&1
    save_diag removed
  }
  check "10.3 no orphaned connection notification is left" 10 no_conn_note
  launch
  if check "10.4 the app shows the Pair screen and says why" 30 ui_has "unpaired on the hub"; then :; else save_diag revoke; fi

  echo "== 10b revoke from the hub's CLI (sy hub token revoke)"
  pair_by_link
  check "10.5 re-pairing works after a revoke" 30 ui_has "Pair with 10.0.2.2:$PORT"
  tap_pair
  check "10.6 the new device connects" 60 link_connected
  PHONE_ID="$(phone_field id)"
  hsy hub token revoke "$PHONE_ID" --yes >/dev/null
  # The CLI writes the database; the running hub notices at its next sweep (link.md 7, once a
  # minute) or when the phone's next request answers 401.
  check "10.7 CLI revoke: the service stops within the sweep" 120 svc_down
  check "10.8 no orphaned connection notification is left" 10 no_conn_note
fi

# ================================================================ 11 claude.ai account tabs
if phase_on 11; then
  echo "== 11 claude.ai account tabs (P17): one WebView profile per seat, never signed in"
  # Never types or taps anything inside claude.ai: the page is only loaded and looked at.
  if ! has_phone; then # phase 10 leaves the phone revoked: pair again
    pair_by_link
    wait_until 30 ui_has "Pair with 10.0.2.2:$PORT" && tap_pair
    check "11.0 the phone is paired again" 60 link_connected
  fi
  EA="alice-e2e@example.com"
  EB="bob-e2e@example.com"
  SA_SESS="$(sid 11)"
  SB_SESS="$(sid 12)"
  ask_as() { # ask_as SESSION N EMAIL REMOTE: a session of account N (the hub makes one seat_<ULID> per account+org)
    "$T/bin/fakemachine" -hub "$(hub_origin)" -token "$MTOKEN" -machine "$MACHINE" -session "$1" \
      -account "acc-e2e-$2" -org "org-e2e-$2" -email "$3" -remote-url "$4" ask >>"$T/fakemachine.log" 2>&1
  }
  seat_of() { api GET /v1/accounts | python3 -I "$LIB" seat "$1"; }
  ok_seat() { [[ "$(seat_of "$1")" =~ ^seat_[0-9A-HJKMNP-TV-Z]{26}$ ]]; }
  ask_as "$SA_SESS" a "$EA" "https://claude.ai/code/session_e2eA"
  ask_as "$SB_SESS" b "$EB" "https://claude.ai/code/session_e2eB"
  check "11.1 the hub made seat_<ULID> ids for account A" 20 ok_seat "$EA"
  check "11.1b and for account B" 20 ok_seat "$EB"
  SEAT_A="$(seat_of "$EA")"
  SEAT_B="$(seat_of "$EB")"
  if [ -n "$SEAT_A" ] && [ "$SEAT_A" != "$SEAT_B" ]; then pass "11.2 the two seats differ" "A=$SEAT_A B=$SEAT_B"; else fail "11.2 the two seats differ" "A=$SEAT_A B=$SEAT_B"; fi
  [ "$(api GET "/v1/sessions/$SB_SESS" | python3 -I "$LIB" json seat)" = "$SEAT_B" ] && pass "11.3 the hub attributes session B to seat B" || fail "11.3 the hub attributes session B to seat B"

  ONLINE=0
  wait_until 45 adbx shell ping -c 1 -W 3 claude.ai && ONLINE=1
  [ "$ONLINE" = 1 ] && note "the emulator reaches claude.ai" || note "the emulator has no internet (ping claude.ai failed): the page-load checks are SKIPped"

  profiles_has() { ui_has "id=tabrow-$1"; }
  profiles_not() { ui_dump && ! python3 -I "$LIB" has "id=tabrow-$1" <"$T/ui.xml"; }
  scroll_to() { # scroll_to TEXT: swipe up until a node with TEXT is on screen
    local i
    for i in 1 2 3 4 5 6 7 8; do
      ui_has "$1" && return 0
      adbx shell input swipe 540 1700 540 600 300
      sleep 1
    done
    ui_has "$1"
  }
  webview_loaded() { # a WebView with rendered text and no fallback strip
    ui_dump || return 1
    python3 -I "$LIB" has "claude.ai did not load" <"$T/ui.xml" && return 1
    python3 -I "$LIB" webcontent <"$T/ui.xml" >"$T/web.txt"
  }
  open_settings() {
    go_bar More
    wait_until 15 ui_has "Settings" && ui_tap "Settings"
    wait_until 15 ui_has "Unpair"
  }

  adbx logcat -c
  launch
  wait_until 30 ui_has "Needs you" || save_diag t0
  go_bar Accounts
  check "11.4 Accounts shows an Open claude.ai button for seat B" 40 ui_has "id=tab-$SEAT_B"
  ui_has "id=tab-$SEAT_A" && pass "11.4b and for seat A" || fail "11.4b and for seat A"

  # ---- A
  ui_tap "id=tab-$SEAT_A"
  check "11.5 Open claude.ai (A) opens the tab screen" 20 ui_has "id=tab-close"
  ui_has "alice-e2e" && pass "11.5a its title is alice-e2e" || fail "11.5a its title is alice-e2e"
  ui_has "id=tab-close" && ui_has "Open in browser" && pass "11.5b it is the tab screen (Open in browser, Close)" || fail "11.5b it is the tab screen"
  ui_has "id=tab-view" && pass "11.5c the native tab view is mounted" || fail "11.5c the native tab view is mounted"
  if [ "$ONLINE" = 1 ]; then
    if wait_until 60 webview_loaded; then pass "11.6 the WebView loads a claude.ai page" "$(head -c 160 "$T/web.txt")"
    else fail "11.6 the WebView loads a claude.ai page"; save_diag tabA; fi
  else
    echo "SKIP  11.6 the WebView loads a claude.ai page  [no internet on the emulator]"
  fi
  sleep 3
  [ -n "$(app_pid)" ] && pass "11.7 the app is still running with the tab open" || fail "11.7 the app is still running with the tab open"
  if adbx logcat -d | grep -q "FATAL EXCEPTION"; then fail "11.7b no crash in logcat" "$(adbx logcat -d | grep -A3 'FATAL EXCEPTION' | head -4)"; else pass "11.7b no crash in logcat"; fi
  ui_tap "id=tab-close"
  check "11.8 Close returns to Accounts" 15 ui_has "id=tab-$SEAT_A"

  # ---- B
  ui_tap "id=tab-$SEAT_B"
  check "11.9 Open claude.ai (B) opens the tab screen" 20 ui_has "id=tab-close"
  ui_has "bob-e2e" && pass "11.9a its title is bob-e2e" || fail "11.9a its title is bob-e2e"
  ui_has "alice-e2e" && fail "11.9b B's tab does not show A's name" || pass "11.9b B's tab does not show A's name"
  if [ "$ONLINE" = 1 ]; then
    if wait_until 60 webview_loaded; then pass "11.10 B's WebView loads a claude.ai page" "$(head -c 160 "$T/web.txt")"
    else fail "11.10 B's WebView loads a claude.ai page"; save_diag tabB; fi
  else
    echo "SKIP  11.10 B's WebView loads a claude.ai page  [no internet on the emulator]"
  fi
  ui_tap "id=tab-close"
  wait_until 15 ui_has "id=tab-$SEAT_A"

  # ---- profiles, in Settings
  open_settings || save_diag settings
  scroll_to "id=tabrow-$SEAT_A" || save_diag profiles
  if profiles_has "$SEAT_A"; then pass "11.11 profile sy-tab-<A> exists (Settings > Claude tabs lists it)"; else fail "11.11 profile sy-tab-<A> exists"; fi
  scroll_to "id=tabrow-$SEAT_B"
  if profiles_has "$SEAT_B"; then pass "11.12 profile sy-tab-<B> exists, separate from A"; else fail "11.12 profile sy-tab-<B> exists, separate from A"; fi
  adbx shell input keyevent KEYCODE_BACK
  adbx shell input keyevent KEYCODE_BACK

  # ---- Open in Claude on B's session
  go_bar Now
  wait_until 30 ui_has "Needs you"
  scroll_to "id=open-$SB_SESS"
  check "11.13 B's session shows Open in Claude on Now" 10 ui_has "id=open-$SB_SESS" || save_diag nowopen
  ui_tap "id=open-$SB_SESS"
  check "11.14 Open in Claude (B's session) opens a tab screen" 20 ui_has "id=tab-close"
  if ui_has "bob-e2e" && ! ui_has "alice-e2e"; then pass "11.14b its title is B's account (the route is /tabs/<seatB>)"; else fail "11.14b its title is B's account"; save_diag openin; fi
  ui_tap "id=tab-close"

  # ---- sign out A
  open_settings
  scroll_to "id=tab-signout-$SEAT_A"
  ui_tap "id=tab-signout-$SEAT_A"
  check "11.15 sign out asks for confirmation" 10 ui_has "Sign out of alice-e2e?"
  ui_tap "Sign out" || ui_tap "SIGN OUT"
  sleep 1
  for toast in "Signed out" "Close the tab first, then try again" "Could not clear the tab"; do ui_has "$toast" && note "the app says: $toast"; done
  check "11.16 sign out (A) removes profile sy-tab-<A>" 20 profiles_not "$SEAT_A" || save_diag signout
  scroll_to "id=tabrow-$SEAT_B"
  profiles_has "$SEAT_B" && pass "11.17 B's profile is untouched" || fail "11.17 B's profile is untouched"
  adbx shell input keyevent KEYCODE_BACK
  adbx shell input keyevent KEYCODE_BACK
  # a new process: the sign-out holds, and B still has its profile
  adbx shell am force-stop "$PKG"
  launch
  wait_until 40 ui_has "Needs you"
  open_settings
  scroll_to "id=tabrow-$SEAT_B"
  if profiles_has "$SEAT_B" && profiles_not "$SEAT_A"; then pass "11.18 after an app restart A's profile is still gone and B's is kept"; else fail "11.18 after an app restart A's profile is still gone and B's is kept"; save_diag restart; fi
  adbx shell input keyevent KEYCODE_BACK

  echo "SKIP  11.19 a non-claude link goes to the browser  [the hub drops any remote_url outside https://claude.ai/, so no session carries one; nothing on screen is a non-claude link]"
fi

# ================================================================ 12 analytics
if phase_on 12; then
  echo "== 12 analytics: More > Analytics shows the totals of a token row"
  if ! has_phone; then # phase 10 leaves the phone revoked: pair again
    pair_by_link
    wait_until 30 ui_has "Pair with 10.0.2.2:$PORT" && tap_pair
    check "12.0 the phone is paired again" 60 link_connected
  fi
  # 1M in + 100k out of claude-sonnet-5 = $3.00 at API list prices, 1 reply, today (UTC)
  "$T/bin/fakemachine" -hub "$(hub_origin)" -token "$MTOKEN" -machine "$MACHINE" -session "$(sid 21)" tokens >>"$T/fakemachine.log" 2>&1 \
    && pass "12.1 the fake machine posted a token_rows record" || fail "12.1 the fake machine posted a token_rows record"
  USD="$(api GET '/v1/analytics/daily?days=7' | python3 -I "$LIB" json totals.api_usd)"
  python3 -I -c "import sys;sys.exit(abs(float(sys.argv[1] or 0)-3) > 1e-6)" "$USD" && pass "12.2 the hub reports \$3.00 for the last 7 days" || fail "12.2 the hub reports \$3.00 for the last 7 days" "api_usd=$USD"
  launch
  wait_until 30 ui_has "Needs you" || save_diag ana0
  go_bar More
  check "12.3 More lists Analytics" 15 ui_has "id=more-analytics"
  ui_tap "id=more-analytics"
  check "12.4 the Analytics screen loads its totals" 30 ui_has "id=total-usd"
  ui_has '$3.00' && pass "12.5 API-equivalent cost reads \$3.00" || { fail "12.5 API-equivalent cost reads \$3.00"; save_diag ana; }
  ui_has "1.1M" && pass "12.6 Tokens reads 1.1M" || fail "12.6 Tokens reads 1.1M"
  ui_has "id=day-bars" && pass "12.7 the daily bars are drawn" || fail "12.7 the daily bars are drawn"
  ui_has "id=heatmap" && pass "12.8 the weekday by hour heatmap is drawn" || fail "12.8 the weekday by hour heatmap is drawn"
  adbx shell input keyevent KEYCODE_BACK
fi

# ================================================================ summary
echo
echo "== $NPASS passed, $NFAIL failed"
for f in "${FAILED[@]:-}"; do [ -n "$f" ] && echo "   FAILED: $f"; done
[ "$NFAIL" -eq 0 ] || { save_diag end; exit 1; }
exit 0
