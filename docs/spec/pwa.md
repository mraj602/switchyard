# The installable web app (phone and browser)

Status: spec v1, phase 4. Implemented in `webui/` (static files, `webui/serve.go`),
`webui/devserver` (mock hub), `webui/smoke` (browser tests), `scripts/icons/`. Pairing is in
[pairing.md](pairing.md), alerts and the push payload in [push.md](push.md). Plan sections 11
and 12; screens `PhoneNow`, `PhoneSessions`, `PhoneSession`, `PhoneAccounts`, `PhoneAlerts`,
`PhonePair`, `PhoneLock`, `DeskAlerts` in `docs/design/screens/`.

## 1. One app, not two

The phone app is the phase 2 board, extended: the same single-page app, the same ES modules,
no build step, served by the hub at `/`. CSS already switches between a sidebar (desktop) and
bottom tabs (phone). Phase 4 adds an install manifest, a service worker, pairing, push
registration and the More / Alerts / Settings screens. Reasons: one code path for the data
(store.js, the stream, the read API), screens that already match the designs, and nothing to
publish. The phase 8 Expo app is the separate native app.

**Requirement:** install and Web Push need a secure context, so the phone must open the hub at
an `https://` address with a publicly trusted certificate (`tailscale serve --bg 8787` gives
`https://<host>.<tailnet>.ts.net`; Caddy on a VPS). The plain-HTTP tailnet bind in
`deploy/docker-compose.tailscale.yml` still works as a board but cannot install or push; the
deploy README and `sy doctor` (phase 4 check) say so.

## 2. Manifest and icons

`/manifest.webmanifest` (served as `application/manifest+json`):

```json
{
  "id": "/",
  "name": "Switchyard",
  "short_name": "Switchyard",
  "description": "Your Claude sessions and limits. Unofficial; not affiliated with Anthropic.",
  "start_url": "/#/",
  "scope": "/",
  "display": "standalone",
  "orientation": "portrait",
  "background_color": "#17191B",
  "theme_color": "#17191B",
  "icons": [
    {"src": "icons/icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
    {"src": "icons/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": "icons/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
    {"src": "icons/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"}
  ],
  "shortcuts": [
    {"name": "Needs you", "url": "/#/", "icons": [{"src": "icons/icon-192.png", "sizes": "192x192"}]},
    {"name": "Sessions", "url": "/#/sessions", "icons": [{"src": "icons/icon-192.png", "sizes": "192x192"}]}
  ]
}
```

`index.html` adds: `<link rel="manifest" href="manifest.webmanifest">`,
`<link rel="apple-touch-icon" href="icons/apple-touch-icon-180.png">`,
`<meta name="theme-color" content="#F3F4F1" media="(prefers-color-scheme: light)">` and the
`#17191B` dark one, `<meta name="apple-mobile-web-app-title" content="Switchyard">`,
`<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">` (the
layout already uses `viewport-fit=cover`; tabs pad with `env(safe-area-inset-bottom)`).

**Icons** come from the logo (`components/nav.js`: two nested chevrons, outer `M3 18 L11 4
L19 18`, inner `M7 18 L11 11 L15 18` in a 22-unit box, stroke 2): both chevrons amber
`#E8B04B` on `#17191B`. Source `scripts/icons/icon.svg`:

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
  <rect width="512" height="512" fill="#17191B"/>
  <g transform="translate(80 80) scale(16)" fill="none" stroke="#E8B04B" stroke-width="2"
     stroke-linejoin="miter" stroke-linecap="butt">
    <path d="M3 18 L11 4 L19 18"/>
    <path d="M7 18 L11 11 L15 18"/>
  </g>
</svg>
```

At scale 16 the drawing (apex miter included) stays within 193 px of the centre, inside the
205 px safe circle of a maskable icon, so the same art serves `any` and `maskable`.
`scripts/icons/gen.sh` renders, with `rsvg-convert` (librsvg) or, if absent, headless Chrome
through the smoke-test puppeteer setup: `icon-192.png`, `icon-512.png`, `maskable-512.png`,
`apple-touch-icon-180.png` (opaque, square: iOS rounds it), and `badge-96.png` (the chevrons
white on transparent, for Android's monochrome status-bar `badge`). The PNGs are committed
under `webui/static/icons/`; a Go test checks each exists with the right size
(`image.DecodeConfig`) so no tool is needed at build time. The favicon stays the inline SVG.

## 3. Routes and screens

Hash routes (phase 2 had `#/`, `#/sessions`, `#/sessions/<id>`, `#/accounts`, `#/machines`,
`#/login`):

