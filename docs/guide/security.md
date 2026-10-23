# Security and privacy

What Switchyard sends, what the hub keeps, who can do what, and what it does not protect
against. The specs hold the detail: [record.md](../spec/record.md) (what a record may contain),
[tokens.md](../spec/tokens.md), [pairing.md](../spec/pairing.md), [keys.md](../spec/keys.md),
[link.md](../spec/link.md), [push.md](../spec/push.md) and [peers.md](../spec/peers.md).

To report a vulnerability, follow [SECURITY.md](../../SECURITY.md). Do not open a public issue.

## Principles

- **Your hub, your data.** The hub is a program you run. There is no hosted Switchyard service
  and nothing is sent to the project's authors.
- **The hub never talks to Anthropic and never holds a Claude login.** It only receives what
  your machines send.
- **Claude logins are never touched.** `sy` never reads, copies, backs up or sends
  `.credentials.json`, a Keychain item, `oauthAccount` or `sessions/*.key`. Logging in is always
  Claude Code's own `claude auth login`, run by you.
- **Hooks never decide anything.** `sy hook` always exits successfully with no decision, so it
  cannot approve a permission or type into a session.
- **No automatic account switching.** Switchyard may show which account has room; it never
  switches or rotates accounts to get around limits.
- **The hub cannot command clients.** `sy` never runs anything it receives from the hub. Hub
  replies to ingest are lists of accepted record ids. A compromised hub can leak what it stores;
  it cannot reach into your machines.

## What leaves each machine

| Never | By default | Only if you turn it on (or off) |
| --- | --- | --- |
| Cookies, Claude Code tokens, prompts, code, file contents, tool input and output, permission suggestions | Session ids, states and times, the project folder name, the account email and organisation, models and token counts, limit percentages and reset times, Claude Code's own notification text ("Claude needs your permission to use Bash"), lengths of removed fields | The tail of Claude's last message (up to 400 characters) and the text of an AskUserQuestion question, used for the board, question detection and push text. **On by default for your own hub.** Turn it off with `sy config set privacy.last_message false` |

`sy hook` strips content fields before a record is written to the local outbox; the exact
field-by-field rules are in [record.md](../spec/record.md).

## What the hub stores

Everything is in one SQLite file, `hub.db`, in the hub's data folder (`sy paths`).

| Group | Contents |
| --- | --- |
| Records | Every record exactly as received (after the client-side stripping above), kept by default so state and analytics can be rebuilt |
| Accounts | Account ids, emails, organisation names and plans, your labels and colours |
| Machines and sessions | Machine names, OS, `sy` version, last seen; session ids, folders, projects, titles, states, Remote Control links |
| Usage | Token counts per reply, limit samples, web usage responses (the raw numbers claude.ai returned to the desktop app), limit windows |
| Devices | One row per paired device with the **SHA-256 hash** of its token, its scopes, and when it was used or revoked |
| Alerts | Rules, alerts, delivery log, push targets (Web Push subscriptions, ntfy topics, Android link targets) |
| Keys and peers | Device certificates, the owner key's public part, matrix versions, peer message and audit **metadata** (never the text) |

Treat `hub.db`, its backups and `sy hub export` archives as sensitive: they hold project names,
emails, session snippets, token hashes, the Web Push private key and the pull key. Keep them on
an encrypted disk. Export archives can be encrypted with `sy hub export --encrypt`. The hub
signing key lives outside the database (`<state>/hub_sign.key`), so a copy of `hub.db` cannot
forge frames or certificates.

