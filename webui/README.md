# webui

The hub's web app, embedded into `sy` with `embed.FS` (`webui.FS()`) and served by
`webui.Handler`. One SPA: the desktop board (sidebar) and the installable phone app (PWA,
tab bar Now / Sessions / Accounts / More) at the same URL. Plain ES modules and CSS, no
build step. See `doc.go` for the file layout.

## Develop

    go run ./cmd/webui-dev -dir webui/static     # mock hub on http://127.0.0.1:8788, prints a login token

`-dir` serves the files from disk so a browser refresh picks up edits; without it the
embedded copy is used. `-noauth` skips the login page. The mock implements the identity, pairing, device, push and machine
endpoints in memory. Fixed values (webui/devserver, `api.go`): `PairSecret`/`PairCode`
(open `/#/pair?s=<PairSecret>&c=482913`), `OwnerSecret` (`/#/owner?s=<OwnerSecret>`),
`PhoneToken`, `OwnerToken`. Helpers: `POST /_dev/build {"hash":"..."}` switches the build
hash (update banner), `POST /_dev/reset`, `POST /_dev/promote`.

## PWA

- `manifest.webmanifest`, icons in `static/icons/` (committed PNGs). Regenerate with
  `scripts/icons/gen.sh` (rsvg-convert, else ImageMagick with the RSVG delegate, else
  node + puppeteer-core); sources are `scripts/icons/icon.svg` and `badge.svg`.
- `sw.js` (classic script) + `sw-lib.js`: shell cache-first, navigations and data GETs
  network-first with a stored copy (`X-Sy-Cached-At`), push display, notification clicks,
  `pushsubscriptionchange`. A new worker waits until the page asks (`skip-waiting`).
- `webui.Handler` replaces `__SY_BUILD__` in sw.js with a hash of all embedded files. A Go
  test keeps the `ASSETS` list in sw.js equal to the embedded files: regenerate the list
  when adding or removing a static file.
- Every state-changing request sends `Content-Type: application/json` (the hub's CSRF rule
  for cookie requests); the mock enforces it.

## What the hub must provide

- `webui.Handler(webui.FS())` at `/` without authentication, GET/HEAD only. It sets the
  content types, `Cache-Control: no-cache`, `Service-Worker-Allowed: /` and the strict CSP
  (`webui.CSP()`: no inline script, `worker-src`/`manifest-src` self).
- `webui.UIBuild()` is the value for `/health` `ui` (the update banner compares it with the
  running build).
- The read API and the identity, pairing and device API in `docs/spec/api.yaml`; the app starts from `/v1/me`.
- HTTPS (Tailscale serve) for the service worker and push on phones.

## Tests

- `go test ./webui/...`: embed completeness, ASSETS list, Handler, fixtures, mock hub.
- `node --test webui/static/*.test.cjs`: sw-lib, sw.js in a vm, routes, platform helpers.
- Browser smoke at 390 and 1440 px (needs Chrome and puppeteer-core in a temp dir; the `web board (browser smoke)` job of `.github/workflows/ci.yml` runs it):

      mkdir -p $TMP/pp && (cd $TMP/pp && npm init -y && npm i puppeteer-core)
      SY_WEBUI_BROWSER=1 SY_PUPPETEER_MODULES=$TMP/pp/node_modules SY_WEBUI_SHOTS=$TMP/shots go test ./webui/smoke
