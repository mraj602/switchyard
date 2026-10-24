# Backups, upgrades and moving the hub

`wy paths` prints the three folders this guide refers to: **config** (`config.toml`), **state**
(machine id, device token, outbox, keys, logs, setup backups) and **data** (the hub database,
only on the hub machine). Details: [paths.md](../spec/paths.md).

## Backups

### The hub

The hub database is the long-term record. Make a consistent copy while the hub runs:

```sh
wy hub backup                          # <data>/backups/hub-<UTC time>.db
wy hub backup --to /path/to/hub.db     # or a file of your choice (it must not exist yet)
```

The copy is one complete file. It does not need the `-wal` file.

With Docker (from `deploy/`):

```sh
docker compose exec hub wy hub backup  # lands in the hub-data volume under /data/backups
```

Then copy the backup off the machine with your usual tool, or back up the whole Docker volume
`watchyard_hub-data`.
A cron job running `wy hub backup` once a day is enough for most setups.

Also keep, on the hub machine:

- `<state>/hub_sign.key`, the hub signing key. It is not in `hub.db`. Without it, a restored hub
  makes a new key, and every device that pinned the old one must re-pin
  (`wy agent enable --repin-hub`) and phones must pair again.
- `<state>/pair_pepper`. Owner sessions and pairing codes are hashed with it; without it every
  owner sign-in ends and you sign in again with `wy hub owner-link`.
- `<state>/pull/`, if the hub pulls servers over SSH.

`wy hub export` (below) packs all of these into one archive and is the simplest full backup when
you can afford to freeze the hub; use `--no-freeze` to copy without freezing.

Backups hold project names, emails, session snippets, token hashes and private keys. Store them
encrypted.

### Each machine

| Keep | Why |
| --- | --- |
| The recovery code of the owner key, on paper, offline | Rebuilds `owner.sealed` with `wy keys recover` |
| `<state>/keys/owner.sealed` (owner machine only) | Saves you typing the recovery code on a restored laptop |
| `config.toml` | Your labels, colours and settings; `wy setup` can recreate the rest |
| Your own copies of `settings.json` and `CLAUDE.md` | `wy setup` keeps its own backups under `<state>/backup/`, but those live on the same disk |

Never copy `.credentials.json`, a Keychain item or `sessions/*.key` from a Claude folder into a
backup. Log in again with `claude auth login` instead.

Losing the state folder loses unsent records and makes the machine look new to the hub; reconnect
it with `wy connect` ([install.md](install.md)).

### Restore the hub

1. Stop the hub: `systemctl --user stop watchyard-hub` (or `docker compose stop hub`).
2. Move the current `<data>/hub.db` aside, and remove `hub.db-wal` and `hub.db-shm` next to it.
3. Copy the backup to `<data>/hub.db`.
4. Make sure `<state>/hub_sign.key` and `<state>/pair_pepper` are the ones from the same hub.
5. Start the hub again and check `wy doctor` and the board.

Records sent while the hub was down wait in each machine's outbox and arrive when it is back.
Records the hub accepted after the backup was taken are lost unless the machines still have
them.

## Upgrades

### `wy` on a machine

