# Switchyard desktop app (`app/`)

The phase 6 desktop app: one tab per claude.ai account (each in its own signed-in session),
the web usage of each account sent to your hub, and the hub board in a Hub view, with OS
notifications and a tray badge when a session needs you. Unofficial; not affiliated with
Anthropic.

It is a separate package from the root prototype (`main.js`, `shell/`, ...), which stays live
until you retire it. Both can run at the same time: they use different user data folders
(`switchyard-desktop` here, `switchyard` for the prototype) and this app never opens the
prototype's folder except to copy from it when you ask (Import, below).

Specs: `docs/spec/desktop.md` (contracts C1-C12), `docs/phases/6.md` (work packages, test plan).

## Develop

Node 22 or newer, and Go (for `sy`).

```
cd app
npm ci                 # with scripts: downloads the Electron binary
npm start              # electron .
```

The app runs `sy` for everything that touches the hub: `SY_BIN` (unpackaged only), else `sy`
on `PATH` or `~/.local/bin/sy` when it is at least `switchyard.minSy` (package.json), else the
copy bundled in the package. Without `sy` the account tabs still work and the Hub view says
how to set it up (`sy setup`).

Variables read only when the app is **unpackaged** (`main/test-hooks.js`, which the package
does not contain):

| Variable | Effect |
| --- | --- |
| `SY_APP_USER_DATA` | Absolute user data folder (tests use a temp one) |
| `SY_APP_TEST_CLAUDE_ORIGIN` | A stand-in for `https://claude.ai` (the fake in `test/fakes/claude/`) |
| `SY_APP_TEST_CADENCE_MS` | Replaces the 300 s / 60 s / 10 s usage cadences |
| `SY_BIN` | The `sy` binary to run |
| `SY_APP_TEST_EXPOSE=1` | Puts the main-process modules on `globalThis.__switchyard` for the smoke test |

The renderer's dev page runs the real renderer against a mock `sy`, in any browser:
`python3 -m http.server -d app/src`, then open
`/renderer/dev/index.html?scenario=default|empty|no_sy|needs_pairing|stopped|warning|owner`.

Layout: `src/main/` (main process; `boot.js` wires the modules in `START_ORDER`, each module
exports `create(deps)` and gets Electron and Node as arguments; only `main/index.js` imports
`electron` and only `main/sy/` imports `child_process`), `src/preload/shell.cjs` (the only
preload, the shell's `window.sy`), `src/renderer/` (the shell UI), `src/shared/` (constants,
IPC table, pure helpers used by both sides).

## Test

```
npm run lint           # node --check + the security lint (scripts/lint-security.mjs)
npm test               # unit tests, plain Node: no display, no Electron binary needed
npm run smoke          # Electron smoke S1-S12 (Linux: under xvfb-run)
npm run e2e            # the packaged AppImage end to end (after npm run dist:linux)
```

Or from the repo root: `make app-test`, `make app-smoke`, `make app-dist`, `make app-e2e`.

The smoke (`test/smoke/`) starts a real hub (`sy hub serve` on a free loopback port, built
from this repo, or `SY_BIN`), connects to it with `sy connect`, serves the fake claude.ai and
launches the app with Playwright. Everything lives in a temp folder: `HOME`, `SY_HOME`, the XDG
folders, the user data folder and a dead D-Bus address, so the real `~/.claude*`,
`~/.config/switchyard`, the keyring and your services are never touched. On Linux the app
gets `--ozone-platform=x11 --password-store=basic`; unset `WAYLAND_DISPLAY` so Electron does
not pick Wayland. `SY_SMOKE_OUT=<dir>` keeps the screenshots and logs; `SY_SMOKE_KEEP=1` keeps
the temp folder.

The e2e (`test/e2e/`) checks the packaged AppImage: the asar holds only the app's code, the
fuses are flipped (`ELECTRON_RUN_AS_NODE=1` does not run Node), the bundled `sy` is found with
no `sy` on `PATH`, and after `sy connect` the app pairs and the Hub view shows the signed-in
board. The Chromium sandbox stays on: on Ubuntu 24.04 that needs
`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`. The packaged app ignores the
test variables, so the usage read itself is covered by the smoke (S4), not here.

CI: `.github/workflows/app.yml` (unit tests on Linux, macOS and Windows; smoke; packaging; e2e).
Its `guard` job fails when a commit changes the root prototype
(`scripts/check-prototype-untouched.sh`).

## Package

```
node scripts/fetch-sy.mjs auto auto      # builds the bundled sy into resources/sy/<os>-<arch>/
npm run dist:linux                       # or dist:mac, dist:win; output in dist/
```

`fetch-sy.mjs <os> <arch> --from <dir>` copies a release build instead (checked against its
`SHA256SUMS`). Packages are never signed and never published (`electron-builder.yml`,
`publish: null`); signing is a manual step. `build/after-pack.cjs` flips the Electron fuses
(desktop.md C2). Linux targets: AppImage, deb, rpm.

## Import from the prototype

Copy-only, and only while the prototype is not running (quit it from its tray first):

- Settings, then Import from the prototype: a dry run first, then Import. The app asks to
  quit and start again so the imported tabs open signed in.
- Or from a terminal: `Switchyard --import-prototype --dry-run` prints the JSON report and
  exits; `Switchyard --import-prototype` (optionally `=<folder>`) imports and starts.
  Exit codes: 0 on a dry run, 3 when the prototype is running, 1 on any other failure.

It reads the prototype's `accounts.json` and copies each account's partition into a new tab;
it never writes the prototype's folder. Each imported tab asks for the usage warning again.
Retiring the prototype is a manual step (docs/phases/6.md "Manual: migrate and retire").

## Security model

Details and the tests that hold them: desktop.md C2 (baseline), C3 (IPC), C6 (token).

- Every window and view has `contextIsolation`, `sandbox`, no Node integration, no webview
  tag. Only the shell has a preload, and it exposes a fixed list of functions (`window.sy`);
  each IPC channel checks its sender and its arguments against `shared/ipc.js`.
- The shell is served from `app://shell/` with a strict CSP header (no inline script or
  style, no network); only files under `src/renderer/` and `src/shared/` are served.
- Account tabs live in their own `persist:tab-<id>` sessions, may navigate only to claude.ai
  and its sign-in hops over `https:`, and send other links to the system browser. The Hub view
  (`persist:sy-hub`) may navigate only to the hub's origin. Permissions are deny by default.
- Usage is read in the main process, through the tab's own session, only after you accept the
  warning for that tab, and stops at once when you turn it off or claude.ai answers 429.
- The hub token comes from `sy desktop token`, is stored encrypted with `safeStorage` in
  `hub-token.bin` (mode 0600; on Linux without a keyring, `basic_text`, and Settings warns),
  and never enters a page: main signs the Hub view in with a cookie.
- `sy` is run with `execFile` (never a shell) from an argv allow-list (`main/sy/commands.js`);
  secrets go on stdin, never argv. Nothing from the hub can make the app run anything.
- Packaged builds flip the fuses: no `ELECTRON_RUN_AS_NODE`, no `NODE_OPTIONS`, no
  `--inspect`, asar integrity on, app only from the asar.
