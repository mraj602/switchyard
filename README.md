# Switchyard (prototype)

One window for all your Claude accounts: a tab per account with its own isolated claude.ai
sign-in (chat, cowork, Claude Code / Remote Control sessions), a dashboard with every account's
limits and reset times, a full usage Analytics tab, and notifications from Claude Code sessions.
It can also run hidden at login and keep an optional self-hosted server (Docker) up to date for
your phone.

> Unofficial. Not affiliated with, endorsed by or sponsored by Anthropic. "Claude" is a trademark of Anthropic.

## New architecture (in progress)

Switchyard is being rebuilt as one Go binary, `sy`, that runs on every machine (hooks,
status line, outbox) and as the hub (`sy hub serve`: storage, session states, usage, web
board). The prototype described below keeps working, unchanged, until the new parts replace
it phase by phase.

- [ARCHITECTURE.md](ARCHITECTURE.md): repo map, data flow, package boundaries.
- [docs/plan.md](docs/plan.md): the product and architecture plan.
- [docs/spec/](docs/spec/): the contract (records, hub schema, session states, API, tokens,
  identity, outbox, paths).
- [docs/phases/](docs/phases/): the task list of each phase.
- [docs/TESTING.md](docs/TESTING.md): an ordered manual test guide for a real setup (laptop,
  SSH worker, phone). Most of what is below has not been tested by hand yet.

```bash
make build    # ./dist/sy (Go 1.27)
make check    # gofmt, go vet, go test ./..., spec checks
```

### Install

```bash
curl -fsSL https://<site>/install.sh | sh      # installs sy, then runs `sy setup`
sy setup                                        # or run it again any time: shows a plan first
```

Inside Claude Code, `/switchyard:setup` walks through the same steps. `sy setup` changes
nothing until you accept the plan it shows, backs up every file it touches, and can be
taken back:

```bash
sy setup --undo-list     # what each run changed
sy setup --undo          # undo the last run (byte for byte)
sy setup --detect        # what sy sees on this machine; changes nothing
sy account add work      # one more Claude account folder, sharing like the others
sy uninstall             # remove what sy installed; your folders and logins stay
```

See [docs/spec/setup.md](docs/spec/setup.md) for every flag.

### Keys, the link and peers

Newer parts, all off until you turn them on (see [docs/TESTING.md](docs/TESTING.md) to try them):

| What | Commands | Spec |
| --- | --- | --- |
| Owner key: a passphrase-sealed key on your own machine that signs what machines and phones may do; the hub cannot widen on its own with it set up | `sy keys init`, `sy keys status`, `sy keys certify`, `sy hub keys confirm-owner` | [keys.md](docs/spec/keys.md) |
| The link and `sy agent`: one encrypted, hub-signed channel per device (`/v1/link`), and the per-machine agent service that holds it | `sy agent enable`, `sy agent status` | [link.md](docs/spec/link.md) |
| Peers: Claude Code sessions message each other across accounts and machines, gated by an account matrix you set; the hub relays ciphertext and never sees the text. Includes the `peers` skill and Settings → Peers on the board and phone | `sy peers enable`, `matrix`, `allow`, `deny`, `pause`, `send`, `audit` | [peers.md](docs/spec/peers.md) |
| Usage from the terminal: tokens, API-equivalent cost, models, projects, sessions, limits, hits and a heatmap, from the hub's analytics (uses the read token of `sy board connect`) | `sy analytics`, `sy analytics models --days 7 --json` | [analytics.md](docs/spec/analytics.md) |
| Android app notifications without Google, Expo or any account: the app keeps its own link to your hub and posts them itself (Settings → Notifications), with an in-app updater and a release build you sign yourself | `make phone-apk-release` | [push.md 13-14](docs/spec/push.md), [phone.md](docs/spec/phone.md) |

Owner-key commands refuse to run inside a Claude Code session; run them in a normal terminal.

The rest of this README describes the prototype.

## Run

```bash
npm install
npm start          # data lives in ~/.config/switchyard (override with SWITCHYARD_DATA=/path)
npm test           # server test + end-to-end desktop test (needs a display)
```

Shortcuts: `Ctrl+0` dashboard, `Ctrl+Shift+A` analytics, `Ctrl+1…9` accounts,
`Ctrl+Shift+R` refresh usage, `Ctrl+Q` quit.

## Adding accounts

`+` → name it → a tab opens with a fresh, isolated session. Sign in with **email + code**
(Google sign-in is blocked by Google inside embedded browsers). Every tab stays signed in and
live at the same time; switching tabs never logs anything out. Sign-ins are flushed to disk
every 30 s and on quit, so restarts never cost a login.

## Usage tracking

