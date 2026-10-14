# Helpers for the setup E2E scenarios. Source it from a scenario:
#   . "$(dirname "$0")/../lib/common.sh"
#
# Environment (set by run.sh, native or in a container):
#   SY_E2E_SRC     repo root (scripts, fixtures)
#   SY_E2E_OUT     build output: $SY_E2E_OUT/release (build.sh output),
#                  $SY_E2E_OUT/bin/{treesnap, claude (the fake, when built)}
# A scenario gets a private temp folder (SY_E2E_TMP, removed on exit), starts from a fixture
# home, runs as the current (non-root in containers) user with HOME pointing into the temp
# folder, and never touches the real home.
set -eu

: "${SY_E2E_SRC:?run scenarios through test/e2e/setup/run.sh}"
: "${SY_E2E_OUT:?run scenarios through test/e2e/setup/run.sh}"
# Scrub the environment first: a scenario must never see the caller's Claude Code or sy
# settings (a leaked CLAUDE_CONFIG_DIR would make sy and the fake claude act on real folders).
# The one exception: run.sh passes the user systemd bus of the systemd container (s6b) in
# SY_E2E_SYSTEMD=1 + the two variables; they reach sy only through in_home's SD_ENV.
# The user manager of that container looks for units in the real HOME of its user, so s6b
# builds its fixture in $HOME there: allowed only inside a container (SD_HOME).
SD_ENV=
SD_HOME=
if [ "${SY_E2E_SYSTEMD:-}" = 1 ] && { [ -f /run/.containerenv ] || [ -f /.dockerenv ]; }; then SD_HOME=$HOME; fi
if [ "${SY_E2E_SYSTEMD:-}" = 1 ]; then SD_ENV="XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-} DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS:-}"; fi
for v in $(env | sed -n 's/^\([A-Za-z0-9_]*\)=.*/\1/p' | grep -E '^(CLAUDE[A-Za-z0-9_]*|SY_[A-Z_]*|XDG_[A-Z_]*|FAKE_[A-Z_]*)$'); do
	case $v in SY_E2E_SRC|SY_E2E_OUT) ;; *) unset "$v" ;; esac
done
E2E=$SY_E2E_SRC/test/e2e/setup
RELEASE=$SY_E2E_OUT/release
TOOLS=$SY_E2E_OUT/bin
SCENARIO=${SCENARIO:-$(basename "$0" .sh)}

# pwd -P: the physical path (macOS TMPDIR lives behind the /var symlink), so the paths sy prints and the paths the scenarios build agree.
SY_E2E_TMP=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/sy-e2e.XXXXXX")" && pwd -P)
SERVERS=
cleanup() {
	for s in $SERVERS; do "$E2E/install-server.sh" stop "$s" 2>/dev/null || true; done
	# A straggler (a detached child of `sy hook`) may still write while the tree is removed
	# ("Directory not empty" on macOS): retry, and never let cleanup change the exit status.
	for _ in 1 2 3 4 5; do rm -rf "$SY_E2E_TMP" 2>/dev/null && break; sleep 1; done
	return 0
}
trap 'rc=$?; cleanup; exit $rc' EXIT
trap 'exit 130' INT TERM

FAILS=0
CHECKS=0
pass() { CHECKS=$((CHECKS + 1)); printf '  ok   %s\n' "$*"; }
fail() { CHECKS=$((CHECKS + 1)); FAILS=$((FAILS + 1)); printf '  FAIL %s\n' "$*" >&2; }
note() { printf '%s: %s\n' "$SCENARIO" "$*"; }
skip_scenario() { printf '%s: SKIP %s\n' "$SCENARIO" "$*"; trap - EXIT; cleanup; exit 77; }

# finish: print the verdict and exit non-zero when a check failed (call last).
finish() {
	if [ "$FAILS" -ne 0 ]; then
		printf '%s: FAILED (%d of %d checks)\n' "$SCENARIO" "$FAILS" "$CHECKS" >&2
		exit 1
	fi
	printf '%s: ok (%d checks)\n' "$SCENARIO" "$CHECKS"
}

