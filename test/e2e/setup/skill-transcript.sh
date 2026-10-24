#!/bin/sh
# Transcript test for /watchyard:setup: runs `claude -p` with
# the plugin folder and a fake `wy` that records its argv, then checks that the dry run came
# before the apply and that --expect-plan <id> was passed. Spends a little real Claude usage
# and needs a logged-in `claude`, so it is run by hand, not by run.sh.
#
#   test/e2e/setup/skill-transcript.sh [claude command]      (default: claude)
set -eu
claude=${1:-claude}
src=$(cd "$(dirname "$0")/../../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
log=$tmp/argv.log
cat > "$tmp/bin/wy" <<EOF
#!/bin/sh
echo "\$*" >> "$log"
case "\$*" in
	"--version") echo "wy 0.0.0-fake" ;;
	*--detect*) echo '{"claude":{"version":"2.1.296"},"folders":[{"path":"~/.claude","logged_in":true}],"hub":{"connected":false}}' ;;
	*--dry-run*) echo "plan: 1 change" >&2; echo '{"plan_id":"p_fake123","actions":[{"op":"write"}],"needs":[],"summary":{"changes":1,"links_changed":0}}' ;;
	*--yes*) echo '{"applied":true,"needs":[]}' ;;
esac
EOF
chmod +x "$tmp/bin/wy"
PATH=$tmp/bin:$PATH "$claude" -p --plugin-dir "$src/plugin/watchyard" \
	--allowedTools 'Bash(wy:*)' 'Bash(command -v wy)' \
	'/watchyard:setup Use the defaults for everything (hub later, default sharing, no labels, no new accounts) and answer yes to applying the plan.' \
	>"$tmp/transcript.txt" 2>&1 || true
cat "$log"
dry=$(grep -n -- '--dry-run' "$log" | head -n 1 | cut -d: -f1)
apply=$(grep -n -- '--yes' "$log" | head -n 1 | cut -d: -f1)
[ -n "$dry" ] && [ -n "$apply" ] && [ "$dry" -lt "$apply" ] || { echo "FAIL: dry run did not come before apply" >&2; exit 1; }
grep -- '--yes' "$log" | grep -q -- '--expect-plan p_fake123' || { echo "FAIL: --expect-plan p_fake123 not passed" >&2; exit 1; }
echo "ok: dry run before apply, --expect-plan passed"
