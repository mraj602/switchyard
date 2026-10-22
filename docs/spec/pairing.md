# Pairing, owner sign-in and devices

Status: spec v1. Implemented in `internal/hub/pair` (codes, claim, rate limits, QR),
`internal/hub/auth` (owner sessions, expiry, cookie rules), `internal/hub/api` (handlers),
`internal/hub/cli` (`sy hub pair`, `sy hub owner-link`), `cmd/sy` (`sy phone pair`,
`sy connect --code`). Replaces tokens.md section 5.

## 1. What pairing changes

| Without pairing | With pairing |
| --- | --- |
| Devices exist only through `sy hub token create` on the hub machine. | A device can also be made by claiming a one-time **pairing code** (any kind). |
| The board signs in by pasting a token; its cookie is a read-only `browser` device. | Unchanged, plus: an **owner session** (`sy_o_`) from a one-time sign-in link, needed for everything that changes the hub (pairing, devices, rules, channels). |
| No device ever expires. | `device.expires_at` (schema v3): owner sessions expire after 30 days. Other devices still never expire. |
| `pair_code` table is a placeholder. | Recreated in schema v3 with its final shape (push.md section 9). |

## 2. Pairing codes

### 2.1 Shape

One pairing code has two secrets, both single use together:

| Part | Shape | Entropy | Used by |
| --- | --- | --- | --- |
| **Code** | 6 decimal digits, shown `482-913`, from `crypto/rand` (uniform, no modulo bias: reject values ≥ 1 000 000 from 20-bit draws) | ~20 bits | People typing it: `sy connect --code`, the phone's "type a code" field |
| **Secret** | 16 random bytes, base64url without padding (22 characters) | 128 bits | The QR code and links: never typed |

Owner sign-in links (section 4) have a secret and no code.

- Stored as `code_hash = HMAC-SHA256(pair_pepper, "code:" + 6 digits)` and
  `secret_hash = HMAC-SHA256(pair_pepper, "secret:" + secret)`. `pair_pepper` is 32 random
  bytes in the file `<state>/pair_pepper` (mode 0600, made at first use), deliberately
  **not** in the database: a copy of the database or a backup alone cannot test the 10^6
  possible digits offline. Losing the file (moving the hub) only invalidates codes that are
  live at that moment.
- Lifetime: **10 minutes** from creation (`pair.ttl_s` setting, 60–3600). Single use: a
  claimed, cancelled or expired code never works again.
- At most **5 live codes** at once (unclaimed, uncancelled, unexpired). Making a sixth
  cancels the oldest. This bounds the guessing target for typed codes.
- No two live codes share digits: generation retries until the code hash matches no live
  row (`pair_code_live` index). Expired rows are kept for 30 days (audit), then pruned by
  the hub's daily maintenance.
- Every code is for one device **kind** chosen by its maker: `machine`, `desktop`, `phone`,
  `browser` (owner links: `owner`). The claimer cannot change the kind. Codes for `machine`
  and `desktop` may carry a `name`; the claimer supplies its machine id.

### 2.2 The QR code and the pairing URL

The QR encodes one HTTPS URL, so the phone's own camera app opens it with nothing installed:

```
https://hub.example.ts.net/#/pair?s=Q2xhdWRlIGlzIG5vdCBoZXJl&c=482913
```

| Part | Meaning |
| --- | --- |
| origin | The hub's public URL (2.3). The installed web app is bound to this origin. |
| `#/pair` | The web app's pairing route. Everything after `#` stays in the browser: it is never sent to the hub, to a proxy log or as a referrer. |
| `s` | The secret. |
| `c` | The 6 digits, so the page can show them for the iPhone path (5.2). |

The native phone app scans the same QR and reads the same fields (it ignores the origin's
web app and calls `POST /v1/pair/claim` itself).

**Design decision:** the QR holds the hub address and the code, not a public key: the hub is reached over HTTPS with a
publicly trusted certificate (`tailscale serve` or Caddy), which already authenticates it,
and the web app fetches the VAPID key from the hub after pairing. tokens.md 5's JSON QR
(`{"hub", "code", "kind"}`) is replaced by this URL, because a phone camera opens URLs and
not JSON.

