# Desktop app

The desktop app puts every claude.ai account you use in one window, one tab per account, each
with its own sign-in. Under each tab a usage strip shows that account's limits. A **Hub** tab
shows the board, and the app turns hub alerts into OS notifications and a tray badge. It is
optional: `sy` and the hub work without it. The spec is [desktop.md](../spec/desktop.md).

Platforms: Linux (AppImage, deb, rpm), macOS (dmg) and Windows (NSIS installer). Packages are
built locally and are not code-signed, so macOS and Windows warn on first start.

## Build and run

There are no published packages yet. You need Node 22 or later and Go (the package bundles
its own `sy`).

```sh
make app-dist                 # packages in app/dist/
```

`make app-dist` builds `sy` for this machine, bundles it and runs electron-builder for this
OS. On Linux without `libcrypt.so.1` (Fedora: `dnf install libxcrypt-compat`) only the
AppImage is built; set `APP_DIST_LINUX="AppImage deb rpm"` to choose targets.

To run from the source tree instead:

```sh
cd app
npm ci                        # downloads the Electron binary
npm start
```

The app looks for `sy` on `PATH` or in `~/.local/bin` (when it is recent enough), else uses
its bundled copy. Without any `sy` the account tabs still work, and the Hub tab says to run
`sy setup` ([install.md](install.md)).

## Account tabs

1. Click **+**, name the tab, and a fresh, isolated session opens on claude.ai.
2. Sign in on claude.ai's own page inside the tab, with your email and the code claude.ai
   sends. Google sign-in does not work in embedded browsers.
3. Every tab stays signed in at the same time; switching tabs never signs anything out.

How sign-in is handled:

- You sign in on Anthropic's own claude.ai page. Switchyard shows no login form of its own.
- Each tab's sign-in lives in its own local Electron session (`persist:tab-<id>`) on this
  computer. It never reaches the hub or any other machine.
