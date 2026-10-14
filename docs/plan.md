# Switchyard — product and architecture plan

Oct 11, 2026 · @Mayank Raj

## 1. At a glance

Switchyard is an unofficial, open-source tool for people who use **several Claude accounts across several machines and many Claude Code sessions at once**. It does three things:

- It sets up and maintains the accounts on each machine, including what they share.
- It keeps one long-term record of every account's usage and every session's state.
- It tells you, wherever you are, when a session needs you.

It never touches a Claude login: Claude Code and claude.ai stay exactly as Anthropic ships them.

&#91;embedded content: how the pieces connect · machines → hub → viewers\]

Every machine runs the same small `sy` tool, which Claude Code itself starts on each event; nothing stays running. Records travel out over HTTPS to the hub, which may be the same laptop. The hub keeps them, works out each session's state and each account's usage, and sends pushes. Everything you look at (phone, desktop, browser, terminal) reads from the hub.

**Four offerings**

| Offering | What it is | Needed? | Ships as |
| --- | --- | --- | --- |
| **Core:** `sy` + hub | Account setup and sharing, hooks and status line, outbox, the hub with its web dashboard | Yes, the only required part | One binary (Linux, macOS, Windows), plus a Docker image for the hub |
| **Desktop app** | All claude.ai accounts in one window, web usage without Claude Code, every setting in one place | Optional; for claude.ai users | Electron app that bundles `sy` |
| **Phone app** | Needs-you inbox, every account's usage, every session, pushes and widgets | Optional; recommended | Phase 1: the hub's installable web app. Phase 2: Expo app |
| **Switchyard Terminal** | A terminal whose sidebar lists every Claude session, grouped by account, with live state and context | Separate, optional | Fork of the open-source Warp client, plus a lighter pane that works inside Claude Code in any terminal (section 14) |

