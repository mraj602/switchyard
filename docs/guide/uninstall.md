# Uninstall

Watchyard never deletes your Claude Code folders or logins. Removing it is a few steps,
depending on what you set up. Every `wy` step shows its plan first.

## 1. Remote machines (from your laptop)

For each server added with `wy machine add`:

```sh
wy machine list
wy machine remove <name> --revoke
```

This removes the SSH forward block, the pull key line in the server's `authorized_keys`, the
hub's pull target and the entry in your machine list. With `--revoke` it also revokes the
machine's devices on the hub (the hub keeps its history). `--keep-remote` leaves the server
untouched. The server's `wy`, outbox and Claude folders stay; finish there with
`ssh <host> wy uninstall` (the command prints it) and section 2 below.

## 2. Each machine

Run these on every machine where `wy` is installed.

1. Stop the optional pieces:

   ```sh
   wy peers disable       # removes the peers agent service (same as wy agent disable); keys stay
   wy term off            # removes the pane, tab titles and Warp tab configs, if you set them up
   ```

2. See what `wy uninstall` will do, then do it:

   ```sh
   wy uninstall --dry-run
   wy uninstall
   ```

   It removes, from its own ledger and with a backup set:
   - the Watchyard plugin and its local marketplace from every folder;
   - the status line wrap (your original status line command comes back);
   - the timer and, if the hub runs here, the hub service;
   - the launchers `wy` wrote (`claude-2`, ...), if you have not changed them;
   - MCP server syncing between folders (the servers stay as they are).

   It keeps: every config folder, every login, every shared link and copy (your folders keep
   sharing what they share), labels, the backups, `config.toml`, the outbox and the device
   token. The hub database is never removed by `wy uninstall`.

3. To also remove Watchyard's own files on this machine:

   ```sh
   wy uninstall --purge
   ```

   This also deletes `config.toml` and the state folder: the outbox (unsent records), the
   device token, the machine id, the keys (including `owner.sealed` on the owner machine) and
   every setup backup. It asks first. Keep your owner recovery code if you might come back.

4. Remove the binary: `rm ~/.local/bin/wy` (the last line of `wy uninstall` prints it).

Alternatives:

- `wy setup --undo-list` and `wy setup --undo [<backup id>]` reverse a single setup run byte for
  byte, for example to go back to how your folders were before.
- `wy disconnect` only removes the hub URL, the token and the timer, and keeps everything else
  (queued records stay in the outbox).
- `wy account remove <folder|label>` stops managing one account folder; add `--unshare` to turn
  its shared links into independent copies.

## 3. The hub

**User service** (merged mode or a server without Docker). `wy uninstall` removes the service
`wy setup` installed. If you installed it by hand from `deploy/systemd/`:

```sh
systemctl --user disable --now watchyard-hub
rm ~/.config/systemd/user/watchyard-hub.service
systemctl --user daemon-reload
```

**Docker** (from `deploy/`):

```sh
docker compose down          # stops and removes the container; the data volume stays
docker compose down -v       # also deletes the volume: the database, backups and keys are gone
```

Take a last `wy hub backup` (or `wy hub export --no-freeze`) first if you may want the history.

The data folder (`wy paths`, `data`) holds `hub.db` and its backups; delete it by hand when you
no longer need them. If you used `tailscale serve` for the hub, turn it off with
`tailscale serve --https=443 off`.

## 4. Devices on the hub

If the hub stays and only some devices go, revoke them so their tokens stop working:

- On the board (owner sign-in): Devices → Revoke.
- On the hub machine:

  ```sh
  wy hub token list
  wy hub token revoke <id|name>
  ```

## 5. The desktop app

1. Quit it (tray menu → Quit, or Ctrl+Q / Cmd+Q).
2. Turn off Run at login in Settings first if you enabled it, or remove the autostart entry
   (Linux: `~/.config/autostart/watchyard-desktop.desktop`).
3. Remove the package the usual way for your OS (delete the AppImage, `dnf remove` / `apt remove`
   for the rpm or deb, move the app to the Trash on macOS, Apps & features on Windows).
4. Delete its user data folder to remove the account tabs' sign-ins and settings:
   `~/.config/watchyard-desktop` (Linux), `~/Library/Application Support/watchyard-desktop`
   (macOS), `%APPDATA%\watchyard-desktop` (Windows).
5. Revoke its desktop token on the board (Devices).

## 6. The Android app

1. In the app: Settings → **Unpair this phone** (this also removes it from the hub), or revoke it
   on the board under Devices.
2. Uninstall it like any app. Its token, keys and claude.ai tab sign-ins are deleted with it.

If you also used the web app on a phone, remove it from the home screen and revoke its device on
the board.
