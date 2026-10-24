# Changelog

All notable changes to Watchyard are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- The move from the Switchyard names (`wy update --post-update`) keeps the old units' state: a
  disabled or stopped hub, agent or flush timer stays disabled or stopped instead of being
  enabled and started, and a legacy plugin installed at project or local scope is uninstalled
  with its own `--scope` and project folder instead of failing.

## [1.0.0] - 2026-10-24

The first public version. Everything below is new.

### Added

- `wy`, one static Go binary for Linux, macOS and Windows (Windows without peers and the owner
  key): hooks and status line recording into a local outbox, `wy status`, `wy doctor`,
  `wy update`.
- `wy setup`: detects Claude Code config folders, labels and colours accounts, makes them share
  settings, instructions, add-ons, plugins, MCP servers, history and prompts, adds launchers,
  and installs the Claude Code plugin. Shows a plan first, backs up every change, and
  `wy setup --undo` reverses a run.
- The hub (`wy hub serve`): SQLite storage of every record, session states, usage windows per
  account, analytics, a read API and a live stream, pairing and per-device tokens, backups,
  export and import. Runs as a user service, with Docker or with systemd.
- The board: a web UI served by the hub that installs on a phone as a web app, `wy board` in a
  terminal, and a pane inside Claude Code.
- Alerts for sessions that need you, failures, limit thresholds and silent machines, through
  Web Push, ntfy and the Android app, with quiet hours, private notifications, snooze and
  batching.
- More machines through direct connection, an SSH port forward, or pull over SSH.
- The desktop app (Linux, macOS, Windows): one claude.ai tab per account, opt-in usage reading
  per account, the board in a Hub view, OS notifications and a tray.
- The Android app: the board's screens, notifications over its own connection to the hub (no
  third-party push service), Open, Snooze and Seen, six home-screen widgets, and an updater.
- Keys and peers: an owner key, machine certificates, a signed link between devices and the
  hub, and end-to-end encrypted messages between your own Claude Code sessions, gated by a
  peers matrix.
- The Android app has no proprietary Google code (no Firebase, Play services or ML Kit); CI
  checks the built APK. Package name `io.github.mraj602.watchyard`.
- The desktop app pins a self-signed hub certificate on first use, warns when a hub on another
  machine is plain http, limits the frames inside account tabs to an allow-list, and shows when
  `wy` or the hub is newer. Release packages are unsigned but carry build-provenance
  attestations.
- Sessions that coordinate with each other through peers don't send "asked" alerts; permission
  prompts and real questions still do (`skip_peers`, on by default).
- Documentation: user guides, architecture, behaviour specs, contributing guide, security
  policy, release guide, a website and a demo recorded from sample data.
- Licensed under the GNU AGPL-3.0.

### Security

- Desktop: the tabs' sign-in cookies are encrypted on disk (Electron's cookie encryption fuse is
  on). A tab signed in by an earlier unpacked build may ask you to sign in again once.
- The hub's Android app mirror pins the release signing certificate and checks the APK itself,
  not only the manifest. It mirrors nothing until the maintainer sets the pin.
- Android: no app data in cloud backup or device transfer, lock-screen notifications show only a
  generic text, widget taps open the app with an explicit intent, and the phone no longer sends
  its token to the `moved_to` address in an unsigned `/health` reply (it asks you to pair again).
  `make phone-apk-release` refuses a wrong package id, cleartext traffic or a debuggable build.
- Desktop: a page can trigger only a few external opens or session links in a short time, and
  never brings the window or the browser to the front. Usage read from the page's own
  context is used only for a known account, and usage data is size- and shape-checked first.
- Desktop: sites that ask for a client certificate get none.
- The web board's "Open in app" link is now an Android intent pinned to the Watchyard package,
  so no other app can receive the pairing secret.

### Fixed during the release candidate

- The web board's sign-in page no longer loses a half-typed token or owner link when the page
  updates in the background (for example while the offline app installs).
- Desktop: signing in to claude.ai in an account tab works again. The tabs blocked the frames of
  the sign-in page's hCaptcha check, so it failed with "Couldn't verify your browser".
- Desktop: account tabs send Chrome's reduced user agent (`Chrome/<major>.0.0.0`), as Chrome
  itself does, instead of the full build number. claude.ai's Claude Code section kept asking to
  verify the device again, because the string was unusual and changed with every Electron update.

### Renamed from Switchyard (`sy`)

- Every name is now Watchyard and `wy`: tokens (`wy_…`), the board cookie (`wy_session`), the
  link subprotocol (`wy-link.1`), JSON version keys (`wy`, `wy_version`), recovery codes
  (`WYR1-…`), file markers, ingest headers (`X-Wy-Backlog…`), encrypted exports (`.wyx`) and
  internal names. Devices paired and files written by the first 1.0.0 build keep working: the
  old forms are still accepted, and `wy` also sends what an older hub reads. Signed labels, two
  database columns and the `LC_SY_*` SSH variables keep their old names on purpose
  (docs/spec/rename.md 2.12).
- Also carried over: `SY_*` environment variables (read as `WY_*`), Docker `.env` files with
  `SY_*` names, a status line still wrapped by `sy`, `sy.old` for `wy update --rollback`, and
  a plugin move that failed half-way. `wy doctor` names system units of the old name, which
  are moved by hand (scripts/rename/RUNBOOK.md); on Windows the update prints the commands that
  replace the old scheduled task.
- The Docker volume is now `watchyard_hub-data`. A hub started with the first build keeps its
  data in `switchyard_hub-data`: copy it once with the commands in docs/spec/rename.md 2.11.

[Unreleased]: https://github.com/mraj602/watchyard/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/mraj602/watchyard/releases/tag/v1.0.0