Prune old data with `sy hub prune` ([maintenance.md](maintenance.md#pruning)).

## claude.ai sign-ins

The desktop app and the Android app each show one claude.ai tab per account.

- You sign in on Anthropic's own page, in that tab.
- Each tab keeps its sign-in in its own local browser session on that device (an Electron
  partition on the desktop, a WebView profile on Android).
- Switchyard never reads, exports or sends the tabs' cookies or tokens. Nothing from a tab's
  session reaches the hub.
- The desktop app's account tabs present a plain Chrome user agent string, because claude.ai's
  bot protection rejects Electron's default one. claude.ai sees an ordinary Chrome browser; the
  string does not change what the tab sends or stores. Details:
  [claude-ai.md](claude-ai.md#the-browser-identity-of-the-tabs).
- The Android app asks claude.ai only which account a tab is signed in to, so it can warn about
  a tab signed in to the wrong account. It does not read chats, page content or usage.

**Usage reading** in the desktop app is the one part of Switchyard that calls claude.ai's
undocumented endpoints. It is off for each account until you accept a warning for that
account. When on, a small script inside that account's tab reads the numbers claude.ai shows
under Settings → Usage every 5 minutes and hands them to `sy`. It stops on 401, 403 or 429 and
backs off on errors. These endpoints can change or break at any time, and automated access may
not be allowed by your plan's terms. See [usage.md](usage.md).

## Tokens and devices

Every device has its own token; the hub stores only its hash and shows the token once.

| Device | Prefix | Scopes | Can |
| --- | --- | --- | --- |
| Machine | `sy_m_` | write | Send records for its own machine id and its own sessions only. Cannot read anything. Can also propose an owner key (it does nothing until you confirm its fingerprint) and pause peer messaging or narrow who may message whom (it cannot widen or resume) |
| Desktop app | `sy_d_` | write, read | Send web usage for its machine; read the board |
| Phone (Android app, web app) | `sy_p_` | read, push | Read; register its own push targets; snooze and mark alerts seen |
| Browser | `sy_b_` | read, push | As phone, for the web app on a computer |
| Owner session | `sy_o_` | owner, read, push | Everything: devices, rules, pairing codes, matrix widening. Cookie only, 30 days, renewed when used |

A stolen machine token can add fake records for that machine, pause peer messaging and file an
owner-key proposal that you would have to confirm, but it reads nothing and cannot widen
anything. The hub ties each session to the machine that first reported it and ignores records
from any other machine about it, so the token cannot end, rename or raise alerts on other
machines' sessions ([record.md section 5.1](../spec/record.md#51-sessions-belong-to-one-machine)). Revoke any device on the board
(Devices) or with `sy hub token revoke <id|name>`.

**Pairing codes.** A code from `sy hub pair`, `sy phone pair` or the board's Add phone / Add
machine is single use, lasts 10 minutes by default, is rate limited, and is tied to one device
kind. The QR code also carries the hub key's fingerprint and a seal for the phone's keys, so a
phone paired by QR knows it reached your hub.

**Owner sign-in.** `sy hub owner-link`, run on the hub machine, prints a one-time link (10
minutes). Opening it signs that browser in as the owner for 30 days. Whoever can run `sy` on
the hub machine already owns the data, so this needs no password.

## Transport

- The hub listens on `127.0.0.1` by default and the shipped Docker files never publish it on
  `0.0.0.0`.
- Clients use `https://` except to loopback or Tailscale (a `*.ts.net` name or a `100.64.0.0/10`
  address). Plain `http://` to any other address, including a short name such as `myhub`, is refused unless you pass `--insecure-lan` (to `sy connect` or `sy hub serve`).
- Phones need `https://` for the web app, Web Push and the Android app's release build. Use
  `tailscale serve` or a reverse proxy with TLS ([install.md](install.md)).
- Never expose the hub port to the internet directly. The read API returns project names and
  session snippets; it always needs a token.

## Keys and signatures

On top of tokens, Switchyard uses keys for three things a token cannot give:

1. **Signatures a stolen database cannot make.** The hub signs every frame on the link and
   every device certificate with a key kept outside `hub.db`. Devices pin that key when they
   pair.
2. **Encryption to a device.** Frames and peer messages are sealed to keys made on the
   receiving device, so the hub's queue and every proxy see ciphertext only. Android
   notifications sent over the app's link are sealed to a key in the Android Keystore.
3. **An authority the hub does not hold.** The owner key, on your own computer behind a
   passphrase, certifies machines, binds the accounts each machine may speak for, and signs
   widenings of the peers matrix. The hub can always narrow; it can widen only for an owner
   sign-in, and only on machines that allow it (`peers.hub_may_widen`).

See [peers.md](peers.md) for the owner key steps.

**Accounts per machine.** The peers matrix is a rule between accounts, but each machine says
itself which account a session belongs to. So `sy keys certify` also signs, per machine, the
list of accounts it may speak for, and both the hub and the receiving machine refuse a peer
message whose sender account is not on the sending machine's list (`unbound_account`). A
session on one machine therefore cannot pose as an account that lives only on another
machine. It can still act as any account bound to its own machine, as it could by starting
Claude Code in that account's folder. Details:
[keys.md section 4.3](../spec/keys.md#43-machine-accounts).

**After upgrading.** Machines certified before this list existed have none, and their peer
messages to other machines are refused until you run `sy keys certify --all` on the owner
machine once more. It shows the accounts the hub sees on each machine; compare them with
`sy keys status` on that machine before you confirm. `sy doctor` warns on a machine with no
bound accounts. Update the hub and the machines together: a new `sy` refuses messages relayed
by an older hub, which does not pass the list on. Removing an account from a machine's list
does not reliably take it away (an older signed list may still be presented); renew that
machine's keys for that.

## Threat model

| Attacker has | Can | Cannot |
| --- | --- | --- |
| A machine token | Send fake records for that machine and its sessions | Read anything, change other machines' sessions, pair devices, widen peers |
| A phone or browser token | Read the board, snooze alerts, narrow peers | Change rules or devices, widen peers |
| A read-only copy of `hub.db` or a backup | Read everything stored there | Forge link frames, notifications, certificates or peer messages (the hub signing key is not in it) |
| Write access to `hub.db` only | Add or change rows | Mint an owner session, install an owner key (needs both signatures) |
| Code running as the hub's OS user | Read `hub_sign.key` and sign as the hub; with the default `hub_may_widen = true`, widen the peers matrix | Sign as the owner without the passphrase |
| A Claude Code session on your machine | Anything your OS user can do on that machine, including bypassing Switchyard locally and sending peer messages as any account bound to that machine | Type your owner passphrase; pose as an account bound only to another machine; make another machine deliver a peer pair its matrix refuses for the accounts bound to this machine |
| A network observer between devices and the hub | See traffic metadata | Read traffic (TLS, and sealed frames on the link) |

**The signing guard.** Owner-key commands refuse to run inside a Claude Code session or
without a terminal (exit 4). It stops a session from signing by accident or because a message
told it to. It is not a sandbox: a process running as your user can evade each check. What
really protects the owner key is the passphrase, which is never stored and which a session
cannot type into your terminal. Details: [keys.md section 8.1](../spec/keys.md#81-what-the-guard-can-and-cannot-do).

If Claude Code sessions run on the hub machine as the hub's user (merged mode, or a server that
is also the hub), set `sy config set peers.hub_may_widen false` on every machine, or run the hub
as its own OS user. `sy doctor` warns about this.

## Who can read push text

| Channel | Readable by | Private pushes protect |
| --- | --- | --- |
| Android app (link) | Only the app; sealed end to end to a Keystore key | The lock screen and notification history |
| Web Push | Only the subscribed browser (RFC 8291 encryption); the push service sees ciphertext, size and timing | The lock screen and notification history |
| ntfy, self-hosted | Your ntfy server | The lock screen, and the server if it is not yours |
| ntfy.sh | ntfy.sh and anyone who learns the topic name | Content; **private is forced on** for ntfy.sh targets unless you turn it off on the target |

Private pushes replace the text with "1 session needs you" and similar, and the link names no
project, account or session. On ntfy they keep the Snooze and Seen buttons, so the ntfy server
also sees the hub's address and two links with a one-time token that lasts 24 hours and can
only snooze or mark that one alert seen. That is metadata, not message text; if it matters
to you, use a self-hosted ntfy server on your tailnet or the Android app. Turn it on per device or as the hub default ([alerts.md](alerts.md)).

## Supply chain

Release archives come with `SHA256SUMS` and GitHub build provenance attestations. Desktop
packages have `SHA256SUMS-desktop` and attestations too.

The two checks prove different things:

- **The checksum** shows the download is intact. `SHA256SUMS` comes from the same release as
  the archive, so it cannot tell you who built the archive: someone who could replace the
  archive on the release could replace the sums too.
- **The attestation** shows the file was built by this repository's release workflow. It
  needs the `gh` CLI.

The install scripts and `sy update` check the attestation whenever `gh` is installed, and stop
if it fails. When `gh` is not installed they say so and install on the checksum alone. To make
that an error, set `SY_VERIFY_ATTESTATION=1`; to skip the check (for example offline), set
`SY_VERIFY_ATTESTATION=0`, or use `sy update --no-attestation` or
`update.verify_attestation = false`. With `sy update --machines`, a machine that cannot reach
the internet gets the archive from you after your `sy` checked it. To check one yourself, run
`gh attestation verify <file> --repo mraj602/switchyard`.

The install scripts only talk https (TLS 1.2 or newer); `SY_INSTALL_BASE_URL` must be https,
except for a loopback address used in tests. The desktop packages are not code-signed
([desktop.md](desktop.md#unsigned-builds-on-macos-and-windows)). The Android APK is signed
with the maintainer's own key; check it as [android.md](android.md#verify-the-apk) says. You
can also build from source ([install.md](install.md)). The Android app's updater installs an update only if its signing
certificate equals the installed app's.