# assert_eq <what> <want> <got>
assert_eq() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1: want [$2] got [$3]"; fi; }
# assert_rc <what> <want rc> <got rc>
assert_rc() { assert_eq "$1 (exit code)" "$2" "$3"; if [ "$2" != "$3" ] && [ -f "$ERR" ]; then tail -n 15 "$ERR" >&2; fi; }
assert_file() { if [ -f "$1" ]; then pass "exists: ${2:-$1}"; else fail "missing: ${2:-$1}"; fi; }
assert_no_file() { if [ ! -e "$1" ] && [ ! -L "$1" ]; then pass "absent: ${2:-$1}"; else fail "should not exist: ${2:-$1}"; fi; }
# assert_contains <what> <needle> <haystack>
assert_contains() { case $3 in *"$2"*) pass "$1" ;; *) fail "$1: [$2] not in [$3]" ;; esac; }

# new_home <name> <builder>: build homes/<builder>.sh into $SY_E2E_TMP/<name>/home; prints the path.
new_home() {
	h=$SY_E2E_TMP/$1/home
	mkdir -p "$h"
	"$E2E/homes/$2.sh" "$h"
	echo "$h"
}

# Ignored by default: sy's state folder (and its parent, which setup creates) and the sy binary.
SNAP_IGNORE=.local/state,.local/bin/sy
# snap <home> <file> [treesnap flags]: snapshot a tree (default ignores of treesnap apply:
# the state folder and ~/.local/bin/sy). Add -claude-json to compare .claude.json by
# oauthAccount and mcpServers only.
snap() {
	d=$1; f=$2; shift 2
	"$TOOLS/treesnap" snap -ignore "$SNAP_IGNORE" "$@" "$d" "$f"
}
# snap_equal <what> <home> <file> [treesnap flags]: assert the tree still equals the snapshot.
snap_equal() {
	w=$1; d=$2; f=$3; shift 3
	if out=$("$TOOLS/treesnap" check -ignore "$SNAP_IGNORE" "$@" "$d" "$f"); then pass "tree unchanged: $w"
	else fail "tree changed: $w"; printf '%s\n' "$out" >&2; fi
}

# serve <name> <release dir>: start the local release server; sets BASE_URL.
serve() {
	st=$SY_E2E_TMP/server-$1
	BASE_URL=$("$E2E/install-server.sh" start "$2" "$st")
	SERVERS="$SERVERS $st"
}

# run_install <home> <args...>: pipe install.sh from the local server into sh, as a user would
# (curl | sh), with HOME=<home> and the server as the release base. Output goes to
# $SY_E2E_TMP/install.out (stdout+stderr); the exit code is left in $rc.
# The shell runs through notty (no controlling terminal), so the setup step never starts:
# it prints the "run: sy setup" hint unless --no-setup is given.
run_install() {
	h=$1; shift
	mkdir -p "$SY_E2E_TMP/tmp"
	rc=0
	(
		export HOME=$h SY_INSTALL_BASE_URL=$BASE_URL TMPDIR=$SY_E2E_TMP/tmp
		curl -fsSL "$BASE_URL/install.sh" | notty sh -s -- "$@"
	) >"$SY_E2E_TMP/install.out" 2>&1 || rc=$?
}

# notty <command...>: run without a controlling terminal, so /dev/tty cannot be opened
# (portable setsid: python on macOS, which has no setsid(1)).
notty() { python3 -I -c 'import os, sys; os.setsid(); os.execvp(sys.argv[1], sys.argv[1:])' "$@"; }

# ---- running sy as the person in a fixture home ----------------------------------------
# Every call goes through in_home: HOME is the fixture, the fake claude and the installed sy
# come first on PATH, no sy/XDG/Claude variable is set, stdin is /dev/null unless given.
# $H is the current fixture home (set it, or use new_home). Calls never touch the real HOME.
FAKE_LOG=$SY_E2E_TMP/fake-claude.log
OUT=$SY_E2E_TMP/out.txt
ERR=$SY_E2E_TMP/err.txt
rc=0

