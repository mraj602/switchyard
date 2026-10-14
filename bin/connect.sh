#!/usr/bin/env bash
# Connect Claude Code accounts (config dirs) to Switchyard's local hook receiver.
#
#   connect.sh --config-dir ~/.claude-3 [--account <label>]     one config dir
#   connect.sh --all                                             every ~/.claude* dir
#   connect.sh --config-dir ~/.claude-3 --uninstall              (or --all --uninstall)
#
# Installs one shared hook script (~/.claude/switchyard-hook.sh) and adds SessionStart,
# Notification and Stop hooks running it to <config-dir>/settings.json. The script works out
# the account at run time from CLAUDE_CONFIG_DIR (the login email in that dir's .claude.json)
# and reads the token and port from Switchyard's config.json, so nothing account-specific is
# baked into settings.json and one settings.json can be symlinked into every config dir.
# A symlinked settings.json is edited at its target; the symlink itself is left in place.
# Existing hooks are kept; a timestamped backup is written first. Hooks never block Claude Code.
set -euo pipefail

dirs=() account="" uninstall=0 all=0
while [ $# -gt 0 ]; do
  case "$1" in
    --config-dir) dirs+=("$2"); shift 2 ;;
    --all)        all=1; shift ;;
    --account)    account="$2"; shift 2 ;;
    --port)       echo "--port is no longer needed (the hook reads it from Switchyard's config.json); ignoring" >&2; shift 2 ;;
    --uninstall)  uninstall=1; shift ;;
    -h|--help)    sed -n 2,14p "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
if [ "$all" = 1 ]; then
  for d in "$HOME"/.claude "$HOME"/.claude-*; do [ -d "$d" ] && dirs+=("$d"); done
fi
[ "${#dirs[@]}" -gt 0 ] || { echo "pass --config-dir <dir> or --all" >&2; exit 2; }
case "$account" in *[!A-Za-z0-9\ ._@-]*) echo "--account: letters, digits, space . _ @ - only" >&2; exit 2 ;; esac

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
hook="$HOME/.claude/switchyard-hook.sh"
marker="switchyard-hook"
# Drop any previous Switchyard entries (identified by the marker in the command).
strip='
  .hooks //= {} |
  .hooks |= with_entries(
    .value |= ( map(.hooks |= map(select((.command // "") | contains($m) | not)))
                | map(select((.hooks | length) > 0)) )
  ) |
  .hooks |= with_entries(select((.value | length) > 0)) |
  if (.hooks | length) == 0 then del(.hooks) else . end'

if [ "$uninstall" = 0 ]; then
  mkdir -p "$(dirname "$hook")"
  install -m 0755 "$here/switchyard-hook.sh" "$hook"
  [ -n "$(jq -r '.hookToken // empty' "${SWITCHYARD_DATA:-$HOME/.config/switchyard}/config.json" 2>/dev/null)" ] ||
    echo "note: Switchyard hasn't created its config yet; start it once and hooks begin working" >&2
fi

cmd="bash $hook # $marker"
[ -n "$account" ] && cmd="CLAUDE_ACCOUNT='$account' $cmd"

done_targets=" "
for cfg in "${dirs[@]}"; do
  cfg="${cfg/#\~/$HOME}"
  [ -d "$cfg" ] || { echo "skip: no such config dir: $cfg" >&2; continue; }
  settings="$cfg/settings.json"
  # Write through symlinks: edit the file the link points at, never replace the link.
  target="$(readlink -f "$settings")"
  case "$done_targets" in *" $target "*) echo "$cfg: shares $target (already done)"; continue ;; esac
  done_targets="$done_targets$target "
  if [ "$uninstall" = 1 ] && [ ! -f "$target" ]; then continue; fi
  [ -n "$account" ] && [ -L "$settings" ] &&
    echo "warning: $settings is a symlink, so --account '$account' applies to every dir sharing $target" >&2
  [ -f "$target" ] || echo '{}' > "$target"
  cp -p "$target" "$target.bak.switchyard.$(date +%Y%m%d%H%M%S)"
  tmp="$(mktemp "$(dirname "$target")/.settings.switchyard.XXXXXX")"
  if [ "$uninstall" = 1 ]; then
    jq --arg m "$marker" "$strip" "$target" > "$tmp"
  else
    jq --arg m "$marker" --arg cmd "$cmd" "$strip"' |
      .hooks //= {} |
      .hooks.SessionStart = ((.hooks.SessionStart // []) + [{"hooks": [{"type": "command", "command": $cmd, "timeout": 5}]}]) |
      .hooks.Notification = ((.hooks.Notification // []) + [{"matcher": "", "hooks": [{"type": "command", "command": $cmd, "timeout": 5}]}]) |
      .hooks.Stop         = ((.hooks.Stop // [])         + [{"hooks": [{"type": "command", "command": $cmd, "timeout": 5}]}])
    ' "$target" > "$tmp"
  fi
  # cat (not mv) keeps the target's inode, owner and mode
  cat "$tmp" > "$target" && rm -f "$tmp"
  if [ "$uninstall" = 1 ]; then echo "$cfg: removed Switchyard hooks from $target"
  else echo "$cfg: hooks → $hook (settings: $target)"; fi
done
[ "$uninstall" = 1 ] || echo "Restart running Claude Code sessions to pick up the hooks."
