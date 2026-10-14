# Keys, certificates and signed documents

Status: spec v1, F0 (the foundation track of the plan addendum). Built in F1:
`internal/keys` (formats, fingerprints, sealing, signing guard), `internal/hub/keys` (the hub
key, certificates, owner-key confirmation, revocations), `cmd/sy` (`sy keys`). The link that
carries these documents is [link.md](link.md); the account matrix is [peers.md](peers.md) 5.
Schema v7 ([schema.sql](schema.sql)). Unofficial; not affiliated with Anthropic.

Bearer tokens (tokens.md) do not change: they still authenticate every HTTP API call. The keys
here add three things tokens cannot give:

1. **Signatures a stolen database cannot make.** The hub signs every link frame and every
   device certificate with a key kept outside the database, and devices pin that key at
   pairing. A copy of `hub.db` or a backup can neither forge a notification nor a peer
   message.
2. **Encryption to a device.** Frames are sealed to keys made on the device, so the queue at
   rest and every proxy on the path see ciphertext only.
3. **An authority the hub does not hold.** The owner key, on your own computer behind a
   passphrase, certifies machines and signs the account matrix and revocations. The hub
   relays these documents; it can always narrow, and it can widen the matrix only through an
   owner session (5.4).

## 1. The keys

| Key | Algorithm | Made by | Kept in | Used for |
| --- | --- | --- | --- | --- |
| **Owner key** | Ed25519 | `sy keys init` on one computer (the "owner machine", normally the laptop) | `<state>/keys/owner.sealed`, sealed with a passphrase (3.1); recoverable from the printed recovery code (3.2) | Signs device certificates of machines and desktops, matrix documents, revocation lists |
| **Hub signing key** | Ed25519 | `sy hub serve` at first start | `<state>/hub_sign.key` on the hub machine, **not** in the database (3.4) | Signs every link frame, every device certificate (as the second signature), the link challenge, hub-made matrix and revocation documents |
| **Machine device keys** | Ed25519 (sign) + X25519 (box) | `sy` on the machine, at pairing (`sy connect --code`) or `sy agent enable` | `<state>/keys/device.json` | Link login; signing peer envelopes; opening frames and peer bodies sealed to it |
| **Desktop device keys** | as machine | the bundled `sy` of the desktop app (desktop.md C11), in the same state folder as the machine | as machine | as machine |
| **Phone device keys** | P-256 sign + P-256 key agreement, plus a 16-byte auth secret | The Android app's `sy-link` module at pairing (push.md 14) | Android Keystore, non-exportable (`sy_link_sign`, `sy_link_agree`); the auth secret wrapped by the Keystore key `sy_link_wrap` | Link login; opening RFC 8291 frames (push.md 13) |
| Browsers, owner sessions | none | — | — | Cookies only, as today (pairing.md 3.2, 4) |

Random bytes always come from `crypto/rand` (Go) or `SecureRandom` (Android). Everything on the
Go side is in the standard library of Go 1.27: `crypto/ed25519`, `crypto/ecdh`,
`crypto/ecdsa`, `crypto/hpke` (link.md 4.2), `crypto/hkdf`, `crypto/pbkdf2`, `crypto/aes`,
`crypto/cipher`, `crypto/sha256`. No new module.

## 2. Encodings and fingerprints

- Binary values in JSON are **base64url without padding** (`b64u`).
- Ed25519 and X25519 public keys are their 32 raw bytes. P-256 public keys are 65 bytes SEC1
  uncompressed (`0x04 ‖ X ‖ Y`), the same form as Web Push's `p256dh`.
