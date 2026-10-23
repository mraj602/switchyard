# testdata

Realistic, redacted sample data for Switchyard's Go tests (golden files, record round-trips). Captured on 2026-10-12 with **Claude Code 2.1.296**, on Linux, from the Warp terminal.

Maintainer note: `scripts/fixtures/regenerate.sh` reads the maintainer's real `~/.claude*` folders and
spends real Claude usage; contributors should not run it. Run
`scripts/fixtures/check-redaction.sh` before committing any change here.

Everything here went through `scripts/fixtures/redact.py` (run with `python3 -I`). Do not edit files by
hand; regenerate them (see the end). Real vs synthetic is stated per file.

## Redaction rules (what the fakes look like)

| Real | Fake | Stable across files? |
| --- | --- | --- |
| e-mail address | `user1@example.com`, `user2@example.com` ... (slot order) | yes |
| account uuid | `aaaaaaaa-0000-4000-8000-00000000000N` | yes |
| org uuid | `bbbbbbbb-0000-4000-8000-00000000000N` | yes |
| display / full name | `Test User N` (all three logins are one person, so they share one fake) | yes |
| org name | `Org N` | yes |
| account id (8 hex) / account label | `acct000N` / `Account N` | yes |
| `/home/<user>`, `-home-<user>-` | `/home/user`, `-home-user-` | yes |
| hostname | `host-1` (a no-op here: the hostname equals the user name, so the home-path rule wins) | yes |
| company / area words | `area-a`, `area-b` | yes |
| `sk-ant-`, `gho_`, `ghp_`, Bearer, JWT, `sessionKey=`, claude.ai `session_...` ids | `[REDACTED]` / `session_REDACTED` | |
| values under secret-like keys (token, cookie, authorization, signature ...) | `[REDACTED]` | |
| assistant text, prompts, tool output, thinking (hooks, transcripts) | first 5 words + `...[truncated]` | |

Kept as they are: session ids, prompt ids, request ids, tool-use ids, message ids, timestamps, model ids,
token counts, every usage-response field (unknown fields such as `iguana_necktie` included).
Note the config-dir order in the fixtures: slot 1 = `~/.claude`, 2 = `~/.claude-2`, 3 = `~/.claude-3`;
slots 4 to 7 are logged out on this machine.

## hooks/ (REAL, captured)

Captured with `scripts/fixtures/capture-hook.sh`, registered for every hook event through a per-run
`--settings` file plus `--setting-sources local` (no real settings file was touched, the user's own
hooks did not fire). 8 headless `claude -p` runs in a throwaway project dir, none with
`--dangerously-skip-permissions`:

| Run | Prompt (short) | What it produced |
| --- | --- | --- |
| 01-write-and-bash | create hello.txt, `cat` it, reply DONE (`acceptEdits`, Write + Bash(cat) allowed) | SessionStart, UserPromptSubmit, PreToolUse x2, PostToolUse x2, Stop, SessionEnd |
| 02-permission-needed | `echo hi > out.txt` with Bash not allowed, default mode | adds PermissionRequest; in `-p` mode it is auto-denied (no PostToolUse), then Stop |
| 03-question | "ask me which colour I prefer and stop" | Stop whose `last_assistant_message` is a question |
| 04-subagent | Agent tool, subagent replies OK | SubagentStart, SubagentStop (+ Pre/PostToolUse for tool `Agent`) |
| 05-tool-failure | `cat nonexistent.txt` | PostToolUseFailure |
| 06-stop-failure | `--model claude-bogus-9` | StopFailure with `error: "model_not_found"` (no Stop) |
| 07-compact | `/compact` on an empty session | PreCompact (`trigger`, `custom_instructions`) |
| 08-plain-with-env | "Reply DONE" | same as a minimal run, with the env block filled |

Files:
- `hooks/by-event/<Event>.json`: stdin payload of the first occurrence of each event that fired. Tool events
  also have `<Event>.<Tool>.json` (e.g. `PreToolUse.Bash.json`, `PermissionRequest.Bash.json`). Fired:
  SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest, Stop,
  StopFailure, SubagentStart, SubagentStop, PreCompact, SessionEnd.
- `hooks/runs/NN-name.jsonl`: the whole ordered capture log of each run, one envelope per line:
  `{"event": <name the script was given>, "env": {...}, "payload": <stdin JSON>}`. In runs 01 to 07 `env` is
  `{}`: the first version of the capture script lost its env block (a jq bug, fixed before run 08), so only
  run 08 has env. Good for ordering/state-machine tests.
- `hooks/env.json`: the allow-listed environment the hook process saw in run 08 (`CLAUDE_CONFIG_DIR`,
  `CLAUDE_PROJECT_DIR`, `CLAUDE_ACCOUNT`, `CLAUDE_AREA`, `CLAUDE_CODE_ENTRYPOINT`, `TERM_PROGRAM`,
  `TERM_PROGRAM_VERSION`, `WARP_*`). `CLAUDE_ACCOUNT` and `CLAUDE_AREA` come from the user's `claude-launch`
  wrapper, not from Claude Code. The Warp terminal-session uuid is replaced by `000...01`.