**Keys:** when the hub has keys the QR also carries `h` (the hub signing key's fingerprint) and `k` (a
registration seal), and a claim may carry the device's keys (`keys` or `keys_sealed`); the
reply then carries the hub key, the device's certificate and the link URLs. That supersedes
the deviation above for the Android app: see [keys.md](keys.md) 7. The web app ignores both
parameters.

QR encoding: byte mode, error correction **M**, quiet zone 4 modules. Library
`rsc.io/qr` (pure Go, BSD, no dependencies; the QR standard does not change, so its age is
not a concern). The hub renders the module matrix once and serves it in two forms:

- **Terminal** (`sy hub pair`, `sy phone pair`, `sy setup` step 5): two modules per character
  row with `▀`, `▄`, `█` and space, wrapped in ANSI `\e[30;47m` (black on white) so it scans
  on dark and light terminals. When stdout is not a TTY or `NO_COLOR` is set, the QR is
  omitted and only the URL and code are printed (`--qr` forces it).
- **API**: `qr` in the `POST /v1/pair/codes` reply is `{size, rows}`, `rows` being `size`
  strings of `0`/`1`. The board draws it as `<rect>`s with DOM calls (no `innerHTML`).

### 2.3 The hub's public URL

The phone must be given the address it will use, which the hub cannot always see. In order:

1. `--url <url>` on `sy hub pair` / `sy phone pair`, or `url` in `POST /v1/pair/codes`.
2. Setting `hub.public_url` (`sy hub serve --public-url <url>` stores it; also editable on
   the board).
3. For a code made through the API: the origin the board itself was loaded from
   (`X-Forwarded-Proto` + `Host` when the request came through a loopback proxy, else the
   request's own scheme and `Host`).
4. Otherwise the CLI stops: "Pass --url https://<your hub address> (the address your phone
   opens)".

