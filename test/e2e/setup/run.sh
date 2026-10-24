#!/bin/sh
# Setup E2E runner (docs/spec/setup.md).
#
#   run.sh <fedora|ubuntu> [podman|docker] [-s PATTERN]   scenarios in a container
#   run.sh --native [-s PATTERN]                          same scenarios on this machine
#                                                         (macOS), temp HOME, no container
#
# Builds on the host (no Go in the images): scripts/release/build.sh for this machine's
# platform (version 0.0.0-e2e), the treesnap tool and, when it exists, the fake claude
# (test/e2e/setup/fakeclaude, built as "claude"). Output goes to dist/e2e/. Then every
# scenarios/*.sh matching PATTERN (default all) runs as a separate process; exit 77 = skipped.
# Scenario contract: lib/common.sh (WY_E2E_SRC, WY_E2E_OUT). Never touches the real HOME:
# scenarios build fixture homes in temp folders.
#
# Internal: --inside <out dir> runs the scenarios against an existing build (used in the
# container, where the repo is mounted read-only at /src and the build output at /e2e).
set -eu

# Never let the caller's Claude Code / wy / XDG settings reach the build or the scenarios (a
# leaked CLAUDE_CONFIG_DIR would point wy and the fake claude at the real folders).
# (XDG_RUNTIME_DIR is kept for the systemd container; lib/common.sh passes it on only there.)
for v in $(env | sed -n 's/^\([A-Za-z0-9_]*\)=.*/\1/p' | grep -E '^(CLAUDE[A-Za-z0-9_]*|WY_[A-Z_]*|XDG_[A-Z_]*|FAKE_[A-Z_]*)$'); do
	[ "$v" = XDG_RUNTIME_DIR ] || unset "$v"
done
here=$(cd "$(dirname "$0")" && pwd)
src=$(cd "$here/../../.." && pwd)
pattern=

die() { echo "run.sh: $*" >&2; exit 2; }