If you installed from a release, run `wy update` ([below](#wy-update)), or run the install
script again: it replaces `wy` with the latest release. If you built from source:

```sh
cd watchyard
git pull
make build
install -m 0755 dist/wy ~/.local/bin/wy
wy version
wy doctor
```

Running sessions pick up the new binary at their next hook; nothing needs restarting. If the hub
runs on this machine as a user service, restart it. On Linux:
`systemctl --user restart watchyard-hub`. On macOS, or on either system, run
`wy desktop hub stop` and then `wy desktop hub start`. They act on the user service that
`wy setup --hub here` installed (a systemd user unit on Linux, a LaunchAgent on macOS), and exit 4
if it is not installed. The desktop app's Start and Stop buttons do the same. A stopped service
starts again at your next login.

The hub migrates its database schema at start. Before it migrates an existing database, it
writes a full copy next to it, `<data>/hub.db.pre-v<N>-<UTC time>.bak` (`N` is the new schema
version). An upgrade needs no manual step. Nothing deletes these `.bak` files; remove old ones by
hand. An older `wy` refuses a database with a newer schema, so to roll back an upgrade, restore
that `.bak` (see "Restore the hub") and start the older `wy`. A machine on an older `wy` keeps working with a newer hub; `wy doctor` says
when it should be updated.

### The hub in Docker

```sh
cd watchyard && git pull
cd deploy && docker compose up -d --build     # add -f docker-compose.tailscale.yml if you use it
docker compose ps                             # healthy within about 30 s
```

See [deploy/README.md](../../deploy/README.md).

### `wy update`

`wy` updates itself from the GitHub releases, verifying the checksum and, when the `gh` CLI is
installed, the build attestation ([security](security.md#supply-chain)). Without `gh` it says
the attestation was not checked. `--no-attestation` skips that check:

```sh
wy update --check            # what is installed and what is latest
wy update                    # update this machine (asks first; --yes to skip)
wy update --machines all     # also every machine added with wy machine add
wy update --rollback         # swap the previous binary back
wy update --version 0.2.0    # a specific version
```

Offline machines can install an archive you copied there:
`wy update --from-file wy_<ver>_linux_amd64.tar.gz --sums-file SHA256SUMS`.

### The desktop app

Download the new package from the release (or rebuild it with `make app-dist`) and install it
over the old one ([desktop.md](desktop.md)). Account tabs, sign-ins and settings live in the app's user data
folder and are kept.

### The Android app

Install the new APK over the old one. It must be signed with **the same key**; Android refuses
an update signed by another key, and you would have to uninstall (and pair again). Pairing and
settings are kept. Once releases are published, the app offers updates itself
([android.md](android.md)).

## Moving the hub

To move the hub to another computer (for example from your laptop to an always-on server):

```mermaid
sequenceDiagram
    actor You
    participant Old as Old hub
    participant New as New hub
    participant M as Machines
    You->>Old: wy hub export --moved-to https://new... --encrypt
    Note over Old: frozen: refuses new records (503)
    You->>New: copy the archive, wy hub import
    You->>New: start the hub service
    You->>M: wy machine repoint https://new... (from the laptop)
    M->>New: records resume, same tokens
```

1. On the old hub, freeze it and write an encrypted archive:

   ```sh
   wy hub export --moved-to https://hub.example.ts.net --encrypt --out ~/hub.wyx
   ```

   From now on it answers 503 to new records. Machines keep them in their outbox.
2. Copy `~/hub.wyx` to the new machine (for example with `scp`). It holds every token hash and
   key: treat it like a password.
3. On the new machine, with `wy` installed and no hub running yet:

   ```sh
   wy hub import ~/hub.wyx --public-url https://hub.example.ts.net
   ```

4. Start the hub there as a service ([install.md](install.md)), and put HTTPS in front of it.
5. Point the machines at it. From the laptop, for every machine added with `wy machine add`:

   ```sh
   wy machine repoint https://hub.example.ts.net
   ```

   On any other machine, and on the laptop itself:

   ```sh
   wy connect https://hub.example.ts.net --keep-token
   ```

6. Check `wy doctor` on each machine and the board at the new address. Delete `~/hub.wyx`.

Tokens keep working. Phones keep working if the public URL stayed the same; otherwise pair them
again. `wy doctor` on a machine whose hub is frozen prints the exact `wy connect ... --keep-token`
line. To call the move off, run `wy hub unfreeze` on the old hub.

With Docker on either side, see section E of [deploy/README.md](../../deploy/README.md).

## Pruning

The hub keeps raw records by default (roughly half a gigabyte a year at heavy use). To keep less:

```sh
wy hub prune --older-than 90d --dry-run    # count first
wy hub prune --older-than 90d
wy hub prune --before 2026-01-01
```

This deletes raw records and fine-grained rows older than the cut-off. Take a backup first.
