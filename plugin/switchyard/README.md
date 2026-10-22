# switchyard (Claude Code plugin)

Records what every Claude Code session does, through the `sy` command. Unofficial; not
affiliated with Anthropic.

- `.claude-plugin/plugin.json`: name `switchyard`, version pinned to the `sy` release.
- `hooks/hooks.json`: one `async` command hook per event of `docs/spec/states.md` section 2
  (PreToolUse only for `AskUserQuestion|ExitPlanMode`). Each runs
  `command -v sy >/dev/null 2>&1 && sy hook <Event> || true`, so it resolves `sy` through
  `PATH` and does nothing, silently, when `sy` is not installed. Synchronous extras:
  `sy hook --title <Event>` (tab titles, docs/spec/terminal.md T6) and, on SessionStart,
  `sy hook --peers-context SessionStart`, which tells a new session that sessions of other
  accounts and machines are reachable (docs/spec/peers.md 2.5).
- `skills/status/`: `/switchyard:status`, which runs `sy status`.
- `skills/setup/` and `skills/add-account/`: `/switchyard:setup` and `/switchyard:add-account`.
- `skills/peers/`: teaches Claude to message other sessions with `sy peers list` and `sy peers send` (docs/spec/peers.md 12).

`sy setup` installs the plugin into every Claude Code config folder it manages, from a local
marketplace pinned to the installed `sy` version. To install it by hand into one folder
instead, from inside Claude Code:

```
/plugin install switchyard --marketplace mraj602/switchyard
```

The hooks do nothing until `sy` is installed and on `PATH`.

Check it: `claude plugin validate plugin/switchyard --strict`.

Try it without installing anything (a plugin for one run only):

```
claude --plugin-dir plugin/switchyard
```

The status line is not part of the plugin (a plugin cannot set the main status line); `sy`
writes `sy statusline --wrap '<old command>'` into the user's `settings.json` instead.
