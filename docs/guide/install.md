# Install

This guide takes you from a fresh clone to a working setup: `wy` on your computer, a hub,
and more machines when you want them. The desktop app and the Android app have their own
guides ([desktop.md](desktop.md), [android.md](android.md)).

The quickest way is the install script from the latest release. Building from source is the
alternative. See [Releases](#releases) for what each release contains.

## Requirements

| Piece | Needs |
| --- | --- |
| `wy` and the hub | Linux or macOS. Windows runs it, with gaps (below). To build from source you also need Go 1.27 or newer, `make` and `git`. |
| Claude Code | Installed and on `PATH` as `claude` on every machine whose sessions you want to see. Watchyard is tested against Claude Code 2.1.296 and later. |
| Desktop app (optional) | Node.js 22 or newer, plus Go to build the bundled `wy` |
| Android app (optional) | Node.js 22.13 or newer, JDK 17, the Android SDK; a phone with Android 12 or newer |
| Phone access to the hub | An `https://` address for the hub, for example from `tailscale serve` or Caddy |

**Windows.** `wy` builds for Windows and the setup, hooks, status line and terminal board run
there, but Windows is less tested than Linux and macOS (CI runs the terminal packages and a
best-effort setup test). Peers and the owner-key commands are not available on Windows. WSL
counts as its own Linux machine.

## Words used here

- **Machine:** a computer that runs Claude Code. Every machine runs `wy`.
- **Hub:** `wy hub serve`, one process with one SQLite file. It stores records from your
  machines, works out session states and usage, sends alerts and serves the board. It can run
  on your laptop or on an always-on server.
- **Board:** the hub's web page. It also installs on a phone as a web app.

## 1. Install `wy`

### From a release (Linux, macOS)

```sh
curl -fsSL https://github.com/mraj602/watchyard/releases/latest/download/install.sh | sh
wy version
```

What `install.sh` does:

- It picks the archive for your OS and CPU (Linux or macOS, amd64 or arm64), and downloads it
  and `SHA256SUMS` from the release into a temporary folder.
- It checks the archive's SHA-256 against `SHA256SUMS`. If the check fails, or no
  `sha256sum`, `shasum` or `openssl` exists, it installs nothing and exits with code 3.
- It installs only `wy`, into `~/.local/bin` (replacing an older `wy` in one step). It edits
  no shell profile and no `PATH`. If that folder is not on your `PATH`, it prints the line to
  add for your shell.
- When a terminal is available it then runs `wy setup`. Otherwise it prints `run: wy setup`.

Options go after `sh -s --`:

```sh
curl -fsSL https://github.com/mraj602/watchyard/releases/latest/download/install.sh \
  | sh -s -- --version 1.0.0 --bin-dir ~/bin --no-setup
```

- `--version VER`: install this version, with or without the leading `v`. The default is the
  latest release.
- `--bin-dir DIR`: install into `DIR` instead of `~/.local/bin`.
- `--no-setup`: only install `wy`.
- `-- <args>`: everything after a second `--` goes to `wy setup`.

If the `gh` CLI is installed, the script also checks the build-provenance attestation
(`gh attestation verify`) and installs nothing if it fails. Without `gh` it says so and installs
on the checksum alone, which shows the download is intact but not who built it
([security](security.md#supply-chain)). Set `WY_VERIFY_ATTESTATION=1` to make a missing `gh` an
error, or `WY_VERIFY_ATTESTATION=0` to skip the check. `WY_INSTALL_BASE_URL` must be https
(http only for a loopback address).

### From a release (Windows)

In PowerShell 5.1 or later:

```powershell
irm https://github.com/mraj602/watchyard/releases/latest/download/install.ps1 | iex
```

It does the same checks and puts `wy.exe` in `%USERPROFILE%\.local\bin`. It does not change
`PATH`; it prints the command to add the folder. To pass options (`-Version`, `-BinDir`,
`-NoSetup`, `-SetupArgs`), run it as a script block:

```powershell
& ([scriptblock]::Create((irm https://github.com/mraj602/watchyard/releases/latest/download/install.ps1))) -Version 1.0.0 -NoSetup
```

### From source

```sh
git clone https://github.com/mraj602/watchyard.git
cd watchyard
make build                                   # writes ./dist/wy
install -D -m 0755 dist/wy ~/.local/bin/wy   # macOS: mkdir -p ~/.local/bin && cp dist/wy ~/.local/bin/
wy version
```

`~/.local/bin` must be on your `PATH`; Claude Code's own installer uses the same folder.
`go install github.com/mraj602/watchyard/cmd/wy@latest` also works, but it puts `wy` in
`~/go/bin` and leaves the version as `dev`.

### Before changing anything

You can look first:

```sh
wy doctor                 # read-only checks; warnings about "not set up yet" are normal
wy setup --detect         # what wy sees: Claude Code, your config folders, who is logged in where
wy setup --dry-run        # the plan wy setup would apply; changes nothing
```

## 2. Choose where the hub runs

| Setup | Hub runs on | Good for | Trade-off |
| --- | --- | --- | --- |
| **Laptop only** ("merged") | Your laptop, `127.0.0.1:8787`, as a user service | One computer, getting started | Alerts for servers pause while the laptop sleeps |
| **Always-on server** | A home server, NAS or small VPS, in Docker or as a service | Several machines, laptop often closed | One more thing to run |

You can start on the laptop and move the hub later without losing history
([maintenance.md](maintenance.md#moving-the-hub)).

The hub port is never published on `0.0.0.0`. Machines reach it over loopback, a Tailscale
tailnet, or HTTPS behind a reverse proxy. A client refuses plain `http://` to any other host
unless you pass `--insecure-lan`.

## 3a. Hub on your laptop: `wy setup`

`wy setup` does everything for one computer: it labels your Claude Code accounts, sets up
sharing between them, installs the Watchyard plugin and status line, and (if you choose
"Just this computer") installs the hub as a user service and connects this machine to it.

```sh
wy setup
```

1. **Detect.** It lists Claude Code, every config folder (`~/.claude`, `~/.claude-2`, ...)
   and which account each is logged in to. Nothing has changed yet.
2. **Accounts.** Pick a label and colour per folder; add new folders with `a`.
3. **Sharing.** Choose what the folders share (settings, instructions, skills, plugins, MCP
   servers, history, prompt history). Logins are never shared or touched.
   See [accounts.md](accounts.md).
4. **Hub.** Choose **Just this computer**. Setup installs the hub service
   (systemd user unit on Linux, a LaunchAgent on macOS, a scheduled task on Windows), makes a
   machine token, and connects this machine.
5. **Phone and done.** It can pair a phone now if the hub has an https address, prints a
   summary of every change and a backup id, and runs `wy doctor`.

Nothing is applied until you accept the plan. Every change is backed up and can be undone
with `wy setup --undo` (see [accounts.md](accounts.md#plans-backups-and-undo)).

After setup, **restart your open Claude Code sessions** so they load the plugin. Then:

```sh
wy status                 # local sessions and their states
wy board --once           # one frame of the board in this terminal
wy hub owner-link         # a one-time link that signs this browser in to the board as the owner
```

On Linux, run `loginctl enable-linger "$USER"` once if you want the hub and the timer to keep
running while you are logged out. Setup tells you when it is needed; it does not run it for you.

Inside Claude Code, `/watchyard:setup` walks through the same steps: it shows the plan,
asks you, and applies exactly that plan.

## 3b. Hub on a server

Pick one of these on the server. In each case the hub keeps its data in one SQLite file
(`hub.db`); back it up ([maintenance.md](maintenance.md)).

### Docker (recommended for a server)

The compose file builds the image from this repository and publishes the hub on
`127.0.0.1:8787` only.

```sh
git clone https://github.com/mraj602/watchyard.git && cd watchyard/deploy
docker compose up -d --build
docker compose ps                         # STATUS becomes "healthy" within about 30 s
```

To let machines on your tailnet reach it, publish it on the server's Tailscale address with
the override file:

```sh
echo "WY_BIND=$(tailscale ip -4 | head -n1)" > .env
docker compose -f docker-compose.yml -f docker-compose.tailscale.yml up -d --build
```

Run `wy` commands for the hub inside the container, for example
`docker compose exec hub wy hub owner-link`. Data lives in the
`watchyard_hub-data` volume.

### systemd user service, no Docker

```sh
make build && install -D -m 0755 dist/wy ~/.local/bin/wy
mkdir -p ~/.config/systemd/user
cp deploy/systemd/watchyard-hub.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now watchyard-hub.service
loginctl enable-linger "$USER"            # keep it running without a login session
curl -s http://127.0.0.1:8787/health
```

The unit listens on `127.0.0.1:8787`. Edit `ExecStart` to add `--public-url` (below) or to
listen on a tailnet address with `--listen 192.0.2.10:8787 --insecure-lan` (only when the
address is on a private network or tailnet).

### HTTPS: needed for phones

Phones need the hub on `https://` to install the web app, get notifications and pair the
Android app. Machines can use plain `http://` over loopback or a tailnet.

- **Tailscale:** on the hub's computer run `tailscale serve --bg 8787`. The hub is then at
  `https://<name>.<tailnet>.ts.net` (for example `https://hub.example.ts.net`) for every
  device on your tailnet. Enable HTTPS certificates for the tailnet in the Tailscale admin
  console first.
- **Caddy (public address):** keep the hub on loopback, edit the host name in
  `deploy/Caddyfile`, point DNS at the server, open ports 80 and 443 only, and run
  `caddy run --config deploy/Caddyfile`. The read API returns project names and session
  text, so it is never served without a token.

Then tell the hub its address, so pairing QR codes and notification links use it:

- Docker: `echo "WY_PUBLIC_URL=https://hub.example.ts.net" >> .env && docker compose up -d`
- Service: add `--public-url https://hub.example.ts.net` to `ExecStart`, then
  `systemctl --user daemon-reload && systemctl --user restart watchyard-hub`

The setting is stored in the database, so once is enough.

More detail, including pulling locked-down servers over SSH from a Docker hub:
[deploy/README.md](../../deploy/README.md).

### Connect your laptop to the server's hub

Run `wy setup` on the laptop and choose **Connect to my hub**, or connect by hand. The easy
way is a one-time pairing code:

1. On the hub: `wy hub pair --kind machine` (in Docker: `docker compose exec -it hub wy hub pair --kind machine`).
   It prints a code such as `482-913` and the command to run.
2. On the laptop: `wy connect https://hub.example.ts.net --code 482-913`

Or make a token and pass it on stdin, so it never lands in your shell history:

```sh
wy machine                                             # on the laptop: prints its machine id
wy hub token create --kind machine --name laptop --machine <id>   # on the hub; printed once
printf '%s\n' "$TOKEN" | wy connect https://hub.example.ts.net --token-stdin   # on the laptop
```

`wy connect` checks `/health` and the token, stores the URL in `config.toml` and the token in
the state folder (mode 0600), sends anything already queued, and installs a timer that runs
`wy flush` every minute. Records wait in a local outbox while the hub is unreachable and are
sent without duplicates when it is back.

## 4. More machines

From a computer that can already `ssh` to the new machine (with your keys, and `claude`
installed there):

```sh
wy machine add user@server.example.com --dry-run    # prints the steps; runs only a read-only probe
wy machine add user@server.example.com --name worker
wy machine list
```

`wy machine add` copies `wy` over your own `ssh`, connects it to the hub, sets up its
accounts (you may be asked to log each folder in: Claude Code prints a URL and waits for the
code), and installs the timer. It picks how the machine reaches the hub (`--reach`):

| Reach | How records travel | Use when |
| --- | --- | --- |
| `direct` | The machine posts to the hub over HTTPS or the tailnet | The machine can reach the hub (the default when it can) |
| `forward` | Through an SSH port forward from your laptop | The machine can reach nothing, but you SSH to it |
| `pull` | The hub runs `ssh <machine> wy outbox drain --stdio` every minute | A locked-down server the hub can SSH into |

Remove a machine with `wy machine remove worker --revoke`. Details:
[docs/spec/machines.md](../spec/machines.md).

You can also install by hand on the machine: install `wy` there (the install script, or a
build), run `wy setup`, and choose
**Connect to my hub** with a pairing code from `wy hub pair --kind machine`.

## 5. Optional pieces

- **Terminal board and Claude Code pane:** `wy board` shows the board in any terminal;
  `wy term setup --pane` adds a live pane inside Claude Code. See [board.md](board.md).
- **Desktop app:** [desktop.md](desktop.md).
- **Android app:** [android.md](android.md).
- **Peers (sessions messaging across accounts and machines):** [peers.md](peers.md).

## Releases

Releases are on [GitHub Releases](https://github.com/mraj602/watchyard/releases).

A tag `vX.Y.Z` publishes:

| Files | What |
| --- | --- |
| `wy_<ver>_<os>_<arch>.tar.gz` (Linux, macOS), `wy_<ver>_windows_<arch>.zip`; amd64 and arm64 | `wy` (`wy.exe`), `LICENSE`, `README.md` |
| `SHA256SUMS` | One SHA-256 line per archive |
| `install.sh`, `install.ps1` | The install scripts above |
| `Watchyard-<version>-<os>-<arch>.<ext>` (AppImage, deb, rpm, dmg, exe) | The desktop app ([desktop.md](desktop.md#download)) |
| `SHA256SUMS-desktop` | One SHA-256 line per desktop package |

Every archive and package also has a GitHub build-provenance attestation. To check one:

```sh
gh attestation verify wy_<ver>_linux_amd64.tar.gz --repo mraj602/watchyard
```

The Android APK is a separate release, `phone-vX.Y.Z`, signed with the maintainer's own key.
Its note lists the certificate fingerprint ([android.md](android.md#install-the-apk)).

To upgrade, run `wy update`, or the install script again
([maintenance.md](maintenance.md#upgrades)). To build everything yourself, use the source
steps above, `make app-dist` for the desktop app and [android.md](android.md#build-the-apk)
for the APK.

## Next

- [accounts.md](accounts.md): accounts, labels and what the folders share.
- [alerts.md](alerts.md): what notifies you, and where.
- [troubleshooting.md](troubleshooting.md): when something does not show up.