**Screens:** every screen named in this doc is drawn in the companion canvas [Switchyard screens](https://claude.ai/artifact/ExUAQtHWibGTetRzTjtQ9y): installer, desktop app, phone, terminal.

**What changes from today's prototype:**

- The hook script, `connect.sh` and transcript scanning move into `sy`, with an outbox so nothing is lost.
- Analytics move from the desktop app to the hub.
- The desktop app keeps its account tabs and usage reading and becomes optional.
- The server becomes the hub, with per-device tokens, a session state engine and push rules.
- The Signal / Board / Lamp ideas from the other session's "Companion Suite" page are folded in: Signal is the push path (section 11), Board is the session board (sections 6 and 14), and Lamp is the terminal tab colour and badge (section 14).

## 2. Who uses it, and in which setup

| Person | Typical day | Installs | Hub | Gets |
| --- | --- | --- | --- | --- |
| **Several accounts, one laptop** (the core user) | 2 to 4 logins, many Claude Code sessions in parallel | `sy` | Merged, on the laptop | Accounts set up and shared in one step, the session board, phone pushes, all usage in one place |
| **Several accounts, laptop + SSH servers** (you) | Sessions on the laptop and on servers, often left running in tmux | `sy` on every machine | On the always-on server, or merged on the laptop | The above, across machines; the SSH server's sessions reach the phone |
| **claude.ai-heavy** | Chat and Cowork in several accounts, little Claude Code | Desktop app (it brings `sy` along) | Merged | All accounts in one window, every limit in one view, phone |
| **One account, one laptop** | One login, a few sessions | `sy`, if anything | Merged | Little: Claude's own app and Remote Control already notify. Setup says so honestly and offers the board and history as the reason to stay |
| Small team sharing one hub |  |  |  | Not in scope now; the schema leaves room (section 5) |

**Deployment modes**

| Mode | Hub | Machines | Phone reaches hub | Trade-off |
| --- | --- | --- | --- | --- |
| Solo, merged | Laptop, `127.0.0.1` | The laptop | Tailscale app (only for details) | Zero setup; SSH-server pushes pause while the laptop sleeps |
| Solo + home server | The always-on box, Docker or `sy hub serve` | All, over Tailscale | Tailscale app | One more thing to run; nothing pauses |
| VPS | Small VPS + Caddy | All, over HTTPS | HTTPS | Costs a few dollars a month; reachable from anywhere; needs a strong owner login |

**Requirements:** Linux, macOS, or Windows (WSL counts as its own machine), and a recent Claude Code (designed against 2.1.296). Only the desktop app and the phone app are more than a single binary.

## 3. Installing it

There are four ways in, and all of them end in the same short setup run by `sy`. Every step has a sensible default, so a first-time user can press Enter through it, and every step has a flag, so a script or Claude can run it without questions.

&#91;embedded content: install paths · 4 ways in, 1 setup\]

**The ways in**

| Way | Command | Best for |
| --- | --- | --- |
| One-line install | `curl -fsSL https://<site>/install.sh \| sh`, which downloads the release binary, checks its SHA-256, puts `sy` in `~/.local/bin`, then starts `sy setup`. Also `brew install <tap>/sy`, `winget install switchyard` | Anyone with a terminal |
| Ask Claude | In any Claude Code session: "Install Switchyard" with the install page link, or `claude plugin install switchyard@<marketplace>` then `/switchyard:setup`. The skill asks you the setup questions in chat and runs `sy setup` with flags | People who'd rather not read docs; it also works over SSH |
| Desktop app | `.dmg`, `.AppImage` or `.exe`. It bundles `sy`, and its first run shows the same steps as screens | People who use claude.ai in the browser |
| Add-machine line | From any signed-in screen, **Add machine** gives `curl …/install.sh \| sh -s -- --hub <address> --code 482-913`. Or from the laptop, `sy machine add user@host` installs over SSH | Each extra SSH server or computer |

The hub on its own is `docker run` or `docker compose up`, or simply `sy hub serve` on any machine.

**The setup, step by step** (`sy setup`, about two minutes)

1. **Detect.** Finds `claude`, every `~/.claude*` folder, which ones are logged in (via `claude auth status`), existing hooks, status line, symlinks and launchers. It shows one summary such as "3 folders: personal (a@…), work (b@…), \~/.claude-4 not logged in". Nothing has been changed yet.
2. **Accounts.** How many accounts, a label for each, and login for any that aren't logged in. Existing folders are kept as they are.
3. **Sharing.** The switches from section 4, with defaults already set.
4. **Hub.** One of three:
   - "Just this computer" (default): runs the hub here as a background service.
   - "Connect to my hub": paste an address and code.
   - "I'll run the hub on another server": prints the Docker command for that server, then waits for its code.
5. **Phone (optional).** Shows a QR code right in the terminal and lets you pick a channel. It can be skipped and done later from any screen.

It ends with `sy doctor`, a short list of what was installed, and one reminder: Claude Code sessions that are already open pick up the hooks after a restart.

**For scripts and for Claude**, every question has a flag:

```
sy setup --yes --accounts personal,work,client --share settings,instructions,skills,plugins,mcp,history \
         --hub local --phone skip
sy setup --yes --hub https://hub.example --code 482-913      # an extra machine
```

`--yes` never logs anyone in on its own. Login always needs the person, so Claude's skill stops at that point and shows the login link or code.

**Changing things later**

- `sy account add | login | label | share`
- `sy connect` (switch hubs)
- `sy phone pair`
- `sy doctor`
- `sy uninstall`, which removes the plugin, restores your original status line, and removes the timer and launchers. It keeps every config folder and login. `--purge` also deletes the outbox and Switchyard's config.

## 4. Several Claude accounts on one machine

Claude Code keeps one account per **config folder**: `~/.claude` (whose `.claude.json` sits outside it, at `~/.claude.json`), then `~/.claude-2`, `~/.claude-3` and so on, chosen with `CLAUDE_CONFIG_DIR`. The installer creates these folders, logs each one in, and links the parts you want shared back to the first folder. This is what you already do by hand with `claude-launch` and the symlinks; Switchyard makes it a guided step that anyone can repeat. Checked against Claude Code 2.1.296.

**What a config folder holds, and what can be shared**

| Part | What it is | Installer default | How it is shared |
| --- | --- | --- | --- |
| `settings.json` | Hooks, permissions, enabled plugins, status line | Shared | Symlink to the first folder. Edits always go to the target, so the link survives |
| `CLAUDE.md`, `rules/` | Your own instructions | Shared | Symlink |
| `skills/`, `agents/`, `commands/`, `keybindings.json` | Your add-ons | Shared | Symlink each |
| Plugins | Marketplaces and installed plugins | Shared | **Not** a symlink: the install records hold absolute paths (on this machine one points into `~/.claude-2`). The account launchers set `CLAUDE_CODE_PLUGIN_CACHE_DIR` to one folder instead |
| MCP servers | Lives inside `.claude.json` (user scope) | Shared | `.claude.json` can never be linked: it holds the login, and Claude Code rewrites it atomically, which would replace a link. Switchyard keeps one list and copies only the `mcpServers` key into each account's file, on every change and on the timer |
| MCP sign-ins | OAuth tokens for servers such as Linear or Notion | Never | Stored with the account's credentials; each account signs in to a shared server once |
| `projects/` (transcripts and auto-memory) | Conversation history, and the memory Claude writes per project | Ask | Shared: any account can resume any session, and memory follows the project, not the account. Separate: each account has its own. Caveat when shared: two accounts resuming the same session write into one transcript |
| `history.jsonl`, `plans/` | Prompt history (up-arrow), plan files | Shared | Symlink |
| Login: `.credentials.json` (Keychain on macOS), `.claude.json` | Who you are, tokens, per-project trust | Never | Per folder, always |
| `sessions/`, `daemon/`, `jobs/`, `file-history/`, `session-env/`, `stats-cache.json`, `backups/`, caches | Live-session and per-account state | Never | Per folder, always |

The installer shows this as one screen of switches (Settings, Instructions, Skills and add-ons, Plugins, MCP servers, History and memory), with the "never" rows listed underneath, greyed out, so it is clear why they can't be shared. The same switches live later in `sy account share` and in the desktop app's Accounts page.

**Adding an account**, start to finish:

1. `sy account add` (or the wizard) picks the next free folder, `~/.claude-4`, and asks for a label ("work").
2. It applies the sharing switches: links, plugin folder, MCP list.
3. It runs `claude auth login --claudeai` with that folder. On an SSH server it shows the login URL and the code prompt, as Claude Code itself does.
4. It reads `claude auth status --json` (email, org, plan) to confirm which login landed in the folder, without ever touching a token.
5. It writes a launcher, `claude-4` (and `claude-work` if you want a named one), into `~/.local/bin`. The launcher just sets `CLAUDE_CONFIG_DIR` and the plugin folder, then runs the real `claude`.

**Existing setups are adopted, not replaced.** If folders, links or launchers already exist (as on this laptop), the installer lists what it found and only adds what is missing. It never rewrites a symlinked `settings.json` into a plain file. Your `~/bin/claude-launch` keeps working; Switchyard's launchers are optional.

**Windows and macOS**

- macOS keeps the login in the Keychain, keyed to the folder, so Switchyard detects logins only through `claude auth status`.
- Windows can't make file symlinks without Developer Mode. The installer uses directory junctions for folders and a copy kept in sync by `sy` for single files such as `settings.json` and `CLAUDE.md`.
- WSL is a separate machine as far as Switchyard is concerned.

## 5. Identity: matching accounts, folders, tabs, machines and sessions

The rule: **an account is a Claude login, never a position.** "claude-2" on the laptop, the first tab in the desktop app and "claude" on an SSH server can all be the same login, or three different ones. Switchyard joins them by the login itself, so your example (claude-1 is email A, the desktop app's first tab is email B) needs no special handling: they are two accounts, each shown once, with every place it is used listed under it.

| Thing | What it is | Key | Where the key comes from |
| --- | --- | --- | --- |
| Account | One Claude login (one person, one email) | Account uuid; email as fallback and for display | Claude Code: `oauthAccount.accountUuid` and `emailAddress` in the folder's `.claude.json`. Desktop app: claude.ai `/api/account` |
| Organization | Who the limits belong to: your personal plan, or a Team/Enterprise org | Org uuid | Claude Code: `oauthAccount.organizationUuid`. Desktop app: `/api/organizations` |
| Seat | One account inside one organization. **This is what has limits**, and what a usage card shows | (account uuid, org uuid) | Both of the above |
| Machine | A computer that runs `sy` | Id minted at pairing + a name you choose | `sy connect` |
| Slot | One config folder on one machine (`~/.claude-2` on the laptop) | (machine, folder path) | `sy` |
| Web seat | One account tab inside the desktop app | Tab id; joined to a seat once signed in | Desktop app |
| Session | One Claude Code conversation | Claude's session id | Hooks, transcripts, `claude agents --json` |

**How the joins work**

1. A slot belongs to a seat *at a point in time*. Each record `sy` sends carries the login it read from that folder's `.claude.json` at that moment, so logging a folder out of A and into B starts a new period cleanly, without rewriting history.
2. A session belongs to the seat its first record named and keeps it, even if the folder is re-logged later.
3. A web seat joins the same seat as soon as its tab is signed in. That is how web usage % and Claude Code tokens land on one card.
4. Labels and colours ("work", teal) belong to the **seat**, not the folder. Every slot and tab of that seat shows them: the terminal tab colour, the phone's account ring and the desktop tab all match.
5. Folder names are kept as a secondary tag ("laptop · claude-2") because they are what you type.
6. Two different logins are never merged automatically, even with the same name. Old sessions that can't be attributed (from before setup, with only a transcript) show as **Unattributed**, and the history import credits them as well as the per-folder history allows.

**One owner per hub.** The hub is single-user: one owner who sees every account. The tables still carry an owner id so a shared team hub can be added later without a migration, but that is out of scope for now.

## 6. The desktop app (optional)

The desktop app is for people who also use claude.ai in the browser, or who want a window instead of a terminal. It is the only part that stays running, because reading claude.ai usage needs a live, signed-in claude.ai page, and those sign-ins must stay on your computer. Without it, Claude Code users still get 5-hour and 7-day % from the status line.

**Its five jobs**

| Job | How | Sends to the hub |
| --- | --- | --- |
| All your claude.ai accounts in one window | One tab per account, each with its own isolated, persistent sign-in (email + code; Google sign-in is blocked inside embedded browsers). Chat, Cowork and claude.ai/code all work in the tab | Nothing |
| Usage without Claude Code | Every 5 min, and when you open a tab, each signed-in tab reads the same usage data claude.ai's Settings → Usage page shows | `web_usage`: the full raw response, every field kept (`iguana_necktie`, extra usage, per-model weeks) |
| Window onto the hub | Home, Sessions and Analytics are the hub's own screens, shown inside the app | Nothing; it reads |
| Settings in one place | Accounts and sharing, hub, machines, phone pairing, notification rules (below) | Settings changes |
| Desktop notifications | Listens to the hub's live stream and shows OS notifications for this computer | Nothing |

**Should web usage go to the hub?** Yes, by default, for each account (a per-account switch turns it off). It is the only source of the per-model weekly limits, cloud session credits and extra usage, and of usage for accounts with no Claude Code session open. Keeping it on the hub is what makes long-range charts, "what expired unused" and pace predictions possible. It holds numbers only: no prompts, no cookies.

**Desktop app and CLI accounts join automatically.** When a tab signs in, the app learns its account and org, and the hub attaches it to the same seat as any Claude Code folder with that login (section 5). So `claude-2` = A and tab 1 = B simply show as two cards, while a tab and a folder with the same login share one card.

**Screens**

| Screen | Shows |
| --- | --- |
| Home | One card per seat: 5h and 7d rings, per-model weeks, credits, extra usage, reset times, pace. Above them, a "needs you" strip of sessions waiting on you |
| Sessions | The session board from the hub, grouped by account, then machine. Click to see detail or open Remote Control |
| Account tabs | The full claude.ai page, with a thin usage bar for that account above it |
| Analytics | The hub's analytics |
| Settings → Accounts | Each seat with label, colour and tracking switch. For Claude Code folders on **this** computer: add, log in, sharing switches (it calls `sy` underneath) |
| Settings → Hub | Merged (start or stop the local hub) or remote (address + pairing). Status, version, backup |
| Settings → Machines | Every machine with last-seen time, queued records, `sy` version; "Add machine" shows the one-line command; revoke |
| Settings → Phone | QR code for pairing, paired phones, test push |
| Settings → Notifications | Rules (what, after how long, which accounts or projects), quiet hours, channels (ntfy, web push, phone app) |
| Settings → Background | Run at login (hidden), keep running when the window closes, tray icon |

**The app is not the hub.** In merged mode the hub runs as its own small background service (`sy hub serve`, installed as a systemd user service or launchd agent), not inside the app. That way quitting the app never stops pushes, and Claude Code users who never open the app have the same hub. The app just starts and stops that service from Settings → Hub.

**Built with** Electron, which the prototype already uses. Each account is a persistent, isolated session partition, and sign-ins are flushed to disk every 30 s and on quit, so restarts never cost a login.

## 7. The server (hub)

The hub keeps all long-term data, computes every number and sends every push. It never talks to Anthropic and never holds a Claude login. It ships **inside the same `sy` binary** (`sy hub serve`) and as a Docker image, so a merged setup needs nothing extra.

| # | Part | One job | Reads | Writes |
| --- | --- | --- | --- | --- |
| S1 | Ingest | Accept record batches, check the device token, drop duplicates, reply with the accepted ids | HTTPS | Raw tables |
| S2 | Store | Keep everything, long-term |  | One SQLite file (WAL mode) |
| S3 | Session state | Fold events into each session's current state; flag plain-text questions | Hook events, session lists | Session state |
| S4 | Usage model | Merge status-line and web samples per seat into windows, resets and pace | Limit samples, web usage | Windows |
| S5 | Analytics | Tokens, API-equivalent cost, history, attribution, daily rollups | Token rows, windows | Rollups |
| S6 | Alert rules | Decide what deserves a push; delays, dedupe, quiet hours | State changes, windows | Alerts |
| S7 | Notifier | Deliver each alert to every channel you set up; retry | Alerts | Delivery log |
| S8 | Read API + live stream | Serve the dashboard, desktop app, phone and terminal; push live changes (server-sent events) | Everything |  |
| S9 | Web UI | Dashboard, session board, analytics, accounts, machines, alert settings; installs on a phone home screen | Read API |  |
| S10 | Devices and auth | Owner login, device tokens, pairing codes, revoking a lost machine or phone |  | Devices |

Each part talks to the others only through the store, so any part can be rewritten without touching the rest.

**Where it runs**

| Mode | Runs on | Started by | Reachable from the phone through | Good for |
| --- | --- | --- | --- | --- |
| Merged | Your laptop, `127.0.0.1` | The desktop app, or a user service `sy` installs | Tailscale or a tunnel, only for opening details; pushes work anyway | One person, one laptop (with or without SSH servers) |
| Home server | A box that is always on (your OCI server, a NAS, a Pi) | Docker or a system service | Tailscale (recommended), or a reverse proxy with HTTPS | Several machines, laptop often asleep |
| VPS | A small rented server | Docker + Caddy for HTTPS | Its public HTTPS address | No home server, or a team |

**Storage stays small enough to keep forever.** Today's local database holds 136k replies from about 30 days, every raw usage response included, in 46 MB, which is roughly 0.5 GB a year at heavy use. Raw records are kept by default. Daily rollups keep old ranges fast, and `sy hub prune --before <date>` exists for anyone who wants less.

**Operations**

- Upgrades: the schema migrates itself at start, and a copy of the file is made first.
- Backup: `sy hub backup` writes a consistent copy (SQLite's online backup), which a cron job or the Docker volume backup can take.
- Moving from merged to a home server: `sy hub export` on the laptop, `sy hub import` on the server, then `sy connect --hub <new address>` re-points each machine. Device tokens move with the data, so nothing has to be re-paired except the phone's address.
- Health: `/health` for uptime checks; the dashboard shows each machine's last record time, so a quiet machine is visible.

**Postgres** is only worth it for a shared, hosted hub. The store sits behind one interface, so it can be added later.

## 8. Networking

Three rules keep this simple:

- **Every connection is outgoing.** Machines push to the hub, and the hub pushes to phones. Nothing has to reach into a laptop or an SSH server.
- **The outbox absorbs every gap.** If the hub can't be reached, records wait on disk (capped at 50 MB, oldest token rows dropped first, hook events kept) and flush on the next hook or timer tick. The hub drops duplicates by record id.
- **The hub port is never opened to the internet directly.** It is reached through loopback, a tailnet, or HTTPS behind a proxy.

**Where the hub lives decides the rest**

| Hub on | SSH servers reach it via | Phone opens details via | Laptop asleep or offline |
| --- | --- | --- | --- |
| Laptop (merged) | Tailscale, or SSH `RemoteForward` while you are connected | Tailscale app on the phone | Server records wait in their outbox; no pushes for those sessions unless direct push is on (below) |
| Home server, always on | Tailscale (`tailscale serve` gives it HTTPS on the tailnet) | Tailscale app | Nothing is affected |
| VPS | Its HTTPS address (Caddy, automatic certificates) | Its HTTPS address | Nothing is affected |

**Recommended:** if you have an always-on box (you do: the OCI server), put the hub there and every machine and the phone on Tailscale. The free Personal plan covers this. If you don't have one, run the hub merged on the laptop and accept that SSH-server pushes pause while the laptop sleeps, or turn on direct push.

**An SSH server talking to a hub on your laptop**, in order of preference:

1. **Tailscale on both.** `sy connect` on the server takes the laptop's tailnet address. It works whenever both are online, whether or not you are SSH'd in. A server without a TUN device can use Tailscale's userspace mode.
2. **SSH `RemoteForward`, no extra account.** `sy connect --via ssh` adds one block to the laptop's `~/.ssh/config` for that host: `RemoteForward 127.0.0.1:47830 127.0.0.1:8787`, `ServerAliveInterval 30`, and `ExitOnForwardFailure no`, so a second SSH window to the same host doesn't fail. On the server, `sy` sends to `127.0.0.1:47830`. Records flow only while an SSH connection is open. A session left running in tmux after you disconnect queues its records until you reconnect.
3. **The hub pulls over SSH.** For locked-down servers where neither of the above is allowed, the laptop's timer runs `ssh host sy outbox drain` and ingests the result. This needs nothing on the server's network, but it lags by one timer tick and needs the laptop awake.

**Direct push** (optional, off by default) covers the gap in options 2 and 3. When `sy` on a server has failed to reach the hub for a few minutes and a session starts needing you, it sends one ntfy push itself (outgoing HTTPS to your ntfy topic), with the same delay and dedupe rules the hub uses. When the hub catches up it sees the push was already sent and doesn't repeat it. Only ntfy works this way, since web and native pushes need keys that stay on the hub.

**Hosting the hub on the internet** differs from a local hub in four ways:

- It needs TLS. Caddy or `tailscale serve` provide it.
- The owner login must be strong: a passkey or a long password, plus rate limiting.
- Sign-up stays closed: the hub has one owner, set at first start.
- The read API returns your project names and, if allowed, Claude's last messages, so it must never be public without login.

A local hub on `127.0.0.1` needs none of this. A Cloudflare Tunnel is a fine alternative to a VPS if you own a domain there. Throwaway "quick tunnels" are not supported, because their address changes on every run.

**`sy doctor`** checks, on any machine, that the hub is reachable, TLS is valid, the token is accepted, the hooks and status line are installed in every folder, the timer runs (on Linux SSH servers it also checks that `loginctl enable-linger` is on, otherwise the timer stops at logout), and how many records are queued. It prints one line of fix for each failure. This targets the most common complaints about Beszel and Netdata: agents that are "connected" but silent.

## 9. Data model

Everything lives in one SQLite file on the hub. Clients keep only an outbox of records not yet accepted, plus one small config file.

&#91;embedded content: hub data model · 4 groups, 21 tables\]

Arrows inside a box name the table it points to. The seat is highlighted because every usage number, card, colour and label hangs off it.

**Every upload is kept as received.** The `record` table stores each record exactly as uploaded (kind, device, client time, receive time, body). The typed tables are built from it, so a bug in the state engine or the analytics can be fixed and replayed over the full history. It also keeps every field of the web usage response, `iguana_necktie` and any later additions included.

**Main tables**

| Table | Key | Main columns | Kept |
| --- | --- | --- | --- |
| `account` | account uuid | email, display name | Forever |
| `org` | org uuid | name, plan (from `rate_limit_tier` / `subscriptionType`), billing type | Forever |
| `seat` | id; unique (account, org) | label, colour, order, hidden | Forever |
| `machine` | id | name, OS, `sy` version, last seen | Until removed |
| `slot` | id; unique (machine, folder) | folder path, launcher name | Until removed |
| `slot_login` | (slot, since) | seat; until | Forever |
| `device` | id | kind (machine, desktop, phone, browser), machine, token hash, scopes, created, last used, revoked | Forever (revoked rows kept) |
| `record` | record id (client-made uuid) | device, kind, client time, receive time, body (JSON) | Forever by default |
| `session` | Claude session id | seat, slot, machine, folder, project, name, title, started, ended, Remote Control link | Forever |
| `hook_event` | record id | session, event, notification type, time, short last message (if allowed) | Forever |
| `session_state` | session | state, since, question text, last event, needs-you flag | Current only; history is in `hook_event` |
| `token_row` | API message id | session, seat, model, input, output, cache read, cache write, time, project | Forever |
| `limit_sample` | (seat, window, time) | source (status line or web), used %, resets at | Forever |
| `web_usage` | (seat, time) | full raw response | Forever |
| `window` | (seat, window key, resets at) | started, peak, final, unused at reset | Forever |
| `rollup_daily` | (day, seat, model, project) | tokens by kind, replies, API-equivalent $ | Rebuilt on demand |
| `rule` | id | trigger, delay, seats/projects it applies to, quiet hours, channels | Until changed |
| `alert` | id | rule, session or seat, created, due, fired, cancelled | 1 year |
| `delivery` | (alert, channel) | status, tries, last error | 1 year |
| `channel` | id | kind (ntfy, web push, native relay), settings | Until removed |
| `push_target` | id | device, channel, endpoint and keys | Until the phone is removed |

**Session states** (in `session_state.state`)

| State | Entered when | Left when | Needs you? |
| --- | --- | --- | --- |
| `working` | SessionStart, UserPromptSubmit | Any other event | No |
| `needs_permission` | PermissionRequest, or Notification `permission_prompt` | Next prompt, Stop, or the next PostToolUse if enabled | Yes |
| `asked` | Notification `elicitation_dialog`, or Stop whose last message reads as a question | Next prompt | Yes |
| `done` | Stop with no question | Next prompt | No |
| `failed` | StopFailure (rate limit, login, billing, overload…) | Next prompt | Yes, except overload |
| `ended` | SessionEnd |  | No |
| `gone` | No longer listed by `claude agents --json` and no SessionEnd | It reappears | No |

"Reads as a question" starts simple (the last paragraph ends with a question mark, or offers numbered options) and can be refined later on the hub without touching any client.

## 10. Contract and API

There is one record format, one upload endpoint and one token per device. Clients only write and viewers only read. Nothing on the hub can make a client do anything.

**How Claude Code calls Switchyard**

| Piece | Delivered as | Why this way |
| --- | --- | --- |
| Hooks (SessionStart, UserPromptSubmit, PermissionRequest, Notification, Stop, StopFailure, SessionEnd) | A small Claude Code plugin, `switchyard`, enabled once in the shared `settings.json` (or in each folder's own settings when that account doesn't share settings). Each hook is a command hook running `sy hook` with `async: true` | One place to update, and removing the plugin removes every hook. Async means Claude Code never waits on us |
| Status line | Written into user `settings.json` as `sy statusline --wrap '<your old command>'` | A plugin can't set the main status line. Ours records limits on the side and prints whatever yours printed |
| Skills | Same plugin: `/switchyard:setup`, `/switchyard:status`, `/switchyard:add-account` | Lets Claude itself install and check Switchyard (section 3) |

Claude Code also has native `http` hooks that post the hook JSON straight to a URL, with no process started. They are not the default, because when the hub is unreachable (laptop asleep, SSH server offline) the event is simply lost, and the token would have to sit in settings. `sy hook` writes to the outbox first, so nothing is lost. A future "direct" option for merged mode could use http hooks.

**The record envelope** (one upload)

```json
{"v": 1, "device": "dev_7Qk…", "records": [
  {"id": "01JA2X…", "kind": "hook_event", "ts": "2026-10-11T14:03:22.418Z",
   "slot": "~/.claude-2",
   "login": {"account": "3f1c…", "email": "a@example.com", "org": "9b20…"},
   "body": {"event": "Stop", "session_id": "c41e…", "cwd": "/home/me/api",
            "last_message": "Should I also update the migration?"}}
]}
```

- The reply is `{"accepted": [ids], "rejected": [{id, reason}]}`. The client deletes only accepted ids, so resending is always harmless.
- `login` is read from the slot's `.claude.json` at the moment of the event (section 5). `last_message` is present only if allowed (section 13).
- An unknown `kind` or field is stored raw, never refused. Old clients keep working with a newer hub.

| Kind | Sent by | When |
| --- | --- | --- |
| `hook_event` | `sy hook` | Each hook event |
| `limit_sample` | `sy statusline` | When a 5h or 7d value changes |
| `web_usage` | Desktop app | Every 5 min per signed-in tab, raw response kept whole |
| `token_rows` | `sy scan` | After each Stop for that session, and on the timer |
| `session_list` | `sy agents` | On the timer: live sessions with busy, idle or waiting |
| `history_import` | `sy scan`, first run | Once: Claude Code's own daily totals from before Switchyard |
| `slot_info` | `sy` | On connect and when a login changes: folder, launcher name, plan |

**Endpoints**

| Endpoint | Who | Does |
| --- | --- | --- |
| `POST /v1/ingest` | Write token | Upload a batch (gzip JSON) |
| `GET /v1/overview` | Read token | Seats with usage, sessions needing you, machine health: one call for the home screens |
| `GET /v1/sessions?state=&seat=&machine=` | Read token | The session board |
| `GET /v1/sessions/{id}` | Read token | One session: timeline, last message, how to resume |
| `GET /v1/seats`, `/v1/seats/{id}/windows` | Read token | Usage cards and history |
| `GET /v1/analytics/{report}` | Read token | Tokens, cost, models, projects, heatmap |
| `GET /v1/stream` | Read token | Server-sent events: state and usage changes, live |
| `POST /v1/alerts/{id}/snooze`, `/seen` | Read token | Quiet a push from the phone; changes only the hub's own state |
| `POST /v1/push/targets` | Read token | Register a phone or browser for pushes |
| `POST /v1/pair/codes` | Owner | Make a one-time pairing code (10 min) |
| `POST /v1/pair/claim` | Pairing code | Swap the code for a device token |
| `GET`, `DELETE /v1/devices` | Owner | List and revoke machines, desktop apps, phones |
| `GET /health` | Anyone | Up, version, schema |

**Tokens**

| Token | Held by | Scopes |
| --- | --- | --- |
| Machine token | `sy` on each machine | write |
| Desktop token | The desktop app | write + read (it shows the dashboard) |
| Phone token | The phone app or installed web app | read + push registration |
| Owner session | You, in a browser, after the owner password or passkey | everything, including devices and rules |

Tokens are random 32 bytes. The hub stores only their hash, and each one can be revoked on its own.

**Pairing** works the same way for every device:

1. In any signed-in screen, choose **Add machine** or **Add phone**.
2. The hub shows a one-time code and, for a machine, a single command: `sy connect https://hub.example --code 482-913`. For a phone it shows a QR holding the hub address, the code and the hub's public key.
3. The device claims the code and gets its own token.

In merged mode the installer pairs the local machine itself, with no code to type.

## 11. Notifications, end to end

The hub decides and sends every push; phones only receive. Sending a push is an outgoing connection, so it works even when the hub is a laptop behind a home router. Reaching the hub is needed only to open the details, and even that is optional, because the push itself carries a link to the session.

**One plain-text question, hop by hop**

1. Claude finishes a turn in a session on an SSH server with a question: "Should I also update the migration?"
2. The Stop hook runs `sy hook`, which writes one record to the outbox and starts a detached send. Claude Code isn't slowed down.
3. The record reaches the hub, by Tailscale, an SSH forward, or later from the outbox.
4. The state engine marks the session `asked`.
5. The "asked a question" rule waits one minute. If you reply in the terminal first, UserPromptSubmit cancels it.
6. Otherwise the notifier sends one push to each channel you set up.
7. The phone shows "work · api · asked a question" with the first line of the question.
8. Tapping it opens the session page in the Switchyard app, or straight in the Claude app if Remote Control is on for that session. Replying happens in Claude's own app or terminal; Switchyard never types into a session.

**Channels**

| Channel | Path to the phone | Works with a self-hosted hub | iOS | When to use |
| --- | --- | --- | --- | --- |
| ntfy | Hub → an ntfy topic → ntfy app | Yes | A self-hosted ntfy relays through ntfy.sh for instant delivery; ntfy.sh sees only a hash and a message id | Works today, nothing to build; the fallback for direct push from SSH servers |
| Web Push | Hub signs with its own keys → Apple / Google / Mozilla push service → the installed web app | Yes, no relay | Home-screen install, iOS 16.4+; reportedly not in the EU | Phase 1, along with the web app |
| Switchyard phone app | Hub → Expo's push service → APNs / FCM → app | Yes, through Expo (see below) | Reliable, action buttons, widgets | Phase 2 |
| Desktop | Hub's live stream → desktop app → OS notification | Yes |  | Always, when the app runs |

**Native pushes without a project-run server, at first.** The phone app registers its Expo push token with your hub, and the hub posts straight to Expo's push API. That needs no Switchyard-run relay, and Expo accepts up to 600 pushes a second per project. If abuse ever forces Expo's access-token protection on, the project adds a small relay that holds that token and rate-limits each installation (Home Assistant allows 500 a day per device), the Bitwarden and Nextcloud pattern.

**What a push carries.** By default: account label, project and what happened, plus the first line of Claude's question if you allowed last messages (section 13). Apple, Google and Expo can read that text. **Private pushes** (one switch) send only "1 session needs you" plus an id, and the app fetches the details from the hub when you open it, the same trick ntfy uses on iOS. Every push also carries the Remote Control link when the session has one, so tapping works even when the hub isn't reachable.

**Rules** (all adjustable, per account and per project)

| Event | Default |
| --- | --- |
| Needs permission | Push after 30 s unanswered |
| Asked a question (dialog or plain text) | Push after 1 min unanswered |
| Failed: rate limit, login, billing | Push at once |
| Turn finished | No push; shown on the board |
| 5h or 7d limit at 80% / 95% | Push at once, once per window |
| A limit resets | Optional reminder, off |
| A machine silent for 1 hour while it has live sessions | Push once |

- A push is cancelled if the session moves on before its delay ends.
- Several sessions waiting at once become one push ("3 sessions need you").
- Quiet hours hold everything except failures.
- Sessions with Remote Control on can be skipped, so you don't get Claude's own push and ours for the same thing.
- Each push has buttons: **Open**, **Snooze 15 min**, **Seen**.

## 12. The phone app

You're right that the "everything in one place" view matters most on the phone. At the desk the terminal is already open; away from it, the phone is the only way to know a session is stuck. The phone app is the hub's dashboard made for one hand: who needs you, how much each account has left, and what every session is doing, with one tap to the session.

**Screens** (bottom tabs: Now, Sessions, Accounts, More)

| Screen | What's on it |
| --- | --- |
| Now | The **needs-you inbox** (permission, question, failure), newest first; swipe for Seen or Snooze. Under it, one row of rings per account: 5h and 7d used, time to reset. Under that, a count of running sessions per machine |
| Sessions | Every session grouped by account, then machine. Each row: status dot, project, session name, last line, how long in that state. Filters: needs you, working, done, all |
| Session detail | State history, Claude's last message (if allowed), account, machine, folder. Buttons: **Open in Claude** (Remote Control link), **Copy resume command** (`claude-2 --resume <id>`, with the right launcher), Snooze |
| Accounts | One card per seat: 5h, 7d, each per-model week, cloud credits, extra usage, resets, pace ("at this rate, 100% on Thursday"). Tap a card for its history chart |
| Analytics (under More) | Tokens and API-equivalent $ by day, account, model, project; the same numbers as the desktop |
| Alerts (under More) | Recent pushes, rule switches, quiet hours |
| Settings (under More) | Hub address and status, channels, private pushes, last-message sharing, widgets, unpair |
| Pairing | Scan the QR from any screen of the hub, desktop app or `sy phone pair`, or type the code |

Pushes show **Open**, **Snooze 15 min** and **Seen**. Widgets show the next-reset account ring and the needs-you count (small), or every account's rings (medium). The hub pushes widget updates, because iOS allows a widget only about 40 to 70 refreshes a day on its own.

**How it gets built, in two steps**

1. **Phase 1, installable web app.** The hub's web UI already installs to the home screen. It gets the screens above plus Web Push, and costs no app store work. It proves the screens and the data first.
2. **Phase 2, Expo app** (React Native, the same choice as Happy). It adds reliable native pushes with buttons, widgets, QR scanning with the camera, and a lock-screen countdown (Live Activity) for a limit reset. It ships through TestFlight and the Play Store, plus a sideloadable APK.

**All your claude.ai accounts on the phone?** It is possible but deliberately not in the first version.

- **How:** iOS 17+ (`WKWebsiteDataStore(forIdentifier:)`) and current Android WebView (`ProfileStore`) can each keep separate, persistent cookie stores per account. No React Native or Capacitor plugin does this today, so it would take a small native module, about 100 lines each of Swift and Kotlin.
- **Login:** Google sign-in is blocked inside embedded browsers, so it would be email + code only, as on the desktop.
- **App stores:** an app that is mostly wrapped websites risks rejection (Apple 4.2, Google's webview-spam rule). The native dashboard is what makes it an app.
- **Terms:** the account tabs sit in the same grey zone as the desktop app (section 13).
- **Proposal:** phase 3, as an optional "Account tabs" screen, only after the terms question is settled. Its biggest win is the problem below.

**The wrong-account problem.** "Open in Claude" hands the session link to the official Claude app, but that app is signed in to one account at a time. A session from another account will open in the wrong account, or not at all (to be tested). Until phase 3, the session page also offers "Copy resume command" and "Open in browser". With account tabs, the link would open in that account's own tab, which is exactly the all-accounts-in-one-place idea.

## 13. Security, privacy and Anthropic's terms

Not legal advice. What follows checks each piece against the published text.

**What the terms say**

- Claude Code's legal page: third parties may not "offer Claude.ai login into their own applications", may not "route requests through Free, Pro, or Max plan credentials on behalf of their users", and may not "collect, store, or intermediate Claude.ai credentials or session tokens". Sign-in "must complete through Anthropic's own flow".
- The consumer terms forbid accessing the services "through automated or non-human means, whether through a bot, script, or otherwise" (except via an API key or where explicitly permitted), forbid sharing login credentials, and forbid scraping.

**Each piece against those lines**

| Piece | What it does | Assessment |
| --- | --- | --- |
| Account folders and launchers | Run the unmodified `claude` with `CLAUDE_CONFIG_DIR`; login via `claude auth login` | Fine: Anthropic's own flow, and the documented variable |
| Hooks, status line, transcripts, `claude agents` | Read what Claude Code itself hands to local programs | Fine: these are documented extension points |
| Hub, phone, terminal | Show numbers and states; never call Anthropic; never hold a token | Fine |
| Desktop account tabs | claude.ai's own page in an embedded browser; you sign in on Anthropic's page; Switchyard never reads cookies or tokens | **Grey zone**: arguably not "offering login" (it adds no Claude features of its own), but nothing in the text addresses it. Ferdium-style apps exist without a known Anthropic statement |
| Desktop usage reading | A script inside each signed-in tab fetches claude.ai's internal usage endpoint every 5 min, in the background | **Highest risk.** This is automated access to an undocumented endpoint. Decided (section 17): read every 5 minutes after a plain warning accepted per account, with an off switch, error backoff and a hard stop on 401/403/429. Anthropic is asked before any public release |

**What Switchyard never does**

- Read, copy or send any Claude token or cookie.
- Send a request to Anthropic with your login, except what the official app or website itself sends.
- Approve permissions or type into a session. `sy hook` always exits successfully with no decision, so it can't change Claude's behaviour.
- Switch or rotate accounts automatically to get around limits. It may suggest which account has room; you choose.
- Use "Claude" in its name. Every surface carries "Unofficial, not affiliated with Anthropic".

**Privacy: what leaves each machine**

| Never | By default | Only if you turn it on |
| --- | --- | --- |
| Cookies, Claude Code tokens, prompts, code, file contents, tool output | Counts, models, times, session ids, states, project folder name, account email, limit % | Claude's last message, cut to about 200 characters, for the board, question detection and push text |

For your own hub, last messages are on by default. For any hub you don't run yourself, they are off, and the setup says so in plain words.

**Security**

- **Tokens.** Every device has its own token, and the hub stores only the hash. Machine tokens can only write. A stolen machine token can add fake records but can't read anything, and is revoked in one click. Phone tokens can only read.
- **The hub can't command clients.** `sy` never executes anything it receives, and the hub's replies are lists of accepted ids. A compromised hub leaks data but can't reach into your machines.
- **Owner login.** A passkey, or a long password with rate limiting; sign-up closed after the first owner.
- **Transport.** HTTPS whenever the hub isn't on `127.0.0.1` (Caddy or `tailscale serve`). Plain HTTP to a non-local address is refused unless `--insecure-lan` is passed.
- **At rest.** The hub file holds project names, emails and, if allowed, message snippets. Keep it on an encrypted disk. `sy hub backup --encrypt` encrypts backup copies with a passphrase.
- **Supply chain.** Release binaries come with SHA-256 sums and GitHub build attestations. The install script verifies them. The Claude Code plugin is pinned to a version and updated by `sy update`, not silently.
- **Pushes.** By default push text passes through Apple, Google, Expo or ntfy. Private pushes (section 11) keep it to "1 session needs you".

## 14. Switchyard Terminal

The goal: one place in the terminal that shows every Claude session, grouped by account. Each tab is marked with its account, its state and how full its context is. It comes in three layers, cheapest first. Each layer is useful on its own, and each later layer reuses the data from the one before. The screens are on the Terminal page of [Switchyard screens](https://claude.ai/artifact/ExUAQtHWibGTetRzTjtQ9y).

**What Warp's open-source code allows today** (checked against `warpdotdev/warp` master `c1ec372`, 10 Oct 2026)

| Need | What Warp has | Where in the code |
| --- | --- | --- |
| Status per tab | Built in. Warp's Claude Code plugin sends `OSC 777` events to `warp://cli-agent` (session start, prompt, tool done, stop, failure, permission, question). Warp shows a status icon on the tab and pane, and sends notifications. Its states are in progress, success, failed, blocked (permission, question or input) and cancelled. | `crates/warp_core/src/cli_agent_protocol.rs`, `app/src/…/cli_agent_sessions/` |
| Tab title | `OSC 0/2` sets it, unless you renamed the tab yourself | `crates/warp_terminal/src/model/ansi/mod.rs` |
| Tab colour | Six fixed ANSI colours. You set them by hand, with a tab config file, or with a per-folder rule. No escape code changes them, and public builds have no API for it. | `app/src/tab.rs`, `ui_components/color_dot.rs`, `tab_configs/` |
| Focus a tab from outside | Every pane gets `WARP_FOCUS_URL`; opening that URL brings that exact pane forward | `crates/warp_terminal/src/focus_env.rs` |
| Add a sidebar or plugin | Not possible. The left panel is a fixed list of four built-in views (files, search, Drive, conversations), and there is no plugin API. | `app/src/workspace/view/left_panel.rs` |
| Remote-control API | `warpctrl` has 84 actions, including tab colour, rename, focus and session list. It is internal-only: it is off in public builds and lacks title, folder and agent state (issue #16111). | `crates/local_control`, `specs/warp-control-cli/` |
| SSH | Warp's own SSH wrapper forwards the protocol variables, so a Claude session on a server shows status in the local tab. Plain `ssh`, mosh and tmux don't. | `bootstrap/bash_body.sh` |
| Licence | The app is AGPL-3.0; the UI framework crates are MIT | `LICENSE-AGPL`, `LICENSE-MIT` |

**Layer 1: the pane (any terminal).** A Claude Code mod in the `switchyard` plugin adds three things:

- a side pane listing every session from the hub, grouped by account;
- a band above the prompt when another session needs you;
- the account, limits and context in the status line.

It works the same in Warp, Ghostty, iTerm, Windows Terminal, over SSH and inside tmux. It only reads from the hub; it never types into a session. This is the board called "Pane inside Claude Code".

**Layer 2: Warp hookups without a fork.** `sy setup` adds these when it finds Warp on the machine.

1. **Status icons for every account.** Warp's own install check only looks in one config folder, so the other accounts get no icons. `sy setup` installs Warp's `claude-code-warp` plugin into every account folder through the shared plugin cache. It only sends when Warp's variables are present, so other terminals are unaffected. `sy` never sends a second copy of these events.
2. **Account in the tab title.** `sy hook` returns a `terminalSequence` with an `OSC 2` title such as `work · api · asked`: account label first, then project, then state. The launcher sets `CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1` only while Switchyard titles are on, so the two never fight. A tab you renamed keeps your name.
3. **Account colour on new tabs.** For each account, `sy` writes `~/.warp/tab_configs/switchyard-<label>.toml` containing a title, the nearest of Warp's six colours, and the command (`claude-2` and so on). `sy open work` opens it via `warp://tab_config/switchyard-work`. The colour is fixed when the tab opens. If you later run another account in that tab, the colour is wrong, but the title is still right. A seventh account repeats a colour, and its label tells them apart.
4. **Click to focus.** `sy hook` adds `term: {app: "warp", id: WARP_TERMINAL_SESSION_UUID, focus_url: WARP_FOCUS_URL}` to its `hook_event` record. These are optional fields; any other terminal fills in `app` only. The desktop app, the pane and the board then show **Focus** only when they run on the same machine as the tab. Focus opens the URL, which brings up that exact tab. Phones and other machines never see the URL.
5. **SSH sessions.** `sy ssh <host>` is a thin wrapper, or one line in `~/.ssh/config`. It passes the laptop's focus URL as `LC_SY_FOCUS`, which most servers accept by default (`AcceptEnv LC_*`). A Claude session on the server then records which laptop tab to bring forward. Without it, server sessions still appear everywhere, just without Focus.

What layer 2 cannot do:

- change a tab's colour after it opens;
- show the account inside Warp's own status UI;
- show context % on the tab;
- list sessions from other machines inside Warp.

Those need layer 3.

**Layer 3: Switchyard Terminal, a thin Warp fork (optional).** All changes go in new files plus small edits to files that rarely change upstream. `workspace/view.rs` is 30k lines with about 40 commits in two months, so the fork leaves it alone.

| Change | Where | Size |
| --- | --- | --- |
| An `account` field (label, colour, seat id) in the cli-agent event. Stock Warp ignores unknown fields, so the same events still work there. | `cli_agent_protocol.rs`, `event/v1.rs` | small |
| An account badge on each tab: a coloured stripe and short label in any colour, updated live from events, in both horizontal and vertical tabs | `tab.rs`, `vertical_tabs.rs` | 200–500 lines |
| A Sessions panel: a new `ToolPanelView::Sessions` in the left panel. Local sessions come live from Warp's `CLIAgentSessionsModel`; everything else comes from the hub (`/v1/sessions` + `/v1/stream`, desktop token). Clicking a session reuses Warp's own focus-pane path. | new module + `left_panel.rs` | 1–2k lines, 1–2 weeks |
| New tab as account: a menu of this machine's account folders with who is logged in to each, which opens a tab running that account's launcher | new module | small |
| A footer chip with the focused tab's account limits (5-hour, 7-day) and context % | new module | small |
| Settings › Switchyard: hub address, pair this terminal, show other machines, account colours (read from the hub) | new module | small |
| Rebrand and cloud off: new name and icon, no Warp marks; Drive, hosted AI, Oz and team features hidden. The open-source build already skips login and turns off telemetry and autoupdate. | build flags | medium |

How Warp's states map to Switchyard's: in progress → working; blocked on permission → needs\_permission; blocked on a question or input → asked; success → done; failed → failed; cancelled → ended. When both are known, the hub's state wins, because it also knows about gone sessions, remote sessions and timing.

**The Sessions panel**

- **Needs you** at the top: every session waiting on you, across all accounts and machines, oldest first.
- Then one collapsible group per account in that account's colour. The header shows 5-hour use with its reset time, and 7-day use.
- **Each row** shows a state dot, project, session title, a machine chip if the session is elsewhere, context % (amber from 80, red from 92) and age.
- **Clicking a row:**
  - a tab in this window, including an SSH tab, is focused;
  - a session elsewhere opens a menu: "Open in Claude app" (the Remote Control link) or "Copy resume command" (`ssh host -t 'cd … && claude-2 --resume <id>'`).
  - It never sends keystrokes to a session.
- **Filters:** All, Needs you, This window, or one machine. **Keys:** Ctrl+Shift+S toggles the panel, ↑↓ moves, Enter focuses.
- **Close ended tabs:** one button clears finished sessions, which also eases Warp's memory use (see below).
- **If the hub is unreachable,** local sessions stay live from Warp's own events, and other machines grey out with "hub offline since 14:02".

**Costs and risks of the fork**

- **Size:**
  - Warp is about 1.46 million lines of Rust across 80 crates, with long builds.
  - Its Linux build script is Debian/Ubuntu only. Fedora needs the packages mapped to dnf, a container, or the repo's `flake.nix`.
- **Churn:**
  - Upstream sees roughly 40–50 commits a week, many by a bot.
  - A weekly CI job rebases the fork and builds it; a conflict opens an issue instead of blocking.
- **AGPL:**
  - The fork's full source must be published under AGPL, with notices kept, changes marked, and no Warp name or logo.
  - `sy`, the hub and the apps stay under Switchyard's own licence, because they only talk to the terminal through escape codes and HTTP. That reading should be checked before release.
  - It is unclear whether Warp's terms let a renamed fork call Warp's servers, so the fork turns those features off.
- **Memory:** Warp issues #15262, #12154, #8314 and #2611 report memory growth with many long-lived tabs, including Claude Code tabs. That is exactly how Switchyard users work, so test with 50+ tabs before recommending the fork.
- **Drift:** Warp could change the cli-agent protocol, focus URLs or tab configs. The protocol is versioned (`v: 1`) and has been stable. `sy doctor` checks that each still works.
- **Exit plan:** offer the `account` field and the tab badge upstream. If Warp accepts them, most of the fork can be retired, leaving only the Sessions panel, which layer 1 already covers.

**Recommendation.** Ship layers 1 and 2 in phase 6; together they take days. Build the fork only if, after real use, you still miss a live account colour on tabs and the panel inside Warp (open question 9).

## 15. Scenarios, case by case

| Scenario | What happens | What you do |
| --- | --- | --- |
| **First install, one laptop** | One-line install, then `sy setup`. Detect finds `~/.claude` logged in; you add two accounts, keep the sharing defaults, choose "Just this computer", scan the QR | About two minutes; restart open Claude sessions |
| **First install by Claude** | In a session you ask "install Switchyard". The skill asks how many accounts, what to share, and where the hub goes, then runs `sy setup --yes …` | Log in to each new account when it shows the link or code |
| **Existing hand-made setup** (yours) | Detect finds `~/.claude`, `-2`, `-3`, symlinks and `claude-launch`, and adopts them. It adds only the plugin, the status line wrapper and the timer | Nothing else |
| **Adding an account later** | `sy account add work` or Settings → Accounts → Add: new folder, sharing applied, login, launcher `claude-4` | Log in |
| **A folder re-logged to another account** | The next record carries the new login; the folder is attached to the new seat from that moment, and older sessions stay with the old one | Nothing |
| **Same login on laptop and server** | Both folders join the same seat, one card; sessions show which machine they're on | Nothing |
| **claude-1 is A, desktop tab 1 is B** | Two seats, two cards; joined by login, never by position | Nothing |
| **Adding an SSH server** | Add machine → copy the one-line command → run it on the server. It installs, detects the server's folders, pairs, and checks reachability | Paste one line; log in to accounts there |
| **SSH server, hub on the laptop, no Tailscale** | `sy connect --via ssh`: a `RemoteForward` in your SSH config; records flow while you're connected and queue otherwise | Optionally turn on direct ntfy push |
| **Laptop closed, sessions running on a server** | Hub on a home server or VPS: nothing changes. Hub merged on the laptop: the server's records queue; direct push, if on, still sends "needs you" | Nothing |
| **Hub down or being upgraded** | Every machine's outbox holds records; on return they flush in order and duplicates are dropped | Nothing |
| **Pairing a phone** | Any screen → Add phone → QR. The app or web app claims the code, gets a read token, and registers for pushes | Scan; allow notifications |
| **Away from home, hub not reachable** | Pushes still arrive. Tapping opens the session in Claude via its Remote Control link; the full details need the tailnet | Turn on the Tailscale app for details |
| **Lost phone or laptop** | Devices → Revoke. Its token stops working at once | One click |
| **Moving from merged to a home server** | `sy hub export` → `sy hub import` on the server → `sy connect --hub <new>` on each machine | One command per machine; re-scan the phone QR |
| **Session dies without SessionEnd** (crash, `kill`) | The timer's `claude agents` check no longer lists it, so it shows as `gone` | Nothing |
| **Context nearly full** | The status line reports context %, and the board shows it per session | Optionally a rule: push at 90% context |
| **Rate limited mid-task** | StopFailure `rate_limit` → `failed` → immediate push naming the account and its reset time, plus which account has room | Switch in the terminal yourself |
| **Uninstall** | `sy uninstall`: plugin, status line wrapper, timer and launchers removed; config folders and logins kept. `--purge` also clears the outbox | One command |

## 16. Build plan

Ten phases, built one at a time. Each phase ships something you can use on its own, and nothing later forces a rewrite of something earlier. Phase 1 replaces `bin/switchyard-hook.sh`, `bin/connect.sh` and the in-app scanner; the prototype keeps running until phase 2 has taken over its job.

**How every phase is run**

- **One branch per phase,** merged as small pull requests. When the phase's checklist passes, it is tagged `v0.<phase>`.
- **Spec first:** a change to the record format, the API or the schema goes into `docs/spec/` before any code.
- **Each phase ends with a demo on your own machines** against its "Done when" list. A phase isn't done because its code is written.
- **A phase starts only when you say so.** Its first step is a short task list in the repo, `docs/phases/<n>.md`, taken from this section.

| Phase | You get | Size | Needs |
| --- | --- | --- | --- |
| 0. Baseline and spec | The prototype saved in git, the spec, real test data, CI | 2–3 days | — |
| 1. `sy` on one machine | Every session on this laptop recorded with the right account; status line | \~1 week | 0 |
| 2. Hub and web board | The hub on the OCI server; a live board in the browser | 1.5–2 weeks | 1 |
| 3. Installer and sharing | `sy setup` for first-time users and for adopting existing folders | \~1 week | 2 |
| 4. Phone web app and pushes | The phone dashboard and alerts that cancel if you answer first | \~1 week | 2 |
| 5. More machines | SSH servers report with the laptop closed | \~1 week | 2, 3 |
| 6. Desktop app | claude.ai account tabs, web usage, every setting in one place | \~1 week | 2, 3 |
| 7. Terminal pane + Warp hookups | The session pane in any terminal; account titles, colours and click-to-focus in Warp | 3–5 days | 2 |
| 8. Phone store app | Native pushes with buttons, widgets, Live Activity | \~2 weeks | 4 |
| 9. Warp fork (optional) | Account badges on tabs and a Sessions panel inside the terminal | 2–4 weeks | 7 |

Phases 4, 6 and 7 can be done in any order once 2 and 3 are in. The order above puts the phone first because the all-accounts dashboard matters most on mobile.

**Every screen and the phase that builds it.** These are the 20 designs on [Switchyard screens](https://claude.ai/artifact/ExUAQtHWibGTetRzTjtQ9y). Copies live in the repo under `docs/design/screens/`, so each phase builds against the same picture. The web board in phase 2 is a browser version of the desktop board screens: it shows the same content, but without account tabs or settings that change anything.

| Screen | Device | What it shows | Phase |
| --- | --- | --- | --- |
| `sy setup` · 1 Detect | Terminal | Claude Code, its config folders and who is logged in to each | 3 (`sy doctor` and detection in 1) |
| `sy setup` · 2–3 Accounts and sharing | Terminal | Labels and colours; the switches for what's shared and what never is | 3 |
| `sy setup` · 4 Hub | Terminal | Join a hub, run one here, or later | 3 (`sy connect` in 2) |
| `sy setup` · 5 Phone + done | Terminal | Pairing QR and a summary of the changes | 3 (QR shown in 4) |
| Installed by Claude | Terminal | Claude running `/switchyard:setup` with flags and `--json` | 3 |
| Home | Desktop / web | Needs you, account limits, live sessions, machines | 2 (web, read-only), 6 (app) |
| Sessions board | Desktop / web | Every session grouped by account, with state, machine, context and age | 2 (web), 6 (app) |
| Account tab | Desktop | A claude.ai login in its own tab, with a usage strip | 6 |
| Settings · Accounts and sharing | Desktop | Accounts, logins per machine and folder, sharing switches | 2 (web, read-only list), 6 (settings) |
| Settings · Hub and machines | Desktop / web | Hub address, machines, last seen, backlog, version | 2 (web, read-only), 5, 6 |
| Settings · Phone and notifications | Desktop | Paired devices, channels, rules, quiet hours | 4, 6 |
| Now | Phone | Needs-you inbox and account limits | 4 |
| Sessions | Phone | All sessions, filtered and grouped | 4 |
| Session detail | Phone | State, last message, timeline, Open in Claude | 4 |
| Accounts | Phone | Limits per account, pace, most room | 4 |
| Alerts and rules | Phone | Rule switches and the recent-pushes log | 4 |
| Pairing | Phone | QR scan, or a code and an address | 4 |
| Lock screen: push + widgets | Phone | Push with Open, Snooze and Seen, plus widgets | 4 (web push), 8 (native) |
| Session pane inside Claude Code | Terminal | Side pane, band and status line | 1 (status line), 7 |
| Switchyard Terminal (Warp fork) | Terminal | Account-badged tabs, Sessions panel, new-tab-as-account menu | 9 |

### Phase 0: Baseline and spec

**Build**

1. Commit the prototype as it is and tag it `prototype`. The repo has no commits yet; `.env`, the database and `node_modules` stay ignored.
2. Set up the new layout:
   - `cmd/sy/` and `internal/` (record, outbox, identity, hook, statusline, scan, hub/ingest, hub/store, hub/state, hub/usage, hub/api, hub/push, hub/pair)
   - `plugin/switchyard/`
   - `app/` (the Electron app, moved)
   - `web/`
   - `deploy/` (compose file, Caddy, systemd and launchd units)
   - `docs/spec/`
3. Write the spec from sections 9 and 10:
   - `record.md` (envelope plus the seven record kinds)
   - `api.yaml` (OpenAPI for every endpoint)
   - `schema.sql`
   - `states.md` (session state transitions)
   - `tokens.md` (scopes, pairing)
4. Capture real test data from claude-1, claude-2 and claude-3:
   - one payload per hook event;
   - the status line input;
   - `claude auth status --json`;
   - the claude.ai usage response, with every field kept, `iguana_necktie` included.

   A script removes emails and tokens, and the results go in `testdata/`.
5. Write a one-off importer that moves the prototype's SQLite database into `schema.sql`.
6. Set up CI on Linux and macOS: `go vet`, tests, golden-file tests on the test data, and a migration test.

**Done when**

- [ ] Every captured payload turns into a record and back without losing a field.
- [ ] The prototype database (46 MB) imports, and the counts of sessions, events and usage rows match.
- [ ] CI is green.

### Phase 1: `sy` on one machine

No hub yet: everything is written to the local outbox and read back locally.

**Build**

1. **The Go binary and its folders:**
   - config in `~/.config/switchyard/config.toml`;
   - state in `~/.local/state/switchyard` (on macOS `~/Library/Application Support/Switchyard`, on Windows `%LOCALAPPDATA%\Switchyard`).
2. **Identity:**
   - For each config folder, read `claude auth status --json`, cached and refreshed at SessionStart, to get the seat (account + organisation).
   - The slot is this machine plus the folder.
   - Tokens are never read.
3. **`sy hook <event>`:**
   - reads the hook's JSON from stdin;
   - adds the slot, seat, machine and terminal info (`term` from section 14);
   - appends one record to the outbox, fsynced, one file per day;
   - exits within 20 ms, uses `async` where Claude Code allows it, and never blocks or fails a session.
4. **`sy statusline`:**
   - prints the account label, 5-hour and 7-day use, and context %;
   - writes a `limit_sample` record at most once a minute per session.
5. **`sy scan`:** session list and token rows from transcripts (port `core/transcripts.js` and `core/pricing.js`), plus a one-time `history_import` of old transcripts.
6. **The `switchyard` plugin:** `hooks.json` for every event, the status line, and the `/switchyard:status` skill. It is installed by hand into each folder for now; the installer comes in phase 3.
7. **`sy status`** (a local table of sessions) and the first `sy doctor`.

**Done when**

- [ ] For a full day of normal work on claude-1/2/3, every session shows in `sy status` with the right account and the right state, including `asked` and `needs_permission`.
- [ ] Hook time stays under 20 ms (p95), with no lag you can feel.
- [ ] Unplugging the network changes nothing.
- [ ] `~/.claude-N/settings.json` are still symlinks.

### Phase 2: Hub and web board

**Build**

1. **The hub:** `sy hub serve` on SQLite in WAL mode, with migrations from `schema.sql`.
2. **`/v1/ingest`:**
   - idempotent by record id, and replies with the accepted ids;
   - every raw record kept whole.
3. **The state engine** (`states.md`):
   - the usage model per seat;
   - machine heartbeats;
   - "gone" detection.
4. **The read API:** `/v1/overview`, `/v1/sessions`, `/v1/stream` (SSE), `/health`.
5. **Tokens:**
   - three scopes: machine (write), desktop (write + read), phone (read + push);
   - stored hashed;
   - created with `sy hub token create`.
6. **Getting records to the hub:**
   - `sy connect <url>` sets the machine up;
   - `sy flush` runs after each hook (in the background, debounced) and on an OS timer (systemd user timer or launchd).
7. **The web board:** port `web/` to the new API: Now, Sessions, Accounts and Machines, matching the desktop screens.
8. **Deploy the hub to the OCI server over Tailscale** (decision 3), using the compose file in `deploy/`. Merged mode on the laptop is the fallback: a user service with `loginctl enable-linger`.
9. **`sy hub backup`** runs nightly, using SQLite's online backup, and `sy hub prune` removes old data.

**Done when**

- [ ] For a full day, laptop sessions appear on the board within 2 seconds of each event.
- [ ] Restarting the hub mid-session, and 30 minutes without network, lose nothing and create no duplicates.
- [ ] The prototype's history shows on the board.
- [ ] The prototype's own hook and scanner are switched off.

### Phase 3: Installer and sharing

**Build**

1. **`install.sh` and `install.ps1`:** download the release binary, check its checksum, and put it in `~/.local/bin`. Nothing else.
2. **`sy setup`,** the screens on the Install page:
   - finds Claude Code, the config folders and who is logged in to each;
   - offers the sharing switches from section 4;
   - sets up the shared plugin cache;
   - syncs only the `mcpServers` key of each folder's `.claude.json`;
   - asks where the hub is: join one (code or QR), run one here, or later;
   - installs the plugin into every folder;
   - creates the `claude-N` launchers.
3. **Safety:**
   - prints a dry-run plan first;
   - backs up every touched file to `~/.local/state/switchyard/backup/<time>`;
   - `sy setup --undo` reverses it all.
4. **`sy account add`:**
   - creates a new folder;
   - runs `claude auth login --claudeai` (with code paste over SSH);
   - sets the label and colour.
5. **Run by Claude:** every question has a flag, `--yes` and `--json` make it scriptable, and the `/switchyard:setup` skill runs it (the "Install by Claude" screen).
6. **Adopting an existing layout:** it recognises setups like your `~/bin/claude-launch` and area folders, and leaves them working instead of replacing them.
7. **Windows and macOS:** junctions or copies where Windows can't link; a note about the macOS Keychain.
8. **The full `sy doctor`.**

**Done when**

- [ ] On fresh Fedora, Ubuntu and macOS machines, going from install to three accounts on the board takes under 10 minutes, with no docs.
- [ ] On this laptop, it adopts the existing folders, no login changes, and `settings.json` stays a symlink.
- [ ] `--undo` puts every file back exactly.

### Phase 4: Phone web app and pushes

**Build**

1. **The installable web app:** Now, Sessions, Session, Accounts, and More (Alerts, Analytics, Settings). It shows the last known state when offline.
2. **Pairing:** `sy phone pair` or the board shows a QR code. A one-time code becomes a phone token, which can read and register for push.
3. **The rules engine:**
   - delays: permission 30 s, question 1 min;
   - immediate alerts: failures;
   - limits at 80% and 95%, and a machine silent for 1 hour;
   - quiet hours;
   - cancel if answered first, deduplication, snooze.
4. **Channels:**
   - Web Push, with VAPID keys made by the hub;
   - ntfy, on your own server or ntfy.sh with a random topic;
   - an optional private mode that leaves text out of the push;
   - the Remote Control link in every push.
5. **The recent-pushes log.**

**Done when**

- [ ] On an iPhone and an Android phone: install from the hub address, pair by QR, and a question asked on the laptop arrives in about 1 minute.
- [ ] Answering in the terminal at 40 seconds means no push is sent.
- [ ] Crossing 80% of a limit sends exactly one push.

### Phase 5: More machines

**Build**

1. **`sy machine add user@host`:** copies the binary over SSH, runs `sy setup --yes --join`, and logs in accounts with code paste.
2. **Three ways to reach the hub:**
   - directly (Tailscale);
   - through the laptop with SSH `RemoteForward`;
   - the hub pulling over SSH (`sy outbox drain`).
3. **Optional direct ntfy** from `sy` on servers that can't reach the hub.
4. **The Machines screen:** last seen, outbox backlog, version, and the silent-machine alert.
5. **`sy update`** across all machines, with mixed versions handled through the record's `v` field.
6. **`sy hub export` / `import`** to move the hub.

**Done when**

- [ ] gpu-box and the OCI server keep reporting with the laptop lid closed.
- [ ] After a 1-hour network cut, they catch up in order.
- [ ] Adding a new server takes one command.

### Phase 6: Desktop app

**Build**

1. **Slim the app:** move the Electron app to `app/`, and remove its local analytics database, hook receiver and scanner (the hub does those now).
2. **Account tabs:** one claude.ai login per tab, in persistent partitions so logins survive restarts and updates. Labels and colours come from the matching seat in the hub.
3. **Web usage** (decision 1):
   - read every 5 minutes, and when the tab opens;
   - a plain warning you accept once per account when adding it, and an off switch per account;
   - backoff on errors, and a stop on 401, 403 or 429 for that account;
   - the raw response kept whole as `web_usage` records, every field included.
4. **Settings screens:** accounts, sharing, hub, machines, alerts, phone pairing, devices.
5. **Hub and alerts:**
   - starts and stops the merged hub service;
   - a tray icon with the needs-you count;
   - desktop notifications.
6. **Builds:** AppImage/rpm/deb, dmg and exe. Auto-update is off by default.

**Done when**

- [ ] Every setting lives in one app.
- [ ] The numbers match the board.
- [ ] Logins survive restarts and updates.
- [ ] The warning shows once per account, and turning reading off stops it immediately.

### Phase 7: Terminal pane and Warp hookups

**Build:** layers 1 and 2 from section 14.

- The Claude Code mod: side pane, band above the prompt and status line, all fed from `/v1/stream`.
- Warp's plugin installed in every folder.
- `OSC 2` account titles.
- Tab configs per account, opened with `sy open <label>`.
- Focus URLs, and `sy ssh`.

**Done when**

- [ ] In Ghostty and in Warp, the pane matches the board.
- [ ] Focus on the board or the pane jumps to the right Warp tab, including an SSH tab.

### Phase 8: Phone store app

**Build:** an Expo app on the same API.

- Native pushes from the hub with Open, Snooze and Seen buttons.
- Small and medium widgets.
- A Live Activity counting down to a reset.
- TestFlight and APK builds.

**Done when**

- [ ] Two weeks of daily use.
- [ ] Push buttons work from the lock screen.

### Phase 9: Warp fork (optional)

**Build:** layer 3 from section 14. Start only if, after at least two weeks with phase 7, you still miss a live account colour on tabs or the panel inside Warp.

**Done when**

- [ ] The weekly rebase job is green four weeks running.
- [ ] A 50-tab memory test passes.
- [ ] The account field has been offered upstream.

### Before going public

- Ask Anthropic about the desktop account tabs and the 5-minute web usage reading (decision 2), and include the warning text.
- A security review of tokens, pairing and push payloads, with a short threat model.
- Signed binaries with checksums and an SBOM. Choose Switchyard's licence; the Warp fork is AGPL.
- A docs site and a privacy page, an "unofficial, not affiliated with Anthropic" notice, and no "Claude" in any name.

**Later:** phone account tabs (after decision 2), a shared team hub, Postgres.

## 17. Decisions

Confirmed on 12 October 2026. Decision 1 differs from the earlier recommendation: web usage is read every 5 minutes, behind a warning.

| # | Question | Decision | Why it matters |
| --- | --- | --- | --- |
| 1 | **Background web-usage reading.** The desktop app's 5-minute script read of claude.ai's internal usage endpoint is the riskiest piece under the consumer terms (automated access). Keep it on by default, make it opt-in with a plain warning, or read only when you open the tab? | Every 5 minutes, after a plain warning you accept once per account, with an off switch per account. Errors back off, and 401/403/429 stops reading for that account | It is the one piece that could get an account flagged |
| 2 | Ask Anthropic before shipping the desktop account tabs and usage reading publicly? | Yes, before a public release; fine for your own use meanwhile | Removes the grey zone instead of guessing |
| 3 | Where does your hub live? | The OCI server, over Tailscale | Decides whether SSH-server pushes pause when the laptop sleeps |
| 4 | `sy` language | Go: one static binary, easy on SSH servers | Node would reuse prototype code but needs Node everywhere |
| 5 | Claude's last message to the hub (\~200 characters) | On for your own hub, off elsewhere | Needed for plain-text question detection and useful push text |
| 6 | PreToolUse / PostToolUse hooks | Off; "needs permission" clears at the next prompt or Stop | On means instant clearing, at one process per tool call |
| 7 | First phone channel | Installable web app + Web Push and ntfy, then the Expo app | The Expo app needs store accounts and an Apple developer fee |
| 8 | Phone account tabs (claude.ai on the phone) | Phase "later", after question 2 | App store and terms risk |
| 9 | Switchyard Terminal: build the Claude Code pane first, then the Warp fork? | Yes (section 14) | The pane works in every terminal, Warp included, at a fraction of the cost. Warp has no plugin API, so anything inside Warp means a fork that is rebased weekly onto a fast-moving 1.46M-line repo, with AGPL duties. The fork waits until the pane and the no-fork hookups have been used for real |
| 10 | The product name and site | Keep "Switchyard"; no "Claude" in any name | Trademark rule in the terms |
| 11 | The other session's "Switchyard Companion Suite" page | Folded in here; delete it | Avoids two competing plans |

## 18. Sources

**Claude Code (checked against 2.1.296 on this machine)**

- [Hooks](https://code.claude.com/docs/en/hooks): events, payloads, `async`, `http` hooks, `terminalSequence`
- [Status line](https://code.claude.com/docs/en/statusline): `rate_limits`, context, `session_name`
- [Settings and `CLAUDE_CONFIG_DIR`](https://code.claude.com/docs/en/settings), [Plugins](https://code.claude.com/docs/en/plugins), [Remote Control](https://code.claude.com/docs/en/remote-control), [Deep links](https://code.claude.com/docs/en/deep-links)
- [Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance), [Consumer Terms](https://www.anthropic.com/legal/consumer-terms)
- Local: `claude auth status --json`, `claude agents --json`, `.claude.json` `oauthAccount` keys, the prototype's `usage.db`

**Multi-account tools:** claude-swap, CCS, ccsw, ccm, claude-code-profiles, cc-switch (their READMEs).

**Install, networking, push**

- Install and enrolment: [Beszel agent install](https://beszel.dev/guide/agent-installation), [Komodo connect servers](https://komo.do/docs/setup/connect-servers), [Tailscale auth keys](https://tailscale.com/kb/1085/auth-keys), [Netdata install](https://learn.netdata.cloud/docs/netdata-agent/installation/linux), [uv install](https://docs.astral.sh/uv/getting-started/installation/), [GitHub artifact attestations](https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds)
- Networking: [tailscale serve](https://tailscale.com/kb/1242/tailscale-serve), [userspace networking](https://tailscale.com/kb/1112/userspace-networking), [Cloudflare quick tunnels](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/do-more-with-tunnels/trycloudflare/), [Caddy automatic HTTPS](https://caddyserver.com/docs/automatic-https), [ssh\_config](https://man.openbsd.org/ssh_config)
- Push: [ntfy iOS instant notifications](https://docs.ntfy.sh/config/#ios-instant-notifications), [Home Assistant notifications](https://companion.home-assistant.io/docs/notifications/notification-details), [Bitwarden push relay](https://bitwarden.com/help/configure-push-relay/), [Nextcloud push](https://nextcloud.com/pushnotifications), [Expo push sending](https://docs.expo.dev/push-notifications/sending-notifications/)

**Mobile**

- [WKWebsiteDataStore(forIdentifier:)](<https://developer.apple.com/documentation/webkit/wkwebsitedatastore/init(foridentifier:)>), [WebKit profiles](https://webkit.org/blog/14423/building-profiles-with-new-webkit-api/), [androidx.webkit releases](https://developer.android.com/jetpack/androidx/releases/webkit)
- [Google embedded-webview OAuth block](https://developers.googleblog.com/en/upcoming-security-changes-to-googles-oauth-20-authorization-endpoint-in-embedded-webviews/), [App Review Guidelines](https://developer.apple.com/app-store/review/guidelines/), [WidgetKit refresh budget](https://developer.apple.com/documentation/widgetkit/keeping-a-widget-up-to-date), [expo-widgets](https://docs.expo.dev/versions/v56.0.0/sdk/widgets/)
- [Ferdium claude.ai login issue](https://github.com/ferdium/ferdium-app/issues/2403), [Happy](https://github.com/slopus/happy)

**Warp:** github.com/warpdotdev/warp (master c1ec372, 10 Oct 2026; files named in section 14), github.com/warpdotdev/claude-code-warp (the hooks and OSC 777 payload), Warp issues #16111 (warpctrl metadata), #15262, #12154, #8314 and #2611 (memory with many tabs).

The research notes behind each section are kept in this session's scratch folder; the claims marked unverified there are left out of this doc or labelled "to be tested".
