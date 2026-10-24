# Rename

<!-- rename:keep-begin: this file names the project before and after the rename on purpose; the tool leaves it as written. -->

Status: spec v1. How the project changes its name without breaking an existing install.
Implemented in `internal/brand` (Go), `scripts/rename/` (the one-shot tool) and the
compatibility code named below. The day-of procedure is `scripts/rename/RUNBOOK.md`.

The rename: **Switchyard becomes Watchyard**, and the command **`sy` becomes `wy`**. The
version stays 1.0.0: the releases `v1.0.0` and `phone-v1.0.0` are deleted and published again
under the new name (`--reissue`, section 5), so "before" below is the first 1.0.0 build and
anything an install of it left behind.

| | Before (first 1.0.0) | After (1.0.0 re-issued and later) |
| --- | --- | --- |
| Display name, slug | `Switchyard`, `switchyard` | `Watchyard`, `watchyard` |
| Command, binary | `sy`, `sy.exe` | `wy`, `wy.exe` |
| Environment variables | `SY_*` | `WY_*` (`SY_*` still read) |
| Release archives | `sy_<v>_<os>_<arch>` | `wy_<v>_<os>_<arch>` (every release also ships `sy_` copies) |
| Go module | `github.com/mraj602/switchyard` | `github.com/mraj602/watchyard` |
| Folders | `~/.config/switchyard`, ... | `~/.config/watchyard`, ... (old ones still used) |
| Services | `switchyard-hub`, `switchyard-agent`, `switchyard-flush` (timer) | `watchyard-hub`, ... (moved) |
| Claude Code plugin | `switchyard@sy-local`, `switchyard-pane@sy-local` | `watchyard@wy-local`, `watchyard-pane@wy-local` (moved) |
| Desktop app id | `io.github.mraj602.switchyard.desktop` | `io.github.mraj602.watchyard.desktop` |
| Android application id | `io.github.mraj602.switchyard` (`.dev`) | `io.github.mraj602.watchyard` (`.dev`): a new app |

A rename is one commit made by `scripts/rename/rename.py`. Everything that carries the name
in the repository changes in that commit; what an older release already put on a person's
machines and hub keeps working, and moves to the new name on its own where that is safe.

## 1. The two names

`internal/brand` holds every name twice:

| Constant | Rewritten by the tool | Kept (`rename:keep`) |
| --- | --- | --- |
| Display name, slug | `Name`, `Slug` | `LegacyName`, `LegacySlug` |
| Command | `CLI` (and `EnvPrefix`, `Marketplace` built from it) | `LegacyCLI` |

`brand.Renamed()` and `brand.CLIRenamed()` are false while a pair is equal, and every
migration below is then a no-op. Code never spells a name for something an older install has
on disk: it asks `brand` (`Slug`, `OldSlug()`, `CLI`, `OldCLI()`, `OldEnvPrefix()`,
`LegacyMarketplace()`, `LegacyPluginIDs()`, `Legacy(s)`, `Current(s)`). Tests pretend an
earlier name with `brand.SetLegacyForTest` and `brand.SetLegacyCLIForTest`, so the migrations
are tested before the rename happens.

Outside Go, the legacy name sits in one constant per file, marked `rename:keep`, next to the
current one: `install/install.sh` and `install.ps1` (`LEGACY_REPO`, `LEGACY_CLI`),
`scripts/release/build.sh` (`LEGACY_CLI`), `phone/scripts/release-apk.sh` (`old=`),
`app/src/shared/constants.js` (`LEGACY_*`), `app/src/main/tray.js`,
`webui/static/components/notif-note.js` and `deploy/docker-compose.yml`.

A later rename sets the legacy constants to the name being left. Installs two names behind
are not migrated.

## 2. What an existing install keeps

### 2.1 Folders

The config, state and data folders (paths.md 1) are named after the slug (`Name` on
Windows). When the folder of the current name does not exist and the one of the legacy name
does, the command uses the legacy one, for each folder on its own (for config: when it holds
`config.toml`). The folders are never moved: a moved state folder would break a running
hook, the timer and a hub that has its database open. Only the defaults and the XDG
locations fall back; `WY_HOME` and `WY_<ROLE>_DIR` are used as given. `wy paths` says
"..., <old> folder from before the rename" for a legacy folder.

