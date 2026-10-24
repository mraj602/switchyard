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

On macOS the planner wrote `dev.<slug>.hub.plist` while the service code looked for
`com.<slug>.hub.plist`. Both are now read; new installs write `com.<slug>.hub.plist`.

### 2.3 Timer

The tick timer (`<slug>-flush`, launchd `com.<slug>.flush`, task `<Name> Flush`) of the legacy
name is removed when the timer is installed or removed, and the post-update step installs the
current one with the environment the legacy unit had. `wy doctor` accepts either while the
move is pending.

### 2.4 Claude Code plugin, pane and marketplace

The plugin is `<slug>@<cli>-local` and the pane `<slug>-pane@<cli>-local`; the local
marketplace is `<cli>-local`. `wy setup` detects an enabled `<old slug>@...` or
`<old slug>-pane@...` (from any marketplace, and an `<old slug>` skills-dir plugin) per
settings file; the plan disables the legacy ids, removes the marketplace `<old cli>-local`
from each plugin root that knows it (its folder is the same `<state>/plugin/marketplace`,
now carrying the new name), adds `<cli>-local` and installs the current plugin, with a
warning. The post-update step does the same in every plugin root the ledger records and
updates the ledger. `wy setup --undo` restores the legacy pointers. `wy doctor` warns while a
legacy plugin is enabled. Slash commands follow the plugin name (`/watchyard:setup`).

### 2.5 SSH forward file

`~/.ssh/<slug>.conf` and its `Include` line (machines.md 3.3). While only the legacy file
exists it keeps being used and recognised (blocks, `Include` detection, removal); nothing is
written next to it.

### 2.6 Hub export

The export manifest kind is `<slug>-hub-export`. Import accepts the legacy kind too.

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
  `<appData>/<old>-desktop`, the folder is renamed to the new name (the app is not running
  then); if the rename fails the old folder is used.
- Run at login on Linux: the first `get()` that finds only `<old>-desktop.desktop` in
  `~/.config/autostart` writes the current entries; every `set()` removes the legacy ones.
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

`deploy/docker-compose.yml` names the volume `switchyard_hub-data` explicitly, so the hub's
data stays in the same volume under any project name. The project, image and container names
change: stop the old project (`docker compose -p switchyard down`, which keeps volumes) before
starting the new one.

### 2.12 Unchanged on purpose

Formats that existing installs have stored or that older releases send:

- Token prefixes `sy_<kind>_` (hashed on the hub, held by every device) and the cookie
  `sy_session`.
- Signed-protocol labels (`sy-link-v1`, `sy-doc-v1`, `sy-owner-v1`, `sy-reg-v1`,
  `sy-peer-v1`, ...): they are inside signatures, sealed keys and certificates.
- File markers written into the person's files (`sy-launcher v1`, `sy-warp-tab v1`, ...):
  they are how a file is recognised as ours.
- JSON keys named `sy` (the version in `machine_info`, the drain hello, link frames, the
  hub export manifest, the desktop command's output).
- Internal module names with an `sy-` prefix (`phone/modules/sy-link`, ...) and identifiers
  such as `SyLinkModule`: invisible to users.
- `SWITCHYARD_DATA`, which the command asserts it never reads (paths.md).
- The release signing certificate pin.
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