With tracking on (per account), the app reads the same data claude.ai's Settings → Usage page
shows — 5-hour, 7-day, model-scoped weekly limits, cloud session credits (`iguana_necktie`)
and the per-surface split — from inside that account's own session every 5 minutes. Every
poll is stored in `usage.db` (SQLite) with the raw API response, so fields the API adds later
can still be analysed. Notifications fire when a window crosses 80% and 95%.

Caveat: these endpoints are undocumented and may change. claude.ai cookies never leave the app.

## Analytics

The Analytics tab combines two sources:

- **Limits** (official %, all surfaces): pace of every current window against even use
  (comfortable → runaway), projected value at reset and time-to-100%, a "use next" ranking,
  weekly burndown per account, weekly points used per day, every weekly window with its peak
  and what expired unused, 5-hour window stats, cloud credits over time, surface split.
- **Claude Code tokens** from local transcripts (`~/.claude*/projects/**/*.jsonl`, scanned
  incrementally in a worker thread, deduplicated per API response): API-equivalent $ per day
  by account or model, model mix, token mix and cache hit rate, weekday × hour heatmap,
  projects, biggest sessions (with "Jump in"), and every "you've hit your limit" reply.
  Sessions are attributed to accounts via the SessionStart hook, Remote Control owner ids, and
  per-config-dir state; older sessions that can't be told apart show as "Unattributed".

$ figures are API-list-price equivalents for comparison only — subscriptions bill in %.
Every chart has a table view ("Show data tables").

## Run in the background at login

Settings (gear icon) → **Run at login**, or from a terminal:

```bash
npx electron . --autostart=on      # off to remove; works while Switchyard is running too
```

On Linux this writes `~/.config/autostart/switchyard.desktop` (plus an app-grid launcher)
starting `--background`: no window, usage polling, hook receiver and transcript scanning all
run. Closing the window also just hides it (turn off "Keep running" to quit instead). Open it
again from the app launcher or the tray icon; quit with `Ctrl+Q` or the tray menu.

## Self-hosted server (Docker)

For your phone and other browsers: a small zero-dependency Node server with the same
Analytics view, an overview of every account, the Claude Code activity feed, and phone pushes
via [ntfy](https://ntfy.sh).

```bash
cp .env.example .env               # set SWITCHYARD_TOKEN (openssl rand -base64 24)
docker compose up -d               # restart: unless-stopped → comes back after reboots
```

Then in the desktop app: Settings → server URL `http://127.0.0.1:8787` + the token →
Sync now. The desktop pushes new rows every minute and right after each hook event.

- **Why the desktop app still polls usage:** reading limits needs your claude.ai sign-ins, and
  those stay on your machine. The server never talks to claude.ai; it only receives what the
  desktop sends. Limit % therefore updates while the desktop app runs (it can run hidden).
- **Transcripts without the desktop app:** mount `~/.claude*` read-only at the same paths and
  set `SWITCHYARD_SCAN_HOME` (see `docker-compose.yml`), and the server scans token usage itself.
- **Phone access:** compose binds to `127.0.0.1` only. Expose it through Tailscale, a
  Cloudflare Tunnel or a reverse proxy with HTTPS; sign in with the token (HttpOnly cookie,
  read-only — writes always need the bearer token). It installs as a home-screen web app.
- **Pushes:** set `NTFY_URL` to a topic URL (use a long random topic on ntfy.sh, or your own
  ntfy) and install the ntfy app. You get permission prompts / questions from Claude Code and
  80% / 95% alerts for 5-hour and 7-day windows; tapping opens the session (Remote Control) or
  `PUBLIC_URL`.
- **Without Docker:** `SWITCHYARD_TOKEN=… npm run server` (Node 22.13+).
- Hooks can also post straight to the server: `POST /hook?email=<login email>` with the token.

## Claude Code notifications

Click the bell for the command, or run:

```bash
bin/connect.sh --all                                   # every ~/.claude* config dir
bin/connect.sh --config-dir ~/.claude-3                # or just one
bin/connect.sh --all --uninstall                       # remove again
```

It installs one shared hook script, `~/.claude/switchyard-hook.sh`, and adds `SessionStart`,
`Notification` and `Stop` hooks running it (backup of `settings.json` kept, existing hooks
untouched). The script works out the account at run time from `CLAUDE_CONFIG_DIR` (the login
email in that dir's `.claude.json`) and reads the token and port from Switchyard's
`config.json`, so nothing account-specific is written into `settings.json` — one settings file
can be symlinked into every config dir, and symlinks are edited at their target, never
replaced. Sessions that need permission, ask a question or finish show up under "Claude Code
activity", as an unread badge on the tab, and as a desktop notification — including Remote
Control sessions, since those run locally.

## Not in this prototype

- Native mobile app (the server's web UI + ntfy cover phones for now).
- Notifications for chat/cowork come from claude.ai itself and only while the app is open.

## License

No license has been granted yet: the source is public to read, but all rights are reserved
(© 2026 Mayank Raj). A license will be chosen before the first release.
