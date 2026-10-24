# watchyard (Claude Code plugin)

Records what every Claude Code session does, through the `wy` command. Unofficial; not
affiliated with Anthropic.

- `.claude-plugin/plugin.json`: name `watchyard`, version pinned to the `wy` release.
- `hooks/hooks.json`: one `async` command hook per event of `docs/spec/states.md` section 2
  (PreToolUse only for `AskUserQuestion|ExitPlanMode`). Each runs
  `command -v wy >/dev/null 2>&1 && wy hook <Event> || true`, so it resolves `wy` through
  `PATH` and does nothing, silently, when `wy` is not installed. Synchronous extras:
  `wy hook --title <Event>` (tab titles, docs/spec/terminal.md T6) and, on SessionStart,
  `wy hook --peers-context SessionStart`, which tells a new session that sessions of other
  accounts and machines are reachable (docs/spec/peers.md 2.5).
- `skills/status/`: `/watchyard:status`, which runs `wy status`.
- `skills/setup/` and `skills/add-account/`: `/watchyard:setup` and `/watchyard:add-account`.
- `skills/peers/`: teaches Claude to message other sessions with `wy peers list` and `wy peers send` (docs/spec/peers.md 12).

`wy setup` installs the plugin into every Claude Code config folder it manages, from a local
marketplace pinned to the installed `wy` version. To install it by hand into one folder
instead, from inside Claude Code:

```
/plugin install watchyard --marketplace mraj602/watchyard
```

The hooks do nothing until `wy` is installed and on `PATH`.

Check it: `claude plugin validate plugin/watchyard --strict`.

Try it without installing anything (a plugin for one run only):

```
claude --plugin-dir plugin/watchyard
```

The status line is not part of the plugin (a plugin cannot set the main status line); `wy`
writes `wy statusline --wrap '<old command>'` into the user's `settings.json` instead.
