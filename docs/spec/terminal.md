# Terminal board, pane and Warp hookups

Status: spec v1. Implemented in `internal/board` (+ `hubsrc`, `tui`),
`internal/termctl`, `internal/warp`, `internal/termenv`, `plugin/watchyard-pane/` and the
`wy board`, `wy term`, `wy open`, `wy ssh` and `wy hook --title` commands. Unofficial; not affiliated with Anthropic.

Two parts: the session board in any terminal (`wy board`, and the same board as a Claude Code
pane mod), and Warp hookups with no fork (tab titles, a tab config per account, click to
focus, Warp's own Claude Code plugin in every account folder). Nothing here changes the hub,
the API, the schema or a record kind; record.md 3.3 only says who sets `term.host`.

Rules that override everything below (hard rules): no live settings or Warp
config is touched without an explicit `wy` command, and every such change goes through the
setup.md 7 journal and `wy setup --undo`; in Warp's folders `wy` writes only its own
`watchyard-*.toml` tab configs; the board and the pane read and never type; the board token
never leaves `<state>/board/token.json`; the terminal packages take their folders from
`termenv.Env` (`internal/termenv/lint_test.go` refuses `os.UserHomeDir` and reads of `HOME`
and similar in them).

Related: record.md 3.3 (`term`), states.md (states, details, questions), api.yaml (the read
API and `/v1/stream`), desktop.md C4 (`wy desktop token`, reused by `wy board connect`),
setup.md 5.2 (the `wy-local` marketplace), 5.5 (settings pointers), 7 (journal and undo), 9.3
(ledger), paths.md 3 and 4.

## T1. Commands

The board is read-only: it only reads the hub (or the local outbox) and never ingests, uploads or writes records. Its commands change nothing on the hub except `wy board connect`, which claims a read token.

| Command | Does | Exit codes |
| --- | --- | --- |
| `wy board [--once] [--json] [--follow --ndjson] [--compact] [--ascii] [--source auto\|hub\|local] [--filter all\|needs\|here\|machine:<name>] [--session <id>] [--cols N]` | Without `--once`/`--follow`: the interactive TUI (T4). `--once`: one text frame to stdout (no raw mode). `--once --json`: one `View`. `--follow --ndjson`: one `View` per line on change (T5). | 0; 2 usage; 4 `--source hub` without a usable hub; 5 hub unreachable with `--source hub` |
| `wy board connect [--code-stdin] [--json]` | Gets the board token (T3). Prints `{device, hub}`, never the token | 0; 3 refused; 4 no admin channel and no `--code-stdin`; 5 unreachable |
| `wy board disconnect` | Deletes `<state>/board/token.json`. Revoke is left to `wy hub token revoke` (printed) | 0 |
| `wy analytics [report] [--days N] [--seat S] [--json]` | The hub's analytics reports as text tables, with the board token ([analytics.md](analytics.md) 6) | 0; 2 usage; 3 refused; 4 no hub or no board token; 5 unreachable |
| `wy term focus --session <id> \| --url <u>` | Focuses the tab (T7) | 0; 4 no usable focus URL here; 6 opener failed |
| `wy term setup [--pane] [--warp-plugin] [--titles] [--warp-tabs] [--accept-update] [--dry-run] [--yes] [--json]` | Plans and applies the chosen terminal pieces (T10). `--accept-update` (implies `--warp-plugin`) records the commit Warp's plugin checkout moved to, after the person read what changed (T10) | as `wy setup`: 0, 1 rolled back, 2 usage/locked, 4 precondition changed |
| `wy term titles on\|off`, `wy term warp on\|off`, `wy term off` | Shorthands for one piece of `term setup`; `off` removes everything in `ledger.terminal` | as above |
| `wy term status [--json]` | What is on, per folder and settings target; Warp detected (channel, folder); Claude Code version vs. the mod minimum | 0 |
| `wy open [<label>] [--here]` | No label: lists accounts (label, folder, launcher, login, 5h %, reset, "most room" mark), choosing nothing. With a label in Warp: opens its tab config. `--here`, or outside Warp: `exec`s the launcher in this terminal | 0; 2 unknown label; 4 tab config missing (prints `wy term warp on`) |
| `wy ssh <ssh args…>` | T9 | ssh's own exit code; 127 no `ssh` |
| `wy hook --title <Event>` | Prints the `terminalSequence` JSON or nothing (T6) | always 0 |

`wy open` never picks an account for the person ("it may suggest which
account has room; you choose").

## T2. The view model (`internal/board`, JSON `"v": 1`)

```jsonc
{
  "v": 1,
  "generated_at": "2026-10-20T14:06:00Z",
  "source": "hub",                     // hub | local | mixed (hub offline, local rows live)
  "hub": {"state": "live", "url": "https://hub…", "offline_since": null},   // live | offline | none | unpaired
  "machine": {"id": "m_…", "name": "laptop"},
  "counts": {"sessions": 14, "machines": 3, "needs_you": 3},
  "needs_you": ["c41e…", "9a0b…"],     // row ids, oldest waiting first
  "groups": [
    {"seat": "s_…", "label": "work", "colour": "#f59e0b", "warp_colour": "yellow",
     "five_hour": {"pct": 41, "resets_at": "…", "text": "41% · 15:10"},
     "seven_day": {"pct": 30, "text": "30%"}, "limited": false, "rows": ["…"]}
  ],
  "rows": {
    "c41e…": {
      "id": "c41e…", "state": "asked", "detail": "question", "needs_you": true,
      "glyph": "needs",               // working | needs | done | failed | ended | gone
      "status": "asked",              // the short word shown: asked, perm, limit · 17:00, done, 2m, new
      "project": "api", "title": "migrate-orders",
      "account": "work", "colour": "#f59e0b", "seat": "s_…",
      "machine": "m_…", "machine_name": "gpu-box", "here": false, "this_session": false,
      "context_pct": 47, "context_level": "ok",   // ok | warn (>= 80) | high (>= 92)
      "since": "…", "age": "4m",
      "line2": "Update the migration, or keep the old column?",
      "line2_kind": "question",       // question | recap | error | none
      "stale": false, "greyed": false,
      "can_focus": false, "resume": "claude-2 --resume c41e…", "remote_url": "https://claude.ai/…"
    }
  }
}
```

The Go types are `internal/board/view.go` (`board.View`, `Row`, `Group`, `Window`, `HubInfo`,
`Machine`, `Counts`, and constants for every enum above). `testdata/view-v1.json` is the
round-trip golden; `board.Decode` parses one View and refuses `v != 1` and an id in
`needs_you` or `groups[].rows` that is not in `rows` (unknown fields are ignored). Times
are RFC 3339 UTC. `offline_since`, `five_hour`, `seven_day` and `context_pct` are `null`
when unknown; `resets_at` and `remote_url` are left out when unknown; `truncated` is
present only when rows were dropped. Every producer is a `board.Source`:
`Start(ctx) (<-chan View, error)` and `Refresh()`.

Rules, all in Go and table-tested:

- **States and words.**
  - `asked` with `detail = question` reads "asked", and `line2` is the question line (states.md 4).
  - Other `asked` details read "asked · \<tool or dialog>".
  - `needs_permission` reads "perm", or "perm · Bash" when the last PermissionRequest names a tool.
  - `failed` with `rate_limit` reads "limit · \<reset time of that seat's 5h window>". Other errors show the error word.
  - `working` shows its age ("40s", "2m").
  - `done` reads "done", or "new" for `detail = started`.
  - `ended` and `gone` rows appear only with the `e` toggle (`include_ended`, 2 days).
- **Recap / last lines.** For `done` and `working` rows, `line2` is the session's `last_message` (the last ≤ 200 characters the hub keeps). Code fences are removed and whitespace is collapsed to one line, with `line2_kind = recap`. When last messages are off (privacy), `line2` is empty and `line2_kind = none`. A failed row's `line2` is the error detail.
- **Order.** `needs_you` keeps the hub's order (oldest waiting first). Groups follow `/v1/accounts` order, with "Unattributed" last. Rows inside a group keep `/v1/sessions` order (needs-you first, then newest activity). The local source sorts the same way (`status.rank`).
- **`here`** is `machine == this machine` (or `term.host == this machine`). A machine chip is shown only when `here` is false. `this_session` is true for `--session <id>`.
- **`can_focus`** is true only when `focus_url` was delivered (hub) or recorded locally, and it passes T7's allow-list. The URL itself is **not** in the `View`: `wy term focus --session` looks it up again, so it never travels through the mod.
- **Limits.** The `View` holds at most 300 rows and is capped at 512 KiB. Overflow keeps all needs-you rows, then the newest, and adds `"truncated": n`.
- **Text safety.** Every string from a record (project, title, question, last message, labels, machine names) is stripped of C0/C1 controls, ESC and bidi overrides before it enters the `View`. That makes it safe to print raw in a terminal and safe in a title (T6).

## T3. Sources and the board token

- **Selection (`--source auto`, the default).** Use the hub when `hub.url` is set and
  `<state>/board/token.json` exists for that URL (it is dropped when the URL differs, as in
  desktop.md C6). Otherwise use local, with `hub.state = none` (no URL) or `unpaired` (no
  token), and show "run `wy board connect`" in the footer.
- **Hub.**
  - Fetch `/v1/overview` first, then `/v1/accounts`, then `/v1/sessions?limit=300`.
  - Then `GET /v1/stream?last_event_id=<overview.stream_id>`. A `session` event updates
    one row, a `usage` event refetches accounts, and `reset` refetches everything.
  - Reconnect after 1, 2, 4 … 60 s. With no `ping` for 60 s, reconnect.
  - A `401` drops the token and shows `unpaired`.
  - Requests have a 10 s timeout and follow no redirects. Plain `http:` is allowed only for
    loopback or for a URL `wy` already accepted (`hub.insecure_lan`). Replies are capped at
    4 MiB.
  - Forward-mode machines (`hub.dial`) dial as `wy flush` does.
- **Offline.** After the first failed reconnect, `hub.state = offline` with `offline_since`
  set. This machine's rows come from the local fold, live. Other machines' rows keep their
  last hub values with `greyed = true`. The last hub view is kept in
  `<state>/board/last-view.json` (0600), so a board started while the hub is down still shows
  other machines, greyed.
- **Local.**
  - `status.Load` + `status.Adapt` over the last 7 days of the outbox.
  - Re-folded when the newest outbox file's size or mtime changes, polled every 1 s and
    debounced 300 ms.
  - Accounts and limits come from `slots.labels` / `colours` and the newest `limit_sample`
    per slot.
  - Budget: a fold of a 20 MiB outbox under 200 ms (benchmark).
- **Token.** `wy board connect` calls `desktop.Token` with name `terminal · <machine name>`,
  through the admin channel (machines.md 4.4) or with `--code-stdin` (a **desktop** pairing
  code from More → Devices → Add desktop app, or `wy hub pair --kind desktop`). It writes
  `{"v":1,"url","device","token"}` atomically, mode 0600. On a hub host in merged mode this
  is one command with no code.

## T4. `wy board` TUI (`internal/board/tui`)

- **Terminal handling.**
  - Raw mode and the alternate screen when stdout is a terminal. A line fallback
    (`TERM=dumb`, not a tty, or `--once`) prints one frame.
  - It redraws on change (at most every 100 ms) and on `SIGWINCH`. On Windows it polls the
    size every 500 ms.
  - On Windows the console is put in VT mode (`ENABLE_VIRTUAL_TERMINAL_PROCESSING` through
    `x/sys/windows`), and the old mode is restored on exit.
  - `NO_COLOR` turns colour off. `COLORTERM=truecolor|24bit` gives 24-bit account colours,
    else the nearest of 256.
  - `--ascii` (or a non-UTF-8 locale) swaps the glyphs `● ▲ ○ ■` for `* ! o x`.
- **Layouts.**
  - **Compact** (`--compact`, or ≤ 60 columns) is one line per session, as in the TermPane
    design: `▲ api · migrate-orders  asked`.
  - **Full** (> 60 columns) is two lines. Line 1: glyph, project · title, machine chip,
    status, context %, age. Line 2: dimmed `line2`.
  - Both layouts have a header (`Watchyard  14 sessions · 3 machines · hub live`), a NEEDS
    YOU block, then one block per account with its 5h / 7d text, and a footer of keys.
- **Keys.**
  - `↑ ↓` / `j k` move. `Enter` focuses when `can_focus`, else shows why.
  - `c` copies the resume command (OSC 52 to its own terminal; it also prints the command in
    the footer for terminals that drop OSC 52).
  - `o` opens `remote_url`, only if it is `https://claude.ai/…`, through the T7 opener.
  - `f` cycles the filter (All → Needs you → This machine → each machine). `/` searches
    project, title and account.
  - `e` toggles ended rows, `r` refetches, `?` shows help, and `q` or `Ctrl-C` quits.
- **It writes only to its own stdout.** No key sends input anywhere else.

## T5. The pane mod (`plugin/watchyard-pane/`)

- **Packaging.** Files: `.claude-plugin/plugin.json` (`name: watchyard-pane`, the same
  version as `wy`, `"types": "./types/index.d.ts"`), `hooks/hooks.json`
  (`{"modules": ["./register.tsx"]}`), `hooks/register.tsx`, `types/index.d.ts` (its
  `$.state` contract), and tests `hooks/*.test.ts`. The tree is embedded as
  `plugin.PaneFS()` (`plugin.PaneName`). `pluginmkt` writes it into `wy-local` as
  `plugins/watchyard-pane/` next to `plugins/watchyard/` (the marketplace manifest lists
  both; the id is `pluginmkt.PaneID = watchyard-pane@wy-local`), sets its version, and passes
  every file through `pluginmkt/pane.go`'s `paneFile`, which replaces the placeholder
  `"__WY_PATH__"` in `register.tsx` with the absolute path of `wy`, as it already does for
  hook commands. Writing the pane into the marketplace installs nothing: only
  `wy term setup --pane` runs `claude plugin install watchyard-pane@wy-local`.
- **Skeleton.** `register.tsx` and `types/index.d.ts` are placeholders that register
  no hook and declare no state, so the written tree passes `claude plugin validate --strict`
  on 2.1.296 (a `modules` entry naming a missing file fails validation of the plugin, though
  not of the marketplace). Both are replaced when the pane is implemented.
- **Lifecycle.**
  - On `session.start`, it registers the `/board` command and spawns
    `[WY, "board", "--follow", "--ndjson", "--session", <session id>]` with
    `$.process.spawn`.
  - Each line is parsed into one atom (`View`). A line that is not valid JSON or has
    `v != 1` is dropped and counted.
  - If the child exits, it restarts after 2, 4 … 60 s.
  - On `session.end`, it stops the child.
  - It opens the pane unasked only when `e.viewport.isFullscreen` is true and
    `terminal.pane_auto_open` is true (the engine seats it from 144 columns). `/board`
    toggles it at any width.
- **Pane (`ui.render` `Pane`).** The same blocks as the compact layout, sized to
  `e.props.bodyColumns`, with each row a `Button` (`plain`).
  - Pressing a row with `can_focus` runs `[WY, "term", "focus", "--session", id]` through
    `$.process.run`.
  - A second key, the row's `c` hotkey, calls `$.ui.copy` with `resume`.
  - `remote_url` is a `Link` only when it is `https://claude.ai/…`.
- **Band (`ui.render` `AbovePrompt`).** Shown when at least one needs-you row is not
  `this_session` and `terminal.band` is true:
  `▲ Watchyard  work · api (gpu-box) asked a question 4 min ago   +2 more need you`.
  Otherwise it calls `next(e)` and draws nothing. The engine's `[-]` hides it until the
  needs-you set changes.
- **Status entry.** `$.ui.status("needs you: 3")`, or cleared at 0.
- **Allowed surface (checked by `claude plugin validate` output in a test).** On 2.1.296
  `claude plugin validate --json <plugin dir>` reports, for each module, two notes under
  `contents[].notes` of the `hooks` entry: `"./register.tsx hooks: <list or nothing>"` and
  `"./register.tsx calls: <list or nothing on $>"` (and for the manifest's `types`,
  `"types ./types/index.d.ts declares state: …"`). `pane-check.sh` parses those two lines.
  - Hooks: `session.start`, `session.end`, `command.run`, `ui.render` (Pane, AbovePrompt)
    and `ui.press`.
  - Calls: `$.process.spawn` / `$.process.run` with `argv[0] === WY`, `$.ui.*`,
    `$.command.register`, `$.clock.*` and `$.state`.
  - Anything under `$.prompt`, `$.tool`, `$.model`, `$.agent`, `$.http`, `$.session.append`
    or `$.fs.write`, and any `tool.call` / `prompt.*` hook, fails the test.
- **Minimum Claude Code.** 2.1.296, the version the skeleton was checked on (`plugin
  validate`, `marketplace add` of the two-plugin `wy-local`, `install
  watchyard-pane@wy-local`; the mod API names in this section are present in that build).
  `wy term setup --pane` refuses an older `claude` with that message.

## T6. Titles

- **Format.** `<label> · <project> · <state word>`, cut to 60 characters with `…`. State
  words: working, permission, asked, done, failed (or `limit` for `rate_limit`) and ended.
  The label is the slot label (config), the project is the last folder of `cwd`, and both
  pass T2's text safety. A `;` or BEL in either is replaced.
- **Delivery.** `wy hook --title <Event>` reads the hook stdin, folds only that event onto
  `<state>/term/title-<session>.json` (the last state word), and prints
  `{"terminalSequence":"\u001b]2;<title>\u0007"}`. It prints nothing when
  `terminal.titles` is false, when there is no label, or when the title is unchanged since
  the last print for that session (that keeps `-p` and SDK runs, which ignore the field,
  quiet).
- **Events.** It is installed for SessionStart, UserPromptSubmit, PermissionRequest,
  Notification, Stop, StopFailure and SessionEnd. Each is a **synchronous** hook with
  `timeout: 1`. The p95 budget is under 15 ms per call.
- **Stdout.** For SessionStart and UserPromptSubmit, stdout is context for Claude unless it
  is hook JSON. The command prints only valid hook JSON or nothing, never plain text
  (unverified).
- **Claude Code's title** is off while titles are on. On 2.1.296
  `CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1` also skips the background request that names the
  session (`/resume` shows no generated title) and turns off Claude Code's OSC 7501
  program-status reports to the terminal (code.claude.com/docs/en/terminal-config, "See
  session status in your terminal"). `wy term titles on` prints both in its plan, and
  `wy term status` shows them. A tab the person renamed in Warp may or may not keep its name
  (unverified).

## T7. Focus and the opener (`internal/termctl`)

- **Allow-list.**
  - `^(warp|warppreview)://session/[0-9a-f]{32}$`, length ≤ 128, checked on the raw
    string with no decoding.
  - The pattern list is the only place new terminals are added later.
  - `--url` is accepted only when it also matches the allow-list, and only when
    `TERM_PROGRAM` or Warp detection says Warp runs on this machine.
- **Same machine only.** `--session` resolves the URL from the hub (`/v1/sessions/{id}` with
  the board token, so the hub's own machine check applies) or from the local outbox. It
  refuses if the session's focus target is another machine.
- **Opener.** Always argv, never a shell:
  - Linux: `xdg-open <url>`.
  - macOS: `open <url>`.
  - Windows: `rundll32.exe url.dll,FileProtocolHandler <url>`.

  The opener has a 5 s timeout. Its stderr goes to `<state>/logs/wy.log`.
- **Test seam.** The `Opener` interface (`internal/termctl/termctl.go`:
  `Open(ctx context.Context, url string) error`). Under the build tag `fakeopen`, `WY_TEST_OPENER=<file>`
  records argv to a file instead. Release builds do not contain that code (checked as in
  the hub tests' `fakeclock`).

## T8. Warp files (`internal/warp`)

| OS | Stable folder | Preview |
| --- | --- | --- |
| macOS | `~/.warp/tab_configs/` | `~/.warp-preview/tab_configs/` |
| Linux | `${XDG_DATA_HOME:-~/.local/share}/warp-terminal/tab_configs/` | `…/warp-terminal-preview/tab_configs/` |
| Windows | `%APPDATA%\warp\Warp\data\tab_configs\` | `%APPDATA%\warp\WarpPreview\data\tab_configs\` |

**Detection.** Warp is "installed" when its data folder (the parent of `tab_configs`)
exists, or when `TERM_PROGRAM=WarpTerminal`. The channel decides the URI scheme (`warp://` or
`warppreview://`). With both channels present, `--channel stable|preview` picks one; the
default is stable.

File `watchyard-<label>.toml` (label already `[a-z0-9-]`, setup.md 3), mode 0644:

```toml
# wy-warp-tab v1 slot=~/.claude-2 label=work (written by wy term warp on; wy term warp off removes it)
name = "Watchyard · work"
title = "work"
color = "yellow"

[[panes]]
id = "main"
type = "terminal"
commands = ["/home/u/.local/bin/claude-2"]
```

- **Marker.** The first line marks the file as ours (`# wy-warp-tab v1`).
- **Colour.** The seat colour (hub label/colour if paired, else `slots.colours`) maps to the
  nearest of `red, green, yellow, blue, magenta, cyan` by OKLab distance. `black` and `white`
  are never used. The seventh account repeats a colour.
- **Command.** The account's launcher by absolute path (`claude` for `~/.claude`). On
  Windows it is the `.cmd` path. A folder with no launcher gets no tab config and a plan
  warning. Warp runs `commands` in the pane's shell (bash, zsh, fish or PowerShell, which
  `wy` cannot know and which quote differently), so a launcher path that would need
  quoting (white space, a quote, `$`, `;`, `&`, `|`, `<>`, `()`, `[]`, `{}`, `*?!#%^,`,
  a `\` in a Unix path) gets no tab config either, with a plan warning to move it.
- **Opening.** `wy open work` opens `warp://tab_config/watchyard-work` (Warp matches the
  file stem case-insensitively) through the T7 opener. This is a second fixed URI pattern,
  allowed only for files `wy` wrote.

## T9. `wy ssh` and `term.host`

- **Running ssh.** `wy ssh <args>` execs the `ssh` on `PATH` (or `WY_SSH_COMMAND`, as
  `internal/sshx`), with `-o SendEnv=LC_WY_FOCUS -o SendEnv=LC_WY_HOST` put before the
  person's args. Nothing else is added.
- **Environment.** `LC_WY_FOCUS` = `LC_WY_FOCUS` if set (a nested hop), else
  `WARP_FOCUS_URL`, kept only if it passes T7. `LC_WY_HOST` = this machine's id. With
  neither, it is plain `ssh`.
- **Hook side.** `term.host` = `LC_WY_HOST` when it matches `^m_[0-9A-HJKMNP-TV-Z]{26}$` and
  differs from this machine's id. Otherwise it is absent. (record.md 3.3.)
- **`wy ssh --print-config`** prints, but never writes, the `SendEnv` lines for people who
  prefer their own `ssh` alias. Writing `~/.ssh/config` for this is a non-goal.

## T10. Plan, ledger and undo (`wy term setup`)

**Planner.** `plan.Terminal(State, TermChoices) → model.Plan` is pure and deterministic, with
the same `plan_id` rule as setup.md 6.3. `model.TermChoices{Pane, WarpPlugin, Titles,
WarpTabs, Channel, Off}`; `Off` plans the reverse of the chosen pieces from the ledger.
The hooks-plugin refresh is a `write_tree` in `StepPlugin` and `terminal.titles` a
`config_set` in `StepConfig`; every other terminal action is in the new `model.StepTerminal`
(local; between `launchers` and `config` in `StepOrder`). Actions, in apply order:

| Piece | Actions |
| --- | --- |
| `--pane` | Marketplace tree refresh (the existing `write_tree`), then `run claude plugin install watchyard-pane@wy-local` per settings target (as setup.md 5.2) |
| `--warp-plugin` | `run claude plugin marketplace add warpdotdev/claude-code-warp#main` once per plugin root, then `run claude plugin install warp@claude-code-warp` per settings target. A commit cannot be pinned (item 4 under "Claude Code facts" below): the ledger records the commit `main` resolved to (`git -C <installLocation> rev-parse HEAD`, read-only, after the add) and `wy doctor` warns when the checkout moves (risk 7). It warns if `jq` is missing, because the plugin needs it |
| `--titles` | `json_set /env/CLAUDE_CODE_DISABLE_TERMINAL_TITLE "1"` per settings target, the hooks plugin refresh (title hooks, T6) and `config_set terminal.titles true` |
| `--warp-tabs` | `mkdir` the `tab_configs` folder if absent, then `write_file watchyard-<label>.toml` per account with a launcher |

**Ledger.** It gains a `terminal` block (`model.LedgerTerminal`, field `Ledger.Terminal`):
`{pane: {targets, installed, markets}, warp_plugin: {roots, targets, installed, ref, commit},
titles: {targets}, title_prior: {<target>: <raw JSON>}, tabs: [{path, sha256, label}], warp:
{channel, folder}}`. `installed` lists the plugin roots `wy` ran `claude plugin install` in,
`roots` / `markets` the roots it added the marketplace to (a pane `marketplace add wy-local`
in a root that lacked it counts), and `title_prior` the value the env key had before
`titles on` set it (kept after `titles off`, so an undo of the off can use it again).
`commit` is recorded when a run adds Warp's marketplace or installs its plugin, or when
none is recorded yet; a later run never moves it, so `wy doctor` keeps warning about a
moved checkout until the person accepts it with `wy term setup --warp-plugin
--accept-update`. `ledger.Update` calls
`updateTerminal(l, action, entry)` for every finished entry, and `ledger.Revert` calls
`revertTerminal(l, entry)` first for every reverted entry; when that returns true the generic
revert code is skipped for the entry (a pane install creates paths under `wy-local` and
touches `installed_plugins.json`, which the generic code reads as the watchyard plugin going
away). Both live in `internal/setup/ledger/terminal.go`. `wy term … off` and `wy uninstall`
plan the reverse from it:

- `json_delete` only where the value is still `"1"`, or a `json_set` back to `title_prior`
  where there is one;
- `claude plugin disable` / `uninstall` / `marketplace remove` `run` actions only in the
  targets and roots the ledger records (a copy the person installed, enabled or added
  stays; wy-local stays where the watchyard plugin still is). Without `claude` these
  are skipped with a warning and their records stay; titles and tab configs still go;
- removal of tab files whose hash still matches.

A second `on` with nothing changed gives "0 changes" and no backup set. The link guard
(setup.md 7.5) applies: settings files behind links are edited at their target, and links
stay.

## T11. `wy doctor` additions

- **Titles.** When on: every settings target has the env key, the hooks plugin has the title
  entries, and `wy hook --title` prints valid JSON for a fixture.
- **Board.** The token file is mode 0600, `/v1/me` works, the device kind is `desktop`, and
  the device is bound to this machine.
- **Pane.** It is enabled per target, and `claude --version` is at or above the mod minimum.
- **Warp plugin.** It is enabled in N of M targets, `jq` is on `PATH`, and the marketplace
  ref matches the ledger.
- **Tab configs.** Each file still has our hash (else it is "yours now", shown as info),
  and the launcher path it runs still exists.
- **Warp detection.** The channel, the folder, and (when inside Warp) whether
  `WARP_FOCUS_URL` passes the allow-list. A failure there is the drift signal for Warp
  changing its focus URL format.

## T12. Config and paths

```toml
[terminal]                  # (terminal.md)
titles = false              # written only by wy term titles on|off (wy config set refuses it)
band = true                 # pane: band above the prompt when another session needs you
pane_auto_open = true       # pane: open as a sidebar in wide fullscreen sessions
source = "auto"             # board: auto | hub | local
ascii = false
```

New paths in `<state>`:

- `board/token.json` (0600)
- `board/last-view.json` (0600)
- `term/title-<session>.json` (pruned after 7 days by `wy tick`)

Outside `<state>`, `wy` writes only the `watchyard-*.toml` tab configs (T8) and, through
`claude plugin` and `json_set`, the settings targets (T10). All of them are journaled.

## Claude Code facts

Checked on 13 October 2026 against Claude Code 2.1.296 (the installed `claude`), read
only: `--help` output, the documentation at code.claude.com (hooks, env-vars,
terminal-config, plugins/mods/interface, plugins/marketplace-reference), the strings of the
installed build, and runs in a throwaway `HOME` and `CLAUDE_CONFIG_DIR` from a neutral
folder. No real settings were read or changed. "Checked" means verified by a run or by the
code of the installed build; "uncertain" means documented but not seen working.

1. **`terminalSequence` from a synchronous hook** — **checked**, except the live effect,
   which is **uncertain** until the manual check.
   - The field is in the hook output schema of 2.1.296 with the allow-list OSC 0/1/2/9/99/777
     and BEL; `ESC ] 2 ; … BEL` is inside it. A value outside it is dropped with a debug
     line, never shown.
   - It is read only from a synchronous hook: the output of an `"async": true` hook is
     delivered later as `additionalContext`/`systemMessage` only, and a hook whose JSON is
     the `{"async": …}` form yields no answer at all. So the title hook must be synchronous
     (as T6 says).
   - It works on events that discard `systemMessage` (Notification, StopFailure), and only
     in an interactive session with the interface on screen; `-p` and the SDK ignore it.
   - On SessionStart and UserPromptSubmit only plain-text stdout becomes context. Stdout
     that is one JSON object is parsed as hook output, so `{"terminalSequence":"…"}` is not
     added as context. A JSON object that fails to parse is a non-blocking error and is not
     added either; so `wy hook --title` must print exactly one object or nothing (T6).
   - Not seen: the title actually changing in Warp, Ghostty or tmux. That needs a logged-in
     interactive session; it is the "Titles" manual check. Risk 2's fallback stands.
   - Detail: 2.1.296 drops `terminalSequence` for hooks it treats as the person's own in one
     guarded mode (`personal`). Plugin hooks are not in that mode in a normal session; if a
     capture shows titles missing, this is the first thing to look at.
2. **`CLAUDE_CODE_DISABLE_TERMINAL_TITLE` from settings `env`** — **checked** (docs and
   code), live effect **uncertain**.
   - The docs (env-vars, "Set variables in settings files") say settings `env` values are
     applied to Claude Code's environment however `claude` was started, and a running
     session applies changed values when the file is saved. 2.1.296 reads the variable from
     that environment at every title write (the process title, the tab title from the
     conversation, the resume screen title) and lists it among the variables settings may
     set.
   - Side effects (new, now in T6): it also skips the session-title request and turns off
     OSC 7501 program-status reports.
   - Not seen: Claude Code's title actually staying off in a terminal, and the 2026 reports
     of it being ignored on Windows and macOS are not settled. Risk 3's fallback stands.
3. **`claude plugin validate` accepts a modules-only `hooks.json` next to the existing
   plugin in one marketplace** — **checked** (run).
   - A `wy-local` with `plugins/watchyard` (this repository's plugin) and
     `plugins/watchyard-pane` (`{"modules": ["./register.tsx"]}`): `validate --strict` of the
     marketplace passes; `validate --strict` of the pane passes and reports the module's
     hooks and calls (T5); `marketplace add <dir>` then `install watchyard@wy-local` and
     `install watchyard-pane@wy-local` succeed and both are enabled.
   - A `modules` entry whose file is missing fails `validate` of the plugin (`no such file`)
     but not of the marketplace; hence the placeholder `register.tsx` (T5).
   - `types` in `plugin.json` is checked too: a contract file may export types only.
4. **`claude plugin marketplace add` accepts a pinned ref for `warpdotdev/claude-code-warp`**
   — **no** (run). This is why the piece stays opt-in (T10).
   - The marketplace source takes `owner/repo#ref`, `owner/repo@ref` or `<git url>#ref`, and
     clones with `git clone --depth 1 --branch <ref>`, so a ref is a branch or a tag. A commit
     SHA fails (`Remote branch <sha> not found in upstream origin`) in all three forms.
   - The repository has no tags (only branches; `main` was `53ab8d63…` on the day), so
     nothing immutable can be pinned through `marketplace add`.
   - `#main` works: settings get `extraKnownMarketplaces.claude-code-warp.source =
     {"source": "github", "repo": "warpdotdev/claude-code-warp", "ref": "main"}`, and
     `known_marketplaces.json` records the `installLocation` checkout, whose `HEAD` is the
     commit.
   - So `wy term setup --warp-plugin` adds `warpdotdev/claude-code-warp#main`, the ledger
     records `ref: "main"` and the resolved `commit`, `wy doctor` warns when the checkout's
     `HEAD` differs, and the piece stays opt-in (a fallback). A true pin would need a
     `git-subdir` plugin source with `sha` in wy's own marketplace (id `warp@wy-local`); that
     is not done.
