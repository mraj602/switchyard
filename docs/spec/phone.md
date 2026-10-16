# Phone app

Status: spec v1, phase 8. Implemented in `phone/` (Expo SDK 57, React Native). The work split,
tests, decisions ("decision N"), risks ("risk N") and manual steps are in
[phases/8.md](../phases/8.md); the screens are `docs/design/screens/Phone*.dc.html`. Unofficial;
not affiliated with Anthropic.

The phone app is a `phone` device (tokens.md 3: scopes `read` and `push`) with the PWA's
screens, native notifications with **Open**, **Snooze** and **Seen** (Android, over the app's
own link to the hub), home-screen widgets on iOS and Android, and an iOS Lock Screen countdown
to a limit reset. It talks only to its hub's existing API; the hub side (the link channel and
widget refresh frames) is [push.md 13](push.md#13-the-link-channel-n1).

Related: pairing.md 2.2, 3.1 (the QR, `cookie: false`), tokens.md 3 (`phone` scopes),
push.md 5.4 (`prefs`), 5.5 (texts), 5.8 (deep links), 6.5 (the payload), 8 (action tokens),
13 (the link channel), pwa.md 3 and 5 (screens and the service worker's tap rule), api.yaml
(`DevicePrefs.widgets`, `PushSettings.refresh_min`), schema.sql v11.

Section numbers follow phases/8.md's contract: P8 to P10 (the hub side) were push.md 12, which
is removed; the hub side of widget refresh is push.md 13.5.

**Notifications (N0, built in N1/N2; plan addendum C).** Android only. Native notifications
come over the app's own link to the hub (push.md 13, the `sy-link` module of push.md 14); the
app uses no push service, Expo's, Google's or any other. P2 adds keys to the claim, P6.3
registers nothing with the hub (the link target comes from pairing), P6.7 is the Settings →
Notifications screen, P13 has no EAS or Firebase config and `minSdkVersion` is 31, and P16 is
the in-app updater. iOS sections (P6.6's iOS badge, P12, the iOS widgets of P11) stay in the
code and are not worked on; iOS gets no native notifications (use Web Push in the browser).

Code map (wave 0): `phone/src/contract/` is the TypeScript side of this spec:

| File | Holds |
| --- | --- |
| `api.ts` | Mirrors of the api.yaml schemas the app reads or sends (`Overview`, `Session`, `Seat`, `Window`, `AlertBrief`, `Alert`, `Me`, `Device`, `DevicePrefs`, `PushTarget`, `PairClaim`, `PairClaimReply`, `PushSettings`, `ActionReply`, …) |
| `payload.ts` | `Payload` (exactly Go `model.Payload`, `PAYLOAD_KEYS` in Go's key order), `RefreshData`, `AndroidChannelId` |
| `snapshot.ts` | `WidgetSnapshot` v1 (P11), `TimelineEntry`, `SNAPSHOT_MAX_SEATS` 4, `SNAPSHOT_LABEL_MAX` 12, `TIMELINE_MAX` 8 |
| `live.ts` | `LiveCountdownProps` (P12), `LIVE_MAX_H` 8, `LIVE_MIN_PCT` 80, `LIVE_END_AFTER_MS`, `LIVE_ACTIVITY_NAME` |
| `routes.ts` | `SCHEME`, `AppRoute`, route paths, the hash routes and incoming hosts of P7 |
| `constants.ts` | Storage keys (`sy.hub.v1`, `sy.cache.v1`, `sy.widget.v1`, `sy.local.v1`), category and action ids, channels (P6.1), timeouts, the `ACTION_TOKEN` pattern |
| `platform.ts` | The service interfaces `src/lib` takes as arguments (`FetchLike`, `Clock`, `KVStore`, `SecureStoreLike`, `AppStateSource`, `AppInfo`) |
| `validate.ts` | Runtime validators for all of the above (empty list = valid), used by the contract test and by the app on data it did not make |

Each `src/lib/<module>/index.ts` holds the exported signatures of its wave 1 owner, throwing
`"<module>.<fn>: not implemented (phase 8)"` until implemented.

## P1. The app: layout and versions

```
phone/
  package.json  package-lock.json  app.config.ts  tsconfig.json
  babel.config.js  jest.config.js  eslint.config.js  .gitignore  README.md
  app/                      expo-router routes (P4); thin screens only
  src/contract/             TypeScript mirrors of hub JSON (api.ts, payload.ts), WidgetSnapshot,
                            LiveCountdownProps, route constants. Wave 0; changes go through WP-0's owner
  src/lib/hub/              hub client: base URL, bearer, timeouts, errors, endpoints
  src/lib/pair/             QR/typed input parsing, claim
  src/lib/secure/           keychain wrapper (SecureStore behind an interface + in-memory fake)
  src/lib/data/             poller, last-known cache (AsyncStorage, never secrets), reducer
  src/lib/notify/           channels, categories, response decisions, link frames
  src/lib/links/            hash route ↔ app route, incoming URL allow-list
  src/lib/format/           ages, reset times, labels, state words (port of webui format.js)
  src/lib/widget/           WidgetSnapshot builder, timeline, Android render props
  src/lib/live/             countdown choice, Live Activity props, end policy
  src/lib/integrate/        wave 2 glue (pure): after-refresh widget/live/badge steps, registration
                            on pairing and start, link `refresh` frames, auto countdown
  src/state/                provider (the one source of truth), notification bridge, and glue.tsx:
                            binds the real modules and derives the screens' HubApi from useHub()
  src/components/           shared UI (rows, rings, chips, banners)
  widgets/ios/              expo-widgets components ('widget' directive): NeedsYouSmall,
                            AccountsMedium, ResetCountdown (Live Activity)
  widgets/android/          react-native-android-widget components + task handler
  plugins/                  local config plugins (with-sy-native.js)
  scripts/                  check-no-secrets.mjs, prebuild-check.mjs, dump-fixtures.sh
  test/                     setup.ts (installs the mocks), fakes.ts (recording fakes of the
                            native modules), render-widget.ts, fixtures/hub/*.json,
                            fixtures/widget-snapshot.v1.json
```

Rule: `src/lib/**` imports no `react-native`, no `expo-*` module and no React. It takes
platform services as arguments (`fetch`, clock, storage, a `Notifications`-shaped object). A
jest test walks the import graph and fails on a forbidden import (wave 0, like phase 7's
lint test).

Pinned versions (exact; all released on or before 2026-09-29; the native modules are the
versions `expo@57.0.26`'s `bundledNativeModules.json` names):

| Package | Version | Released | Why |
| --- | --- | --- | --- |
| `expo` | 57.0.26 | 2026-09-29 | SDK 57 (decision 1) |
| `react` / `react-native` | 19.2.3 / 0.86.3 | — | SDK 57 pins |
| `expo-router` | 57.0.24 | 2026-09-29 | Routes, deep links |
| `expo-notifications` | 57.0.21 | 2026-09-24 | Channels, permission prompt, categories, responses |
| `expo-secure-store` | 57.0.4 | 2026-09-11 | Token in Keychain / Android Keystore |
| `expo-camera` | 57.0.6 | 2026-09-29 | QR scanning (`CameraView` `onBarcodeScanned`; `expo-barcode-scanner` is abandoned) |
| `expo-linking` | 57.0.11 | 2026-09-24 | Scheme URLs, opening `remote_url` |
| `expo-constants` / `expo-device` / `expo-application` | 57.0.20 / 57.0.2 / 57.0.3 | ≤ 2026-09-29 | Project id, device name, app version |
| `expo-widgets` | 57.0.22 | 2026-09-29 | iOS widgets and Live Activity (decision 7) |
| `@expo/ui` | 57.0.21 | 2026-09-29 | SwiftUI components for widgets (`expo-widgets@57.0.22` depends on `~57.0.21`) |
| `expo-clipboard` | 57.0.2 | 2026-09-11 | Copy resume command |
| `react-native-android-widget` | 0.22.1 | 2026-08-17 | Android widgets |
| `expo-build-properties` | 57.0.22 | 2026-09-24 | Deployment targets |
| `expo-dev-client` | 57.0.19 | ≤ 2026-09-29 | Development builds (native modules need a development build) |
| `@react-native-async-storage/async-storage` | 2.2.0 | — | Last-known cache only |
| `react-native-safe-area-context` / `react-native-screens` | 5.7.0 / 4.26.2 | 2026-02-24 / 2026-07-16 | expo-router |
| `typescript` | 6.0.3 | 2026-04-16 | SDK 57 template |
| `jest-expo` | 57.0.5 | 2026-08-26 | Test preset |
| `@testing-library/react-native` | 14.0.1 | 2026-06-23 | Screen tests (14.1.0 is too new) |
| `eslint-config-expo` | 57.0.2 | 2026-08-26 | Lint |
| `react-dom` | 19.2.3 | 2025-12-11 | Added in wave 0: expo-router and `@expo/metro-runtime` pull a newer `react-dom` (19.3, which wants React 19.3) unless it is pinned to SDK 57's `bundledNativeModules` value |
| `babel-preset-expo` | 57.0.13 | 2026-09-24 | Added in wave 0: `babel.config.js` needs it at the top level. 57.0.15 (what `expo` nests) declares `expo-widgets ^57.0.24`, newer than our pin; 57.0.13 wants `^57.0.21` |
| `test-renderer` | 1.2.0 | 2026-04-16 | Added in wave 0: RNTL 14's renderer peer; 1.3 uses `react-reconciler` 0.34, which wants React 19.3 |
| `jest` / `@react-native/jest-preset` | 29.7.0 / 0.86.3 | 2023-09-12 / 2026-08-24 | jest-expo 57's runner and preset |
| `eslint` | 9.39.5 | 2026-07-10 | eslint-config-expo 57's flat config (npm marks 9.x end of life; 10 is not supported by the config yet) |
| `@types/react` / `@types/jest` / `@types/node` | 19.2.18 / 29.5.14 / 22.19.1 | ≤ 2026-07-30 | Types (`tsconfig` `types: ["jest", "node"]`: TypeScript 6 no longer loads every `@types` package) |

No `expo-updates` (no over-the-air updates: decision in "Defaults").

Check it with `make phone-check` (`npm ci`, `tsc`, `eslint`, `jest --ci`, the secrets check and
both prebuild checks). `npm ci` prints peer-override warnings (`react-native-worklets`, the
optional React 19.3 peers); they are expected and do not fail the install.

## P2. Pairing

- **Scan.** The Pair screen opens `CameraView` with `barcodeScannerSettings: {barcodeTypes:
  ["qr"]}` and stops at the first string `parsePairInput` accepts.
- **`parsePairInput(text)`** (pure) accepts:
  - `https://<host>[:port]/#/pair?s=<22 base64url>&c=<6 digits>[&h=<24 base32>&k=<22 base64url>]`,
    the QR of pairing.md 2.2 with keys.md 7's hub fingerprint and registration seal;
  - `switchyard://pair?u=<https origin>&s=…&c=…[&h=…&k=…]`, the "Open in app" link the web pair
    page gains in wave 2; `h` and `k` are the QR's pins and are carried through the link route
    to the Pair screen (without them the claim sends no keys and the phone gets no link);
  - typed input: an address plus `482-913`, `482913` or `482 913`.

  It returns `{origin, secret?, code?}` or a reason. The origin must be `https://`, with no
  user info, path, query or fragment kept. Only `__DEV__` builds, and bundled builds made with
  `SY_APP_VARIANT=development` (`extra.cleartext`, the same switch as cleartext traffic in the
  manifest), accept `http://` for loopback, RFC 1918 and Tailscale (100.64.0.0/10) addresses (pairing.md 2.3's
  `--insecure` hubs, for local testing, `test/e2e/android`).
  It refuses a `javascript:` URL, an origin with a backslash and a host over 253 characters.
- **Claim:** `POST <origin>/v1/pair/claim` with `{secret | code, name: "<model> · app",
  expect_kind: "phone", cookie: false, client: "app/<version>+<build>"}`. On 200 it keeps
  `{origin, token, device, name}` (P3). The error mapping follows pairing.md 3: 401 →
  "Invalid or expired code", 409 → "That code is for a <kind>, not a phone", 429 → "Too many
  tries, wait 10 minutes", network → "Can't reach <host>. Is Tailscale on?"
- **Keys in the claim (N1).** Before claiming, the app calls `SyLink.prepareKeys()` (push.md
  14.3). With `k` from the QR it sends `keys_sealed` (keys.md 7.1); with a typed code, `keys`
  in the clear (never with a QR secret: the hub refuses that). The reply's `hub_key.fp` must
  equal `h` when the QR had one, else the app refuses: "This hub's key does not match the QR
  code". It then makes keys.md 7.3's checks (certificate signed by `hub_key`, holding its own
  keys, `reg = qr` after a sealed claim) and on any mismatch deletes the new device and
  refuses. On success it calls
  `SyLink.configure({origin, device, urls: link.urls, pingS: link.ping_s, hubKey, reg, cert,
  qrFp: h})`, which makes the certificate checks natively and rejects `ERR_SY_HUB_KEY` /
  `ERR_SY_TAMPERED` (push.md 14.8), then `SyLink.start()` (from the glue's `onPaired`).
  As built: the QR's `k` is sealed by `SyLink.sealKeys(k, secret)`; a QR with a secret but no
  `k` (a hub before F1) sends no keys at all; a damaged `h` or `k` is refused like a damaged
  secret. A sealed claim whose reply has no `hub_key` or `cert` is refused as tampered; a
  typed-code claim to a hub before F1 (no `hub_key`) pairs without instant delivery. A
  `configure` failure other than the two refusals pairs without the link (Settings →
  Notifications shows it). The refusal deletes the device with `DELETE /v1/devices/self` and
  the new token, then shows the message; nothing is kept.
- After pairing: `GET /v1/me` (to check it), then the notifications screen (P6), then Now.
- A second pairing replaces the first, after confirming "Unpair from <host>?". There is one
  hub per app install.

## P3. Token storage and the hub client

- SecureStore key `sy.hub.v1` holds JSON `{origin, token, device, name, paired_at}`, with
  `keychainAccessible: AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY`. That way the background
  notification task and widget refresh can read it while the phone is locked after its first
  unlock, and it is never restored onto another device. Android: Keystore-backed, and the app
  sets `allowBackup=false` (P13).
- `HubClient` (pure, `fetch` injected): it prefixes the origin, sends `Authorization: Bearer`,
  times out after 10 s (6 s for pairing's `/health` probe, 1.5 s for route checks), follows no cross-origin redirect and
  parses the `Error` schema. On 401 it calls `onUnauthorized` once: the app clears the
  keychain entry, the cache and the widget data, ends Live Activities and shows Pair ("This
  phone was unpaired on the hub"). A 401 from a client of an earlier pairing (after "Unpair
  and pair") is ignored. Its errors and logs carry the path and status, never the token or
  response bodies.
- **Origins and redirects.** An origin is kept normalised: lower-case, no trailing slash, no
  default port (`https://hub:443` is `https://hub`), so every same-origin check compares like
  with like. Every request asks for `redirect: "error"`, and any 3xx answer, or an answer
  whose final URL is on another origin, is fatal. React Native's `fetch` may follow a
  redirect whatever `redirect` says, and then that check is after the fact: the request has
  already gone out with its bearer. So the first contact with a new origin is a
  `GET /health` without credentials (a claim sends the pairing secret or code only after it)
  that must answer from that origin with no redirect; a 503 still counts as an answer. A
  stored origin never changes except by pairing again, which probes again. If a paired
  hub starts redirecting later, and the platform follows it carrying the header, the target
  can see a bearer request before the app refuses the answer. That is the native limit,
  accepted for a hub the person runs on their own tailnet.
- Unpair (Settings): stop polling, record `{origin, token}` in SecureStore key
  `sy.revoke.v1` (a short list), do the same local clearing (which also dismisses the old
  hub's delivered notifications), then `DELETE /v1/devices/self`. An unpairing the hub has
  not confirmed is retried on every start and foreground until it answers 2xx, 401 or 404;
  pairing again with the same hub confirms it first, so its old link target is gone before the
  new claim makes its own.
- A keychain that cannot be read at start (locked before the first unlock) is state
  `locked`, not unpaired: nothing is cleared, notification decisions stay queued, and the
  entry is read again when the app becomes active.

## P4. Screens and routes

The same screens as the PWA (pwa.md 3, plan section 12), with phone tabs **Now, Sessions,
Accounts, More**:

| App route (expo-router) | Hash route it mirrors | Screen |
| --- | --- | --- |
| `/` (tab Now) | `#/` | Needs-you inbox with swipe Seen / Snooze (`POST /v1/alerts/{id}/seen`, `/snooze`), alert status words, rings per account, running count per machine |
| `/sessions?filter=&by=` | `#/sessions` | Grouped list with filter chips |
| `/sessions/[id]?alert=` | `#/sessions/<id>` | State history, last message, **Open in Claude** (`remote_url` in the seat's claude.ai tab on Android, P17; elsewhere with the wrong-account note), **Copy resume command**, Snooze |
| `/accounts?seat=` | `#/accounts` | Cards, "most room", **Count down on Lock Screen** (iOS, P12) |
| `/more` | `#/more` | Alerts, Machines, Peers, Settings |
| `/more/alerts` | `#/more/alerts` | Rule switches as `prefs.mute` (`PATCH /v1/devices/self`), recent pushes (`GET /v1/alerts`) |
| `/more/machines?machine=` | `#/machines` | Machines list |
| `/more/settings` | `#/more/settings` | Hub and status, notifications state, private pushes, tap behaviour, own quiet hours, widgets refresh switch (`prefs.widgets`), app version and **Check for update** (P16), **Unpair** |
| `/more/settings/notifications` | — | P6.7 (N1) |
| `/more/peers` | `#/more/peers` | PE3: **Pause all peers**, the matrix (view; a tapped cell and **Block all** can only narrow; the turn limit is read-only) and the audit list (peers.md 5, 10, 11); see below |
| `/alerts/[id]` | `#/alerts/<id>` | Loads the alert and replaces itself with its deep link (push.md 5.8) |
| `/pair` | `#/pair` | P2 |
| `/tabs/[seat]?url=` | — | P17: the seat's claude.ai tab (Android) |

**Peers** (`PeersScreen`, PE3) reads `GET /v1/peers/matrix`, `/v1/peers/audit` and, when the hub
serves it, `/v1/peers/messages` through the hub client (`matrix`, `editMatrix`, `pausePeers`,
`peerAudit`, `peerMessages`). Top to bottom: the **Pause all peers** switch (`POST
/v1/peers/pause`; one tap pauses; turning it off is disabled with a note, because resuming widens
and a phone is never an owner session); the matrix as a grid of senders (rows) and receivers
(columns) with the version, signer and `max_turns` in the header; the audit list (what, who, a
metadata line, age) and the message list when served. **A tapped cell can only narrow**: both
ways offers one way either way or none, one way offers none, none offers nothing; each is a
`PUT /v1/peers/matrix` `edit` with `base_version` and `deny` entries only, never `allow`
(`narrowings` in `src/lib/format/peers.ts`). Allowing a pair, same account on and resume are on
the board or at a terminal (peers.md 5.4). A 409 reloads the matrix, a 403 says the change needs
the owner. Everything is disabled while offline. A **Block all** button above the grid (confirm:
"Stop all peer messages between accounts? Threads in progress stop too.") sends one `edit` with
every pair in `deny` and `same_account: false` (`blockAllEdit`); Allow all is not offered
(owner-only; the screen says "Allow pairs from a computer (board, Peers)"). The turn limit
(`max_turns`) is shown read-only as "Automated turns per thread: N" with the loop-guard
explanation (peers.md 6.3, 5.4).

Owner-only PWA screens (Devices, Add phone, rule editing) are not in the app. Analytics (More, Analytics; `GET /v1/analytics/{report}`, analytics.md) is: a range (7, 30, 90 days), a seat filter, totals, daily cost bars, a compact weekday by hour heatmap, models, projects, the five dearest sessions, limit windows (peak and final percent as bars) and limit hits.
Wording, ages and state words come from `src/lib/format` and match `webui/static/format.js`
on a shared table (WP-S).

## P5. Data refresh

- Foreground (`AppState` `active`): poll every 15 s, plus at once on foreground, on a
  received push and after a Seen or Snooze. Background: no polling; the `sy-link` module's `refresh` frames
  (P6.5) update the widgets.
- The last good replies go to AsyncStorage key `sy.cache.v1` (no token, no secret). When the
  hub cannot be reached, the screens show them under "Offline · showing 14:02", and Seen,
  Snooze and test push are disabled, as in the PWA.
- After every refresh the app rebuilds the widget snapshot (P11) and updates a running Live
  Activity (P12). It writes the widgets only while the device's `prefs.widgets` is on, and
  only once `GET /v1/me` has answered (private mode comes from `prefs.private`, else the hub's
  `settings.private`); the Live Activity is kept up to date either way, and the badge is set
  from the overview's needs-you count (P6.6). `GET /v1/rules` (`snooze_min`) and `GET /v1/me`
  are read again at most every 5 minutes while polling.
- **Order of the accounts.** Every list of seats (Accounts, Now, Analytics, Peers, Settings →
  Claude tabs, the widget snapshot) follows `seat.order`, the owner's order from the board's
  drag-and-drop (pwa.md 3.0; read-only here, no dragging on the phone): `reduce` puts the
  overview's and the accounts reply's seats in that order (`orderSeats`: ties and seats without
  an `order` keep the reply's order, the latter last). The phone has no stream, so a reorder
  shows on the next poll (15 s in the foreground).

## P6. Notifications

**P6.1 Android channels** (created before any permission or token call; Android 13+ shows
the permission prompt only once a channel exists):

| Channel id | Name | Importance | Used for |
| --- | --- | --- | --- |
| `sy_needs_you` | Needs you | HIGH | `needs_permission`, `asked`, `failed`, summaries |
| `sy_limits` | Limits | DEFAULT | `limit_80`, `limit_95`, `reset` |
| `sy_machines` | Machines | LOW | `machine_silent` |
| `sy_test` | Test | DEFAULT | test pushes |
| `sy_connection` | Connection | MIN | N1: the link service's persistent notification (push.md 14.4) |

The `sy-link` module creates the same channels natively (it posts link notifications itself),
with the same ids, so either side may create them first.

**P6.2 Categories:**

| Category | Actions (`identifier` · `buttonTitle` · options) | Used when |
| --- | --- | --- |
| `sy_alert` | `open` · "Open" · `opensAppToForeground: true`; `snooze` · "Snooze 15 min" · `opensAppToForeground: false`; `seen` · "Seen" · `opensAppToForeground: false` | The payload has `actions` |
| none | Tap only | Summaries, `test`, private pushes still have actions (tokens reveal nothing) |

The Snooze title uses `snooze_min` from `GET /v1/rules` (`settings.snooze_min`; a phone
device may read it), and categories are re-registered when it changes. (Phases/8.md said
`GET /v1/push/config`, which has no settings: corrected in wave 0.) Whether a non-foreground action runs the JS response handler
on iOS while the app is killed is **unverified** (risk 3). If it does not, one constant,
`BACKGROUND_ACTIONS_IOS`, flips Snooze and Seen to `opensAppToForeground: true`: the app
opens briefly, acts and shows a toast.

**P6.3 Registration.** The app registers no push target: the `link` target comes from pairing
(push.md 13.7). On start `expo-notifications` creates the channels (P6.1) and asks for the
permission (`requestPermissionsAsync`; anything but granted → Settings shows "Blocked in system
settings"), then `PATCH /v1/devices/self {prefs: {tz, widgets: true}}`. `widgets: true` goes only
on the first registration, when the device's `prefs.widgets` has never been set (read from
`GET /v1/me` or `GET /v1/devices/self`); later registrations send `{tz}` only, so a person who
turned widget refresh off keeps it off. `widgets` means widget refresh frames over the link.

**P6.4 Responses** (`decideResponse(response, reachable) → Decision`, pure):

| Input | Decision |
| --- | --- |
| `actionIdentifier` `snooze` / `seen` with a token in `data.actions` | `{kind: "act", token}` → `POST <origin>/v1/actions/<token>` with `Content-Type: application/json` and body `{}`, no bearer; then dismiss that notification. The result is not retried, as in pwa.md 5 |
| Default tap or `open` | The `tap` rule of `sw-lib.js` `chooseTarget`: `claude` with `remote_url` → open the link; `auto` with `remote_url` → probe `GET /health` (1.5 s), the link if no answer, else the route; otherwise the route from `data.url` (P7) |
| `data` missing or `v` not 1 | Open Now |

An act goes only to the hub that sent the notification. The payload has no hub identity
today, so the app drops (and dismisses) an act on a notification the OS delivered before the
current pairing was made (`notification.date` against `paired_at`, both on the phone's
clock); when a payload names an `origin`, an act whose origin is not the paired one is
dropped too. Each notification's Snooze or Seen runs once: the `sy-link` module's action handling and
the response listener share the AsyncStorage key `sy.acted.v1` (`"<id>|<action>"`, the last
50, never a token).

The listener is registered at module scope in `app/_layout.tsx`. On cold start,
`getLastNotificationResponseAsync()` is read once and then cleared, so a response is never
handled twice. Foreground notifications are shown (`setNotificationHandler`: banner, list, badge)
and trigger a refresh.

**P6.5 Link frames:** there is no JS background task and no silent push. The `sy-link` module
handles link frames, `refresh` frames (push.md 13.5) and actions natively (push.md 14.5, 14.6) and
emits `refresh` to JS when the app is running; JS then rebuilds the widget timeline, updates the
Live Activity and sets the badge from `needs_you`. Action handling is P6.4's `act`.

**P6.6 Badge:** set after each refresh with `setBadgeCountAsync(needs_you)`.

**P6.7 Settings → Notifications** (N1, route `/more/settings/notifications`, screen
`PhoneNotifySettings` to be drawn in `docs/design/screens/`; built as
`src/components/screens/NotifySettingsScreen.tsx`, reached from a "Notification settings"
button in Settings' Notifications card):

| Row | Content | Action |
| --- | --- | --- |
| **Instant delivery** | Switch, on by default after pairing. Below it: "Keeps a connection to your hub. Uses a small, constant notification." | On: `SyLink.start()` and `PATCH /v1/push/targets/{link id} {enabled: true}`. Off: `SyLink.stop()` (15-minute checks, push.md 14.9) and the target stays enabled. |
| Connection | `LinkState.status` in words: "Connected · 2 h 14 min · WebSocket", "Reconnecting · next try in 20 s", "Not connected: hub unreachable", "Removed from hub". The last error (close code word or network class). Received / rejected counts. | **Reconnect now** |
| Battery | "Battery optimisation: off (good)" or "on: notifications may be late". | **Fix**: `requestBatteryExemption()`; then the maker guide |
| Phone-maker guide | Shown when the exemption is on or `Build.MANUFACTURER` is in the table below. One short paragraph per maker. | **Open settings**: `openBatterySettings()` |
| Notifications permission | "Allowed" or "Blocked in system settings". | **Open settings** |
| Encryption | push.md 14.7: "Verified by QR", or "Not verified: compare check code k7q2-m4xa with the board", or "Hub key changed". | — |
| Ping interval | "Every 3 min" (180 s). Choices 3, 5, 10 min; 5 and 10 only advised on the tailnet. | `setPingInterval` |
| **Send test notification** | — | `POST /v1/push/targets/{link id}/test`; the result arrives over the link. Long press: `sendLocalTest()` (no hub) |
| Backup: Web Push | "Also get alerts in the installed web app (uses Google's push service, encrypted, no account)." | Opens the hub's web app pairing page |

Maker guides (text in `src/lib/notify/makers.ts`, matched on lower-case manufacturer):

| Maker | Guide |
| --- | --- |
| samsung | Settings → Battery → Background usage limits → Never sleeping apps → add Switchyard |
| xiaomi, redmi, poco | Settings → Apps → Switchyard → Autostart on; Battery saver → No restrictions |
| oneplus, oppo, realme | Settings → Battery → Switchyard → Don't optimize; allow background activity |
| huawei, honor | Settings → Battery → App launch → Switchyard → Manage manually, all three on |
| others | Settings → Apps → Switchyard → Battery → Unrestricted |

Doctor rows (plan addendum C) come from the same state: connection age, battery exemption and
verification.

As built (N1): the words are in `src/lib/notify/link.ts` (`linkStatusWords`, `linkErrorWords`,
`verifiedWords`, `batteryWords`); the maker comes from `expo-device`'s `manufacturer`
(`AppInfo.maker`). A paired device with no `link` target (paired before N1, or by a hub before
F1) shows "This phone has no link on the hub yet" and asks to pair again: the app does not call
`PUT /v1/devices/self/keys`, whose reply carries only `cert`, not the `hub_key` and `link.urls`
`configure` needs. The `revoked` and `refresh` events refresh the data at once (a revoked phone
then gets the 401 that shows Pair, P3).

## P7. Deep links

- Scheme `switchyard`. Accepted incoming URLs (an allow-list in `src/lib/links`; everything
  else opens Now):
  - `switchyard://now`
  - `switchyard://sessions/<id>[?alert=<al_…>]`
  - `switchyard://accounts[?seat=<id>]`
  - `switchyard://machines[?machine=<id>]`
  - `switchyard://alerts/<al_…>`
  - `switchyard://pair?u=…&s=…&c=…[&h=…&k=…]`
  - `switchyard://settings/notifications` (N1: the link's persistent notification, push.md
    14.4; `switchyard://settings` alone stays off the list)

  Ids must match `^[A-Za-z0-9_.:-]{1,80}$`.
- `hashToRoute(url)` maps a payload `url` (push.md 5.8: `/#/sessions/<id>?alert=…`,
  `/#/accounts?seat=…`, `/#/machines?machine=…`, `/#/`, `/#/alerts/<id>`) to the app route.
  Like `sw.js`, it rejects anything that does not start with a single `/` or that contains a
  backslash.
- `app/+native-intent.ts` (`redirectSystemPath`) runs every incoming URL through the
  allow-list. A pair link never claims by itself: it opens the Pair screen pre-filled, and
  the person taps **Pair with <host>**. The link's `u`, `s`, `c` become the route params
  `origin`, `secret`, `code` (`/pair?origin=…&secret=…&code=…`), which the Pair screen reads;
  it shows a confirm card for them and claims only on that tap (and, when already paired,
  asks "Unpair from <host>?" after it).
- Widgets link to `switchyard://now` and `switchyard://accounts?seat=<id>`. The Live
  Activity links to `switchyard://accounts?seat=<id>`.

## P11. Widgets

**`WidgetSnapshot` v1** (`src/contract/snapshot.ts`; the jest golden is
`test/fixtures/widget-snapshot.v1.json`):

```json
{
  "v": 1,
  "as_of": "2026-10-13T14:02:00.000Z",
  "hub": "hub.example.ts.net",
  "needs_you": 2,
  "top": {"seat": "work", "project": "api", "what": "asked", "since": "2026-10-13T13:58:00.000Z"},
  "seats": [
    {"id": "seat_…", "label": "work", "colour": "#7c5cff",
     "five_hour": {"pct": 62, "resets_at": "2026-10-13T17:00:00.000Z"},
     "seven_day": {"pct": 41, "resets_at": "2026-10-16T09:00:00.000Z"}}
  ],
  "next_reset": {"seat": "work", "window": "five_hour", "resets_at": "2026-10-13T17:00:00.000Z", "pct": 62},
  "private": false
}
```

- `buildSnapshot(overview, accounts, now, prefs)` (pure). There are at most 4 seats, ordered
  as the Accounts screen orders them: the hub's order (`seat.order`, set by the owner's
  drag-and-drop on the board, pwa.md 3.0; seats without one last). `top` is null in private mode. Labels are cut to 12
  characters.
- `buildTimeline(snapshot, now)` → entries `{date, props}`: one now, one at each `resets_at`
  in the next 24 h (that ring drops to 0 and reads "reset"), and one at `as_of + 60 min`
  that adds "as of 14:02" (stale). That makes at most 8 entries.
- **iOS** (`expo-widgets`, App Group `group.<bundle id>`): `NeedsYouSmall` (`systemSmall`:
  the needs-you count, the top item, and the next reset as `Text` `date` with `dateStyle:
  "timer"`) and `AccountsMedium` (`systemMedium`: up to 4 rings with pct and reset time).
  The app calls `updateTimeline(entries)` after every refresh. Widget components are pure
  props → `@expo/ui/swift-ui` trees, with no hooks or state (an expo-widgets rule).
- **Android** (`react-native-android-widget`): `SyNeedsYou` (target 2×2 cells) and
  `SyAccounts` (4×2), `updatePeriodMillis: 1800000` (the library's minimum). The task handler
  reads the last snapshot from AsyncStorage key `sy.widget.v1` (no secrets) and renders it.
  The app calls `requestWidgetUpdate` after each refresh. Android shows reset as a clock time
  ("resets 17:00"), not a ticking timer (no verified countdown primitive).
- Neither side reads the keychain or calls the hub. Widgets show only what the app last
  wrote.

## P12. Live Activity (iOS)

- `chooseCountdown(snapshot, now)` (pure) returns the seat × window with the highest pct
  among windows resetting within `LIVE_MAX_H = 8` hours and at ≥ 80%, else null. 8 hours is
  the commonly cited limit for an active Live Activity and is **unverified** (risk 7). So the
  5-hour window qualifies and the 7-day window only in its last 8 hours.
- Props: `{seat, colour, window, pct, resets_at, started_at}`. The `ResetCountdown`
  component renders `Text` with `timerInterval: {lower: started_at, upper: resets_at}` (`@expo/ui`
  `ClosedRangeDate`) and `countsDown`
  (lock-screen banner, Dynamic Island compact, minimal and expanded regions).
- Started by the app in the foreground: **Count down on Lock Screen** on a qualifying card,
  or automatically when the app opens from a `limit_95` or rate-limit `failed` push, if
  Settings "Auto countdown" is on (default on). At most one runs at a time (`getInstances()`
  at start).
- "Auto countdown" (iOS only, Settings: "Count down on the Lock Screen when a limit push is
  opened") is a local app setting, not a hub pref: AsyncStorage key `sy.local.v1`
  (`LOCAL_PREFS_KEY`) holds `{auto_countdown: boolean}`, never a secret, and survives an unpair.
  Only a tap or **Open** counts (not Snooze or Seen). The payload carries no failure detail, so
  `isLimitPush` (`src/lib/integrate`) takes a `failed` push as a rate limit when its title ends
  in "rate limited" or its body is the "Resets 17:00 · …" line (push.md 5.5, 5.7); a private
  `failed` push never counts. The response sets a one-shot flag and kicks a refresh; the next
  refresh calls `syncCountdown(snapshot, now, true)`. It is updated after each refresh with the new pct. It ends with
  `end(after(resets_at + 15 min))` (`after` from `expo-widgets`; wave 0 corrected the
  `end("after", …)` form) at the reset, and immediately on unpair.
- The activity is `createLiveActivity('ResetCountdown', component)` (`LIVE_ACTIVITY_NAME`).
  It needs no plugin option: the generated widget bundle always includes
  `WidgetLiveActivity()` (checked in the wave 0 iOS prebuild).
- No push-to-start and no push updates (decision 9).

## P13. Build configuration

- `app.config.ts` reads only non-secret environment values:
  - `SY_APP_ID`: bundle id and package name, default `dev.switchyard.app.local`;
  - `SY_APP_VARIANT`: `development` allows cleartext traffic (P2's loopback hubs); any
    other value, or none, is a release config.
- `scheme: "switchyard"`; iOS `ios.deploymentTarget` 16.4 (the app config field: the
  `expo-build-properties` iOS option is deprecated since SDK 56, and `expo-widgets` reads
  the same field for its extension), Android `minSdkVersion` 24 (through
  `expo-build-properties`; 31 in the Android build below).
- Android `permissions: [POST_NOTIFICATIONS, CAMERA]` and `blockedPermissions:
  [RECORD_AUDIO, SYSTEM_ALERT_WINDOW, READ_EXTERNAL_STORAGE, WRITE_EXTERNAL_STORAGE]`.
  POST_NOTIFICATIONS is listed so the prebuilt manifest shows it (expo-notifications would
  otherwise add it only at Gradle merge time).
- Plugins: `expo-router`; `expo-notifications` (icon, colour); `expo-secure-store`;
  `expo-camera` (camera permission text "Scan the pairing QR code from your hub"; microphone
  off); `expo-widgets` (`groupIdentifier: group.<SY_APP_ID>`, `bundleIdentifier:
  <SY_APP_ID>.widgets` for the extension, the two widgets, `enablePushNotifications: false`,
  `enableAndroid: false`); `react-native-android-widget` (the two widgets);
  `./plugins/with-sy-native.js`.
- `with-sy-native.js` (ours) sets:
  - iOS `NSSupportsLiveActivities: true`;
  - Android `allowBackup="false"`;
  - removes `RECORD_AUDIO` and `SYSTEM_ALERT_WINDOW` if a library adds them;
  - `usesCleartextTraffic` only in development builds;
  - Android Gradle: every `androidx.work` artifact is forced to 2.8.1 in `app/build.gradle`
    (`resolutionStrategy.eachDependency`). `react-native-android-widget` declares
    `work-runtime` 2.8.1, while `expo-widgets`' `androidx.glance` 1.2.0-rc01 pulls
    `work-runtime-ktx` 2.7.1; work 2.8 merged the ktx classes into `work-runtime`, so both
    together fail `:app:checkDebugDuplicateClasses`. Forcing 2.8.1 makes ktx an empty artifact.
    Revisit when Glance or the widget library moves to a common version.

**Android build (N1, N2).** Nothing in it needs an Expo, Google or store account: no EAS
profiles, no `extra.eas.projectId`, no `googleServicesFile` and no push credentials.

- Android `minSdkVersion` **31** (push.md 14.2: Keystore key agreement), `targetSdkVersion`
  the SDK 57 default.
- The `sy-link` module adds its permissions and components through its own
  `AndroidManifest.xml` (push.md 14.2); P13's `permissions` list stays as it is.
  `prebuild-check.mjs android` also asserts the `specialUse` service, its subtype property,
  `RECEIVE_BOOT_COMPLETED`, `REQUEST_INSTALL_PACKAGES`, and that no
  `com.google.firebase` / `google-services` entry is present. The module's manifest is merged
  by Gradle, not by prebuild, so the check reads it from `modules/sy-link` (and copies the
  local modules' `android/` sources into its temporary tree).
- **No FCM entry point.** `expo-notifications` stays (channels and the permission prompt), but
  its config plugin writes `com.google.firebase.messaging.*` meta-data and its library manifest
  declares `ExpoFirebaseMessagingService`. `plugins/with-sy-no-firebase.js` drops the meta-data
  and removes the service from the merged
  manifest (`tools:node="remove"`); it is listed **before** `expo-notifications`, because
  manifest mods run in reverse order of the plugins list. The `firebase-messaging` library
  stays on the classpath (expo-notifications' Kotlin links it) but is never initialised: there
  is no `google-services.json`, no Google Services Gradle plugin and no token call.
- There is no background notification task: there are no silent pushes.
- The glue uses the `sy-link` module only on Android, when the module is present (`systemLink`).
- **Build:** `make phone-apk-release` (you run it, not an agent): `expo prebuild --platform
  android --clean` with `SY_APP_VARIANT=production`, then `./gradlew
  assembleRelease` in `phone/android`. Output `phone/dist/switchyard-<version>.apk`, its
  `SHA256SUMS` (the APK's line) and `manifest.json` (P16.1). A development build for the phase 8 manual check is
  `expo prebuild` + `./gradlew assembleDebug`.
- **Signing:** your own release keystore, outside the repo. The make target refuses to start
  without it: `SY_ANDROID_KEYSTORE` (path), `SY_ANDROID_KEY_ALIAS` and
  `SY_ANDROID_KEYSTORE_PASSWORD` (and `SY_ANDROID_KEY_PASSWORD` when it differs), or the same
  four values as `storeFile`, `keyAlias`, `storePassword`, `keyPassword` in a properties file
  named by `SY_ANDROID_SIGNING_PROPERTIES` (also outside the repo; an environment variable
  wins). The passwords reach Gradle (`scripts/sy-release-signing.init.gradle`, an init script,
  because `expo prebuild --clean` regenerates `android/`) and keytool only through the
  environment, are never printed and never written into `phone/`. `check-no-secrets.mjs`
  already refuses keystores in the tree. Keep two offline copies: losing it means every phone
  uninstalls and reinstalls. The certificate's SHA-256 fingerprint (read from the keystore and
  checked against the built APK with `apksigner verify --print-certs`) is printed by the make
  target and goes into `manifest.json` as `cert_sha256`. No `make` target creates a keystore;
  `phone/README.md` documents the `keytool` command.
- **Version:** `app.config.ts` sets Android's `versionCode` (the manifest's `build`) to
  `MAJOR*10000 + MINOR*100 + PATCH` of `version`, so it only rises with the version.
- **Release:** a GitHub release tagged `phone-vX.Y.Z` with the APK, `SHA256SUMS` and
  `manifest.json`, made by you.

## P14. Tests and fixtures

- `test/with-sy-native.test.ts` unit-tests the plugin's Gradle edit (`alignWorkVersions`:
  forces the `androidx.work` group, idempotent). `make phone-android-debug` is the real proof.
- `test/setup.ts` (jest `setupFilesAfterEnv`) replaces `expo-notifications`,
  `expo-secure-store`, `expo-camera`, `expo-widgets`, `@expo/ui/swift-ui` (and `/modifiers`),
  `react-native-android-widget`, `expo-task-manager` and AsyncStorage with the recording fakes
  of `test/fakes.ts`, makes global `fetch` reject, and resets every fake before each test.
  Tests script the fakes through their exported state (`notificationsState.permission`,
  `secureState.locked`, …) and read `calls` / `callsOf(prefix)`. The secure-store fake never
  records a stored value. `emitNotificationResponse` and `runTask` play the OS's part.
- Widget components render with `test/render-widget.ts` (`renderWidget`, `allOf`, `textOf`),
  which uses `test-renderer` directly with `SwiftUI.Text` as the text host. RNTL's `render`
  does not fit widget trees: it only lets React Native's `Text` hold strings. Every
  `@expo/ui/swift-ui` component renders as a host element `SwiftUI.<Name>` and every
  `react-native-android-widget` component as `Android.<Name>`; modifier helpers return
  `{modifier, args}` and are recorded.
- `test/fixtures/hub/*.json` are `webui/devserver` replies (`phone/scripts/dump-fixtures.sh`,
  paired as a phone with `cookie: false`), with `_meta.json` giving the time they were taken.
  The devserver's data is relative to its start, so tests that format times use
  `_meta.json`'s `now_ms`. `test/fixtures/widget-snapshot.v1.json` is the P11 example.
- `peers-matrix.json` and `peers-audit.json` in the same folder are the devserver's fresh matrix
  (version 1, nothing allowed) and audit (`GET /v1/peers/matrix`, `/v1/peers/audit`);
  `validateMatrixInfo` and `validatePeerAudit` check them, and the Peers screen tests start from
  them (`test/screens.test.tsx`, `test/peers-format.test.ts`).
- `test/contract.test.ts` checks the Go payload goldens with
  `validatePayload(…, exact)` (Go key order, no unknown keys), and every fixture with its
  validator. Wave 0 ships `placeholder-alert.json` and `placeholder-refresh.json`; WP-H replaces
  them with its `message-*.json` goldens.
- `test/imports.test.ts` walks the import graph from `src/lib` and `src/contract` and fails on
  any package import or a relative import that leaves them (P1). `eslint.config.js` also
  forbids `react`, `react-native*`, `expo*`, `@expo/*` and `@react-native*` in `src/lib/**`
  and `console` in `src/**`.
- `test/stubs.test.ts` expects every wave 0 stub to throw; each wave 1 owner deletes their
  module from its list.
- `scripts/check-no-secrets.mjs` fails on a credential-shaped file in `phone/` (outside
  `node_modules`, `android/`, `ios/`) or anywhere in `git ls-files`, and on a private key block
  or service-account JSON in a tracked `phone/` file. `.env.example` / `.env.sample` are allowed.
- `scripts/prebuild-check.mjs android|ios` copies `phone/` to a temp directory (with
  `node_modules` symlinked), runs `expo prebuild --platform <p> --no-install --clean` there
  with `SY_APP_VARIANT=production`, asserts the list in phases/8.md's test plan and deletes
  the copy (`SY_KEEP_PREBUILD=1` keeps it for a look).

## P15. Facts checked in wave 0

Checked on 2026-10-13 on Fedora Linux (Node 22.22.2, no Xcode, no Android build), with the
pins of P1:

| # | Fact | Result |
| --- | --- | --- |
| 1 | `expo prebuild --platform android --no-install --clean` succeeds with every P13 plugin; the manifest has `POST_NOTIFICATIONS`, the `switchyard` intent filter, two `AppWidgetProvider` receivers (`.widget.SyNeedsYou`, `.widget.SyAccounts`) and `allowBackup="false"` | **Checked.** Needed one change: POST_NOTIFICATIONS is only merged from expo-notifications' library manifest at Gradle time, so P13 now lists it in `android.permissions`. Two `appwidget-provider` XML files are generated; react-native-android-widget's plugin writes to the right path (no doubled `android/android`). Cleartext is off in release. |
| 2 | `expo prebuild --platform ios --no-install --clean` succeeds on Linux, with `Info.plist`, entitlements and the widget extension target | **Checked.** `ios/ExpoWidgetsTarget/` holds `NeedsYouSmall.swift`, `AccountsMedium.swift`, `index.swift` (the bundle, which includes `WidgetLiveActivity()`), `Info.plist` and its entitlements. The App Group is on the app and the extension, `aps-environment` on the app, the app-extension target is in `project.pbxproj`, and every target is iOS 16.4. Pods are not installed (`--no-install`), so this proves config, not compilation (risk 6 stays for the native compile). The iOS prebuild check stays in `make phone-check` and CI's `check` job. |
| 3 | `expo-widgets@57.0.22` and `react-native-android-widget@0.22.1` coexist | **Checked.** Both plugins run in the same config without duplicate keys; each platform's prebuild generates its own widget set and the other plugin leaves it alone (`expo-widgets` has `enableAndroid: false`). No fallback needed. |
| 4 | `jest-expo` renders `@expo/ui/swift-ui` widget trees through the mock | **Checked**, with `test/render-widget.ts` rather than RNTL's `render` (P14). `test/widget-mock.test.tsx` renders a `VStack` with a timer `Text` and an Android `FlexWidget`. |

Notes for wave 1 from the same checks:

- `babel-preset-expo` adds a widgets transform whenever `expo-widgets` is installed: widget
  components are functions marked with the `'widget'` directive, serialised for the native
  side. Components must stay pure (props in, tree out; no hooks, no closures over module
  state).
- `expo-widgets` warns about `userInterfaceStyle` on Android without `expo-system-ui`. That is
  harmless; WP-S may ask for `expo-system-ui` if it wants the Android dark-mode setting.

## P16. Updater (N2)

The app is sideloaded (no store), so it updates itself, with your confirmation each time.

### P16.1 Release manifest

`manifest.json`, published with each `phone-vX.Y.Z` release and mirrored by the hub:

```json
{"v":1,"version":"0.9.0","build":14,"min_sdk":31,"apk":"switchyard-0.9.0.apk","size":48213004,
 "sha256":"<64 hex>","cert_sha256":"<64 hex, signing certificate>","notes":"…","published_at":"…"}
```

### P16.2 Hub mirror

- Once a day (and at `sy hub serve` start), the hub asks GitHub for the project's releases
  (`GET https://api.github.com/repos/<owner>/<repo>/releases?per_page=20`, no token,
  `netguard` client), takes the newest non-draft, non-prerelease release whose tag matches
  `^phone-v\d+\.\d+\.\d+$`, downloads `manifest.json`, `SHA256SUMS` and the APK, checks the
  APK's SHA-256 against both, and keeps it in `<data>/app/android/` (the current and the
  previous version only). The repository comes from the build (`version.Repo`); setting
  `app.android_mirror = false` turns the mirror off.
- The off switch and the repository also exist as `sy hub serve` flags (`--no-app-mirror`,
  `--app-repo owner/name`) and as the settings `app.android_mirror` (JSON bool) and
  `app.android_repo`; a flag wins. Downloads follow at most 5 https redirects (GitHub's assets
  redirect to a CDN) and still refuse non-public addresses (`netguard`). A release that does
  not verify is never published, and what is already mirrored stays.
- `GET /v1/app/android` (any device token with `read`) → the manifest plus `"url":
  "/v1/app/android/apk"`, or 404 when nothing is mirrored. `GET /v1/app/android/apk` streams
  the APK (`Content-Type: application/vnd.android.package-archive`, `ETag` = sha256).
- The hub never re-signs or changes the APK; it only serves what the release holds.

### P16.3 In the app

- `SyLink.checkUpdate(origin, token)` at most once a day (and from **Check for update**):
  first the hub (`GET /v1/app/android`), then, if the hub has none or is unreachable, GitHub
  directly (the same release filter as P16.2). An update is offered when `build` is higher
  than the installed `versionCode` and `min_sdk` fits.
- The app (`src/lib/update`) calls it on start when paired and at most every 24 hours, and the
  offer is remembered until installed. A failed check does not count as a check.
- Settings → App version has an **Updates** row (Android): the state ("Not
  checked yet", "Checking…", "Up to date", the error), **Check for update** (never throttled)
  and, when one is offered, the notes, the source ("your hub" or "GitHub") and **Install**.
- Settings shows "Update 0.9.0 available" with the notes. **Install**: download to the app's
  cache, check the SHA-256 against the manifest, read the APK's signing certificate
  (`PackageManager.getPackageArchiveInfo(…, GET_SIGNING_CERTIFICATES)`) and require it to
  equal **both** the manifest's `cert_sha256` and the installed app's own certificate, then
  hand it to `PackageInstaller` (session install). Android asks you to confirm; the first time
  it also asks to allow installs from this app.
- A wrong checksum or a different signing certificate is refused ("This update is not signed
  by your key") and the file is deleted. Android itself also refuses an update signed by
  another key.
- Data is kept across updates (same package, same key). After `MY_PACKAGE_REPLACED` the link
  service restarts (push.md 14.4).
- Google's developer verification for sideloaded apps (from 2027) does not change this flow;
  if a phone requires it, install once with Android's "advanced flow" or `adb install` (plan
  addendum C, H7).

## P17. claude.ai account tabs (8b)

Android only (`minSdkVersion` 31). One claude.ai web tab per seat, each its
own WebView profile, so every account stays signed in at once. **Open in Claude** opens a
session's Remote Control link in its seat's tab. Switchyard never reads the tabs' cookies,
storage or usage: the tab is a plain browser view the person signs in to.

### P17.1 The `sy-tabs` module

A local Expo module, `phone/modules/sy-tabs` (Kotlin, `dev.switchyard.tabs`), with one
dependency, `androidx.webkit:webkit` (the profile API).

| JS | Native |
| --- | --- |
| `available` | The module is in this build (Android) |
| `isSupported(): Promise<boolean>` | `WebViewFeature.isFeatureSupported(MULTI_PROFILE)` (WebView 110 or later) |
| `listProfiles(): Promise<string[]>` | The seat ids of `ProfileStore.getAllProfileNames()` that are tab profiles |
| `deleteProfile(seat): Promise<boolean>` | `ProfileStore.deleteProfile(name)`: cookies, storage and cache of that tab. Rejects `ERR_SY_TABS_IN_USE` while the tab is open, `ERR_SY_TABS_SEAT` on a bad seat id. WebView keeps the profile of a tab that was open in this process "in use" even after the tab is closed and its WebView destroyed, so `deleteProfile` then wipes its cookies and storage, hides it from `listProfiles` and deletes it for real at the next app start (before any WebView exists); opening the tab again cancels that |
| `<TabView seat url onTabState>` | A `WebView` bound with `WebViewCompat.setProfile` to the seat's profile (made on first use) before anything loads, then `loadUrl(url)` |

- **Profile name:** `sy-tab-` + the hub seat id (`^seat_[0-9A-HJKMNP-TV-Z]{26}$`), never an
  email or label. Other names (and `Default`) are never listed or deleted.
- **`onTabState`** `{state, url?, status?, height?, code?, reason?}`: `loading` (main frame started), `loaded`
  (main frame finished, `height` = content height 3 s later), `http` (main-frame HTTP error,
  `status`), `error` (main-frame network error, `code`; or `reason` `unsupported` / `profile` when no tab could be made), `crashed` (renderer gone; the view drops its
  WebView), `download` (a download was refused), `external` (a link went to the browser).
- **Lifecycle:** the screen mounts one `TabView` at a time. Cookies are flushed when the view
  is detached and when the app pauses; profiles are on disk in the app's data folder, so they
  survive restarts and updates (P16.3: same package, same key) and go with an uninstall or
  "Clear data". A seat change on a mounted view rebuilds the WebView.

### P17.2 Tab model and routing

There is no tab list of our own: a seat has a tab once its profile exists. `src/lib/tabs`
(pure) decides where a link opens, `openTarget(session, {supported})`:

1. `remote_url` not an `https://claude.ai/…` link → browser (or nothing when not https).
2. Tabs not supported (no module, not Android, WebView without `MULTI_PROFILE`) → browser,
   reason `unsupported`.
3. No seat, or a seat id that fails the pattern → browser, reason `unattributed`.
4. Else the tab of `session.seat`, at `remote_url`, even if that tab was never opened (it then
   shows the sign-in).

The seat comes from the hub (`Session.seat`, identity.md 1), so a session from account B always
opens in B's tab. **Open claude.ai** on an Accounts card opens the seat's tab at
`https://claude.ai/`.

### P17.3 Screens

- `/tabs/[seat]?url=` (`ClaudeTabScreen`): a header with the account's dot and name,
  **Open in browser** and **Close**, then the `TabView`. A strip says: "Sign in with your email
  and the code claude.ai sends. Google sign-in does not work inside apps."
- Accounts: **Open claude.ai** on each card when tabs are supported.
- Settings → **Claude tabs**: one row per profile (seat name, or "Removed account" for a seat
  the hub no longer lists) with **Sign out (clear this tab)**, which confirms, then
  `deleteProfile`. Shown only when supported; otherwise "This phone's WebView cannot keep
  separate claude.ai sign-ins; links open in the browser."

### P17.4 Fallback

claude.ai may refuse embedded WebViews. `assessTab(events, elapsed)` (pure) turns the events
into `ok`, `waiting`, `refused` (a main-frame `http` 401, 403, 429 or 451 not followed by a
`loaded` page with content within 10 s: Cloudflare's challenge also starts with a 403),
`blank` (`loaded` with `height` 0, or `about:blank`), `failed` (`error`, `crashed`) or
`timeout` (no `loaded` within 20 s). On anything but `ok` and `waiting` the screen shows
"claude.ai did not load in this tab" with the reason and **Open in browser** (the tab's current
claude.ai URL through `Linking.openURL`). It does not switch on its own, so a slow page is not lost.

### P17.5 Security

- No `addJavascriptInterface`, no injected script, no `evaluateJavascript`. JavaScript and DOM
  storage are on (claude.ai needs them); file and content access, geolocation and mixed content
  are off; Safe Browsing stays on; `setSupportMultipleWindows(false)`.
- Switchyard never calls the profile's `CookieManager.getCookie` or `WebStorage` reads; the
  module only flushes and deletes.
- **URL allow-list** (main frame, `shouldOverrideUrlLoading` and the first `loadUrl`):
  `https:` only, host `claude.ai`, `anthropic.com`, `claude.com`,
  `challenges.cloudflare.com` or a subdomain of one (desktop.md C2's list) → in the tab. Any
  other `https:` or `mailto:` link → the browser (`ACTION_VIEW`, `external` event). Anything
  else (`http:`, `intent:`, `javascript:`, `file:`, `data:`, custom schemes) is dropped.
  Subframes are not filtered (sign-in and payment widgets live there).
- Downloads are refused (`setDownloadListener` reports `download`, saves nothing). File
  choosers, camera, microphone, geolocation and other permission requests are denied.
- Certificate errors are never overridden. WebView debugging stays off.

### P17.6 Limits

- Needs WebView 110+ with `MULTI_PROFILE`; older phones open links in the browser.
- Sign-in is email and code only; Google sign-in is blocked in WebViews by Google.
- Attachments and file uploads do not work in a tab (no file chooser); use the browser.
- A refusal that claude.ai serves as a normal page with content cannot be told apart without
  reading the page, which the module never does; **Open in browser** is always in the header.