**Did not fire** in any run: `Notification` (no idle / permission prompt in headless mode), `PermissionDenied`,
`PostCompact`, `UserPromptExpansion`, `TaskCreated`, `TaskCompleted`, `Setup`. They are registered in the
settings file, so re-running after a change in behaviour would catch them. Interactive-only events
(Notification `permission_prompt`, `idle_prompt`, `elicitation_dialog`) need a manual capture: register
`capture-hook.sh Notification` in a real interactive session. No Notification fixture exists; write a synthetic
one from the docs if needed. Note `Stop`'s `last_assistant_message` is the field the "reads as a question" rule
uses (see `03-question`).

## statusline/ (SYNTHETIC, from docs)

`full.json`, `early-session.json` (no rate limits, null context percentages, zero cost), and
`limit-reached-1m-context.json` (5h at 100 %, 1M window, `exceeds_200k_tokens: true`). Built by
`scripts/fixtures/make-statusline.py` from the documented status line schema (`session_id`, `transcript_path`,
`cwd`, `model{id,display_name}`, `workspace{current_dir,project_dir}`, `version`, `output_style`, `effort`,
`cost{...}`, `exceeds_200k_tokens`, `context_window{...}`, `rate_limits{five_hour,seven_day}{used_percentage,resets_at}`,
`vim`), cross-checked against the fields the user's own `~/.claude/statusline-command.sh` reads.
`claude -p` never runs the status line, and an interactive capture was refused by the folder-trust prompt, so
these are not captured. Values are invented.
Replace with a real capture once a pseudo-terminal capture is possible.

## auth/ (REAL)

- `status-N.json`: `claude auth status --json` for `~/.claude` (N=1) and `~/.claude-2` ... `~/.claude-7`. Slots 1 to 3
  logged in (`max`, `max`, `team`), slots 4 to 7 `loggedIn: false` with only the six base keys. The command prints no
  tokens. Quirk: for slot 1 the command must run with `CLAUDE_CONFIG_DIR` **unset**; with it set to `~/.claude` it looks
  for `~/.claude/.claude.json` and reports "not found" (state lives in `~/.claude.json`).
- `oauth-N.json`: only the `oauthAccount` key of each logged-in folder's `.claude.json` (slot 1 reads `~/.claude.json`).
  No other key and no credentials file was read.

## usage/ (REAL)

claude.ai usage responses, every field kept (27 top-level keys, `iguana_necktie` is an object with
`utilization`, `limit_dollars`, `used_dollars`, `remaining_dollars` on the two Max accounts; null on Team).
`response-NN.json`: the earliest and latest stored poll per account (6 files); `index.json` says which
account, timestamp and why. No raw `/api/account` or organizations response was stored, so there is none.
These files are no longer regenerated: they stay as captured.

The hub's usage tests have their own golden pair in `internal/hub/usage/testdata/`:
`snapshots.jsonl` (raw usage responses, one per line, `raw` kept as the original string) and
`windows.jsonl` (the windows expected from those responses). Also kept as captured.

## transcripts/ (REAL, truncated)

Claude Code transcript JSONL of three of the capture runs (`01-write-and-bash`, `03-question`, `07-compact`), every line
kept so the line types are all there: `queue-operation`, `user`, `attachment`, `assistant` (with `message.usage`,
`message.model`, `requestId`, `sessionId`, `cwd`, `version`), `last-prompt`, `system`, `cost-state`, ... Long text
(prompts, assistant text, attachments, tool results) is cut to a few words.

## agents/ (REAL, redacted)

`claude agents --json` per config folder (`claude-agents-1-default.json`: `~/.claude` with `CLAUDE_CONFIG_DIR` unset, `-1-explicit`:
set to `~/.claude`, `-2`, `-3`), plus `sessions-dir/<pid>.json` (the fallback source, copied from `~/.claude-2/sessions/`; the `*.key` files
next to them are never read). Interactive items carry `pid` + `status`, background items `id` + `state: "blocked"` and no pid. Session ids are
replaced by `00000000-0000-4000-8000-00000000000N`, `name` by `session N`, `pidDomain`'s hash by zeros (`scripts/fixtures/collect-agents.sh`, which
then runs `redact.py` and `check-redaction.sh`). `messagingSocketPath` is kept on purpose: a test proves `sy` drops it.

## Regenerating

```
scripts/fixtures/regenerate.sh            # runs capture-runs.sh (spends a little real usage), then everything else
scripts/fixtures/regenerate.sh <capdir>   # reuse cap1..cap8.jsonl from an earlier capture-runs.sh
scripts/fixtures/check-redaction.sh       # zero-hit grep for real identities, tokens and company words
```
Scripts: `collect-agents.sh` (agents/ only), `capture-hook.sh` (the hook), `capture-runs.sh` (the 8 runs), `collect-hooks.py`,
`make-statusline.py`, `redact.py`, `check-redaction.sh`. Raw (unredacted) data only ever lives in a temp dir that is
deleted on exit. `regenerate.sh` replaces everything here except this README; update the README if the outputs change.
