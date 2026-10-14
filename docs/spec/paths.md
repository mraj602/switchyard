# Paths

Status: spec v1, phase 0. Implemented in `internal/paths` (phase 1). Every other package gets
its folders from there; nothing else builds paths from `$HOME`.

`sy` uses three folders:

| Folder | Holds | Can be lost? |
| --- | --- | --- |
| **config** | `config.toml` only. | Recreated by `sy setup` / `sy connect`. |
| **state** | Machine id, device token, outbox, caches, logs, setup backups. | Losing it loses unflushed records and makes this a new machine. |
| **data** | The hub database and its backups. Only on the machine that runs the hub. | No: this is the long-term record. |

## 1. Locations

| | Linux, WSL, BSD | macOS | Windows |
| --- | --- | --- | --- |
| config | `$XDG_CONFIG_HOME/switchyard` (default `~/.config/switchyard`) | `$XDG_CONFIG_HOME/switchyard` (default `~/.config/switchyard`) | `%LOCALAPPDATA%\Switchyard\config` |
| state | `$XDG_STATE_HOME/switchyard` (default `~/.local/state/switchyard`) | `$XDG_STATE_HOME/switchyard` (default `~/.local/state/switchyard`) | `%LOCALAPPDATA%\Switchyard\state` |
| data | `$XDG_DATA_HOME/switchyard` (default `~/.local/share/switchyard`) | `$XDG_DATA_HOME/switchyard` (default `~/.local/share/switchyard`) | `%LOCALAPPDATA%\Switchyard\data` |

XDG variables are used only when set to an absolute path (the XDG rule); otherwise the
default applies. On Windows, `%LOCALAPPDATA%` is used if set and absolute, else
`<home>\AppData\Local`; XDG variables are ignored there.

**Overrides** (in this order, first wins):

1. `SY_CONFIG_DIR`, `SY_STATE_DIR`, `SY_DATA_DIR`: one folder each.
   Overrides must be absolute paths: a relative one is an error (a hook's working folder
   is arbitrary, so a relative path would scatter state).
2. `SY_HOME`: sets all three to `$SY_HOME/config`, `$SY_HOME/state`, `$SY_HOME/data`. Used by
   tests and by running a second, throwaway `sy` next to the real one.
3. The table above.

`sy` never reads `SWITCHYARD_DATA`: that variable belongs to the prototype Electron app and
points at its user-data folder.

`sy paths` (phase 1) prints the three folders and where each came from; `sy doctor` shows the
same.

## 2. Decisions and why

### Linux: `~/.config/switchyard` is shared with the prototype