| Route | Screen (design) | Phase 4 change |
| --- | --- | --- |
| `#/` | Now (`PhoneNow`) | Rows get **Seen** and **Snooze** (swipe left/right on touch; two buttons in a row menu for keyboard and screen readers); seen rows dimmed; alert status ("push in 40 s", "pushed 14:04", "snoozed until 14:20"); offline banner (4) |
| `#/sessions`, `?filter=needs_you|working|done|all`, `?by=machine` | Sessions (`PhoneSessions`) | Filter chips and "By machine" grouping as drawn |
| `#/sessions/<id>?alert=<al>` | Session (`PhoneSession`) | **Open in Claude** (`remote_url`, with the wrong-account note), **Copy resume command**, **Snooze 15 min** (alert), state history |
| `#/accounts?seat=<id>` | Accounts (`PhoneAccounts`) | "Most room right now" line; `seat` scrolls to and highlights a card |
| `#/machines?machine=<id>` | Machines | Under More on phones; stays in the desktop sidebar |
| `#/more` | More (new) | Links: Alerts, Machines, Settings; owner only: Devices, Add phone; Analytics listed as "coming" (its API is still reserved) |
| `#/more/alerts` | Alerts and rules (`PhoneAlerts`) | Rule list (5 below), quiet hours, recent pushes from `GET /v1/alerts` with outcome (open, seen, snoozed, cancelled + the "cancelled means you answered first" note) |
| `#/more/settings` | Settings | Hub address, version, live status; notifications (6); private pushes, tap behaviour, own quiet hours (device prefs); ntfy topic (push.md 7); install status; app version + "Check for update"; **Unpair this phone** |
| `#/more/devices` | Devices (owner; `DeskAlerts` "Paired phones") | `GET /v1/devices`, rename, revoke, test push per target, Add phone / Add machine (QR dialog, pairing.md 2.4) |
| `#/pair?s=…&c=…` | Pairing (`PhonePair`) | pairing.md 5 |
| `#/owner?s=…` | Owner sign-in | Posts `/v1/session/owner`, then `#/more/settings` |
| `#/alerts/<id>` | — | Loads `GET /v1/alerts/<id>` and replaces the route with the alert's deep link (push.md 5.8) |
| `#/login` | Sign in | Unchanged token login; adds "Pair with a code instead" |

Navigation: phone bottom tabs **Now, Sessions, Accounts, More** (plan section 12); desktop
sidebar Now, Sessions, Accounts, Machines, Alerts, Settings. `nav.js` gets a `phone`/`desk`
flag per item; CSS decides as today.

Start-up: `GET /v1/me`. 401 → pair screen when running installed (`display-mode:
standalone`) or on a phone, else the login screen. A `read`-only board session shows every
screen; controls that need `push` or `owner` are hidden or show "Sign in as owner on the hub
(`sy hub owner-link`)".

Rules on the phone (`PhoneAlerts`): one row per base trigger with its global delay as text
("· 30 s") and a switch. For a push-scoped device the switch edits `prefs.mute`
(`PATCH /v1/devices/self`); for an owner session it edits the global rule (`PUT
/v1/rules/{id}`), and the delay is editable. Overrides are listed (owner: add/remove with a
seat/project picker and on/off + delay).

## 4. Service worker and offline

`/sw.js`, a classic script (module service workers are not reliable in every browser the
app targets), scope `/`. Logic that can be tested without a browser lives in `/sw-lib.js`,
loaded with `importScripts` and also loaded by `node --test` through `vm`
(`webui/static/sw-lib.test.cjs`).

**Versioning.** `sw.js` contains the literal `__SY_BUILD__`. `webui.Handler()`
(`webui/serve.go`) replaces it, when serving, with the first 12 hex digits of SHA-256 over
all embedded file names and contents (computed once at start), and serves it with
`Content-Type: text/javascript` and `Cache-Control: no-cache`. Any change to any file
therefore changes `sw.js`, which is how browsers notice an update. `/health` gains
`ui` (the same hash).

**Caches.**

