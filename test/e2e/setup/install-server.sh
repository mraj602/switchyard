#!/bin/sh
# A local HTTP server that looks like GitHub Releases, for install.sh and install.ps1.
#
#   install-server.sh start <release-dir> <state-dir>   serve; prints the base URL
#   install-server.sh stop  <state-dir>                 stop it
#
# <release-dir> is the output of scripts/release/build.sh (archives, SHA256SUMS, install
# scripts). The server root gets the layout install.sh expects, using symlinks into the
# release folder so nothing is copied:
#   /install.sh  /install.ps1
#   /latest/download/<file>
#   /download/v<ver>/<file>
# so WY_INSTALL_BASE_URL=<base URL> makes install.sh download from here. To test a tampered
# release, copy the release folder, change it, and start a second server on the copy.
# Needs python3 (stdlib http.server) and nothing else. Loopback only.
set -eu

die() { echo "install-server.sh: $*" >&2; exit 2; }

cmd=${1:-}
case $cmd in
	start)
		[ $# -eq 3 ] || die "usage: install-server.sh start <release-dir> <state-dir>"
		rel=$(cd "$2" && pwd) || die "no such folder: $2"
		mkdir -p "$3"
		state=$(cd "$3" && pwd)
		[ -f "$rel/SHA256SUMS" ] || die "$rel/SHA256SUMS missing"
		ver=$(awk '$2 ~ /^[a-z]+_[^_]+_(linux|darwin|windows)_/ { n = $2; sub(/^[a-z]+_/, "", n); sub(/_(linux|darwin|windows)_.*/, "", n); print n; exit }' "$rel/SHA256SUMS")
		[ -n "$ver" ] || die "cannot read the version from $rel/SHA256SUMS"
		root=$state/root
		rm -rf "$root"
		mkdir -p "$root/latest" "$root/download"
		ln -s "$rel" "$root/latest/download"
		ln -s "$rel" "$root/download/v$ver"
		for f in install.sh install.ps1; do
			[ ! -f "$rel/$f" ] || ln -s "$rel/$f" "$root/$f"
		done
		port=$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
		python3 -I -m http.server "$port" --bind 127.0.0.1 --directory "$root" \
			>"$state/server.log" 2>&1 &
		echo $! > "$state/pid"
		i=0
		until python3 -I -c 'import socket, sys; socket.create_connection(("127.0.0.1", int(sys.argv[1])), 1).close()' "$port" 2>/dev/null; do
			i=$((i + 1))
			[ "$i" -lt 100 ] || die "server did not start; see $state/server.log"
			sleep 0.1
		done
		echo "http://127.0.0.1:$port" | tee "$state/url"
		;;
	stop)
		[ $# -eq 2 ] || die "usage: install-server.sh stop <state-dir>"
		if [ -f "$2/pid" ]; then
			kill "$(cat "$2/pid")" 2>/dev/null || true
			rm -f "$2/pid"
		fi
		;;
	*) die "usage: install-server.sh start <release-dir> <state-dir> | stop <state-dir>" ;;
esac
