#!/bin/sh
# S8 install script: only
# install.sh against the local release server. Covers: latest and --version installs, --bin-dir,
# a tampered archive, a missing checksum line, the non-TTY hint, and replacing wy while a
# process of the old one runs (a sleeping fake, then a real `wy hook` kept alive on stdin).
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
VER=$(sed -n 's/^[0-9a-f]*  [a-z]*_\([^_]*\)_.*/\1/p' "$RELEASE/SHA256SUMS" | head -n 1)
note "release $VER"

serve main "$RELEASE"
MAIN_URL=$BASE_URL
H=$(new_home good fresh)
snap "$H" "$WY_E2E_TMP/before.json" -ignore ''

# 1. Latest, --no-setup: installs wy and nothing else.
run_install "$H" --no-setup
assert_rc "install latest" 0 $rc
out=$(cat "$WY_E2E_TMP/install.out")
assert_contains "reports version and checksum" "wy $VER downloaded, checksum ok, installed to ~/.local/bin/wy" "$out"
assert_contains "prints the PATH line, does not edit a profile" "is not on your PATH" "$out"
assert_file "$H/.local/bin/wy" "~/.local/bin/wy"
[ -x "$H/.local/bin/wy" ] && pass "wy is executable" || fail "wy is not executable"
assert_contains "wy --version has the version" "$VER" "$("$H/.local/bin/wy" --version 2>&1)"
extra=$("$TOOLS/treesnap" check -ignore '' "$H" "$WY_E2E_TMP/before.json" | sed '1d' | grep -v '^  \.local/bin/wy: unexpected file$' || true)
assert_eq "nothing but ~/.local/bin/wy was added (no temp file, no profile, no Claude folder)" "" "$extra"
assert_eq "~/.local/bin holds only wy" "wy" "$(ls -A "$H/.local/bin")"

# 2. Without --no-setup and without a terminal: prints the hint, exit 0, does not start setup.
run_install "$H"
assert_rc "install without a terminal" 0 $rc
assert_contains "non-TTY prints the hint" "run: wy setup" "$(cat "$WY_E2E_TMP/install.out")"

# 3. --version (with and without v), unknown version, --bin-dir.
H2=$(new_home pinned fresh)
run_install "$H2" --version "$VER" --no-setup
assert_rc "--version $VER" 0 $rc
run_install "$H2" --version "v$VER" --no-setup
assert_rc "--version v$VER" 0 $rc
H3=$(new_home missing fresh)
run_install "$H3" --version 9.9.9 --no-setup
assert_rc "--version 9.9.9 (no such release: download fails)" 4 $rc
assert_no_file "$H3/.local/bin/wy" "wy after a failed download"
run_install "$H3" --bin-dir "$WY_E2E_TMP/elsewhere/bin" --no-setup
assert_rc "--bin-dir" 0 $rc
assert_file "$WY_E2E_TMP/elsewhere/bin/wy" "wy in the --bin-dir"
assert_no_file "$H3/.local/bin/wy" "wy in the default folder when --bin-dir is given"
run_install "$H3" --bogus
assert_rc "unknown option" 2 $rc

# 3b. The base URL must be https (http only for loopback); a bin dir that exists keeps its mode.
H8=$(new_home policy fresh)
INSTALL_BASE_OVERRIDE=http://example.invalid/releases run_install "$H8" --no-setup
assert_rc "plain http to a remote host is refused" 2 $rc
assert_contains "says why" "must be https" "$(cat "$WY_E2E_TMP/install.out")"
INSTALL_BASE_OVERRIDE=http://127.0.0.1.example.invalid/releases run_install "$H8" --no-setup
assert_rc "a look-alike loopback name is refused" 2 $rc
unset INSTALL_BASE_OVERRIDE
mkdir -p "$WY_E2E_TMP/modebin"
chmod 0711 "$WY_E2E_TMP/modebin"
run_install "$H8" --bin-dir "$WY_E2E_TMP/modebin" --no-setup
assert_rc "install into an existing bin dir" 0 $rc
assert_eq "an existing bin dir keeps its mode" "711" "$(stat -c %a "$WY_E2E_TMP/modebin" 2>/dev/null || stat -f %Lp "$WY_E2E_TMP/modebin")"