| Requests | Strategy | Cache |
| --- | --- | --- |
| App shell: every file in `ASSETS` (a constant list in `sw.js`; a Go test checks it equals the embedded files minus tests and dev files) | Precached on `install`; served cache-first | `sy-shell-<build>` |
| Navigations (`/`, `/index.html`) | Network first, 4 s timeout, then the cached `index.html` | shell |
| `GET` of `/v1/me`, `/v1/overview`, `/v1/sessions`, `/v1/sessions/<id>` (last 20), `/v1/accounts`, `/v1/machines`, `/v1/alerts`, `/v1/rules`, `/v1/devices/self`, `/v1/push/config` | Network first, 6 s timeout; a 200 is stored; on failure the stored copy is returned with header `X-Sy-Cached-At: <ms>`; nothing stored → 503 JSON | `sy-data` |
| `/v1/stream`, every non-GET, `/v1/actions/*`, cross-origin (Google Fonts) | Not intercepted (`respondWith` is not called) | — |

- `api.js` sees `X-Sy-Cached-At` and sets `store.offline = {since}`; the shell shows
  "Offline · showing 14:02" and disables Seen/Snooze/test buttons. The stream's existing
  backoff keeps retrying; on the first success the banner goes and everything refetches.
- A 401 from any API call makes the page post `{type: "clear-data"}` to the worker, which
  deletes `sy-data`; unpair and logout do the same. Cached data is the only copy of hub data
  on the phone, and it holds no token.
- `activate` deletes caches whose names are not the current shell or `sy-data`, then
  `clients.claim()`, so the first visit is already offline-capable.

**Update flow.**

1. The browser fetches `sw.js` on navigation; the page also calls `registration.update()` on
   `visibilitychange` → visible when the last check is over an hour old (an installed iOS app
   is often resumed, not reloaded).
2. A new worker installs (precaches the new shell) and waits; it does **not** call
   `skipWaiting` on its own, so a running page never mixes old and new files.
3. The page sees `registration.waiting` and shows "New version ready · Reload". Tapping it
   posts `{type: "skip-waiting"}`; on `controllerchange` the page reloads once.
4. If `/health`'s `ui` differs from the running build and no worker is waiting (a browser that
   checked too early), the banner offers "Reload" too.

## 5. Notifications in the service worker

`push` event: parse the payload (push.md 6.5); if it does not parse, show "Switchyard ·
something needs you" (iOS revokes subscriptions that receive pushes without a notification,
and Chrome shows a generic one). Then:

```js
registration.showNotification(p.title, {
  body: p.body, tag: p.tag, renotify: true, timestamp: Date.parse(p.ts),
  icon: 'icons/icon-192.png', badge: 'icons/badge-96.png',
  data: p,                                         // the whole payload, for clicks
  actions: p.actions ? [{action: 'snooze', title: 'Snooze 15 min'}, {action: 'seen', title: 'Seen'}] : [],
  requireInteraction: false,
});
if (navigator.setAppBadge) navigator.setAppBadge(p.needs_you || 0);   // 0 clears
```

Open windows get `postMessage({type: "push", payload})` so the Now screen refreshes at once.

`notificationclick`:

- `snooze` / `seen`: `fetch('/v1/actions/' + token, {method: 'POST', headers: {'Content-Type':
  'application/json'}, body: '{}'})`; close the notification either way (no retry: if the hub
  is unreachable the alert simply is not snoozed, and the Now screen offers it again).
- Body tap: choose the URL with `chooseTarget(payload, reachable)` from `sw-lib.js`:
  `tap = claude` and a `remote_url` → the Remote Control link; `tap = auto` and a
  `remote_url` → `GET /health` with a 1.5 s timeout, the link if the hub does not answer,
  else the deep link; otherwise the deep link. Then focus an existing app window and
  `navigate` it (or post the route), else `clients.openWindow`.
- **Uncertain:** whether `clients.openWindow` with a `claude.ai` link from an installed iOS app
  hands off to the Claude app (universal link) or opens Safari; the manual checklist covers it.

`pushsubscriptionchange` (Chrome, Firefox; Safari does not fire it): subscribe again with the
same VAPID key and `POST /v1/push/targets`.

## 6. Turning notifications on

Settings → Notifications shows one of: "Not supported here", "Install first" (iOS, not
standalone), "Off" with **Turn on**, "Blocked in system settings" (permission `denied`), or
"On · <n> targets" with **Send test push**.

**Turn on** (always inside a tap handler, required by iOS):

1. `Notification.requestPermission()`; anything but `granted` stops with the matching state.
2. `GET /v1/push/config` → `vapid_public_key`.
3. `registration.pushManager.subscribe({userVisibleOnly: true, applicationServerKey})`. If an
   existing subscription has a different key (the hub moved and made new keys), unsubscribe
   it first.