The prototype desktop app is an Electron app named `switchyard`, so its user-data folder is
`~/.config/switchyard` (`~/Library/Application Support/switchyard` on macOS,
`%APPDATA%\switchyard` on Windows). It holds the claude.ai sign-ins (`Partitions/`),
`config.json` (with the prototype's hook token), `accounts.json`, `usage.db` and Chromium
caches.

**The phase 6 desktop app does not use that folder** (desktop.md C1; this replaces the
phase 0 plan of keeping it). The prototype keeps running next to the new app until the person
retires it, and Chromium holds a singleton lock per user-data folder, so two apps cannot share
one. The new app (`app/`, package `switchyard-desktop`) sets its user data explicitly, before
`ready`, to `<appData>/switchyard-desktop`:

| | Linux | macOS | Windows |
| --- | --- | --- | --- |
| desktop app user data | `~/.config/switchyard-desktop` | `~/Library/Application Support/switchyard-desktop` | `%APPDATA%\switchyard-desktop` |

It refuses to start with any user-data folder whose basename is `switchyard` in any case
(Electron would otherwise derive `Switchyard` from `productName`, which is the prototype's
folder on a case-insensitive macOS volume). `SY_APP_USER_DATA` (absolute) overrides the
folder for development and tests, and only in an unpackaged build. It writes no autostart
entry under the prototype's name (`switchyard.desktop`).

The prototype's folder is left alone: the new app never opens, writes or locks it. The one
exception is the importer ([phase 6 WP-F](../phases/6.md)), which runs only when the person asks, refuses
while the prototype runs, reads `accounts.json` and **copies** (never moves)
`Partitions/acct-<id>/` to the new app's `Partitions/tab-<id>/`. The prototype's folder stays
as the fallback until the person deletes it by hand.

`sy` still uses `~/.config/switchyard` as its config folder, as the plan says, under strict
coexistence rules:

- `sy` owns exactly one name in that folder: `config.toml` (plus its transient
  `.config.toml.<random>.tmp` during an atomic write, and `config.toml.bak` written by
  `sy setup`).
- `sy` never creates any other file there, never lists, cleans, moves or deletes the folder,
  and never reads the prototype's files (in particular not `config.json`).
- `sy uninstall --purge` removes only `config.toml` and `config.toml.bak`, never the folder.
- Neither desktop app writes `config.toml`. The phase 6 app reads what it needs by running
  `sy` (`sy desktop info --json` and the other commands of desktop.md C11); it does not open
  `config.toml` or any `sy` folder itself.
- If `sy` creates the folder (no prototype installed), it uses mode `0700`, as Electron does.

Why not a separate name such as `~/.config/switchyard-sy` or `~/.config/sy`: the plan, the
docs and the screens all say `~/.config/switchyard/config.toml`; `sy` is a short, generic
name likely to clash with other tools. The rules above make the sharing with the prototype
harmless: there is no file-name overlap (`config.toml` vs the prototype's `config.json`), and
neither side deletes the folder. When the prototype is retired, the folder holds only
`config.toml`.

Everything that `sy` writes often, and everything that matters (outbox, token, machine id),
lives in the state folder, which nothing else uses.

### macOS: XDG layout, not `~/Library/Application Support` (deviation from plan)

The plan put macOS state in `~/Library/Application Support/Switchyard`. But Electron's
user-data folder on macOS is `~/Library/Application Support/switchyard`, and the default
APFS volume is case-insensitive, so that is the **same folder** as the Electron app's. Putting
the outbox and the device token inside the desktop app's folder is exactly the clash we want
to avoid. So on macOS `sy` uses the same `~/.config` / `~/.local/state` / `~/.local/share`
layout as on Linux. Command-line tools commonly do this on macOS (git, gh, fish), it is the
same on every Unix machine (simpler docs, the same paths on SSH servers), and there is no
clash: the prototype does not use `~/.config` on macOS, and the phase 6 app uses its own
`~/Library/Application Support/switchyard-desktop`.

launchd agents still go in `~/Library/LaunchAgents` (phase 2/3), and their logs go to the
state folder.

### Windows: `%LOCALAPPDATA%`, not `%APPDATA%`

Electron's user data on Windows is `%APPDATA%\switchyard` (roaming). `sy` uses
`%LOCALAPPDATA%\Switchyard`, which does not clash, and which does not roam: the machine id,
device token and outbox are per machine and must never be copied to another computer by a
roaming profile. Config also lives there, so a roaming profile does not carry one machine's
hub token to another. Subfolders `config`, `state`, `data` keep the three roles apart inside
one folder.

## 3. What lives where

```
<config>/
  config.toml                  user settings (below); mode 0600

<state>/                       mode 0700
  machine.json                 machine id and name (identity.md)
  machine.lock                 taken while creating machine.json
  token                        device token, one line, mode 0600 (tokens.md)
  outbox/                      outbox.md
  identity/                    per-slot login cache (identity.md)
  scan/offsets.json            transcript read offsets (sy scan)
  scan/history.json            slots already history-imported
  statusline/last.json         last limit_sample per session (once-a-minute rule)
  statusline/wrapped-<session>.txt  last good output of the wrapped status line (timeout fallback)
  logs/sy.log                  rotated at 5 MB, 3 kept
  backup/<UTC time>/           journal + saved files of each sy setup/account/uninstall run (setup.md 7)
  setup.lock                   held while a setup run applies
  setup/ledger.json            what sy has installed now (setup.md 9.3)
  plugin/marketplace/          the local sy-local plugin marketplace (setup.md 5.2)
  share/mcp-base.json          last synced mcpServers, mode 0600 (setup.md 5.4)
  share/mcp-conflicts.jsonl    MCP merge conflicts (names only)
  share/copies.json            Windows synced copies (setup.md 5.7)
  hub.json                     last known hub version and record_v_max (machines.md 7.2; phase 5)
  machine-info.json            last machine_info sent (machines.md 6.3)
  machines.json                operator: machines added over SSH, mode 0600 (machines.md 2)
  push/direct.json             direct ntfy topic and token, mode 0600 (machines.md 5.2)
  push/policy.json             direct push policy from the hub (machines.md 5.3)
  push/direct/<hash>.json      one marker per directly pushed episode (machines.md 5.4)
  update/                      downloads and verified archives (machines.md 7.3); cache/ for other platforms
  hub.sock                     forward mode with --forward-socket only (machines.md 3.3)
  pull/                        hub host only: id_ed25519(.pub), generated ssh_config and known_hosts (machines.md 3.4)
  board/token.json             read-only board token for sy board and the pane, mode 0600 (terminal.md T3; phase 7)
  board/last-view.json         last board view shown, mode 0600 (terminal.md T3)
  term/title-<session>.json    last tab title per session, pruned after 7 days by sy tick (terminal.md T6)
  keys/                        mode 0700: device.json, owner.sealed (owner machine), owner.pub, hub.pub,
                               certs.json, matrix.json, revocations.json (keys.md 10; F1)
  hub_sign.key                 hub machine only: the hub signing key, mode 0600, exported with the hub (keys.md 3.4)
  agent/                       sy agent: link.json (last seq), audit-out.jsonl, agent.sock on macOS (peers.md 2)
  peers/sessions/<session>.json  written by sy hook when peers are on: mode, human_at (peers.md 2.4)
  peers/delivered/<session>    time of the agent's last delivery into that session
  peers/threads.json, peers/seen.json, peers/audit.jsonl, peers/off   (peers.md 6.2, 8.4, 10, 11)
  logs/agent.log               rotated like sy.log

<data>/                        mode 0700, hub only
  hub.db, hub.db-wal, hub.db-shm
  backups/hub-<UTC time>.db    sy hub backup
  exports/hub-<UTC time>.syx   sy hub export, mode 0600 (machines.md 8.1)
  app/android/                 mirrored phone release: current and previous APK + manifest (phone.md P16.2)
```

Runtime sockets (Linux, `$XDG_RUNTIME_DIR`): `sy/agent.sock` and `sy/r/<16 hex>.sock`, folders
0700, sockets 0600 (peers.md 2.3, 8.6). `sy` reads, never writes, each slot's
`<config>/sessions/<pid>.json` and never opens `sessions/*.key` (identity.md 4).

Outside these folders, phase 5 writes only `~/.ssh/switchyard.conf` and one `Include` line in
`~/.ssh/config` on the operator (forward mode), and one line in `~/.ssh/authorized_keys` on a
pulled remote (machines.md 3.3, 3.4); both are journaled and undone by `sy machine remove`.
Phase 7 writes only its own `switchyard-*.toml` tab configs in Warp's tab config folder
(terminal.md T8), journaled and undone by `sy term warp off` or `sy setup --undo`.

Inside a Docker image the hub uses `SY_DATA_DIR=/data` (a volume).

## 4. `config.toml`

TOML, read with a small, well-maintained parser (chosen in phase 1; candidate
`github.com/pelletier/go-toml/v2`). Unknown keys are kept on rewrite and reported by
`sy doctor`. Written atomically (temp file, fsync, rename), preserving comments where the
parser allows; `sy config set` is the supported way to change it. `sy config set hub.url <url>`
applies the same check as `sy connect` (plain `http` only to loopback, Tailscale or
single-label hosts, unless `hub.insecure_lan` is true or `--insecure-lan` is given, which
records it) and stores the normalised scheme://host[:port].

```toml
# Switchyard (sy) settings. Unofficial; not affiliated with Anthropic.
version = 1

[hub]
url = "https://hub.tailnet-name.ts.net"   # empty: no hub yet (phase 1), or pulled (phase 5)
insecure_lan = false                         # allow plain HTTP to a non-loopback address
reach = "direct"                             # phase 5: direct | forward | pull (machines.md 3)
dial = ""                                    # phase 5, forward: "127.0.0.1:47830" or "unix:<path>"

[push.direct]                                # phase 5 (machines.md 5); topic/token in <state>/push/direct.json
enabled = false
server = "https://ntfy.sh"
private = true
after_s = 120

[update]                                     # phase 5 (machines.md 7.3)
base_url = ""                                # default GitHub releases; SY_INSTALL_BASE_URL wins
verify_attestation = false

[machines]                                   # phase 5, operator (machines.md 4.4)
hub_admin = ""                               # "local" or a command prefix, e.g. "ssh oci sy"

[privacy]
last_message = true                          # send derived.last_message (on for your own hub)

[hooks]
tool_events = false                          # decision 6: PreToolUse/PostToolUse for all tools

[statusline]
wrap = "bash ~/.claude/statusline.sh"        # the user's original status line command

[outbox]
fsync = true
max_mb = 50
keep_days = 7

[slots]
extra = []                                   # extra config folders outside ~/.claude*
labels = { "~/.claude-2" = "work" }          # local fallback; the hub's seat label wins
colours = { "~/.claude-2" = "#f59e0b" }      # phase 3 (setup.md 9.2)

[share]                                      # phase 3 (setup.md 9.2)
primary = "~/.claude"
mcp = true                                   # true | false | "external"
mcp_exclude = []

[terminal]                                   # phase 7 (terminal.md T12)
titles = false                               # written only by sy term titles on|off (sy config set refuses it)
band = true                                  # pane: band above the prompt when another session needs you
pane_auto_open = true                        # pane: open as a sidebar in wide fullscreen sessions
source = "auto"                              # board: auto | hub | local
ascii = false
```

The device token is **not** in this file (it lives in `<state>/token`), so `config.toml` can
be shown or shared without leaking anything.