run_scenarios() { # <out dir>
	out=$1
	export WY_E2E_SRC=$src WY_E2E_OUT=$out
	# The fake claude (when built) is first on PATH, as in a real user's shell.
	PATH=$out/bin:$PATH
	export PATH
	passed=0; failed=0; skipped=0; failures=
	for s in "$here"/scenarios/*.sh; do
		[ -f "$s" ] || continue
		name=$(basename "$s" .sh)
		case $name in *$pattern*) ;; *) continue ;; esac
		echo "== $name"
		rc=0
		sh "$s" || rc=$?
		case $rc in
			0) passed=$((passed + 1)) ;;
			77) skipped=$((skipped + 1)) ;;
			*) failed=$((failed + 1)); failures="$failures $name" ;;
		esac
	done
	echo "scenarios: $passed passed, $failed failed, $skipped skipped${failures:+ (failed:$failures)}"
	[ "$failed" -eq 0 ]
}

build() { # <out dir> <goos> <goarch>
	out=$1; os=$2; arch=$3
	mkdir -p "$out/bin"
	echo "building release (${os}_${arch}) and tools into $out"
	WY_RELEASE_DIR=$out/release WY_RELEASE_TARGETS="${os}_${arch}" "$src/scripts/release/build.sh" 0.0.0-e2e >/dev/null
	(cd "$src" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -o "$out/bin/treesnap" ./test/e2e/setup/treesnap)
	if ls "$here"/fakeclaude/*.go >/dev/null 2>&1; then
		(cd "$src" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -o "$out/bin/claude" ./test/e2e/setup/fakeclaude)
	else
		echo "note: test/e2e/setup/fakeclaude not present; scenarios that need the fake claude will skip"
	fi
}

# systemd_pass: s6b (`wy setup --hub here`) in a Fedora container whose PID 1 is systemd
# (`podman run --systemd=always`), with the user manager of "me" started (no linger, so the
# linger warning shows). Docker cannot do this; the scenario is then reported as skipped.
systemd_pass() {
	if [ "$distro" != fedora ] || [ "$engine" != podman ]; then
		echo "== s6b-hub-here"; echo "s6b-hub-here: SKIP needs podman and the fedora image (systemd as PID 1)"; return 0
	fi
	echo "== s6b-hub-here (systemd container)"
	cid=$("$engine" run -d --systemd=always --security-opt label=disable --user root \
		-v "$src:/src:ro" -v "$out:/e2e:ro" "$image" /sbin/init 2>/dev/null) || {
		echo "s6b-hub-here: SKIP podman run --systemd=always is not available here"; return 0; }
	rc=0
	i=0
	until [ "$("$engine" exec --user root "$cid" systemctl is-system-running 2>/dev/null)" = running ] ||
		[ "$("$engine" exec --user root "$cid" systemctl is-system-running 2>/dev/null)" = degraded ]; do
		i=$((i + 1))
		if [ "$i" -gt 60 ]; then echo "s6b-hub-here: SKIP systemd did not come up"; "$engine" rm -f "$cid" >/dev/null; return 0; fi
		sleep 0.5
	done
	"$engine" exec --user root "$cid" systemctl start user@1000.service || true
	i=0
	until "$engine" exec --user root "$cid" test -S /run/user/1000/bus; do
		i=$((i + 1))
		if [ "$i" -gt 40 ]; then echo "s6b-hub-here: SKIP the user bus did not come up"; "$engine" rm -f "$cid" >/dev/null; return 0; fi
		sleep 0.5
	done
	"$engine" exec --user me -e WY_E2E_SYSTEMD=1 -e XDG_RUNTIME_DIR=/run/user/1000 \
		-e DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus \
		"$cid" /src/test/e2e/setup/run.sh --inside /e2e -s s6b || rc=$?
	"$engine" rm -f "$cid" >/dev/null 2>&1 || true
	return $rc
}

mode=
distro=
engine=
inside=
while [ $# -gt 0 ]; do
	case $1 in
		--native) mode=native; shift ;;
		--inside) mode=inside; inside=${2:?--inside needs a folder}; shift 2 ;;
		-s) pattern=${2:?-s needs a pattern}; shift 2 ;;
		fedora|ubuntu) mode=container; distro=$1; shift ;;
		podman|docker) engine=$1; shift ;;
		*) die "usage: run.sh <fedora|ubuntu> [podman|docker] [-s PATTERN] | --native [-s PATTERN]" ;;
	esac
done
[ -n "$mode" ] || die "usage: run.sh <fedora|ubuntu> [podman|docker] [-s PATTERN] | --native [-s PATTERN]"

case $mode in
	inside)
		run_scenarios "$inside"
		;;
	native)
		case $(uname -s) in Linux) os=linux ;; Darwin) os=darwin ;; *) die "unsupported OS" ;; esac
		arch=$(go env GOARCH)
		out=$src/dist/e2e-native
		rm -rf "$out"
		build "$out" "$os" "$arch"
		run_scenarios "$out"
		;;
	container)
		if [ -z "$engine" ]; then
			if command -v podman >/dev/null 2>&1; then engine=podman
			elif command -v docker >/dev/null 2>&1; then engine=docker
			else die "neither podman nor docker found; use --native"; fi
		fi
		arch=$(go env GOARCH)
		out=$src/dist/e2e-$distro
		rm -rf "$out"
		build "$out" linux "$arch"
		image=wy-e2e-$distro
		"$engine" build -q -f "$here/Containerfile.$distro" -t "$image" "$here" >/dev/null
		# label=disable: no SELinux relabelling of the real checkout; both mounts read-only.
		status=0
		"$engine" run --rm --security-opt label=disable \
			-v "$src:/src:ro" -v "$out:/e2e:ro" \
			"$image" /src/test/e2e/setup/run.sh --inside /e2e ${pattern:+-s "$pattern"} || status=$?
		# The `--hub here` scenario needs systemd as PID 1: Fedora with podman only.
		case $pattern in *s6*|'') systemd_pass || status=$? ;; esac
		exit $status
		;;
esac