- Switchyard never reads, exports or sends cookies or tokens.
- Account tabs present a plain Chrome user agent for the real operating system instead of
  Electron's default, because claude.ai's bot protection rejects Electron's. claude.ai sees an
  ordinary Chrome browser; see [claude-ai.md](claude-ai.md#the-browser-identity-of-the-tabs).
- A tab may only navigate to claude.ai and its sign-in pages over `https:`; other links open
  in your system browser.

When the hub knows an account that has no tab here, the Hub tab says so ("claude-6 has no tab
here. Add tabs") and **+** offers a tab for it. If a tab is signed in as a different account
than the one it was made for, its strip says so and offers to use the tab for that account or
to sign out and sign in again.

Use only your own accounts, within the terms of your Claude plan. See [faq.md](faq.md).

## Usage strip and usage reading

Under each tab a strip shows the 5-hour and 7-day windows, per-model weekly windows, reset
times, the reading state and "last read". When the hub knows the account, the numbers come
from the hub; otherwise from the tab's own last read, marked "local".

Reading usage in a tab is **off for each account until you accept a warning**. Until then the
strip says **Usage reading not enabled** with a **Review** button. The first time a signed-in
tab could read, the app asks:

> **Read usage for this account?**
> Every 5 minutes, Switchyard runs a small script inside this tab that reads the same numbers
> claude.ai shows under Settings → Usage. It reads numbers only: no chats, no cookies. They go
> to your own hub.
> This uses an internal claude.ai endpoint, not a public API. It may stop working, and
> automated access may not be allowed by Anthropic's consumer terms, so it could get this
> account flagged. Switchyard is unofficial and not affiliated with Anthropic.
> You can turn reading off for this account at any time in its usage bar or in Settings.
> **[Read usage for this account]  [Not now]**

What accepting does:

- Every 5 minutes (and shortly after the tab loads), the app calls claude.ai's internal
  endpoints `/api/organizations`, `/api/organizations/{uuid}/usage` and `/api/account` from
  inside the signed-in tab, with that tab's own session.
- The numbers are handed to `sy outbox append` and sent to your hub as `web_usage` records.
  A tab with "send to hub" off only updates its own strip.
- Errors back off (5 minutes up to an hour). A 401 (signed out), 403 or 429 (rate limited)
  stops reading for that account until you click **Resume**.

You decide whether to enable it. **Not now** leaves the tab working normally. The tray's
**Pause all reading** turns it off for every tab. Where else limits come from is in
[usage.md](usage.md).

## Hub tab and pairing

The Hub tab shows the board, signed in with a read-only desktop token.

1. Run `sy setup` on this computer and connect it to your hub (or run the hub here).
2. Open the Hub tab. If this computer can reach the hub's admin side (a local hub, or one
   added with `sy machine add`), pairing is one click.
3. Otherwise make a desktop code on the board (More, Devices, Add desktop app) or on the hub
   with `sy hub pair --kind desktop`, and type it into the app.

The token is stored encrypted with the OS keyring (`safeStorage`) and never enters a web
page; the app signs the Hub view in with a read-only cookie. On Linux without a keyring the
token falls back to basic storage, and Settings warns.

**Owner sign-in.** Changing rules, devices, peers or keys needs an owner session. Settings,
Hub, **Sign in as owner** asks `sy desktop owner-link` for a one-time link and opens it in
the Hub tab. It lasts 30 days. Keys and Peers are the board's own pages, shown in the Hub tab
(see [peers.md](peers.md)).

## Settings

- **Account tabs:** name, the account's label and colour (editable with an owner session),
  the reading switch, the send-to-hub switch, the warning status, Remove.
- **This computer's Claude Code folders:** the output of `sy account list` with the sharing
  switches. Adding an account or logging in needs a terminal, so the app shows the
  `sy account add` and `sy account login` commands to copy. See [accounts.md](accounts.md).
- **Hub:** address, connection, version, owner yes or no, pairing, and for a local hub the
  service state with Start and Stop.
- **Notifications** and **Background:** run at login, start hidden, keep running when the
  window is closed, tray.
- **Hub settings:** links to the board's Machines, Alerts and rules, Devices, Phone pairing
  and Settings pages.

## Notifications and tray

When the hub fires an alert, the app shows an OS notification: the account and project in
the title, the trigger ("asked a question", "needs permission", "5-hour at 82 %") and the
question, cut short. Clicking it opens that session in the Hub tab. Desktop notifications have
no Snooze or Seen buttons (the desktop token is read-only); use the board for those.

The tray icon gets a dot when a session needs you. Its menu lists up to five such sessions,
**Refresh usage**, **Pause all reading** and **Quit**. On a desktop with no tray (GNOME
without the AppIndicator extension), closing the window quits unless "Keep running" is on.

## Shortcuts

| Keys | Action |
| --- | --- |
| Ctrl/Cmd+0 | Hub tab |
| Ctrl/Cmd+1 to 9 | Account tabs |
| Ctrl/Cmd+, | Settings |
| Ctrl/Cmd+R | Refresh usage |
| Ctrl/Cmd+Q | Quit |

## Running in the background

Settings, Background, **Run at login** starts the app hidden at login (`--background`): no
window, but usage reading and notifications continue. On Linux this writes
`~/.config/autostart/switchyard-desktop.desktop`; on macOS and Windows it uses the OS login
items. A second launch shows the existing window.

## Where its data lives

| OS | Folder |
| --- | --- |
| Linux | `~/.config/switchyard-desktop` |
| macOS | `~/Library/Application Support/switchyard-desktop` |
| Windows | `%APPDATA%\switchyard-desktop` |

It holds `app-state.json` (tabs and settings, mode 0600), `hub-token.bin` (the encrypted
desktop token) and Chromium's own folders, including each tab's sign-in. Deleting a tab
deletes its session. Uninstalling the app does not remove this folder; delete it to remove
every sign-in (see [uninstall.md](uninstall.md)).

## Security baseline

- Every window and view runs with context isolation and the Chromium sandbox, without Node
  integration. Only the app's own shell has a preload, exposing a fixed list of functions;
  each IPC call checks its sender and arguments.
- The shell is served with a strict content security policy and loads nothing from the
  network.
- Permissions (camera, notifications from pages and so on) are denied by default.
- `sy` is run without a shell, from an allow-list of commands; secrets go on stdin, never on
  the command line. Nothing the hub sends can make the app run anything.
- Packaged builds disable `ELECTRON_RUN_AS_NODE`, `NODE_OPTIONS` and `--inspect`, and check
  the integrity of the app archive.

More in [security.md](security.md) and [desktop.md C2](../spec/desktop.md#c2-security-baseline).
