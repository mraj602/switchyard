#!/bin/sh
# Switchyard installer (docs/spec/setup.md 1.2).
#
#   curl -fsSL https://github.com/mraj602/switchyard/releases/latest/download/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- [--version 0.3.0] [--bin-dir DIR] [--no-setup] [-- <sy setup args>]
#
# Puts a checksum-verified `sy` in ~/.local/bin and nothing else: no shell profile, no PATH,
# no Claude folder. Then runs `sy setup` when a terminal is available, unless --no-setup.
# When the gh CLI is installed it also verifies the archive's build attestation (who built it);
# without gh only the checksum is checked, which shows the download is intact but not its origin.
#
# Exit codes: 0 ok, 2 unsupported or usage, 3 verification failed, 4 download failed.
# Environment: SY_INSTALL_BASE_URL (mirror/tests; https, or http on a loopback host only),
# SY_VERIFY_ATTESTATION=1 (fail when gh is missing) or =0 (do not check the attestation).
set -eu

OWNER=mraj602
BASE=${SY_INSTALL_BASE_URL:-https://github.com/$OWNER/switchyard/releases}
BASE=${BASE%/}

say() { printf '%s\n' "$*"; }
err() { printf 'install.sh: %s\n' "$*" >&2; }
die() { code=$1; shift; err "$*"; exit "$code"; }

usage() {
	cat <<'USAGE'
usage: install.sh [--version VER] [--bin-dir DIR] [--no-setup] [-- <sy setup args>]
  --version VER   install this version (with or without a leading v); default: latest
  --bin-dir DIR   install into DIR (default: $HOME/.local/bin)
  --no-setup      only install sy; do not run `sy setup`
  --              everything after goes to `sy setup`
USAGE
}

version=latest
bindir=
nosetup=0
while [ $# -gt 0 ]; do
	case $1 in
		--version)
			[ $# -ge 2 ] || { usage >&2; exit 2; }
			version=${2#v}; shift 2 ;;
		--version=*) version=${1#--version=}; version=${version#v}; shift ;;
		--bin-dir)
			[ $# -ge 2 ] || { usage >&2; exit 2; }
			bindir=$2; shift 2 ;;
		--bin-dir=*) bindir=${1#--bin-dir=}; shift ;;
		--no-setup) nosetup=1; shift ;;
		-h|--help) usage; exit 0 ;;
		--) shift; break ;;
		*) err "unknown option: $1"; usage >&2; exit 2 ;;
	esac
done
case $version in
	''|*[!0-9A-Za-z.+-]*) die 2 "bad version: $version" ;;
esac

[ -n "${HOME:-}" ] || die 2 "HOME is not set"
[ -n "$bindir" ] || bindir=$HOME/.local/bin

# 1. OS and architecture.
case $(uname -s) in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die 2 "unsupported OS $(uname -s), build from source" ;;
esac
case $(uname -m) in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) die 2 "unsupported architecture $(uname -m)" ;;
esac
if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
	[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64 # a shell running under Rosetta on Apple silicon
fi

# 2. The base must be https; plain http only to a loopback host (the tests' local server).
loopback_http=0
case $BASE in
	https://*) ;;
	http://*)
		auth=${BASE#http://}; auth=${auth%%/*}
		case $auth in *@*) die 2 "SY_INSTALL_BASE_URL must not carry credentials" ;; esac
		host=${auth%%:*}
		case $auth in "["*) host=${auth%%]*}"]" ;; esac
		ok=0
		case $host in
			localhost|"[::1]") ok=1 ;;
			127.*[0-9]) case $host in *[!0-9.]*) ;; *) ok=1 ;; esac ;;
		esac
		[ "$ok" = 1 ] || die 2 "SY_INSTALL_BASE_URL must be https (http is allowed only for a loopback host)"
		loopback_http=1 ;;
	*) die 2 "SY_INSTALL_BASE_URL must be an https URL" ;;
esac

# 3. Downloader: https only (no downgrade by redirect), TLS 1.2 or newer.
if command -v curl >/dev/null 2>&1; then
	if [ "$loopback_http" = 1 ]; then
		fetch() { curl -fsSL --proto '=http' --proto-redir '=http' -o "$2" "$1"; }
	else
		fetch() { curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 -o "$2" "$1"; }
	fi
elif command -v wget >/dev/null 2>&1; then
	if [ "$loopback_http" = 1 ]; then
		fetch() { wget -qO "$2" "$1"; }
	else
		fetch() { wget -qO "$2" --https-only --secure-protocol=TLSv1_2 "$1"; }
	fi
else
	die 2 "need curl or wget"
fi

# 4. Where the files are. No GitHub API call, so no rate limit.
if [ "$version" = latest ]; then
	dl=$BASE/latest/download
else
	dl=$BASE/download/v$version
fi