- Ed25519 signatures are 64 raw bytes. P-256 signatures (the phone's link login only) are
  ASN.1 DER, which is what Android's `Signature("SHA256withECDSA")` returns; Go checks them
  with `ecdsa.VerifyASN1` over SHA-256 of the input.
- **Fingerprint** of a public key: `fp = lower(base32(SHA-256(pub)[0:15]))`, RFC 4648 alphabet
  without padding, so exactly **24 characters** (`a`–`z`, `2`–`7`). Shown in 6 groups of 4
  (`k7q2 m4xa 9d…` style: `k7q2-m4xa-pz3c-ufh6-2wbe-q5no`), used ungrouped in the QR and in
  JSON. 120 bits: enough against second-preimage attacks on a key you have already pinned.

## 3. Owner key

### 3.1 Sealed file

`<state>/keys/owner.sealed`, mode 0600, JSON:

```json
{
  "v": 1,
  "alg": "ed25519",
  "pub": "<b64u, 32 bytes>",
  "fp": "k7q2m4xapz3cufh62wbeq5no",
  "kdf": {"id": "pbkdf2-sha256", "iter": 600000, "salt": "<b64u, 16 bytes>"},
  "nonce": "<b64u, 12 bytes>",
  "ct": "<b64u: AES-256-GCM(seed) = 32 + 16 bytes>",
  "created_at": "2026-10-15T09:00:00.000Z"
}
```

- `key = PBKDF2-HMAC-SHA256(passphrase bytes, salt, iter, 32)`; `ct = AES-256-GCM(key, nonce,
  seed, aad = "sy-owner-v1\n" ‖ fp)`. The seed is the 32-byte Ed25519 seed
  (`ed25519.NewKeyFromSeed`).
- **Why PBKDF2 and not scrypt or argon2id:** both live in `golang.org/x/crypto`, which this
  module does not use (go.mod), and the owner key is unsealed only when the matrix or a
  device changes. 600 000 iterations is the OWASP 2023 figure for PBKDF2-SHA256, the same
  KDF `sy hub export --encrypt` already uses (machines.md 8.1). `iter` is stored, so a later
  `sy keys passwd` can raise it. The passphrase must be at least 12 characters (`sy` refuses
  shorter ones); the prompt suggests four or more random words.
- The passphrase is read **only from the controlling terminal** (`/dev/tty`,
  `golang.org/x/term` `ReadPassword`), twice when set. There is no flag, no environment
  variable, no stdin and no file option for it, so a Claude Code session (which has no
  terminal) cannot supply it even if the signing guard (8) were bypassed.
- The unsealed seed lives in memory only for the one command, and is zeroed after use
  (best effort: Go may have copied it).

### 3.2 Recovery code

`sy keys init` makes the owner key **from** a random 32-byte recovery secret `R`, so the
printed code is a full backup of the key, not a second password:

```
seed = HKDF-SHA256(secret = R, salt = "sy-owner-v1", info = "ed25519 seed", length = 32)
code = "SYR1-" + groups of 4 of base32(R) (52 characters, upper case) + "-" + check
check = first 4 characters of base32(SHA-256("sy-recovery-v1" ‖ R))
```

- Printed once, on the terminal only (refused when stdout is not a TTY), with: "Write this
  down and keep it offline. Anyone with it has your owner key. It replaces the passphrase
  and the file if you lose either." `sy keys init` then asks you to type the last two groups
  back before it continues.
- `R` and the code are never written to disk, never logged and never sent anywhere.
- `sy keys recover` asks for the code (case-insensitive; spaces and dashes ignored; `0`→`O`,
  `1`→`I`, `8`→`B`), checks `check`, rebuilds the same key, asks for a new passphrase and
  writes `owner.sealed`. Because the key is the same, no device has to be re-certified and no
  machine has to re-pin anything.
- Losing **both** the passphrase-sealed file and the code means a new owner key: every
  machine must be certified again (`sy keys certify --all`) and every machine's pin replaced
  (3.5). Plan addendum section B names this risk.

### 3.3 Registering the owner key with the hub

1. `sy keys init` (owner machine) makes the key, prints the fingerprint and the recovery code,
   writes `owner.sealed` and `owner.pub` and sends a **proposal** to the hub:
   `POST /v1/keys/owner` with a self-signed `owner_key` document (4.1) and the machine's own
   bearer token. The hub stores it as `owner_key.state = 'pending'`.
2. The proposal becomes active when confirmed:
   - on the board, Settings → Keys, by an **owner session** (pairing.md 4): "Owner key
     `k7q2-m4xa-…` proposed by laptop at 09:00. Is this the fingerprint your terminal
     printed?" → `POST /v1/keys/owner/confirm`; or
   - on the hub machine: `sy hub keys confirm-owner <fp>` (direct database access, like `sy hub
     token create`); or
   - automatically when the hub runs on the same machine and the same state folder (merged
     mode: `sy keys init` writes the row as `active` with `confirmed_by = 'cli'`).
3. A pending key that is not confirmed within 24 hours is deleted. There is at most one
   `active` row (`owner_key_active`). A second proposal while one is active is refused (409)
   unless it is an `owner_rotate` document signed by the active key (3.5).

Why a confirmation step: a stolen machine token must not be able to install its own owner
key on a hub that has none yet.

### 3.4 Hub signing key

- `<state>/hub_sign.key`: the 32-byte Ed25519 seed, raw, mode 0600, made at the first `sy hub
  serve` (or the first `sy hub pair`, whichever comes first) next to `pair_pepper`
  (pairing.md 2.1), and for the same reason kept out of the database: a database copy or
  backup alone cannot sign anything a device will accept.
- The fingerprint is shown by `sy hub keys show`, on the board (Settings → Keys), in
  `GET /v1/keys`, in the pairing QR (`h=`, 7) and in the claim reply.
- **Moving the hub.** `sy hub export` adds `state/hub_sign.key` to the archive, required like
  `pair_pepper` (machines.md 8.1 step 3; the export fails when it is missing), and `sy hub
  import` writes it back the same way (8.2 step 5). Without it every device would have to be
  paired again.
- **No rotation in F1.** A new hub key means every device re-pairs (phones) or re-runs `sy
  agent enable --repin-hub` after you confirm the new fingerprint on that machine. A lost key
  is the same case.

### 3.5 Pins on each device

Each device keeps the keys it trusts in its state folder (paths.md 3):

| File | Holds | Set |
| --- | --- | --- |
| `<state>/keys/hub.pub` | `{"pub", "fp", "pinned_at", "how"}` | At pairing from the claim reply, checked against the QR's `h=` when there was one; `how` = `qr`, `tofu` (typed code, or `sy connect --token`), `ssh` (`sy machine add`) |
| `<state>/keys/owner.pub` | `{"pub", "fp", "pinned_at", "how"}` | From the hub (`GET /v1/keys`, then the link's `welcome`) the first time one is active: trust on first use, printed by `sy agent status`; or out of band by `sy machine add` (`how = ssh`) or `sy keys trust-owner <fp>` |

A pin changes only by:

- an `owner_rotate` document signed by the **currently pinned** owner key and naming the new
  one (`sy keys rotate`, owner machine, passphrase). The agent replaces the pin and logs it;
- `sy keys trust-owner <fp>` / `sy agent enable --repin-hub` typed on that machine, which
  print the old and the new fingerprint and ask for confirmation (TTY only, signing guard 8).

The phone pins the hub key only (`h=` in the QR). It does not check owner signatures: nothing
it receives depends on them.

## 4. Signed documents

### 4.1 Wire form

Every signed object has one shape, whatever its type:

```json
{
  "type": "device_cert",
  "body": "<b64u of the body bytes>",
  "sigs": [
    {"by": "hub", "fp": "<hub fp>", "sig": "<b64u, 64 bytes>"},
    {"by": "owner", "fp": "<owner fp>", "sig": "<b64u, 64 bytes>"}
  ]
}
```

- Each signature is Ed25519 over `"sy-doc-v1\n" ‖ type ‖ "\n" ‖ body bytes`. The body bytes are
  signed **as they are** (no canonical JSON): a verifier checks the signature first, then
  decodes the body. Bodies are compact JSON written by `encoding/json`.
- `by` is `hub`, `owner` or `device`. A verifier looks up the key by `by` (its pin; for
  `device`, the key in the sender's certificate) and ignores `fp` except for messages ("signed by owner key k7q2-…, but this machine pins 3abc-…").
- Unknown body fields are ignored (new fields need no version bump); a body with `v` other
  than 1 is rejected.

| `type` | Body | Required signatures |
| --- | --- | --- |
| `owner_key` | `{"v":1,"pub","created_at"}` | Self-signed with that key (`by: owner`); proves possession at proposal |
| `owner_rotate` | `{"v":1,"old","new","at"}` (fingerprints; plus `new_pub`) | The old owner key **and** the new one |
| `device_cert` | 4.2 | `hub` always; `owner` for machines and desktops before they may send or receive peer messages |
| `matrix` | peers.md 5.1 | One: `owner`, or `hub` under the rules of 5.4 |
| `revocations` | 6 | One: `owner` or `hub` |
| `peer_envelope` | peers.md 8.2 | `device`: the sending machine's key, checked against its certificate |

### 4.2 Device certificates

```json
{
  "v": 1,
  "device": "dev_01JA4…",
  "kind": "machine",
  "name": "laptop",
  "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
  "sign": {"alg": "ed25519", "pub": "<b64u>"},
  "box": {"alg": "x25519", "pub": "<b64u>"},
  "reg": "ssh",
  "hub": "<hub fp>",
  "issued_at": "2026-10-15T09:00:00.000Z"
}
```

| Field | Rule |
| --- | --- |
| `kind` | The device row's kind: `machine`, `desktop` or `phone`. |
| `machine` | For machine and desktop; `null` for a phone. |
| `sign.alg` / `box.alg` | `ed25519` / `x25519` for machine and desktop; `p256` / `p256` for a phone. |
| `reg` | How the hub learned the keys: `qr` (sealed by the QR, 7.2), `tofu` (over TLS with a bearer or a typed code), `ssh` (pushed by `sy machine add` over your SSH session, machines.md 4.2), `local` (the hub's own machine in merged mode). |
| `hub` | The hub key's fingerprint, so a certificate cannot be replayed against another hub. |

Lifecycle:

1. **Hub signature at registration.** When a device's keys arrive (claim, `PUT
   /v1/devices/{id}/keys`, or `sy machine add`), the hub builds the body, signs it and stores
   `device_key` (body bytes in `cert`, `hub_sig`). On every link login the hub verifies
   `hub_sig` over `cert` with its own key before trusting `sign_pub` (link.md 3): a row edited
   in the database fails here, so a database-only attacker cannot add a device key or swap
   one.
2. **Owner signature.** `sy keys certify` (owner machine) lists the machine and desktop devices
   whose certificate has no owner signature, with name, `reg`, created time and the key
   fingerprints, asks which to certify (`--all`, or ids), asks for the passphrase once and
   uploads the signatures (`POST /v1/devices/{id}/cert`). `sy machine add` does the same for
   the machine it adds in the same run, because it already holds your SSH session and read
   the remote's keys over it (`reg = ssh`): no fingerprint comparison needed. For `reg =
   tofu` devices, `sy keys certify` shows the fingerprint `sy agent status` prints on that
   machine and asks you to compare.
3. A certificate never changes. New keys = new certificate (the old row is replaced, and
   the old keys are listed in the next revocation document, 6).

Phones are certified by the hub signature only: the hub learned their keys under the QR seal
(`reg = qr`) or, for a typed code, over TLS (`reg = tofu`, shown as "Not verified" with a
check code, push.md 14.7). `sy keys certify` may add an owner signature to a phone; nothing
requires it.

## 5. Who may sign what

### 5.1 Summary

| Change | Owner key (terminal, passphrase) | Owner session (board, desktop app) | Any device with a link or a token |
| --- | --- | --- | --- |
| Certify a machine or desktop | Yes | No | No |
| Widen the matrix, resume after pause, raise the loop limit | Yes | Yes, hub-signed (5.4) | No |
| Narrow the matrix, pause | Yes | Yes | Yes (phones too), hub-signed |
| Revoke a device | Yes | Yes (Devices → Revoke), hub-signed | Only itself ("Unpair this phone", pairing.md 6) |

### 5.2 Narrowing vs widening

A matrix document **widens** compared with the one before it when any of these is true:
an allowed directed pair is added, `same_account` goes from false to true, `paused` goes
from true to false, or `max_turns` grows. Anything else narrows or is neutral. The rule is
computed by one function (`peers.Widens(old, new)`) used by the hub, the agent and the CLI.

### 5.3 Owner-key documents

`sy peers allow …` and the other widening commands (peers.md 7) build the next matrix body,
show the change, ask for the passphrase, sign with `by: owner` and send it with `PUT
/v1/peers/matrix {"doc": …}`. The hub accepts an owner-signed document from **any**
authenticated caller (a machine bearer is enough: the signature is the authority), provided
its `version` is the current one + 1 and the signature verifies against the active owner
key. The hub then queues it to every machine (link.md 6, frame `matrix`).

### 5.4 Hub-signed documents

The hub signs a matrix or revocation document itself (`by: hub`) when the change comes from:

- an owner session on the board or the desktop app (any change; plan addendum H4: "either
  one");
- any authenticated device, phones included, when the change narrows (`POST
  /v1/peers/pause`, `PUT /v1/peers/matrix` with an `edit` that narrows; a revocation of
  itself).

`matrix_doc.made_by` records the device that asked; `widens` records the 5.2 result.

Each agent decides whether it trusts a hub-signed **widening**: `config.toml` `[peers]
hub_may_widen = true` (default, matching H4). With `false`, a machine accepts hub-signed
documents only when they narrow its current one, so even a compromised hub process (which
holds the hub key) cannot widen what that machine delivers; widening then needs the owner
key. Narrowing documents are always accepted from either signer. This is the one place the
H4 default ("board sign-in is enough") trades against the addendum's "the hub can only
narrow": with the default, a database or backup thief still cannot widen, but an attacker
running code as the hub can.

## 6. Revocations

Body:

```json
{
  "v": 1,
  "version": 4,
  "issued_at": "2026-10-15T09:10:00.000Z",
  "devices": [
    {"device": "dev_01JA…", "keys": ["<sign fp>", "<box fp>"], "at": "2026-10-15T09:10:00.000Z", "reason": "lost"}
  ]
}
```

- Cumulative: version N+1 must contain every entry of version N (a verifier rejects a list
  that drops one). Entries are never removed; a re-paired device has new keys and a new
  certificate.
- Made by `sy keys revoke <device>` (owner key) or by the hub on any device revocation
  (`DELETE /v1/devices/{id}`, `sy hub token revoke`): the hub then sets `device.revoked_at`
  as today, deletes the device's `link_queue` rows, closes its link with 4403 (link.md 8)
  and queues the new list to every machine (frame `revoke`). An owner-signed revocation
  uploaded with `POST /v1/keys/revocations` makes the hub revoke the device the same way.
- An agent drops every peer envelope signed by a listed key, refuses to seal to it, and
  forgets the device's certificate. Effect: within one ping on every connected machine
  (link.md 7), at once on the hub.

## 7. Pairing changes

Pairing.md 2.2's QR gains two fields; everything else in pairing.md stays.

```
https://hub.example.ts.net/#/pair?s=<secret>&c=<digits>&h=<hub fp>&k=<seal key>
```

| Part | Meaning |
| --- | --- |
| `h` | The hub signing key's fingerprint (24 characters). The app checks the hub key in the claim reply against it and refuses a mismatch ("This hub's key does not match the QR code"). This replaces pairing.md 2.2's deviation note: the QR now does carry the hub's key. |
| `k` | `b64u(HMAC-SHA256(pair_pepper, "seal:" ‖ secret)[0:16])`, 22 characters. Recomputed by the hub at claim time from the secret in the request: nothing new is stored. |

Older web pages ignore the two parameters. The web app (pwa.md) does not use them.

### 7.1 Claim with keys

`POST /v1/pair/claim` (pairing.md 3) takes one of two new optional fields:

- `keys`: the device's public keys in the clear, `{"sign": {"alg", "pub"}, "box": {"alg",
  "pub"}, "auth"?}` (`auth`, 16 bytes, phones only). Used by typed-code claims and by `sy
  connect --code` (machines). Registration `reg = tofu`.
- `keys_sealed`: the same JSON sealed with the QR's `k` (phones scanning the QR):

```
regkey      = HKDF-SHA256(secret = k (16 bytes), salt = "sy-reg-v1", info = <secret string bytes>, length = 16)
nonce       = 12 random bytes
keys_sealed = b64u(nonce ‖ AES-128-GCM(regkey, nonce, keys JSON, aad = "sy-reg-v1"))
```

Hub steps, inside the claim transaction after pairing.md 3 step 3:

1. Recompute `k`, open `keys_sealed`. Failure → **400** `bad_request` "key registration
   invalid", and the code is **not** consumed, so a tampered request cannot burn the real
   phone's code.
2. Check the keys: P-256 points on the curve (`ecdh.P256().NewPublicKey`, `ecdsa` parse),
   Ed25519/X25519 32 bytes, `auth` 16 bytes, algorithms matching the code's kind.
3. Create the device (step 4), then `device_key` with `reg = qr | tofu` and the hub-signed
   certificate, and for phones the `link` push target (push.md 13.7), in the same transaction.

A claim without either field works as before (no keys yet; `PUT /v1/devices/{id}/keys`
later, as for devices paired before F1).

### 7.2 What the QR seal buys

A proxy that terminates TLS (a misconfigured `tailscale serve`, a Funnel node you do not
trust, a Cloudflare tunnel) sees the claim, the secret and even the bearer, but not `k`
(it is only in the camera image, and needs the pepper to compute). It cannot swap in its
own phone key: the hub refuses a seal it cannot open, and the pairing fails visibly. It
cannot decrypt frames (no private key, no auth secret) and cannot forge them (no hub key).

### 7.3 Claim reply

`PairClaimReply` gains, when the hub has keys (always from F1 on):

```json
"hub_key": {"alg": "ed25519", "pub": "<b64u>", "fp": "<24 chars>"},
"owner_key": {"pub": "<b64u>", "fp": "…"},
"cert": {"type": "device_cert", "body": "…", "sigs": [ … ]},
"link": {"urls": ["wss://hub.example.ts.net/v1/link"], "ping_s": 180},
"reg": "qr"
```

`owner_key` is null until one is active. `link.urls` comes from the link channel settings
(push.md 13.6); a device tries them in order.

## 8. The signing guard

Every command that unseals the owner key or replaces a pin (`sy keys init | certify | revoke |
passwd | recover | rotate | trust-owner`, `sy agent enable --repin-hub`, and the widening
`sy peers` commands) first runs `keys.CheckNotUnderClaude()`, and refuses with exit 4 and:

```
sy: refusing to sign: this command runs inside a Claude Code session (pid 4242, claude).
Owner-key changes need you at a terminal that Claude Code did not start, or the board's owner sign-in.
```

The check, in order; the first hit refuses:

1. **Own environment.** `CLAUDECODE` is set (Claude Code sets `CLAUDECODE=1` for the Bash tool
   and hooks), or `CLAUDE_CODE_ENTRYPOINT` is set, or `CLAUDE_CODE_MESSAGING_SOCKET` is set.
2. **Ancestors.** Walk from `os.Getppid()` towards pid 1, at most 64 steps, stopping at pid 1
   or a pid that cannot be read. For each process:
   - **Linux:** ppid from `/proc/<pid>/stat` (the field after the last `)`), the executable from
     `readlink /proc/<pid>/exe`, `argv[0]` from `/proc/<pid>/cmdline`, the name from
     `/proc/<pid>/comm`, and the environment from `/proc/<pid>/environ` (readable for your own
     processes; an unreadable one is skipped, not a refusal).
   - **macOS:** `ps -o ppid=,comm=,args= -p <pid>` per step (no environment of other
     processes).
   - **Windows:** owner-key commands are not available in F1 ("use the board's owner sign-in");
     they exit 4 before any check.
   - It is Claude Code when any of these hold: the base name of the executable, of `argv[0]`
     or of `comm` is `claude` (or `claude.exe`); the executable path contains
     `/claude/versions/` (the native installer's layout, where the file is named after the
     version) or `/@anthropic-ai/claude-code/`; `argv[0]` or `args` ends in `claude` with any
     directory; the environment holds `CLAUDECODE=1`.
3. **A terminal.** `/dev/tty` must open; otherwise refuse ("needs a terminal").

**Uncertain:** the exact executable and `comm` names of every Claude Code install (native
installer, npm, Homebrew, the desktop app's bundled CLI). The fallbacks above overlap on
purpose: the environment marker catches a renamed binary, the ancestry catches a cleared
environment, and the terminal and passphrase requirement (3.1) holds even if both are
evaded, because a session has neither. The check is a guard against a session widening its
own reach by accident or by injection, not a sandbox: the OS user remains the trust
boundary, as for Claude Code itself.

The same check (steps 1 and 2 only) is how `sy peers send` finds its calling session
(peers.md 4.1): there a hit is required instead of refused.

## 9. Schema v7

Migration `internal/hub/store/migrations/0007_keys_link.sql` (the link tables are explained in
link.md 5):

| Table | Holds |
| --- | --- |
| `owner_key` | Owner public keys: `pending` proposals, the one `active` key, `retired` ones after a rotation. |
| `device_key` | One row per device that has link keys: algorithms, public keys, `reg`, the certificate body bytes, `hub_sig`, and `owner_fp`/`owner_sig` once certified. |
| `revocation_doc` | Every version of the revocation list with its signer and signature. |
| `link_state`, `link_queue` | link.md 5. |

`device_key.cert`, `*_doc.body`, signatures and `link_queue.body` are `BLOB` columns in STRICT
tables: the store binds `[]byte`, never a string.

## 10. Files (paths.md 3)

```
<state>/keys/                  mode 0700
  device.json                  this device's keys: {"v":1,"sign":{"alg":"ed25519","seed":b64u},"box":{"alg":"x25519","priv":b64u},"device":"dev_…","created_at"}, mode 0600
  owner.sealed                 owner machine only (3.1), mode 0600
  owner.pub                    pinned owner key (3.5)
  hub.pub                      pinned hub key (3.5)
  certs.json                   certificates of other devices seen in peer envelopes, by device id (cache)
  matrix.json                  newest accepted matrix document, as received (wire form)
  revocations.json             newest accepted revocation document
<state>/hub_sign.key           hub machine only (3.4), mode 0600
```

`sy uninstall --purge` removes `<state>/keys/` and `<state>/hub_sign.key` with the rest of the
state folder; it prints a warning first when `owner.sealed` is present ("this deletes your
owner key; you need the recovery code to sign again").

## 11. CLI

```
sy keys init                     owner machine: make the owner key, print fp + recovery code, propose it to the hub
sy keys status [--json]          pins (hub, owner), this device's keys and certificate state, pending proposals
sy keys certify [<device>…|--all] [--yes]   owner-sign machine/desktop certificates (passphrase once)
sy keys revoke <device> [--reason R] [--yes]  owner-signed revocation, uploaded; the hub revokes the device
sy keys passwd                   new passphrase (and iteration count) for owner.sealed
sy keys recover                  rebuild owner.sealed from the recovery code
sy keys rotate                   new owner key, owner_rotate document signed by both (rare; re-certifies all machines)
sy keys trust-owner <fp>         replace this machine's owner pin by hand (prints old and new)
sy keys show-owner               print the owner public key and fp (used by sy machine add)

sy hub keys show [--json]        hub machine: hub key fp, active owner fp, pending proposals
sy hub keys confirm-owner <fp>   hub machine: activate a pending owner key
```

All of them run the signing guard (8) where 8 says so; every command that changes something
prints what it will sign and asks unless `--yes` (setup.md rule 1). `--yes` never skips the
passphrase.

## 12. Tests (F1)

- Fingerprint, base32 and recovery-code goldens; recovery round trip (code → seed → same
  public key); typo handling and the check group.
- Sealed file: wrong passphrase fails, changed `fp` or `pub` fails (AAD), `iter` honoured.
- Document signing and verification for every type; each field of a body changed → reject;
  `by` swapped → reject; a hub signature over a `device_cert` checked on link login, with a
  `device_key.sign_pub` edited in the database → login refused.
- `Widens` table tests (each widening case, each narrowing case, neutral).
- Revocation list that drops an entry → reject; lower version → ignored.
- Signing guard with a fake `/proc` tree: claude parent, grandparent, `versions/2.1.299`
  executable, `CLAUDECODE` in an ancestor's environ, reparented process (ppid 1, no hit),
  64-step cap, unreadable entries, no TTY. macOS path with a fake `ps`.
- QR seal: open with the right `k`; wrong `k` → 400 and the code stays live; typed claim with
  `keys` → `reg = tofu`; algorithm/kind mismatch → 400.
- Export/import carries `hub_sign.key`; export without it fails.
