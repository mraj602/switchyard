# Troubleshooting

## Start with `wy doctor`

```sh
wy doctor          # every check, with a one-line fix for each problem
wy doctor --json   # the same, for a bug report or a script
```

It checks the Claude Code folders (logged in, plugin, status line, shared links), the hub
(reachable, token accepted, version), the timer, `loginctl` linger on Linux, queued records,
the agent and link keys, and on the hub machine the public URL, push keys and pull targets. It
changes nothing.

Other read-only commands that help:

| Command | Shows |
| --- | --- |
| `wy version` | The `wy` version, commit, OS and Go version |
| `wy paths` | The config, state and data folders and where each came from |
| `wy outbox stat` | Records waiting to be sent to the hub |
| `wy status` | Sessions on this machine, as the local records see them |
| `wy board --once` | One frame of the board in the terminal |
| `wy flush` | Sends queued records now and prints the error if it fails |
| `wy agent status` | The peers agent and its link to the hub |

## Logs

| Where | Log |
| --- | --- |
| Every machine | `<state>/logs/wy.log` (rotated at 5 MB) |
| Machines with the agent | `<state>/logs/agent.log` |
| Hub as a Linux user service | `journalctl --user -u watchyard-hub -n 100` |
| Hub in Docker | `docker compose logs hub` (from `deploy/`) |
| Android app | Settings → Notifications shows the link's state and last error |

`<state>` is the state folder from `wy paths`.

## Common problems

| Symptom | Check | Fix |
| --- | --- | --- |
| Nothing shows up on the board | `curl -s http://127.0.0.1:8787/health` (or your hub's address) from the machine answers `{"ok":true,...}`; `wy flush` prints an error; `wy outbox stat` shows records waiting | Start the hub; fix the URL with `wy connect <url> --keep-token`; if the token was rejected, see the next row |
| `wy flush` says the token was rejected | On the hub, `wy hub token list` (`--all` shows revoked devices). Is the device revoked? Is its machine id the one `wy machine` prints? | Make a new token or pairing code and run `wy connect` again ([install.md](install.md)) |
| A server stops reporting when you log out of it | `loginctl show-user $USER -p Linger` says `no` | `loginctl enable-linger $USER` (may need sudo); the user timer and services then keep running |
| Sessions missing after `wy setup` | Sessions started before setup have no hooks yet | Restart those Claude Code sessions; new sessions report at once |
| A session stays "needs you" after you answered | It clears at your next prompt or when Claude stops | Nothing to fix; it is how the hooks report. Run `wy board --once` to compare |
| Phone cannot pair | The phone must open the hub over `https://`; the QR uses the hub's public URL | Run `tailscale serve --bg 8787` (or put the hub behind a TLS proxy), start the hub with `--public-url https://...`, then `wy phone pair` ([android.md](android.md)) |
| Android notifications stop or arrive late | Android app: Settings → Notifications: Connection, Battery, Notifications permission | Tap **Fix** for battery optimisation and follow the maker guide shown; tap **Reconnect now**; check the hub is reachable from the phone |
| No pushes at all | Board → Alerts and rules: is the rule on? Quiet hours? The delivery log row for the alert | `wy doctor` on the hub lists push problems; see [alerts.md](alerts.md) |
| Desktop app says `wy` is missing or not connected | `wy` on `PATH` or in `~/.local/bin`, and `wy doctor` | Install `wy` and run `wy setup` ([desktop.md](desktop.md)); the account tabs work meanwhile |
| Usage strip says "Stopped: signed out (401)" | The tab is signed out of claude.ai | Sign in again in that tab, then **Resume** |
| Usage strip says "Stopped: rate limited (429)" or "not available (403)" | claude.ai refused the read | Wait, then **Resume**; or turn reading off for that account ([usage.md](usage.md)) |
| `wy keys ...` or `wy peers allow` says "refusing to sign" (exit 4) | It ran inside a Claude Code session or without a terminal | Run it in a terminal Claude Code did not start, or use the board's owner sign-in |
| Machine says its hub is frozen | The hub was exported with `wy hub export` | Run the `wy connect ... --keep-token` line `wy doctor` prints, or `wy hub unfreeze` on the old hub to call the move off ([maintenance.md](maintenance.md#moving-the-hub)) |
| `wy agent enable` stops at a hub key mismatch | Compare the two fingerprints it prints with `wy hub keys show` on the hub | Only if the hub's matches: `wy agent enable --repin-hub` |

## Peers messages are refused

`wy peers send` prints one line saying why, and exits with a code:

| Exit | Meaning | What to do |
| --- | --- | --- |
| 0 | Delivered or done | |
| 2 | Usage error or an ambiguous target | Use the handle from `wy peers list`, or the account number |
| 3 | Not supported here | Peers do not run on Windows |
| 4 | Signing guard, or not inside a Claude Code session | `send` and `list` run inside a session; widening runs at a plain terminal |
| 5 | Policy: `not_allowed`, `paused`, `loop_guard`, `off`, `no_matrix` | Check `wy peers matrix`; `wy peers resume`, `wy peers on`, or allow the pair; a loop-guard stop clears when you type in either session |
| 6 | Limits: `too_big`, `rate_limited`, `secret`, `not_text` | Shorten the text, wait for the time it names, remove anything that looks like a key or token |
| 7 | Not delivered: `unknown_target`, `expired`, `target_gone`, `rejected`, `failed`, `hub_unreachable` | Check the target is running (`wy peers list --all`), the hub is reachable, and the other machine's agent runs |
| 8 | Queued, not final yet | The other machine's agent is offline; the message waits until its time to live ends |

A remote target shown as `start (blocked: ...)` means the machines are not owner-certified yet;
finish the steps in [peers.md](peers.md). Inspect with `wy peers status` and
`wy peers audit --since 1h` (metadata only).

## Filing a bug

Open an issue on GitHub with the bug report template. Include:

```sh
wy version
wy doctor --json
wy paths
wy outbox stat
```

plus the failing command's output, the relevant log lines, `claude --version`, and for the
apps their version and your OS or Android version.

Read everything before you paste it. Never paste a device token (`sy_...`), a pairing code or
secret, an owner link, your owner passphrase or recovery code, `.credentials.json`, or anything
from `sessions/*.key`. `wy doctor` output holds no tokens.

Security problems go through [SECURITY.md](../../SECURITY.md), not public issues.
