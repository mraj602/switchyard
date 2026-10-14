# switchyard (Claude Code plugin)

Records what every Claude Code session does, through the `sy` command. Unofficial; not
affiliated with Anthropic.

- `.claude-plugin/plugin.json`: name `switchyard`, version to be pinned to the `sy` release.
- `hooks/hooks.json`: one `async` command hook per event of `docs/spec/states.md` section 2
  (PreToolUse only for `AskUserQuestion|ExitPlanMode`). Each runs
  `command -v sy >/dev/null 2>&1 && sy hook <Event> || true`, so it resolves `sy` through
  `PATH` and does nothing, silently, when `sy` is not installed.
- `skills/status/`: `/switchyard:status`, which runs `sy status`.
- Phase 3 adds `/switchyard:setup` and `/switchyard:add-account`.
- `skills/peers/`: teaches Claude to message other sessions with `sy peers list` and `sy peers send` (docs/spec/peers.md 12).

Check it: `claude plugin validate plugin/switchyard --strict`.

Try it without installing anything (a plugin for one run only):

```
claude --plugin-dir plugin/switchyard
```

The status line is not part of the plugin (a plugin cannot set the main status line); `sy`
writes `sy statusline --wrap '<old command>'` into the user's `settings.json` instead.