4. `POST /v1/push/targets {kind: "webpush", endpoint, keys: {p256dh, auth}, name}` and
   `PATCH /v1/devices/self {prefs: {tz}}`.
5. Offer **Send test push** (`POST /v1/push/targets/{id}/test`), showing the hub's result
   (sent, or the push service's error).

On every start (installed, permission granted): `pushManager.getSubscription()`; if missing,
or `GET /v1/devices/self` shows no live webpush target with that endpoint (the hub marked it
gone, push.md 5.6), repeat steps 2–4 silently.

## 7. Installing

| Platform | Install | Push | Notes |
| --- | --- | --- | --- |
| iPhone / iPad, iOS 16.4+ | Safari → Share → **Add to Home Screen** (iOS 16.4+ also from other browsers' share sheets) | Only in the installed app; permission only from a tap inside it | No notification action buttons (Snooze/Seen live in the app); badge supported; Focus modes apply. Plan section 11 said "reportedly not in the EU": Apple withdrew that change before iOS 17.4 shipped (March 2024), so EU users should work — **uncertain**, to be confirmed on a device. iOS 16.3 and older: the app works as a board, Settings says "needs iOS 16.4 for notifications", ntfy is offered instead |
| Android, Chrome | The page keeps `beforeinstallprompt` and shows **Install app** in the pair and Settings screens; also Chrome menu → Install app | In the browser or installed (WebAPK); shares Chrome's cookies | Two action buttons shown; links into the hub's scope open in the installed app |
| Android, Firefox / Samsung Internet | Browser menu | Works in principle | Not tested in phase 4 |
| Desktop Chrome / Edge / Safari 17+ | Install icon in the address bar / File → Add to Dock | Yes (`browser` pairing code) | Optional; the desktop app (phase 6) is the main desktop path |

The pair page and Settings show the right instructions for the platform they detect
(`navigator.userAgentData`, else the user agent string; `matchMedia('(display-mode:
standalone)')` and `navigator.standalone` for "already installed").

## 8. Hub side

- `webui.Handler(fs.FS) http.Handler` (new, `webui/serve.go`) replaces the bare
  `http.FileServer` in `internal/hub/api/server.go`: content types for `.webmanifest`,
  `sw.js` templating, `Cache-Control: no-cache` everywhere (files are not fingerprinted) and
  the existing `nosniff` / `no-referrer` headers.
- No Content-Security-Policy is added in phase 4 (the app needs none to work; adding one is a
  pre-release item together with self-hosting the fonts).

## 9. Tests

- `go test ./webui/...`: every file the app references is embedded (extend the phase 2
  test to `manifest.webmanifest`, `sw.js`, `sw-lib.js`, icons); `ASSETS` equals the embedded
  file list; icon sizes; `sw.js` is served with the build hash and no-cache; the devserver's
  new fixtures validate against `api.yaml`.
- `node --test webui/static/*.test.cjs` (Node 20+, no packages): `chooseTarget`, payload
  parsing fallbacks, the data-cache URL matcher.
- Headless Chrome (extends `webui/smoke`, `SY_WEBUI_BROWSER=1`, puppeteer-core), against the
  devserver at `http://127.0.0.1` (a secure context):
  - manifest: CDP `Page.getAppManifest` has no errors; `Page.getInstallabilityErrors` is
    empty;
  - the worker registers and controls the page after one reload; `caches.keys()` has the
    shell cache;
  - offline: `page.setOfflineMode(true)` then reload → Now renders from `sy-data` with the
    offline banner; back online → banner gone;
  - update: the devserver switches to another build hash → "New version ready" → click →
    reloaded page runs the new build;
  - pairing: open `/#/pair?s=<devserver secret>` with a phone viewport and an Android UA →
    claim → cookie set → Now renders; with an iPhone UA → no claim, instructions with the
    digits;
  - notifications: `browserContext.overridePermissions(origin, ['notifications'])`. Real
    `pushManager.subscribe` needs Google's push service and usually fails in headless or
    sandboxed CI (`AbortError`), so the smoke test stubs `PushManager.prototype.subscribe`
    (`evaluateOnNewDocument`) to return a subscription whose keys the test generated, and
    checks the app posts it to `/v1/push/targets`. A real subscribe is attempted once and
    only logged;
  - push display: CDP `ServiceWorker.deliverPushMessage` with a payload from push.md 6.5,
    then `registration.getNotifications()` shows the title, tag and actions, and the badge
    call was made (spied).
  - Not testable headless: tapping a notification, real delivery through a push service,
    iOS. Those are in the manual checklist (phases/4.md).