# 3c. Build attestation: checked when gh is installed (a fake gh here), and said so when not.
mkdir -p "$WY_E2E_TMP/gh-ok" "$WY_E2E_TMP/gh-bad"
printf '#!/bin/sh\necho "$@" > "%s/gh-args"\nexit 0\n' "$WY_E2E_TMP" > "$WY_E2E_TMP/gh-ok/gh"
printf '#!/bin/sh\nexit 1\n' > "$WY_E2E_TMP/gh-bad/gh"
chmod 0755 "$WY_E2E_TMP/gh-ok/gh" "$WY_E2E_TMP/gh-bad/gh"
H9=$(new_home attest fresh)
WY_VERIFY_ATTESTATION= INSTALL_PATH_PREFIX=$WY_E2E_TMP/gh-ok run_install "$H9" --no-setup
unset WY_VERIFY_ATTESTATION INSTALL_PATH_PREFIX
assert_rc "gh present, attestation ok" 0 $rc
assert_contains "reports the attestation" "checksum ok, build attestation ok" "$(cat "$WY_E2E_TMP/install.out")"
assert_contains "gh was asked to verify" "attestation verify" "$(cat "$WY_E2E_TMP/gh-args")"
H10=$(new_home attestbad fresh)
WY_VERIFY_ATTESTATION= INSTALL_PATH_PREFIX=$WY_E2E_TMP/gh-bad run_install "$H10" --no-setup
unset WY_VERIFY_ATTESTATION INSTALL_PATH_PREFIX
assert_rc "gh present, attestation fails" 3 $rc
assert_no_file "$H10/.local/bin/wy" "wy after a failed attestation"
if ! command -v gh >/dev/null 2>&1; then
	H11=$(new_home nogh fresh)
	WY_VERIFY_ATTESTATION= run_install "$H11" --no-setup
	unset WY_VERIFY_ATTESTATION
	assert_rc "no gh: installs on the checksum" 0 $rc
	assert_contains "says the attestation was not checked" "gh CLI is not installed" "$(cat "$WY_E2E_TMP/install.out")"
fi

# 4. Tampered archive: exit 3, nothing installed.
cp -R "$RELEASE" "$WY_E2E_TMP/tampered"
for f in "$WY_E2E_TMP"/tampered/wy_*.tar.gz; do printf 'x' >> "$f"; done
serve tampered "$WY_E2E_TMP/tampered"
H4=$(new_home tampered fresh)
run_install "$H4" --no-setup
assert_rc "tampered archive" 3 $rc
assert_contains "says checksum mismatch" "checksum mismatch" "$(cat "$WY_E2E_TMP/install.out")"
assert_no_file "$H4/.local/bin/wy" "wy after a tampered archive"
assert_eq "temp folders removed" "" "$(ls -A "$WY_E2E_TMP/tmp")"

# 5. Archive without a SHA256SUMS line: exit 3, nothing installed.
cp -R "$RELEASE" "$WY_E2E_TMP/nosum"
sed 's/\.tar\.gz$/.tar.gz.bak/' "$WY_E2E_TMP/nosum/SHA256SUMS" > "$WY_E2E_TMP/nosum/SHA256SUMS.new"
mv "$WY_E2E_TMP/nosum/SHA256SUMS.new" "$WY_E2E_TMP/nosum/SHA256SUMS"
serve nosum "$WY_E2E_TMP/nosum"
H5=$(new_home nosum fresh)
run_install "$H5" --version "$VER" --no-setup
assert_rc "no checksum line" 3 $rc
assert_no_file "$H5/.local/bin/wy" "wy without a checksum line"

# 6. Replacing wy while a process of the old one is running: succeeds, the old one finishes.
BASE_URL=$MAIN_URL
H6=$(new_home replace fresh)
cat > "$H6/.local/bin/wy" <<EOF
#!/bin/sh
sleep 2
echo finished > "$WY_E2E_TMP/old-wy-done"
EOF
chmod 0755 "$H6/.local/bin/wy"
"$H6/.local/bin/wy" &
oldpid=$!
sleep 0.3
run_install "$H6" --no-setup
assert_rc "replace a running wy" 0 $rc
assert_contains "wy was replaced by the release binary" "$VER" "$("$H6/.local/bin/wy" --version 2>&1)"
wait $oldpid || true
assert_file "$WY_E2E_TMP/old-wy-done" "the old process finished"

# 7. The same with the real wy: a `wy hook` waiting for its payload on stdin keeps running
# from the replaced inode and finishes normally.
H7=$(new_home hook fresh)
run_install "$H7" --no-setup
assert_rc "install" 0 $rc
HOLD=$WY_E2E_TMP/hold
mkfifo "$HOLD"
( cd "$H7" && exec env -i HOME="$H7" PATH="$H7/.local/bin:$PATH" TERM=dumb TMPDIR="$WY_E2E_TMP/tmp" \
	sh -c 'wy hook Stop <"$1" >"$2" 2>&1; echo $? > "$3"' sh "$HOLD" "$WY_E2E_TMP/hook.out" "$WY_E2E_TMP/hook.rc" ) &
hookpid=$!
exec 3>"$HOLD"
sleep 0.5
run_install "$H7" --no-setup
assert_rc "replace wy while a hook process runs" 0 $rc
assert_contains "the new wy runs" "$VER" "$("$H7/.local/bin/wy" --version 2>&1)"
printf '{"hook_event_name":"Stop","session_id":"s8"}\n' >&3
exec 3>&-
wait $hookpid || true
assert_eq "the old hook process finished with 0" 0 "$(cat "$WY_E2E_TMP/hook.rc")"

finish