For `phone` and `browser` codes the URL must be `https://` (service workers, install and Web
Push need a secure context). `http://` is refused with the fix ("run `tailscale serve --bg
<port>` on the hub and use its https address", with the port of that http address, so a hub
on `--listen 127.0.0.1:19100` reads `--bg 19100`) unless `--insecure` is given, in which case the
pairing works but the web app cannot install or receive pushes, and the CLI says so. Machine
codes follow `sy connect`'s rule (tokens.md 4: loopback, tailnet or `--insecure-lan`).

### 2.4 Making a code

| Where | How | Needs |
| --- | --- | --- |
| Hub machine, CLI | `sy hub pair --kind phone\|browser\|machine\|desktop [--name N] [--machine ID] [--url U] [--ttl 10m] [--wait] [--json]` | Shell on the hub (direct database access, like `sy hub token create`). In Docker: `docker compose exec hub sy hub pair --kind phone` |
| Hub machine, CLI | `sy phone pair [--url U] [--name N]` = `sy hub pair --kind phone --wait` | Same |
| Board / desktop app | Settings → Phones → **Add phone** (also **Add machine**) → `POST /v1/pair/codes` | Owner session |
| `sy setup` step 5 | Calls the same code path as `sy phone pair` when the hub is local ("Just this computer"); for a remote hub it prints "open the board → Settings → Add phone, or run `sy phone pair` on the hub" | Local hub |
| `sy machine add` | Mints a `machine` code through its admin channel (`sy hub pair --kind machine --machine <id> --json`, locally or over the person's SSH to the hub host) and hands the remote the **secret** on stdin (`--code-stdin`), never as an argument; machines.md 4.2, 4.4 | Local hub, or SSH to the hub host |

`sy phone pair` on a machine that is not the hub (no hub database in `<data>`) fails with
that same instruction. A machine token can never make codes: that would let a stolen machine
token mint read access ("machine tokens can only write").

CLI output (interactive):

```
Scan with your phone's camera:

  ██▀▀▀▀▀██ ▄ ▀█▄ ██▀▀▀▀▀██
  …

  or open  https://hub.example.ts.net  and enter  482-913
  The code works once, for 10 minutes (until 14:13).

Waiting for the phone… (Ctrl-C stops waiting; the code stays valid)
Paired: iPhone · Safari (dev_01JA…). It can read and receive pushes; revoke it with
`sy hub token revoke dev_01JA…` or from the board's Devices list.
```

`--json` prints the `PairCode` object of api.yaml (with `secret`, `code`, `url`, `qr`) and,
with `--wait`, a second line `{"claimed": true, "device": {...}}` or
`{"claimed": false, "status": "expired"}`. Exit codes: 0 claimed or not waiting, 3 expired
while waiting, 2 bad flags.

The board's Add-phone dialog shows the QR, the digits, the URL and a countdown, and polls
`GET /v1/pair/codes/{id}` every 2 s while open; on `claimed` it shows the new device and
offers **Test push** (push.md). Closing the dialog does not cancel the code;
**Cancel** does (`DELETE /v1/pair/codes/{id}`).

## 3. Claiming

`POST /v1/pair/claim` takes no token: the code or the secret is the credential.

```json
{"secret": "Q2xhdWRlIGlzIG5vdCBoZXJl", "name": "iPhone · Safari", "cookie": true, "expect_kind": "phone"}
{"code": "482-913", "name": "gpu-box", "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", "expect_kind": "machine"}
```

| Field | Rule |
| --- | --- |
| `secret` or `code` | Exactly one. `code` accepts `482913`, `482-913`, `482 913`. |
| `name` | 1–60 characters after trimming; control characters removed. A code made with a `name` (`sy hub pair --name`) names the device whatever the claimer sends; otherwise this `name`, else `"<kind> <yyyy-mm-dd>"`. The web app sends a name prefilled with a guess from the user agent ("iPhone · Safari", "Pixel 9 · Chrome"): the typed-code form has it as an editable field, and a QR link claims at once with the guess and lets the person change it on the Paired screen (5.1). |
| `machine` | Required for `machine` and `desktop` codes (400 `bad_request` "machine is required" without consuming the code); must match the record envelope pattern. Ignored for other kinds. |
| `expect_kind` | Optional. When it differs from the code's kind the reply is 409 `conflict` and the code is **not** consumed (`sy connect` sends `machine`; the web app sends `phone` or `browser`). |
| `cookie` | Only for `phone` and `browser` codes. When true the token is not returned in the body; it is set as the `sy_session` cookie instead (3.2). |
| `client` | Optional, e.g. `sy/0.4.0`, `pwa/3f9c01ab`. Stored in `device.created_by` detail for the Devices list. |

Steps, in one write transaction:

1. Rate limiter (3.3) first; a locked caller gets 429 before any lookup.
2. Look up the live row by `secret_hash` or `code_hash` (`claimed_at IS NULL AND
   cancelled_at IS NULL AND expires_at > now`). None: 401 `unauthorized` "invalid or
   expired code" (the same body for unknown, used, cancelled and expired, so the reply never
   says whether a code existed); count a failure for typed codes.
3. `expect_kind` / `machine` checks (409 / 400, code untouched).
4. Create the device exactly as `sy hub token create` does (tokens.md 2–3: scopes from the
   kind, hashed token, hint), `created_by = "pair:<pair_code id>"`.
5. `UPDATE pair_code SET claimed_at = now, device_id = ?, claim_ip = ? WHERE id = ? AND
   claimed_at IS NULL`; zero rows changed means a concurrent claim won: roll back, 401.
6. Reply 200 `PairClaimReply`: `{device, kind, name, scopes, token?, hub: {url, version}}`.

The token appears in the reply at most once and is never logged. Replaying the same request
gets 401 (step 2).

### 3.1 After the claim, per kind

| Kind | Claimer | Then |
| --- | --- | --- |
| machine | `sy connect <url> --code 482-913` (and `sy setup --hub join:<url> --code …`) | Writes `<state>/token` and `hub.url`, installs the timer, one flush: tokens.md 4 unchanged, except the token comes from the claim. |
| desktop | Desktop app | As machine, `write` + `read`. |
| phone | The web app (`cookie: true`), or the native phone app (token in body, kept in the OS keychain) | Web app: register Web Push (pwa.md 6), offer a test push. |
| browser | The web app on a computer (`cookie: true`) | As phone. |
| owner | Never through `/v1/pair/claim` (409): owner links go to `/v1/session/owner` (section 4). | |

### 3.2 Cookie mode (the web app)

The phone's token lives only in the `sy_session` cookie, as the board's does (api.yaml
`/v1/session/login`), so no token is ever in JavaScript, `localStorage` or the service
worker's caches:

- `HttpOnly`, `SameSite=Strict`, `Path=/`, `Secure` (always: phone codes require https),
  `Max-Age` = 400 days (the longest Chrome keeps a cookie). The hub re-sets the cookie with a
  fresh `Max-Age` at most once a day when a cookie-authenticated request arrives
  (`Authenticator.touch`), so an app in use never loses it.
- One cookie per origin: claiming replaces any board cookie already there (the board
  session it held is revoked, as on logout).
- **Cookie requests that change state** (any method but GET/HEAD) must carry
  `Content-Type: application/json` (a cross-site form cannot send that without a CORS
  preflight, which the hub never answers) **and**, when an `Origin` header is present, it
  must equal the hub's own origin (2.3), else 403 `forbidden`. Bearer-token requests are not
  affected. `SameSite=Strict` already blocks cross-site sends; these two checks are defence
  in depth for browsers or proxies that get SameSite wrong.

### 3.3 Rate limits and brute force

| Limit | Value | Applies to |
| --- | --- | --- |
| Requests to `/v1/pair/claim` and `/v1/session/owner` per client IP | 20 a minute | All claims |
| Failed **typed-code** claims per client IP | 5 in 10 minutes, then that IP gets 429 for 10 minutes | `code` claims |
| Failed typed-code claims hub-wide | 20 in 10 minutes, then typed-code claims get 429 for 10 minutes for everyone | `code` claims |
| Failed secret claims per IP | 20 in 10 minutes, then 429 for 10 minutes | `secret` claims (only to keep logs quiet: guessing 128 bits is hopeless) |

- **Why this is enough.** At most 5 codes are live, so one guess hits with probability at
  most 5 / 10^6. The hub-wide cap allows at most 20 wrong guesses per 10 minutes, the
  lifetime of a code, so an attacker who can reach the hub has at most about a 1 in 10 000
  chance per 10-minute window **while codes are live**, and only during pairing, which is
  rare and short. Hubs are normally reachable only on a tailnet.
- **Why the QR path is not locked by the hub-wide cap.** A global lockout is a cheap
  denial of service (5 bad requests a minute keep everyone out). Secrets are not guessable,
  so a lockout of typed codes never blocks the QR flow; the owner can always pair by QR.
- **Concurrent guesses.** A typed-code attempt reserves a failure slot (per IP and hub-wide)
  when the limiter admits it and releases it when the claim ends (recording a failure if the
  code was wrong). In-flight attempts count as failures, so 400 simultaneous guesses from 20
  addresses get at most 5 per IP and 20 hub-wide to the database; the rest get 429.
- The limiter is in memory (`internal/hub/pair`): a restart clears it, which an attacker
  cannot cause from outside.
- **Client IP behind a proxy.** The hub listens on loopback behind `tailscale serve` or
  Caddy, so `RemoteAddr` is always the proxy. When `RemoteAddr` is a loopback address and
  `X-Forwarded-For` is present, the client IP is the **right-most** entry (the one the proxy
  added). Otherwise `RemoteAddr`. If every request appears to come from one address, the
  per-IP limits behave like the hub-wide one, which is still safe.
- Every failure, lockout and success is logged as one line without the code or secret:
  `pair claim fail ip=… kind=code`, `pair claim ok device=dev_… kind=phone code=pc_…`.

### 3.4 Replay and leaks

| Threat | Defence |
| --- | --- |
| Someone photographs the QR or code | Valid 10 minutes and single use; the board and CLI show "Paired: <name>" the moment it is claimed, and the device can be revoked. An unexpected claim is visible. |
| A claim request is replayed | The code is consumed in the same transaction as the device is made; a second claim gets 401. |
| Code in server logs | The secret and digits travel in the URL fragment (never sent) and in POST bodies (the hub logs only the path). |
| Database copy or backup | Only HMAC hashes; the pepper is outside the database (2.1). Someone with both the database and the state folder already has everything a paired device could read. |
| A token in a phone's browser | `HttpOnly` cookie only; never in JS-readable storage or the service worker cache. |
| A malicious page asks a paired phone's browser to act | `SameSite=Strict`, JSON content type and `Origin` checks (3.2). Phone scopes cannot change rules or devices other than itself anyway. |
| Lost phone | Board → Devices → Revoke, or `sy hub token revoke`. Its token, cookie and push targets stop at once (section 6). |

## 4. Owner sessions

Pairing codes, devices, global rules and channels need the `owner` scope (tokens.md 3).
The hub provides it with a one-time **sign-in link** made on the hub machine. Password and
passkey sign-in (the schema's `owner.password_hash`) are not implemented yet (desktop settings).

```
sy hub owner-link [--url U] [--ttl 10m] [--json]
→ https://hub.example.ts.net/#/owner?s=4mR8…   (works once, for 10 minutes)
```

- The link is a `pair_code` row of kind `owner` with a secret and no digits, same hashing and
  lifetime rules as section 2.
- The board's `#/owner` route posts `POST /v1/session/owner {"secret": "…"}`. The hub
  makes a `browser` device with scopes `["owner", "read", "push"]`, token prefix `sy_o_`,
  `expires_at` = now + 30 days, `created_by = "owner-link:<pair_code id>"`, name from the
  user agent, and sets it as the `sy_session` cookie (3.2 rules, `Max-Age` 30 days). The
  reply is `204`.
- **Sliding renewal.** An owner session that authenticates a request in its last 7 days
  (`auth.OwnerRenewWithin`) gets `expires_at` = now + 30 days, and a cookie request gets
  the cookie again with `Max-Age` 30 days. So an owner who opens the board at least once in
  any 30 days never signs in again; one left unused for 30 days ends. Because every use
  renews, the board shows no expiry warning banner: Settings, Sign-in shows "Owner access
  until <date>" (`/v1/me` `device.expires_at`) with "Renews when you use the board". A
  session that did end gives a 401 with `reason: owner_expired` (below), and the board says
  "Your owner access ended." There is no upper bound on a session that keeps being used;
  Devices lists it, and revoking it ends it at once.
- A refused link is `401` with a reason in `code`, `at` (when it was used, expired or
  was cancelled) and `ttl_s` (the lifetime the link was made with, so an expired link says
  "links made this way work for 1 minute" rather than a fixed 10 minutes): `link_used`, `link_expired`, `link_cancelled`, or `link_unknown` (no
  owner link has that secret, including another kind of code's secret). Unlike the one
  answer of a typed code (section 3), this tells nothing to a guesser: knowing a link's
  128-bit secret already means having the link. Every refusal counts against the rate
  limit as before. The board strips `s` from the address bar before it posts, sends each
  secret once per page, and says which reason it was (UI audit O1, O2).
- Why a link rather than a password now: whoever can run `sy` on the hub already owns the
  data; the link needs no password storage, cannot be guessed (128 bits), and works the same
  on a VPS. It is a strong owner login for a single-user hub.
- `owner` implies nothing by itself in code: scope checks stay explicit, which is why the
  session also carries `read` and `push`.
- Owner sessions appear in the Devices list as "browser · owner session" with their expiry,
  and are revoked like any device. `POST /v1/session/logout` revokes the session behind the
  cookie when its `created_by` starts with `login:` or `owner-link:` (and a cookie-mode
  phone or browser, section 6); never any other device.
- `auth.Lookup` treats `expires_at <= now` as revoked (401). A 401 for a token that belongs
  to a device carries `reason`: `revoked`, `expired` (a board login past 30 days) or
  `owner_expired` (api.yaml `Unauthorized`). The board's sign-in page shows it as "This
  browser was removed from the hub." or "Your owner access ended.".

### 4.1 Where owner links come from

1. **On the hub's computer:** `sy hub owner-link` (above), the recovery path.
2. **From any owner device:** board More → Devices → **Make another device owner**
   (`POST /v1/owner-links`, owner sessions only). It shows the link, its QR and "Open it on
   the other device, or paste it into the desktop app". The link is a `pair_code` of kind
   `owner` with `created_by = "owner:<device id>"`, always 10 minutes (the `pair.ttl_s`
   setting does not apply), single use, and consumed by `POST /v1/session/owner` exactly
   as one from `sy hub owner-link`. At most 3 such links are live; a fourth cancels the
   oldest. Rate limited per client IP as claims are (3.3); cookie requests follow the CSRF
   rules of 3.2. The hub log gets `pair owner-link made code=pc_… by=dev_… ip=… expires=…`
   and, when used, `pair owner ok device=dev_… code=pc_…`; never the secret or the link.
   The dialog polls `GET /v1/pair/codes/{id}` and says "Used: <device>" once claimed;
   **Cancel this link** is `DELETE /v1/pair/codes/{id}`.
3. **First start:** while the hub has never had an owner session
   (`store.HadOwnerSession`: no device with `created_by` `owner-link:*`, revoked ones
   included), `sy hub serve` makes one owner link at each start and prints it in one block
   on its log (stderr; the journal when it runs as a service) with a terminal QR, the board
   URL and "Open this on your computer to become owner. Pair everything else from the
   board." The `sy setup --hub here` summary prints the same block. The address is
   `--public-url` (or `hub.public_url`), else the listen address (a wildcard address shows as
   `127.0.0.1`). This link is in the log on purpose: it works once, for 10 minutes, and only
   until someone is owner; after the first owner session the hub never prints one again.

The signed-out board (`#/login`, "Sign in to this hub") offers the same three: paste an
owner link (accepted only for this hub's own origin and an `#/owner?s=` link), **Pair with a
code** (read-only), and a fold-out "On the hub's computer" with `sy hub owner-link`. After a
401 it shows the reason (`revoked`: "This browser was removed from the hub.",
`owner_expired`: "Your owner access ended."). Inside the desktop app's Hub view no CLI is
named: non-owner hints read "Use Sign in as owner on the Hub tab." The board knows it is in
the app from `/v1/me` `via.kind = desktop`, or, signed out too, from the user agent token
` SwitchyardDesktop/<version>` the app adds to the view's session (or `?in=desktop` in its
start URL).

## 5. The phone's side

### 5.1 Android (Chrome)

1. The camera opens `https://hub/#/pair?s=…&c=…` in Chrome.
2. The pair page claims at once (`secret`, `cookie: true`, `expect_kind: "phone"`), shows
   "Paired as Pixel 9 · Chrome", and removes `s` and `c` from the address bar
   (`history.replaceState`).
3. It offers **Install app** (the saved `beforeinstallprompt` event) and **Turn on
   notifications** (pwa.md 6). Chrome's installed app (WebAPK) shares Chrome's cookies, so
   the order does not matter.
4. **Name of this device**: an editable field prefilled with the name the hub gave (the
   guess, or the code's `--name`) and **Save** (`PATCH /v1/devices/self {name}`). Every
   Android phone in Chrome guesses "Pixel 9 · Chrome", so this is where a second one gets a
   name of its own. The typed-code form (5.2, 5.3) has the same field before **Pair**.
5. Leaving the screen before **Open the app** (a link, the back button, a typed address)
   does not bounce back to `#/pair`: the session cookie is set, so the app starts on the page
   asked for.

### 5.2 iPhone and iPad (Safari, iOS/iPadOS 16.4+)

A Home Screen web app on iOS has its own cookie and storage jar, separate from Safari, and Web
Push works only inside it. Claiming in Safari would pair Safari, not the app. So:

1. The camera opens the URL in Safari. The pair page sees iOS and
   `navigator.standalone !== true`, does **not** claim, and shows: "1. Tap Share → Add to Home
   Screen. 2. Open Switchyard from your Home Screen. 3. Enter **482-913**." with the digits
   large and a 10-minute countdown.
2. The installed app starts at `/#/`, has no cookie, gets 401 from `/v1/me`, and opens the
   pair screen (``): the digits field (address shown read-only: it is the app's own
   origin) → `POST /v1/pair/claim {"code", "cookie": true, "expect_kind": "phone"}`.
3. Then notifications (pwa.md 6): iOS allows the permission prompt only from a tap inside
   the installed app.

**Uncertain:** whether iOS 17+ copies Safari's cookies into a newly added Home Screen app.
If it does, the claim-in-Safari path would also work; the spec does not rely on it.

### 5.3 Typed pairing ("Type a code and address instead")

The installed web app can only pair with the hub it was installed from (cookies are per
origin). Its pair screen therefore shows the address read-only and asks only for the code.
If the person types a different address, the app offers "Open <address>" (a new tab of
that hub, to be installed separately). The native phone app, which is not bound to an origin,
takes both fields.

### 5.4 Camera scanning inside the app

In the web app, when `BarcodeDetector` with `qr_code`
exists (Chrome on Android), the pair screen offers **Scan** (`getUserMedia`, back camera,
detect every 300 ms, stop on the first `https://<same origin>/#/pair?` URL). Elsewhere
(Safari) it shows "Use your camera app to scan the code" instead. A QR for another origin is
handled as in 5.3.

## 6. Devices: list, rename, revoke

| Call | Who | Does |
| --- | --- | --- |
| `GET /v1/devices` | owner | Every device (revoked ones with `?all=true`): id, kind, name, hint, scopes, created, created by, last used (time, IP), expires, revoked, `current` (the caller), its push targets (kind, name, status). |
| `GET /v1/devices/{id}` | owner; any device for `self` | One device. `self` = the caller. The web app uses `GET /v1/devices/self` for its Settings page. |
| `PATCH /v1/devices/{id}` | owner; push-scoped devices for `self` | `{name?, prefs?}` (prefs: push.md 5.4). |
| `DELETE /v1/devices/{id}` | owner; push-scoped devices for `self` ("Unpair this phone") | Sets `revoked_at`, disables and deletes its push targets, cancels its pending deliveries, revokes board/owner sessions made from it (`created_by = login:<id>`). 204. Revoking `self` also clears the cookie. |
| `POST /v1/session/logout` | the cookie's device | **Sign out** in the board. Revokes the device behind the cookie when it is a board session (`login:`), an owner session (`owner-link:`) or a phone or browser paired in cookie mode (`pair:`), with the sessions made from it, then clears the cookie. So a signed-out browser does not stay listed in Devices. No other device is touched: the owner signing out of one browser leaves every phone and computer it paired alone. A token made with `sy hub token create` and pasted is not revoked. A request with a foreign `Origin` only clears the cookie. |

- Lists (Devices, Alerts' receivers, Settings → Notifications) tell apart devices that share
  a name: the first one paired keeps it, later ones get their place ("Pixel 9 · Chrome · 2",
  by `created_at`); a unique name shows as it is.
- Revocation takes effect on the next request: there is no auth cache (tokens.md 4). An
  open `/v1/stream` of a revoked device is closed within 30 seconds (the stream re-checks
  its device on each ping).
- A revoked phone's web app gets 401, clears its data cache (pwa.md 4) and shows the pair
  screen. Notifications already on its lock screen stay; no new ones arrive.
- The owner cannot revoke the owner session making the request through the list (use
  Sign out); the hub refuses with 409 to avoid locking the owner out by accident.
- `GET /v1/me` (any token) returns the caller: `{device: {id, kind, name, scopes,
  expires_at}, owner: bool, hub: {version, public_url}}`. The web app uses it at start to
  decide between the pair screen, the read-only board and owner settings.

## 7. Tests

- Codes: uniform digits (chi-square over 10^5 draws is not needed; check the rejection
  bound), no two live codes with equal digits, 6th code cancels the oldest, TTL boundaries
  (claim at `expires_at - 1 ms` works, at `expires_at` fails), hashing with the pepper.
- Claim: every row of section 3's field table; concurrent claims of one code (20
  goroutines) make exactly one device; replay gets 401; `expect_kind` mismatch leaves the
  code live; `cookie: true` sets the cookie and omits the token; owner codes refused.
- Limiter with a fake clock: per-IP and hub-wide typed-code lockouts, secret claims still
  work during a hub-wide lockout, right-most `X-Forwarded-For` only from loopback peers.
- Owner link: session scopes, expiry → 401, logout revokes, cannot revoke self from the
  list.
- Cookie CSRF rules: POST with cookie and `text/plain` → 403; wrong `Origin` → 403; bearer
  requests unaffected.
- CLI: `sy hub pair --json` shape; terminal QR decodes back to the URL (decode the half-block
  rendering into a matrix and compare with the encoder's); `sy connect --code` against a
  test hub writes the token and installs nothing on failure.
