# Uninstall

Switchyard never deletes your Claude Code folders or logins. Removing it is a few steps,
depending on what you set up. Every `sy` step shows its plan first.

## 1. Remote machines (from your laptop)

For each server added with `sy machine add`:

```sh
sy machine list
sy machine remove <name> --revoke
```

This removes the SSH forward block, the pull key line in the server's `authorized_keys`, the
hub's pull target and the entry in your machine list. With `--revoke` it also revokes the
machine's devices on the hub (the hub keeps its history). `--keep-remote` leaves the server
untouched. The server's `sy`, outbox and Claude folders stay; finish there with
`ssh <host> sy uninstall` (the command prints it) and section 2 below.

## 2. Each machine

Run these on every machine where `sy` is installed.

1. Stop the optional pieces:

   ```sh
   sy peers disable       # removes the peers agent service (same as sy agent disable); keys stay
   sy term off            # removes the pane, tab titles and Warp tab configs, if you set them up
   ```

2. See what `sy uninstall` will do, then do it:

   ```sh
   sy uninstall --dry-run
   sy uninstall
   ```

   It removes, from its own ledger and with a backup set:
   - the Switchyard plugin and its local marketplace from every folder;
   - the status line wrap (your original status line command comes back);
   - the timer and, if the hub runs here, the hub service;
   - the launchers `sy` wrote (`claude-2`, ...), if you have not changed them;
   - MCP server syncing between folders (the servers stay as they are).

   It keeps: every config folder, every login, every shared link and copy (your folders keep
   sharing what they share), labels, the backups, `config.toml`, the outbox and the device
   token. The hub database is never removed by `sy uninstall`.

3. To also remove Switchyard's own files on this machine:

   ```sh
   sy uninstall --purge
   ```

   This also deletes `config.toml` and the state folder: the outbox (unsent records), the
   device token, the machine id, the keys (including `owner.sealed` on the owner machine) and
   every setup backup. It asks first. Keep your owner recovery code if you might come back.

4. Remove the binary: `rm ~/.local/bin/sy` (the last line of `sy uninstall` prints it).

Alternatives:

- `sy setup --undo-list` and `sy setup --undo [<backup id>]` reverse a single setup run byte for
  byte, for example to go back to how your folders were before.
- `sy disconnect` only removes the hub URL, the token and the timer, and keeps everything else
  (queued records stay in the outbox).
- `sy account remove <folder|label>` stops managing one account folder; add `--unshare` to turn
  its shared links into independent copies.

## 3. The hub

**User service** (merged mode or a server without Docker). `sy uninstall` removes the service
`sy setup` installed. If you installed it by hand from `deploy/systemd/`:

```sh
systemctl --user disable --now switchyard-hub
rm ~/.config/systemd/user/switchyard-hub.service
systemctl --user daemon-reload
```

**Docker** (from `deploy/`):

```sh
docker compose down          # stops and removes the container; the data volume stays
docker compose down -v       # also deletes the volume: the database, backups and keys are gone
```

Take a last `sy hub backup` (or `sy hub export --no-freeze`) first if you may want the history.

The data folder (`sy paths`, `data`) holds `hub.db` and its backups; delete it by hand when you
no longer need them. If you used `tailscale serve` for the hub, turn it off with
`tailscale serve --https=443 off`.

## 4. Devices on the hub

If the hub stays and only some devices go, revoke them so their tokens stop working:

- On the board (owner sign-in): Devices → Revoke.
- On the hub machine:

  ```sh
  sy hub token list
  sy hub token revoke <id|name>
  ```

## 5. The desktop app

1. Quit it (tray menu → Quit, or Ctrl+Q / Cmd+Q).
2. Turn off Run at login in Settings first if you enabled it, or remove the autostart entry
   (Linux: `~/.config/autostart/switchyard-desktop.desktop`).
3. Remove the package the usual way for your OS (delete the AppImage, `dnf remove` / `apt remove`
   for the rpm or deb, move the app to the Trash on macOS, Apps & features on Windows).
4. Delete its user data folder to remove the account tabs' sign-ins and settings:
   `~/.config/switchyard-desktop` (Linux), `~/Library/Application Support/switchyard-desktop`
   (macOS), `%APPDATA%\switchyard-desktop` (Windows).
5. Revoke its desktop token on the board (Devices).

## 6. The Android app

1. In the app: Settings → **Unpair this phone** (this also removes it from the hub), or revoke it
   on the board under Devices.
2. Uninstall it like any app. Its token, keys and claude.ai tab sign-ins are deleted with it.

If you also used the web app on a phone, remove it from the home screen and revoke its device on
the board.
