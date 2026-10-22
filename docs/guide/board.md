# The board

The board is the hub's view of every session on every machine, grouped by account, with each
account's limits. You can open it in a browser, in the desktop app, in the Android app, in any
terminal, or as a pane inside Claude Code. All of them read the same hub.

## Session states

| State | Meaning | Needs you |
| --- | --- | --- |
| `working` | Claude is running a turn | no |
| `needs_permission` | Claude is waiting on a permission prompt | yes |
| `asked` | Claude asked a question, opened a dialog, or ended its turn with a plain-text question | yes |
| `done` | Idle at the prompt (a new session shows as "new") | no |
| `failed` | The turn stopped with an error: rate limit, login, billing | yes (not for overload or server errors) |
| `ended` | The session ended | no |
| `gone` | The session vanished without ending (crash, reboot) | no |

States come from Claude Code's hooks through `sy hook`; the rules are in
[docs/spec/states.md](../spec/states.md). Question detection uses the tail of Claude's last
message, which is sent to the hub only while `privacy.last_message` is on (the default for your
own hub; see [security.md](security.md)).

## In a browser

The board is served by the hub at its address, for example `http://127.0.0.1:8787` on a
laptop hub or `https://hub.example.ts.net` behind `tailscale serve`.

There are two kinds of sign-in:

- **Owner:** `sy hub owner-link` (on the hub's computer, or
  `docker compose exec hub sy hub owner-link`) prints a one-time link valid for 10 minutes. It
  signs that browser in as the owner for 30 days. The owner can change rules, pair and revoke
  devices, edit the peers matrix and confirm the owner key.
- **Read-only:** a device token or a pairing code (`sy hub pair --kind browser`) signs a
  browser in to read the board; controls that change things are hidden or explain why.

Screens: **Now** (what needs you, account limits), **Sessions** (filter by state, group by
machine), **Accounts** (limits, resets, pace, most room right now), **Machines** (last seen,
backlog, version, reach), **Alerts** (rules, quiet hours, recent notifications), **Analytics**
([usage.md](usage.md)), **Peers** ([peers.md](peers.md)), **Keys** ([peers.md](peers.md)),
**Devices** (owner: paired phones, browsers and machines; rename, revoke, add), **Settings**.

"Most room right now" is a suggestion. Switchyard never switches accounts for you.

## On a phone

- **Android:** the Android app has the same screens plus native notifications and widgets
  ([android.md](android.md)).
- **iPhone, or Android without the app:** install the board as a web app. Run
  `sy phone pair --url https://hub.example.ts.net` on the hub, scan the QR with the camera,
  add the page to the home screen, and turn on notifications in Settings
  ([alerts.md](alerts.md)). iPhone needs iOS 16.4 or newer for notifications.

## In a terminal

```sh
sy status                 # this machine's sessions, from the local outbox (no hub needed)
sy board                  # the interactive board
sy board --once           # one frame and exit; --json prints the view as JSON
sy board connect          # get the read-only board token from the hub (once per machine)
sy open                   # list accounts with their 5-hour use and reset time
sy open work              # open a session as that account (a Warp tab, or this terminal with --here)
```

`sy board` reads from the hub when this machine is connected and has a board token, and from
the local outbox otherwise. While the hub is down it keeps showing this machine's sessions
live and other machines' last known state, greyed.

`sy board connect` needs either the hub on this machine or a **desktop** pairing code: make
one with `sy hub pair --kind desktop` (or on the board, Devices, Add desktop app) and pass it
on stdin with `sy board connect --code-stdin`. The same token lets `sy analytics` read reports.

## Inside Claude Code: the pane

```sh
sy term setup --pane --dry-run
sy term setup --pane
```

This installs the `switchyard-pane` mod from the local marketplace: a side pane with the
board, a band above the prompt when another session needs you, and a status entry. It only
reads; it runs nothing but `sy`. Other terminal pieces:

- `sy term titles on`: tab titles that show the account and state.
- `sy term warp on`: one Warp tab configuration per account (`sy open <label>` opens it).
- `sy ssh user@host`: `ssh` that lets the remote session be focused from the board.
- `sy term status` shows what is on; `sy term off` removes every terminal piece.

Every change is journaled and undone by `sy setup --undo`. Details:
[docs/spec/terminal.md](../spec/terminal.md).

## Claude Code plugin from GitHub

`sy setup` installs the Switchyard plugin from a local marketplace it writes itself, with the
path of your `sy` binary filled in. That is the recommended way.

You can also add this repository as a plugin marketplace and install the plugin from there,
for example to get the `/switchyard:setup` skill before `sy` is installed:

```text
/plugin marketplace add mraj602/switchyard
/plugin install switchyard@switchyard
```

On Claude Code 2.1.275 or later one line does both:
`/plugin install switchyard --marketplace mraj602/switchyard`. From a shell, use
`claude plugin marketplace add mraj602/switchyard` and
`claude plugin install switchyard@switchyard`. Its hooks run `sy` from `PATH` and do nothing
when `sy` is not installed. When you later run `sy setup`, it adopts this plugin instead of
installing a second copy (two copies would record every event twice). The pane mod is only
available through `sy term setup --pane`.