# SY_E2E_ENV: extra VAR=value words for in_home (for example FAKE_CLAUDE_FAIL=plugin-install).
in_home() { # <command...>
	[ -n "${H:-}" ] || { echo "in_home: H not set" >&2; return 99; }
	case $H in "$SY_E2E_TMP"/*) ;; "${SD_HOME:-/nonexistent}") ;; *) echo "in_home: refusing $H (not under the scenario temp folder)" >&2; return 99 ;; esac
	mkdir -p "$SY_E2E_TMP/tmp"
	# shellcheck disable=SC2086
	( cd "$H" && exec env -i HOME="$H" PATH="$TOOLS:$H/.local/bin:$PATH" TERM=dumb USER="${USER:-me}" \
		LANG="${LANG:-C}" TMPDIR="$SY_E2E_TMP/tmp" FAKE_CLAUDE_LOG="$FAKE_LOG" $SD_ENV ${SY_E2E_ENV:-} "$@" )
}

# sy_bg <pid file> <out file> <args...>: start $H's sy in the background; the pid file gets the
# pid of sy itself (the subshell execs, so it is the same process), for kill -9.
sy_bg() {
	pf=$1; of=$2; shift 2
	mkdir -p "$SY_E2E_TMP/tmp"
	# shellcheck disable=SC2086
	( cd "$H" && exec env -i HOME="$H" PATH="$TOOLS:$H/.local/bin:$PATH" TERM=dumb USER="${USER:-me}" \
		LANG="${LANG:-C}" TMPDIR="$SY_E2E_TMP/tmp" FAKE_CLAUDE_LOG="$FAKE_LOG" $SD_ENV ${SY_E2E_ENV:-} sy "$@" ) \
		>"$of" 2>"$of.err" </dev/null &
	echo $! > "$pf"
}

# sy <args...>: run $H's sy; stdout to $OUT, stderr to $ERR, exit code in $rc.
sy() { rc=0; in_home sy "$@" >"$OUT" 2>"$ERR" </dev/null || rc=$?; }
# sy_in <stdin file> <args...>
sy_in() { f=$1; shift; rc=0; in_home sy "$@" >"$OUT" 2>"$ERR" <"$f" || rc=$?; }

# install_sy <home>: the built sy into <home>/.local/bin (what install.sh would do).
install_sy() {
	mkdir -p "$1/.local/bin"
	tar -xzf "$(ls "$RELEASE"/sy_*_"$(go_os)"_"$(go_arch)".tar.gz | head -n 1)" -C "$1/.local/bin" sy
	chmod 0755 "$1/.local/bin/sy"
}
go_os() { case $(uname -s) in Darwin) echo darwin ;; *) echo linux ;; esac; }
go_arch() { case $(uname -m) in x86_64|amd64) echo amd64 ;; aarch64|arm64) echo arm64 ;; esac; }

# jget <file> <python expression over d>: JSON access without jq (python3 is needed anyway).
jget() { python3 -I -c 'import json, sys
d = json.load(open(sys.argv[1]))
v = eval(sys.argv[2])
print(v if isinstance(v, str) else json.dumps(v, sort_keys=True))' "$1" "$2"; }

sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
# fake_calls <fixed string>: number of fake-claude log lines containing it.
fake_calls() { if [ -f "$FAKE_LOG" ]; then grep -cF -- "$1" "$FAKE_LOG" || true; else echo 0; fi; }
# mode_of <path>: octal permission bits.
mode_of() { if stat -c %a "$1" >/dev/null 2>&1; then stat -c %a "$1"; else stat -f %Lp "$1"; fi; }
# links_of <dir> [maxdepth]: every symlink under <dir> with its raw target, sorted.
links_of() {
	find "$1" -maxdepth "${2:-2}" -type l | LC_ALL=C sort | while read -r l; do printf '%s -> %s\n' "$l" "$(readlink "$l")"; done
}
# golden_check <what> <golden file> <actual file>: equal, or (BLESS=1) rewrite the golden.
golden_check() {
	if [ "${BLESS:-}" = 1 ]; then mkdir -p "$(dirname "$2")"; cp "$3" "$2"; pass "golden written: $1"; return; fi
	if [ ! -f "$2" ]; then fail "golden missing: $2 (run with BLESS=1)"; return; fi
	if d=$(diff -u "$2" "$3"); then pass "matches golden: $1"; else fail "differs from golden: $1"; printf '%s\n' "$d" | head -40 >&2; fi
}
