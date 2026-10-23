#!/bin/sh
# S8 install script: only
# install.sh against the local release server. Covers: latest and --version installs, --bin-dir,
# a tampered archive, a missing checksum line, the non-TTY hint, and replacing sy while a
# process of the old one runs (a sleeping fake, then a real `sy hook` kept alive on stdin).
. "$(dirname "$0")/../lib/common.sh"

[ -d "$RELEASE" ] || skip_scenario "no release build in $RELEASE"
VER=$(sed -n 's/^[0-9a-f]*  sy_\([^_]*\)_.*/\1/p' "$RELEASE/SHA256SUMS" | head -n 1)
note "release $VER"

serve main "$RELEASE"
MAIN_URL=$BASE_URL
H=$(new_home good fresh)
snap "$H" "$SY_E2E_TMP/before.json" -ignore ''

# 1. Latest, --no-setup: installs sy and nothing else.
run_install "$H" --no-setup
assert_rc "install latest" 0 $rc
out=$(cat "$SY_E2E_TMP/install.out")
assert_contains "reports version and checksum" "sy $VER downloaded, checksum ok, installed to ~/.local/bin/sy" "$out"
assert_contains "prints the PATH line, does not edit a profile" "is not on your PATH" "$out"
assert_file "$H/.local/bin/sy" "~/.local/bin/sy"
[ -x "$H/.local/bin/sy" ] && pass "sy is executable" || fail "sy is not executable"
assert_contains "sy --version has the version" "$VER" "$("$H/.local/bin/sy" --version 2>&1)"
extra=$("$TOOLS/treesnap" check -ignore '' "$H" "$SY_E2E_TMP/before.json" | sed '1d' | grep -v '^  \.local/bin/sy: unexpected file$' || true)
assert_eq "nothing but ~/.local/bin/sy was added (no temp file, no profile, no Claude folder)" "" "$extra"
assert_eq "~/.local/bin holds only sy" "sy" "$(ls -A "$H/.local/bin")"

# 2. Without --no-setup and without a terminal: prints the hint, exit 0, does not start setup.
run_install "$H"
assert_rc "install without a terminal" 0 $rc
assert_contains "non-TTY prints the hint" "run: sy setup" "$(cat "$SY_E2E_TMP/install.out")"

# 3. --version (with and without v), unknown version, --bin-dir.
H2=$(new_home pinned fresh)
run_install "$H2" --version "$VER" --no-setup
assert_rc "--version $VER" 0 $rc
run_install "$H2" --version "v$VER" --no-setup
assert_rc "--version v$VER" 0 $rc
H3=$(new_home missing fresh)
run_install "$H3" --version 9.9.9 --no-setup
assert_rc "--version 9.9.9 (no such release: download fails)" 4 $rc
assert_no_file "$H3/.local/bin/sy" "sy after a failed download"
run_install "$H3" --bin-dir "$SY_E2E_TMP/elsewhere/bin" --no-setup
assert_rc "--bin-dir" 0 $rc
assert_file "$SY_E2E_TMP/elsewhere/bin/sy" "sy in the --bin-dir"
assert_no_file "$H3/.local/bin/sy" "sy in the default folder when --bin-dir is given"
run_install "$H3" --bogus
assert_rc "unknown option" 2 $rc

# 3b. The base URL must be https (http only for loopback); a bin dir that exists keeps its mode.
H8=$(new_home policy fresh)
INSTALL_BASE_OVERRIDE=http://example.invalid/releases run_install "$H8" --no-setup
assert_rc "plain http to a remote host is refused" 2 $rc
assert_contains "says why" "must be https" "$(cat "$SY_E2E_TMP/install.out")"
INSTALL_BASE_OVERRIDE=http://127.0.0.1.example.invalid/releases run_install "$H8" --no-setup
assert_rc "a look-alike loopback name is refused" 2 $rc
unset INSTALL_BASE_OVERRIDE
mkdir -p "$SY_E2E_TMP/modebin"
chmod 0711 "$SY_E2E_TMP/modebin"
run_install "$H8" --bin-dir "$SY_E2E_TMP/modebin" --no-setup
assert_rc "install into an existing bin dir" 0 $rc
assert_eq "an existing bin dir keeps its mode" "711" "$(stat -c %a "$SY_E2E_TMP/modebin" 2>/dev/null || stat -f %Lp "$SY_E2E_TMP/modebin")"

