# Watchyard desktop app (`app/`)

The desktop app: one tab per claude.ai account (each in its own signed-in session),
the web usage of each account sent to your hub, and the hub board in a Hub view, with OS
notifications and a tray badge when a session needs you. Unofficial; not affiliated with
Anthropic.

Spec: `docs/spec/desktop.md` (contracts C1-C12).

## Develop

Node 22 or newer, and Go (for `wy`).

```
cd app
npm ci                 # with scripts: downloads the Electron binary
npm start              # electron .
```

The app runs `wy` for everything that touches the hub: `WY_BIN` (unpackaged only), else `wy`
on `PATH` or `~/.local/bin/wy` when it is at least `watchyard.minWy` (package.json), else the
copy bundled in the package. Without `wy` the account tabs still work and the Hub view says
how to set it up (`wy setup`).

Variables read only when the app is **unpackaged** (`main/test-hooks.js`, which the package
does not contain):

| Variable | Effect |
| --- | --- |
| `WY_APP_USER_DATA` | Absolute user data folder (tests use a temp one) |
| `WY_APP_TEST_CLAUDE_ORIGIN` | A stand-in for `https://claude.ai` (the fake in `test/fakes/claude/`) |
| `WY_APP_TEST_CADENCE_MS` | Replaces the 300 s / 60 s / 10 s usage cadences |
| `WY_BIN` | The `wy` binary to run |
| `WY_APP_TEST_EXPOSE=1` | Puts the main-process modules on `globalThis.__watchyard` for the smoke test |

The renderer's dev page runs the real renderer against a mock `wy`, in any browser:
`python3 -m http.server -d app/src`, then open
`/renderer/dev/index.html?scenario=default|empty|no_wy|needs_pairing|stopped|warning|owner`.

Layout: `src/main/` (main process; `boot.js` wires the modules in `START_ORDER`, each module
exports `create(deps)` and gets Electron and Node as arguments; only `main/index.js` imports
`electron` and only `main/wy/` imports `child_process`), `src/preload/shell.cjs` (the only
preload, the shell's `window.wy`), `src/renderer/` (the shell UI), `src/shared/` (constants,
IPC table, pure helpers used by both sides).

## Test

```
npm run lint           # node --check + the security lint (scripts/lint-security.mjs)
npm test               # unit tests, plain Node: no display, no Electron binary needed
npm run smoke          # Electron smoke S1-S15 (Linux: under xvfb-run)
npm run e2e            # the packaged AppImage end to end (after npm run dist:linux)
```

Or from the repo root: `make app-test`, `make app-smoke`, `make app-dist`, `make app-e2e`.

The smoke (`test/smoke/`) starts a real hub (`wy hub serve` on a free loopback port, built
from this repo, or `WY_BIN`), connects to it with `wy connect`, serves the fake claude.ai and
launches the app with Playwright. Everything lives in a temp folder: `HOME`, `WY_HOME`, the XDG
folders, the user data folder and a dead D-Bus address, so the real `~/.claude*`,
`~/.config/watchyard`, the keyring and your services are never touched. On Linux the app
gets `--ozone-platform=x11 --password-store=basic`; unset `WAYLAND_DISPLAY` so Electron does
not pick Wayland. `WY_SMOKE_OUT=<dir>` keeps the screenshots and logs; `WY_SMOKE_KEEP=1` keeps
the temp folder.

The e2e (`test/e2e/`) checks the packaged AppImage: the asar holds only the app's code, the
fuses are flipped (`ELECTRON_RUN_AS_NODE=1` does not run Node), the bundled `wy` is found with
no `wy` on `PATH`, and after `wy connect` the app pairs and the Hub view shows the signed-in
board. The Chromium sandbox stays on: on Ubuntu 24.04 that needs
`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`. The packaged app ignores the
test variables, so the usage read itself is covered by the smoke, not here.

CI: `.github/workflows/app.yml` (unit tests on Linux, macOS and Windows; smoke; packaging; e2e).

## Package

```
node scripts/fetch-wy.mjs auto auto      # builds the bundled wy into resources/wy/<os>-<arch>/
npm run dist:linux                       # or dist:mac, dist:win; output in dist/
```

`fetch-wy.mjs <os> <arch> --from <dir>` copies a release build instead (checked against its
`SHA256SUMS`). Packages are never signed and never published (`electron-builder.yml`,
`publish: null`); signing is a manual step. `build/after-pack.cjs` flips the Electron fuses
(desktop.md C2). Linux targets: AppImage, deb, rpm.

## Security model

Details and the tests that hold them: desktop.md C2 (baseline), C3 (IPC), C6 (token).

- Every window and view has `contextIsolation`, `sandbox`, no Node integration, no webview
  tag. Only the shell has a preload, and it exposes a fixed list of functions (`window.wy`);
  each IPC channel checks its sender and its arguments against `shared/ipc.js`.
- The shell is served from `app://shell/` with a strict CSP header (no inline script or
  style, no network); only files under `src/renderer/` and `src/shared/` are served.
- Account tabs live in their own `persist:tab-<id>` sessions, may navigate only to claude.ai
  and its sign-in hops over `https:`, and send other links to the system browser. The Hub view
  (`persist:wy-hub`) may navigate only to the hub's origin. Permissions are deny by default.
- Usage is read in the main process, through the tab's own session, only after you accept the
  warning for that tab, and stops at once when you turn it off or claude.ai answers 429.
- The hub token comes from `wy desktop token`, is stored encrypted with `safeStorage` in
  `hub-token.bin` (mode 0600; on Linux without a keyring, `basic_text`, and Settings warns),
  and never enters a page: main signs the Hub view in with a cookie.
- `wy` is run with `execFile` (never a shell) from an argv allow-list (`main/wy/commands.js`);
  secrets go on stdin, never argv. Nothing from the hub can make the app run anything.
- Packaged builds flip the fuses: no `ELECTRON_RUN_AS_NODE`, no `NODE_OPTIONS`, no
  `--inspect`, asar integrity on, app only from the asar.
