#!/bin/sh
# S2: the development laptop of docs/spec/setup.md section 11, with fake logins: seven
# folders (~/.claude, -2, -3 logged in; -4 .. -7 not), 48 hand-made links to ~/.claude,
# prototype hooks, a status line to wrap, three plugins, shell-function launchers calling
# ~/bin/claude-launch, a hand-made MCP sync script, leftover .claude.json.tmp.* files.
. "$(dirname "$0")/_lib.sh"
init_home "$1"
primary_content
P=$H/.claude

put "$P/statusline-command.sh" 0755 <<'SH'
#!/bin/bash
cat >/dev/null
echo "old status line"
SH
put "$P/switchyard-hook.sh" 0755 <<'SH'
#!/bin/sh
# prototype hook
exit 0
SH
put "$P/sync-mcp-to-2.sh" 0755 <<'SH'
#!/bin/sh
# hand-made MCP sync: copies mcpServers from ~/.claude.json to ~/.claude-2/.claude.json
exit 0
SH

put "$P/settings.json" <<JSON
{
  "statusLine": {
    "type": "command",
    "command": "bash $H/.claude/statusline-command.sh"
  },
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "$H/.claude/switchyard-hook.sh SessionStart"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "$H/.claude/switchyard-hook.sh Stop"}]}
    ],
    "Notification": [
      {"hooks": [{"type": "command", "command": "$H/.claude/switchyard-hook.sh Notification"}]}
    ]
  },
  "enabledPlugins": {
    "tool-a@area-tools": false,
    "tool-b@area-tools": false,
    "tool-c@area-tools": false
  },
  "extraKnownMarketplaces": {
    "area-tools": {"source": {"source": "github", "repo": "example/area-tools"}}
  }
}
JSON

mkd "$P/plugins/cache/area-tools/tool-a/1.0.0"
mkd "$P/plugins/cache/area-tools/tool-b/1.0.0"
put "$P/plugins/installed_plugins.json" <<JSON
{
  "version": 2,
  "plugins": {
    "tool-a@area-tools": [{"scope": "user", "installPath": "$H/.claude/plugins/cache/area-tools/tool-a/1.0.0", "version": "1.0.0", "installedAt": "2026-01-02T03:04:05.000Z", "lastUpdated": "2026-01-02T03:04:05.000Z"}],
    "tool-b@area-tools": [{"scope": "user", "installPath": "$H/.claude-2/plugins/cache/area-tools/tool-b/1.0.0", "version": "1.0.0", "installedAt": "2026-01-02T03:04:05.000Z", "lastUpdated": "2026-01-02T03:04:05.000Z"}],
    "tool-c@area-tools": [{"scope": "user", "installPath": "$H/.claude/plugins/cache/area-tools/tool-b/1.0.0", "version": "1.0.0", "installedAt": "2026-01-02T03:04:05.000Z", "lastUpdated": "2026-01-02T03:04:05.000Z"}]
  }
}
JSON
put "$P/plugins/known_marketplaces.json" <<JSON
{
  "area-tools": {
    "source": {"source": "github", "repo": "example/area-tools"},
    "installLocation": "$H/.claude/plugins/marketplaces/area-tools",
    "lastUpdated": "2026-01-02T03:04:05.000Z"
  }
}
JSON

# Folders: logged in 1..3, not logged in 4..7.
login_folder "$P" "$H/.claude.json" a@example.com "Example Org" claude_max
cp "$H/.claude.json" "$P/.claude.json"
login_folder "$H/.claude-2" "$H/.claude-2/.claude.json" b@example.com "Example Org" claude_max
login_folder "$H/.claude-3" "$H/.claude-3/.claude.json" c@example.com "Team Org" claude_team
for n in 4 5 6 7; do mkd "$H/.claude-$n" 0700; done
put "$H/.claude-2/.claude.json.tmp.4242.1767324245000" 0600 <<'JSON'
{}
JSON
put "$H/.claude-5/.claude.json.tmp.4243.1767324245001" 0600 <<'JSON'
{}
JSON
put "$H/.claude-api-env" 0600 <<'ENV'
ANTHROPIC_API_KEY=fake-not-a-key
ENV

# Eight links in each of six folders = 48.
for n in 2 3 4 5 6 7; do
	for item in settings.json CLAUDE.md commands skills plugins projects history.jsonl plans; do
		ln -s "$H/.claude/$item" "$H/.claude-$n/$item"
	done
done

# Launchers: shell functions in ~/.bashrc over ~/bin/claude-launch.
put "$H/bin/claude-launch" 0755 <<'SH'
#!/bin/bash
# per-area launcher: sets CLAUDE_CONFIG_DIR, syncs MCP for N > 1, then runs claude
n=${1:-1}; shift
if [ "$n" -gt 1 ]; then
	export CLAUDE_CONFIG_DIR="$HOME/.claude-$n"
	"$HOME/.claude/sync-mcp-to-2.sh" || true
fi
exec claude "$@"
SH
{
	echo '# hand-made launchers'
	echo 'claude() { "$HOME/bin/claude-launch" 1 "$@"; }'
	for n in 2 3 4 5 6 7; do echo "claude-$n() { \"\$HOME/bin/claude-launch\" $n \"\$@\"; }"; done
} | put "$H/.bashrc"

mkd "$H/.local/bin" 0755
fix_mtimes