`wy update --machines` uploads into the remote's state folder by the same rule, so the upload
never creates an empty new state folder that would hide the remote's state.

### 2.2 Services

The hub service is `<slug>-hub` (systemd user unit `<slug>-hub.service`, LaunchAgent
`com.<slug>.hub`, Task Scheduler task `<Name> Hub`), the agent `<slug>-agent`. `Installed`
finds the unit of either name. The post-update step and `service refresh` move a unit of
the legacy name to the current one: they read its arguments and environment (variables of
the old prefix renamed, 2.13), install the unit of the current name for the current binary,
start it and remove the old one. `Install` removes a legacy unit first; `Uninstall` removes
both. The setup ledger may still name the legacy service; it is read through `brand.Current`.

The move keeps the old unit's state. Before removing it, the step asks the service manager
whether it was enabled and whether it was running (`systemctl --user is-enabled` and
`is-active`; on macOS whether the agent was loaded; on Windows whether the task was
disabled or running), then leaves the new unit the same: enabled and running gets `enable
--now`, enabled and stopped only `enable`, disabled and running only `start`, and disabled
and stopped just the unit file (written, not enabled, not started; on macOS not loaded; on
Windows created and disabled). A hub that moved to a server long before and was left
disabled therefore stays off. An answer the service manager does not give counts as on. This
holds for the hub, the agent and the flush timer (2.3); a plain `Install` (`wy setup`,
`wy agent enable`) still enables and starts. The post-update line says what the new unit was
left as ("restarted", "enabled, not started", "started, not enabled", "left disabled and
stopped").

The old unit is removed before the new one starts: both would listen on the same address.
System-wide units (a hub in `/etc/systemd/system`, a backup timer someone wrote) are not
managed by `wy`; `wy doctor` and the post-update summary name every system unit that starts
with `<old slug>-` or `<old cli>-`, and the runbook moves them by hand.

On macOS the planner wrote `dev.<slug>.hub.plist` while the service code looked for
`com.<slug>.hub.plist`. Both are now read; new installs write `com.<slug>.hub.plist`.

### 2.3 Timer

The tick timer (`<slug>-flush`, launchd `com.<slug>.flush`, task `<Name> Flush`) of the legacy
name is removed when the timer is installed or removed, and the post-update step installs the
current one with the environment the legacy unit had and, like the services (2.2), the state
it had: a legacy timer that was disabled or stopped gives a current one that is not enabled
or not started. `wy doctor` accepts either while the move is pending.

### 2.4 Claude Code plugin, pane and marketplace

The plugin is `<slug>@<cli>-local` and the pane `<slug>-pane@<cli>-local`; the local
marketplace is `<cli>-local`. `wy setup` detects an enabled `<old slug>@...` or
`<old slug>-pane@...` (from any marketplace, and an `<old slug>` skills-dir plugin) per
settings file; the plan disables the legacy ids, removes the marketplace `<old cli>-local`
from each plugin root that knows it (its folder is the same `<state>/plugin/marketplace`,
now carrying the new name), adds `<cli>-local` and installs the current plugin, with a
warning. The post-update step does the same in every plugin root the ledger records and
updates the ledger. `claude plugin list --json` has one row per install, with its `scope`
(`user`, `project` or `local`) and, for the last two, its `projectPath`. Each legacy install
is uninstalled with its own scope: `claude plugin uninstall <id> --scope=<scope>` run with
`projectPath` as the working directory (the default user scope is refused with "installed in
project scope, not user"; Claude Code treats the home folder as a project). The current
plugin is installed in the user scope afterwards. `wy setup --undo` likewise uninstalls every
install of the current plugin by its scope, and a current or legacy install in any scope
counts as present for "a root left with neither plugin". When removing an old plugin fails in any root, it rewrites nothing and asks for the
step to be run again; a root left with neither plugin (the install failed after the removal)
gets the current plugin installed on the next run, not updated. A pane lost that way comes
back with `wy setup`. `wy setup --undo` restores the legacy pointers. `wy doctor` warns while a
legacy plugin is enabled. Slash commands follow the plugin name (`/watchyard:setup`).

### 2.5 SSH forward file

`~/.ssh/<slug>.conf` and its `Include` line (machines.md 3.3). While only the legacy file
exists it keeps being used and recognised (blocks, `Include` detection, removal); nothing is
written next to it.

### 2.6 Hub export

The export manifest kind is `<slug>-hub-export`, with the version under `wy`. Import accepts the
legacy kind, and the version under `sy`, too.

### 2.7 Release attestation

`wy update`, `install.sh` and `install.ps1` verify a release with
`gh attestation verify --repo <owner>/<slug>`, and when that fails with the legacy
repository name. A release built before the rename is attested under the old repository,
a release built after it under the new one. An `sy` from before the rename verifies a later
release against `mraj602/switchyard`, which fails once the repository is renamed: run that one
update with `--no-attestation` (or `SY_VERIFY_ATTESTATION=0`) after checking the release
yourself, or reinstall with the new installer (see the runbook).

### 2.8 Warp tab configs

New tab configs are `<slug>-<label>.toml`. Files with the legacy prefix are still read,
listed by `wy term`, opened (`warp://tab_config/<old>-<label>` is on the allow-list of
terminal.md T7 only after a rename) and, when `wy term warp on` or `wy setup` writes the new
ones, moved to the backup if unchanged since they were written; an edited one is the
person's and stays, with a warning.

### 2.9 Desktop app

- User data: `<appData>/<slug>-desktop`. On the first start that finds only
  `<appData>/<old>-desktop`, the folder is renamed to the new name; if the rename fails the old
  folder is used. While the old app still runs (its `SingletonLock` in that folder names a live
  process on this host; Linux and macOS) the folder is used as it is and moved on a later start.
- Run at login on Linux: the first `get()` that finds only `<old>-desktop.desktop` in
  `~/.config/autostart` writes the current entries; every `set()` removes the legacy ones.
- The hub view's partition folder `Partitions/sy-hub` is renamed to `Partitions/wy-hub` the
  same way, so the board and owner sessions in it survive; if that fails the view signs in
  again (an owner session has to be made again).
- `<OLD>_TRAY_ICON` is read when `<NEW>_TRAY_ICON` is not set.
- The board recognises the user agent token `<Old>Desktop/` of an app not yet updated.
- The command is found as `wy`; an old `sy` on PATH is a link to it after the update (2.13).
- The Linux packages of the new name replace `<old>-desktop` (`Replaces`/`Conflicts` for
  deb, `Obsoletes`/`Conflicts` for rpm). The tool adds this block to `electron-builder.yml`
  during the rename; before it, a package would conflict with its own name.
- Not migrated: the macOS and Windows app identity is new, so the old app stays installed
  beside the new one until removed, and the operating system's key for `safeStorage` may
  differ (**uncertain**): the hub token and claude.ai sign-ins may have to be made again.

### 2.10 Phone app

The Android application id changes (section 4), so the re-issued app is a new app: it installs
beside the old one, has its own Keystore keys, settings and widgets, and is paired again. The
old app's updater never moves to it: the re-issued `phone-v1.0.0` has the same build, and a
later APK is refused (`WRONG_PACKAGE`). The new app is sideloaded once, then the old app is
uninstalled and the hub device of the old app revoked.

The hub's app mirror (phone.md P16.2) keeps a release in `<data>/app/android/<version>/` and
replaces it when the release of the same tag carries an APK with another SHA-256, on its next
pass (at `wy hub serve` start and daily); deleting that folder while the hub is stopped
makes the next start mirror again at once. The deep-link scheme, Kotlin
packages and native module names change with the app; nothing old is registered, so the two
apps never both answer a link.

### 2.11 Docker volume

`deploy/docker-compose.yml` names the volume `watchyard_hub-data` explicitly, so the hub's
data stays in the same volume under any project name. A hub started before the rename keeps its
data in `switchyard_hub-data`; the new compose file does not read it, so it is copied once while
both projects are stopped (the old volume is left as it is):

```sh
docker compose -p switchyard down    # keeps volumes
docker volume create watchyard_hub-data
docker run --rm -v switchyard_hub-data:/from:ro -v watchyard_hub-data:/to alpine cp -a /from/. /to/
docker compose up -d                 # from the new deploy/
```

The project, image and container names change as well. The compose files read `SY_BIND`, `SY_PORT`, `SY_PUBLIC_URL` and
`SY_TRUSTED_PROXY` from an older `.env` when the `WY_` name is not set
(`${WY_BIND:-${SY_BIND:-…}}`).

### 2.12 Old formats

The tool leaves alone the formats that existing installs have stored or that older releases
send (`sy_`, `sy-<word>`, JSON keys named `sy`); a hand pass after it renames them by these
rules. Code that reads an old form says so with a short legacy-compat comment, and each one
has a unit test.

**Written new, old still read.** A new release writes only the new form unless noted; every
reader accepts both, so paired devices and stored files keep working:

- Tokens: new ones are `wy_<kind>_…`. A device paired before keeps its `sy_<kind>_…` token:
  the hub stores a hash of the whole string, so it authenticates unchanged, `sy_o_` is still
  an owner token, and `wy connect`, the peers secret scan and the setup log redaction accept
  both shapes (tokens.md).
- The board cookie is `wy_session`; the hub also reads `sy_session` (when `wy_session` is
  absent) and logout clears both. The desktop app counts either as a board session.
- Link subprotocol `wy-link.1`: the hub accepts `wy-link.1` and `sy-link.1` and prefers the
  new one; `wy` and the phone offer both, new first, so they also reach an older hub (link.md 2).
- JSON version keys named `wy`, read from `sy` when `wy` is absent: the drain hello, the hub
  export manifest (2.6), `wy desktop info` (read by the desktop app), the link frame
  `client.wy`. `machine_info` carries the version under both `wy` and `sy`, because a hub
  older than the rename requires `sy` (record.md 4.8).
- `wy_version` in the API (machines, overview), the setup manifest, `<state>/machines.json`
  and `hub-admin` answers; `sy_version` is read where an older writer may have left it. The
  phone reads `sy_version` from an older hub.
- Recovery codes are printed `WYR1-…`; `SYR1-…` (and the `I`-for-`1` misreading of either)
  is still accepted. The salt and check that derive the key are unchanged (keys.md 5).
- File markers: `wy-launcher v1`, `# wy-warp-tab v1`, `# wy-forward v1` blocks and the
  `wy-pull:<id>` comment in `authorized_keys`. The `sy-` ones are still recognised as ours,
  and are replaced when the file is next written.
- The ingest headers `X-Wy-Backlog` and `X-Wy-Backlog-Oldest`: `wy` sends them under the old
  names too, which is all an older hub reads; the hub prefers the new ones (outbox.md 8).
- Encrypted hub exports are named `.wyx` (`.syx` before); import takes any name. The container
  inside keeps its `SYXE` magic and `syxe1` chunk label (frozen, below).
- The desktop hub view's partition is `persist:wy-hub`; the folder `Partitions/sy-hub` moves
  to it at start (2.9).

**Renamed outright.** Values nothing older reads back: push notification tags
(`wy-s-…`), the ntfy topic prefix of new topics (existing topics are stored and kept), the
pull SSH host alias (its config is written for every pull), the link nonce label (a key of
the running process), the web board's cache and local-storage names (old caches are
deleted by the service worker; the stored owner-session hint and open account rows are
conveniences and start empty once), Android-internal names (a new app, 2.10), and internal
identifiers and modules everywhere.

**Frozen.** These keep the old name, each with a one-line comment where it is used:

- Signed and sealed labels `sy-link-v1` (challenge, hello, frames, seal), `sy-doc-v1`,
  `sy-owner-v1`, `sy-recovery-v1`, `sy-reg-v1`, `sy-peer-v1`. They are inside signatures,
  sealed keys and key derivations that older hubs, phones and machines produce or check. A
  new label would need every verifier to try both and every signer to know which one its
  peer verifies; for hub-signed items that peer may be an older device, so the new label
  could not be used safely. The derivation inputs (owner key, recovery check) must never
  change, or existing keys would not be found again.
- The encrypted export container's `SYXE` magic and `syxe1` chunk label: an older hub must
  still import an export made by a newer one (machines.md 8.1).
- The database columns `schema_version.sy_version` and `pull_target.remote_sy`, and the
  `sy_`/`sy-` examples in comments of `schema.sql`: renaming them needs a migration, and an
  older hub refuses a database with a newer schema, which would break the rollback of the
  runbook. Code calls them `WyVersion` and `RemoteWy`, and the API says `wy_version`.
- The SSH environment variables `LC_SY_FOCUS` and `LC_SY_HOST` (terminal.md): a server's
  `AcceptEnv LC_SY_*`, which `wy doctor` asked people to add, would drop new names.
- `SWITCHYARD_DATA`, which the command asserts it never reads (paths.md), and the release
  signing certificate pin.
- With `--version` (a rename in a new release), CHANGELOG history, tags and links to earlier
  releases. With `--reissue` (this rename) the whole CHANGELOG is rewritten, because its
  releases are published again under the new name.

### 2.13 The command

- Binary: `wy` (`wy.exe`). The post-update step, run by the new binary, renames a
  binary still called `sy` to `wy` in the same folder and leaves `sy` as a symlink to it (a
  copy on Windows), so plugin hooks, status lines, launchers, units and `authorized_keys`
  forced commands written for `sy` keep working until `wy setup` rewrites them. The
  installers do the same for an `sy` they find next to the new `wy`.
- An `sy update` from before the rename looks for `sy_<v>_<os>_<arch>` archives holding `sy`:
  every release also carries those (`scripts/release/build.sh`). `SHA256SUMS` lists both; `wy`
  reads either name.
- `wy update --machines` runs `.local/bin/wy` on each machine and, when it is missing, the
  old `.local/bin/sy`, which updates itself to the requested release and is then renamed. The push path
  (a machine without internet) refuses an old machine with a message.
- Environment: every `SY_*` variable that has no `WY_*` counterpart is copied to it at start
  (`brand.MigrateEnv`), so units, shells and launchers that set `SY_HOME` and the like keep
  working. Units moved by 2.2 get the new names.
- The status line wrapper of the old name (`sy statusline ...`) is recognised as ours.
- The hub reads the version from a `client` of either name (`sy/1.0.0`, `wy/1.0.0`).
- A re-issued release has the version of the one it replaces, so `update` sees nothing newer.
  Existing machines get `wy` from the installer (which links the old `sy` to it) and then run
  the post-update steps by hand, `wy update --post-update 1.0.0`: the same steps an update
  runs (command, plugin, timer, hub and agent services, `machine_info`, doctor).

## 3. Choosing the names

Slug `[a-z][a-z0-9]{1,30}`: it becomes a Go module path element, a Kotlin-safe word, a unit
name and an environment-variable prefix (upper-cased). It must neither contain the old slug
nor be part of it: `brand.Legacy` and `brand.Current` tell old names from new by the slug
they contain. The same holds for the command name. The tool refuses both.

## 4. The Android application id

`--android-id keep` keeps it (the app updates in place, nothing is paired again);
`--android-id new` (the choice for this rename) renames it, the Kotlin packages and the scheme with
the project: a new app, as 2.10 describes.

## 5. The tool

    scripts/rename/rename.py --to-slug watchyard --to-name Watchyard --cli wy \
        --android-id new --reissue [--dry-run] [--list]

Python 3 standard library. It rewrites every tracked text file (`switchyard`, `Switchyard`,
`SWITCHYARD`; with `--cli`, the word `sy`, `sy-local` and `SY_`) and renames tracked paths
with `git mv`, runs `gofmt` on changed Go files and adds the package block of 2.9. With
`--reissue` the version stays and the whole CHANGELOG is rewritten; with `--version X.Y.Z`
instead it bumps `app/` and `phone/` and adds a changelog entry for the rename. It skips lines marked `rename:keep`
and blocks between `rename:keep-begin` and `rename:keep-end`, the formats of 2.12, changelog
history (without `--reissue`), lockfile lines other than the package's own name and version, binary files,
`node_modules`, `go.sum`, untracked files and its own folder. It refuses a dirty working
tree. `--cli-report` prints what the command rename touches without changing anything.
`make rename-test` runs its tests.

<!-- rename:keep-end -->