# 5. Download into a private temp folder, removed on exit.
tmp=$(mktemp -d "${TMPDIR:-/tmp}/sy-install.XXXXXX") || die 4 "cannot create a temp folder"
newfile=
cleanup() {
	[ -z "$newfile" ] || rm -f "$newfile"
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

fetch "$dl/SHA256SUMS" "$tmp/SHA256SUMS" || die 4 "download failed: $dl/SHA256SUMS"

if [ "$version" = latest ]; then
	archive=$(awk -v suffix="_${os}_${arch}.tar.gz" '
		{ n = $2; sub(/^\*/, "", n) }
		n ~ /^sy_/ && substr(n, length(n) - length(suffix) + 1) == suffix { print n; exit }
	' "$tmp/SHA256SUMS")
	[ -n "$archive" ] || die 3 "SHA256SUMS lists no archive for ${os}_${arch}"
	version=${archive#sy_}
	version=${version%"_${os}_${arch}.tar.gz"}
else
	archive=sy_${version}_${os}_${arch}.tar.gz
fi

fetch "$dl/$archive" "$tmp/$archive" || die 4 "download failed: $dl/$archive"

# 6. Verify. The archive must have a line in SHA256SUMS and the digest must match.
want=$(awk -v n="$archive" '{ f = $2; sub(/^\*/, "", f) } f == n { print $1; exit }' "$tmp/SHA256SUMS")
[ -n "$want" ] || die 3 "$archive is not listed in SHA256SUMS; nothing installed"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	got=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
elif command -v openssl >/dev/null 2>&1; then
	got=$(openssl dgst -sha256 "$tmp/$archive" | awk '{print $NF}')
else
	die 3 "no sha256sum, shasum or openssl to verify with; nothing installed"
fi
[ "$got" = "$want" ] || die 3 "checksum mismatch for $archive; nothing installed"
# The build attestation shows the archive was built by this repository's release workflow,
# which a checksum from the same release cannot. Checked whenever gh is installed.
proof="checksum ok"
note=
case ${SY_VERIFY_ATTESTATION:-} in
	0) note="build attestation not checked (SY_VERIFY_ATTESTATION=0); the checksum shows the download is intact, not who built it" ;;
	*)
		if command -v gh >/dev/null 2>&1; then
			gh attestation verify "$tmp/$archive" --repo "$OWNER/switchyard" \
				--signer-workflow "$OWNER/switchyard/.github/workflows/release.yml" >/dev/null 2>&1 ||
				die 3 "attestation verification failed for $archive; nothing installed (gh must be signed in and reach github.com; SY_VERIFY_ATTESTATION=0 skips this check)"
			proof="checksum ok, build attestation ok"
		elif [ "${SY_VERIFY_ATTESTATION:-}" = 1 ]; then
			die 3 "SY_VERIFY_ATTESTATION=1 needs the gh CLI, which is not installed; nothing installed"
		else
			note="build attestation not checked because the gh CLI is not installed; the checksum shows the download is intact, not who built it. Install gh (https://cli.github.com) and run the installer again to check it"
		fi ;;
esac

# 7. Extract only sy, then replace atomically (a running `sy hook` keeps its old inode).
tar -xzf "$tmp/$archive" -C "$tmp" sy || die 3 "$archive does not contain sy"
if [ ! -d "$bindir" ]; then # only a folder made here gets a mode; an existing one is left alone
	mkdir -p "$bindir"
	chmod 0755 "$bindir" 2>/dev/null || true
fi
newfile=$(mktemp "$bindir/.sy.XXXXXX") || die 4 "cannot create a temp file in $bindir"
cp "$tmp/sy" "$newfile"
chmod 0755 "$newfile"
mv -f "$newfile" "$bindir/sy"
newfile=

# 8. Report. PATH is never edited: print the line to add.
shown=$bindir
pathdir=$bindir # for the PATH line: "~" is not expanded inside double quotes, $HOME is
case $bindir in "$HOME"/*) shown="~${bindir#"$HOME"}"; pathdir="\$HOME${bindir#"$HOME"}" ;; esac
say "sy $version downloaded, $proof, installed to $shown/sy"
[ -z "$note" ] || say "note: $note"
case ":${PATH:-}:" in
	*":$bindir:"*) ;;
	*)
		say "$shown is not on your PATH. Add it:"
		case $(basename "${SHELL:-sh}") in
			fish) say "  fish_add_path $shown" ;;
			zsh) say "  echo 'export PATH=\"$pathdir:\$PATH\"' >> ~/.zshrc" ;;
			bash) say "  echo 'export PATH=\"$pathdir:\$PATH\"' >> ~/.bashrc" ;;
			*) say "  export PATH=\"$pathdir:\$PATH\"" ;;
		esac
		;;
esac

# 9. Hand over to `sy setup` when there is a terminal.
if [ "$nosetup" = 1 ]; then
	exit 0
fi
if (: </dev/tty) 2>/dev/null; then
	trap - EXIT
	cleanup
	exec "$bindir/sy" setup "$@" </dev/tty
fi
say "run: sy setup"