# 3c. Build attestation: checked when gh is installed (a fake gh here), and said so when not.
mkdir -p "$SY_E2E_TMP/gh-ok" "$SY_E2E_TMP/gh-bad"
printf '#!/bin/sh\necho "$@" > "%s/gh-args"\nexit 0\n' "$SY_E2E_TMP" > "$SY_E2E_TMP/gh-ok/gh"
printf '#!/bin/sh\nexit 1\n' > "$SY_E2E_TMP/gh-bad/gh"
chmod 0755 "$SY_E2E_TMP/gh-ok/gh" "$SY_E2E_TMP/gh-bad/gh"
H9=$(new_home attest fresh)
SY_VERIFY_ATTESTATION= INSTALL_PATH_PREFIX=$SY_E2E_TMP/gh-ok run_install "$H9" --no-setup
unset SY_VERIFY_ATTESTATION INSTALL_PATH_PREFIX
assert_rc "gh present, attestation ok" 0 $rc
assert_contains "reports the attestation" "checksum ok, build attestation ok" "$(cat "$SY_E2E_TMP/install.out")"
assert_contains "gh was asked to verify" "attestation verify" "$(cat "$SY_E2E_TMP/gh-args")"
H10=$(new_home attestbad fresh)
SY_VERIFY_ATTESTATION= INSTALL_PATH_PREFIX=$SY_E2E_TMP/gh-bad run_install "$H10" --no-setup
unset SY_VERIFY_ATTESTATION INSTALL_PATH_PREFIX
assert_rc "gh present, attestation fails" 3 $rc
assert_no_file "$H10/.local/bin/sy" "sy after a failed attestation"
if ! command -v gh >/dev/null 2>&1; then
	H11=$(new_home nogh fresh)
	SY_VERIFY_ATTESTATION= run_install "$H11" --no-setup
	unset SY_VERIFY_ATTESTATION
	assert_rc "no gh: installs on the checksum" 0 $rc
	assert_contains "says the attestation was not checked" "gh CLI is not installed" "$(cat "$SY_E2E_TMP/install.out")"
fi

# 4. Tampered archive: exit 3, nothing installed.
cp -R "$RELEASE" "$SY_E2E_TMP/tampered"
for f in "$SY_E2E_TMP"/tampered/sy_*.tar.gz; do printf 'x' >> "$f"; done
serve tampered "$SY_E2E_TMP/tampered"
H4=$(new_home tampered fresh)
run_install "$H4" --no-setup
assert_rc "tampered archive" 3 $rc
assert_contains "says checksum mismatch" "checksum mismatch" "$(cat "$SY_E2E_TMP/install.out")"
assert_no_file "$H4/.local/bin/sy" "sy after a tampered archive"
assert_eq "temp folders removed" "" "$(ls -A "$SY_E2E_TMP/tmp")"

# 5. Archive without a SHA256SUMS line: exit 3, nothing installed.
cp -R "$RELEASE" "$SY_E2E_TMP/nosum"
sed 's/\.tar\.gz$/.tar.gz.bak/' "$SY_E2E_TMP/nosum/SHA256SUMS" > "$SY_E2E_TMP/nosum/SHA256SUMS.new"
mv "$SY_E2E_TMP/nosum/SHA256SUMS.new" "$SY_E2E_TMP/nosum/SHA256SUMS"
serve nosum "$SY_E2E_TMP/nosum"
H5=$(new_home nosum fresh)
run_install "$H5" --version "$VER" --no-setup
assert_rc "no checksum line" 3 $rc
assert_no_file "$H5/.local/bin/sy" "sy without a checksum line"

# 6. Replacing sy while a process of the old one is running: succeeds, the old one finishes.
BASE_URL=$MAIN_URL
H6=$(new_home replace fresh)
cat > "$H6/.local/bin/sy" <<EOF
#!/bin/sh
sleep 2
echo finished > "$SY_E2E_TMP/old-sy-done"
EOF
chmod 0755 "$H6/.local/bin/sy"
"$H6/.local/bin/sy" &
oldpid=$!
sleep 0.3
run_install "$H6" --no-setup
assert_rc "replace a running sy" 0 $rc
assert_contains "sy was replaced by the release binary" "$VER" "$("$H6/.local/bin/sy" --version 2>&1)"
wait $oldpid || true
assert_file "$SY_E2E_TMP/old-sy-done" "the old process finished"

# 7. The same with the real sy: a `sy hook` waiting for its payload on stdin keeps running
# from the replaced inode and finishes normally.
H7=$(new_home hook fresh)
run_install "$H7" --no-setup
assert_rc "install" 0 $rc
HOLD=$SY_E2E_TMP/hold
mkfifo "$HOLD"
( cd "$H7" && exec env -i HOME="$H7" PATH="$H7/.local/bin:$PATH" TERM=dumb TMPDIR="$SY_E2E_TMP/tmp" \
	sh -c 'sy hook Stop <"$1" >"$2" 2>&1; echo $? > "$3"' sh "$HOLD" "$SY_E2E_TMP/hook.out" "$SY_E2E_TMP/hook.rc" ) &
hookpid=$!
exec 3>"$HOLD"
sleep 0.5
run_install "$H7" --no-setup
assert_rc "replace sy while a hook process runs" 0 $rc
assert_contains "the new sy runs" "$VER" "$("$H7/.local/bin/sy" --version 2>&1)"
printf '{"hook_event_name":"Stop","session_id":"s8"}\n' >&3
exec 3>&-
wait $hookpid || true
assert_eq "the old hook process finished with 0" 0 "$(cat "$SY_E2E_TMP/hook.rc")"

finish
