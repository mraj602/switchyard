# Keys, certificates and signed documents

Status: spec v1. Implemented in:
`internal/keys` (formats, fingerprints, sealing, signing guard), `internal/hub/keys` (the hub
key, certificates, owner-key confirmation, revocations), `cmd/wy` (`wy keys`). The link that
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
   owner session, and only on machines that allow it (5.4).

## 1. The keys

| Key | Algorithm | Made by | Kept in | Used for |
| --- | --- | --- | --- | --- |
| **Owner key** | Ed25519 | `wy keys init` on one computer (the "owner machine", normally the laptop) | `<state>/keys/owner.sealed`, sealed with a passphrase (3.1); recoverable from the printed recovery code (3.2) | Signs device certificates of machines and desktops, matrix documents, revocation lists |
| **Hub signing key** | Ed25519 | `wy hub serve` at first start | `<state>/hub_sign.key` on the hub machine, **not** in the database (3.4) | Signs every link frame, every device certificate, the link challenge, the confirmation of the owner key (3.3), hub-made matrix and revocation documents |
| **Machine device keys** | Ed25519 (sign) + X25519 (box) | `wy` on the machine, at pairing (`wy connect --code`) or `wy agent enable` | `<state>/keys/device.json` | Link login; signing peer envelopes; opening frames and peer bodies sealed to it |
| **Desktop device keys** | as machine | the bundled `wy` of the desktop app (desktop.md C11), in the same state folder as the machine | as machine | as machine |
| **Phone device keys** | P-256 sign + P-256 key agreement, plus a 16-byte auth secret | The Android app's `wy-link` module at pairing (push.md 14) | Android Keystore, non-exportable (`wy_link_sign`, `wy_link_agree`); the auth secret in the module's private storage (push.md 14.3) | Link login; opening RFC 8291 frames (push.md 13) |
| Browsers, owner sessions | none | — | — | Cookies only, as today (pairing.md 3.2, 4) |

Random bytes always come from `crypto/rand` (Go) or `SecureRandom` (Android). Everything on the
Go side is in the standard library of Go 1.27: `crypto/ed25519`, `crypto/ecdh`,
`crypto/ecdsa`, `crypto/hpke` (link.md 4.2), `crypto/hkdf`, `crypto/pbkdf2`, `crypto/aes`,
`crypto/cipher`, `crypto/sha256`. No new crypto module (the passphrase prompt uses
`golang.org/x/term`, already in go.mod). Byte strings follow link.md 1's rules (`‖`, UTF-8,
`"\n"`, `decimal`).

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
  seed, aad = "wy-owner-v1\n" ‖ fp)`. The seed is the 32-byte Ed25519 seed
  (`ed25519.NewKeyFromSeed`).
- **Why PBKDF2 and not scrypt or argon2id:** PBKDF2 is in the standard library
  (`crypto/pbkdf2`, Go 1.24+); scrypt and argon2id live only in `golang.org/x/crypto`, which
  this module does not use (go.mod), and the owner key is unsealed only when the matrix or a
  device changes. 600 000 iterations is the OWASP 2023 figure for PBKDF2-SHA256, the same
  KDF `wy hub export --encrypt` already uses (machines.md 8.1). `iter` is stored, so a later
  `wy keys passwd` can raise it. The passphrase must be at least 12 characters (`wy` refuses
  shorter ones); the prompt suggests four or more random words.
- The passphrase is read **only from the controlling terminal** (`/dev/tty`,
  `golang.org/x/term` `ReadPassword`), twice when set. There is no flag, no environment
  variable, no stdin and no file option for it. A Claude Code session's commands have no
  controlling terminal (8), and even one that makes itself a pseudo-terminal still has to know
  the passphrase: that, not the guard, is what stops it (8.1).
- The unsealed seed lives in memory only for the one command, and is zeroed after use
  (best effort: Go may have copied it).

### 3.2 Recovery code

`wy keys init` makes the owner key **from** a random 32-byte recovery secret `R`, so the
printed code is a full backup of the key, not a second password:

```
seed = HKDF-SHA256(secret = R, salt = "wy-owner-v1", info = "ed25519 seed", length = 32)
code = "WYR1-" + groups of 4 of base32(R) (52 characters, upper case) + "-" + check
check = first 4 characters of base32(SHA-256("wy-recovery-v1" ‖ R))
```

- Printed once, on the terminal only (refused when stdout is not a TTY), with: "Write this
  down and keep it offline. Anyone with it has your owner key. It replaces the passphrase
  and the file if you lose either." `wy keys init` then asks you to type the last two groups
  back before it continues.
- `R` and the code are never written to disk, never logged and never sent anywhere.
- `wy keys recover` asks for the code (case-insensitive; spaces and dashes ignored; `0`→`O`,
  `1`→`I`, `8`→`B`), checks `check`, rebuilds the same key, asks for a new passphrase and
  writes `owner.sealed`. Because the key is the same, no device has to be re-certified and no
  machine has to re-pin anything.
- Losing **both** the passphrase-sealed file and the code means a new owner key: every
  machine must be certified again (`wy keys certify --all`) and every machine's pin replaced
  (3.5). This is the main risk of the owner-key design.

### 3.3 Registering the owner key with the hub

1. `wy keys init` (owner machine) makes the key, prints the fingerprint and the recovery code,
   writes `owner.sealed` and `owner.pub` and sends a **proposal** to the hub:
   `POST /v1/keys/owner` with a self-signed `owner_key` document (4.1) and the machine's own
   bearer token. The hub stores it as `owner_key.state = 'pending'`.
2. The proposal becomes active when confirmed:
   - on the board, Settings → Keys (`#/more/keys`, pwa.md 3.2), by an **owner session** (pairing.md 4): "Owner key
     `k7q2-m4xa-…` proposed by laptop at 09:00. Is this the fingerprint your terminal
     printed?" → `POST /v1/keys/owner/confirm`; or
   - on the hub machine: `wy hub keys confirm-owner <fp>` (direct database access and
     `hub_sign.key`, like `wy hub token create`); or
   - automatically when the hub runs on the same machine and the same state folder (merged
     mode: `wy keys init` writes the row as `active` with `confirmed_by = 'cli'` and the hub
     signature of step 3).
     A hub database on this machine counts as merged mode only when a hub is running on it
     (it holds `hub.lock`) and its hub key is the key of the hub this machine is connected
     to; a folder left from before the hub moved (which may keep the same hub key) gets
     nothing and the proposal goes to the connected hub.
3. **Confirmation signature.** On activation the hub adds its own signature to the
   `owner_key` document (`by: hub`, over the same bytes, 4.1) and stores it
   (`owner_key.hub_sig`). The hub uses an owner key only after checking that signature with
   its own key, and devices pin one only with both signatures (3.5): a row written straight
   into the database is ignored, so editing rows cannot install an owner key. (Confirming
   through the API needs an owner session; who can get one is 5.4's table.)
4. A pending key that is not confirmed within 24 hours is deleted. There is at most one
   `active` row (`owner_key_active`).
5. **Replacing it** (passphrase and recovery code both lost, or the key leaked): `wy keys init
   --replace` proposes a new key the same way; confirming it retires the active one. No
   machine follows automatically: each keeps its pin until you run `wy keys trust-owner <fp>`
   there (3.5), and every machine must be certified again (`wy keys certify --all`). There is
   no "rotation" document signed by the old key, on purpose: a leaked key would sign one too.

Why a confirmation step: a stolen machine token must not be able to install its own owner
key on a hub that has none yet.

### 3.4 Hub signing key

- `<state>/hub_sign.key`: the 32-byte Ed25519 seed, raw, mode 0600, made at the first `wy hub
  serve` (or the first `wy hub pair`, whichever comes first) next to `pair_pepper`
  (pairing.md 2.1), and for the same reason kept out of the database: a database copy or
  backup alone cannot sign anything a device will accept.
- The fingerprint is shown by `wy hub keys show`, on the board (Settings → Keys), in
  `GET /v1/keys`, in the pairing QR (`h=`, 7) and in the claim reply.
- **Moving the hub.** `wy hub export` adds `state/hub_sign.key` to the archive, required like
  `pair_pepper` (machines.md 8.1 step 3; the export fails when it is missing), and `wy hub
  import` writes it back the same way (8.2 step 5). Without it every device would have to be
  paired again.
- **No hub key rotation.** A new hub key means every device re-pairs (phones) or re-runs `wy
  agent enable --repin-hub` after you confirm the new fingerprint on that machine. A lost key
  is the same case.

### 3.5 Pins on each device

Each device keeps the keys it trusts in its state folder (paths.md 3):

| File | Holds | Set |
| --- | --- | --- |
| `<state>/keys/hub.pub` | `{"pub", "fp", "pinned_at", "how"}` | At pairing from the claim reply, checked against the QR's `h=` when there was one; `how` = `qr`, `tofu` (typed code, or `wy connect --token`), `ssh` (`wy machine add`), `hand` (`wy agent enable --repin-hub`) |
| `<state>/keys/owner.pub` | `{"pub", "fp", "pinned_at", "how"}` | Out of band when possible: by `wy keys init` on the owner machine (`how = local`), by `wy machine add` over your SSH session (`how = ssh`), or by `wy keys trust-owner <fp>` (`how = hand`). Otherwise from the hub the first time one is active (the claim reply, `GET /v1/keys` or the link's `welcome`), only with the owner's self-signature and the hub's confirmation signature (3.3): trust on first use, `how = tofu`, printed by `wy agent status` with the fingerprint to compare |

A pin changes only by `wy keys trust-owner <fp>` / `wy agent enable --repin-hub` typed on that
machine, which print the old and the new fingerprint and ask for confirmation (TTY only,
signing guard 8). Nothing received over the network replaces a pin. A hub key that differs
from the pin stops `wy agent enable` (and the link) with both fingerprints and the hint to
run `wy agent enable --repin-hub`; `wy machine add` refuses to hand a remote a hub key other
than the one the operator's machine pinned, and `wy keys pin-ssh` refuses to replace a
different hub pin.

**What TOFU trusts.** A machine that pins the owner key from the hub trusts the hub at that
moment. Edited rows cannot exploit that (3.3 step 3). Anyone who can confirm a key of their own
(an owner session, so also code running as the hub, 5.4) can make a
machine **that pins afterwards** trust it; machines already pinned keep the real key, and
their checks (peers.md 8.4) are the ones that count. `wy agent status` and the board show both
fingerprints so a wrong pin is visible; `wy machine add` pins over SSH and avoids the window
entirely, and `wy keys trust-owner <fp>` fixes a wrong pin.

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

- Each signature is Ed25519 over `"wy-doc-v1\n" ‖ type ‖ "\n" ‖ body bytes`. The body bytes are
  signed **as they are** (no canonical JSON): a verifier checks the signature first, then
  decodes the body. Bodies are compact JSON written by `encoding/json`. `type` must be one of
  the table below before anything is verified. The prefix keeps documents apart from link
  frames (`"wy-link-v1…"`, link.md 3, 4.3), and `type` keeps the document kinds apart, so no
  signature made for one can be read as another.
- A verifier requires **exactly** the signatures the table names for that type, each by a
  distinct `by`, and ignores none: an unexpected extra signature is a reject, not a pass.
- `by` is `hub`, `owner` or `device`. A verifier looks up the key by `by` (its pin; for
  `device`, the key in the sender's certificate) and ignores `fp` except for messages ("signed by owner key k7q2-…, but this machine pins 3abc-…").
- Unknown body fields are ignored (new fields need no version bump, and none may grant
  anything: an old verifier must be safe ignoring it); a body with `v` other than 1 is
  rejected.
- The hub re-verifies every stored document (`owner_key`, `device_key`, `device_accounts`,
  `matrix_doc`, `revocation_doc`) when it loads it, not only when it accepts it; a row that fails is ignored
  and logged, so editing the database changes nothing the hub enforces.

| `type` | Body | Required signatures |
| --- | --- | --- |
| `owner_key` | `{"v":1,"pub","hub","created_at"}` (`hub`: the hub key fp) | Self-signed with that key (`by: owner`), proving possession at proposal; plus `by: hub` once confirmed (3.3) |
| `device_cert` | 4.2 | `hub` always; `owner` for machines and desktops before they may send or receive peer messages |
| `matrix` | peers.md 5.1 | One: `owner`, or `hub` under the rules of 5.4 |
| `revocations` | 6 | One: `owner` or `hub` |
| `peer_envelope` | peers.md 8.2 | `device`: the sending machine's key, checked against its certificate |
| `machine_accounts` | 4.3 | `owner` |

`matrix` and `revocations` bodies carry `"hub": "<hub fp>"` as certificates do, so a document
made for a test hub with the same owner key is refused by machines of another hub.

**`owner_key` is the one self-describing type.** Its `by: owner` key is not looked up in a pin:
the verifier decodes `pub` from the body first (the only body read before a signature is
checked), requires `fp` = fingerprint(`pub`), checks the self-signature with that key and then
the `by: hub` signature with the pinned (or the hub's own) hub key. A device pins the key only
when both verify (3.5); the hub uses a row only when both verify. Code: `keys.VerifyOwnerKey`.

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
| `machine` | For machine and desktop; `null` for a phone. The device row's `machine_id`, which is the machine's own identity id (`identity.LoadOrCreateMachine`): `wy connect` and `wy setup` send it in the claim, a pull device is bound to the id its `hello` names, and ingest refuses records for another machine. The one exception is `wy hub token create --machine ID`, which takes the id as typed; give it the id `wy machine` prints on that machine, or the peer relay refuses its agent (it compares the two) or routes another machine's messages to it. |
| `sign.alg` / `box.alg` | `ed25519` / `x25519` for machine and desktop; `p256` / `p256` for a phone. |
| `reg` | How the hub learned the keys: `qr` (sealed by the QR, 7.2), `tofu` (over TLS with a bearer or a typed code), `ssh` (pushed by `wy machine add` over your SSH session, machines.md 4.2), `local` (the hub's own machine in merged mode). |
| `hub` | The hub key's fingerprint, so a certificate cannot be replayed against another hub. |

Lifecycle:

1. **Hub signature at registration.** When a device's keys arrive (claim, `PUT
   /v1/devices/{id}/keys`, or `wy machine add`), the hub builds the body, signs it and stores
   `device_key` (body bytes in `cert`, `hub_sig`). On every link login the hub verifies
   `hub_sig` over `cert` with its own key before trusting `sign_pub` (link.md 3): a row edited
   in the database fails here, so a database-only attacker cannot add a device key or swap
   one.
2. **Owner signature.** `wy keys certify` (owner machine) lists the machine and desktop devices
   whose certificate has no owner signature, with name, `reg`, created time and the key
   fingerprints, asks which to certify (`--all`, or ids), asks for the passphrase once and
   uploads the signatures (`POST /v1/devices/{id}/cert`). `wy machine add` does the same for
   the machine it adds in the same run, because it already holds your SSH session and read
   the remote's keys over it (`reg = ssh`): no fingerprint comparison needed. For `reg =
   tofu` devices, `wy keys certify` shows the fingerprint `wy agent status` prints on that
   machine and asks you to compare.
3. A certificate never changes. New keys = new certificate (the old row is replaced, and
   the old keys are listed in the next revocation document, 6). `PUT /v1/devices/{id}/keys`
   (bearer only) may replace keys of a `tofu` device that has no owner signature; for a
   `qr`, `ssh` or owner-certified device it is refused (409 "re-pair this device"), so a stolen
   bearer cannot downgrade a verified device to keys of its own. Registering the keys a
   device already has is a no-op that returns the stored certificate (so `wy agent enable`
   and `wy machine add` can run again). Keys on the revocation list are refused (409 "these
   keys were revoked; make new ones on the machine and register those"); `wy machine add`
   then has the remote make new keys (`wy keys device --renew`) and registers those.
4. **Admin registration (`reg = ssh`).** `wy hub keys register <device> --machine <id>
   --keys-stdin` runs on the hub host (the admin channel `wy machine add` already uses,
   machines.md 4.4): it needs a machine device of that machine id, may replace keys of any
   `reg` (the old keys are revoked as in 3: whoever runs commands on the hub host is
   already trusted), and turns an uncertified `tofu` row with the same keys into `ssh`. `wy
   hub keys certify <device> --fp <owner fp> --sig-stdin` stores an owner signature made on
   the owner machine, checked exactly like `POST /v1/devices/{id}/cert`.

Phones are certified by the hub signature only: the hub learned their keys under the QR seal
(`reg = qr`) or, for a typed code, over TLS (`reg = tofu`, shown as "Not verified" with a
check code, push.md 14.7). `wy keys certify` may add an owner signature to a phone; nothing
requires it.

### 4.3 Machine accounts

The peers matrix (peers.md 5) is a rule between **accounts**, but a machine reports which
account each of its sessions belongs to itself. Without more, a machine could claim any
account in a peer envelope (or in the records that fill the hub's session table) and get
past the matrix on another machine. So the owner also signs which accounts each machine may
speak for:

```json
{
  "v": 1,
  "hub": "<hub fp>",
  "device": "dev_01JA4…",
  "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
  "sign": "<fp of the certificate's sign key>",
  "accounts": ["3f1c…", "7a20…"],
  "issued_at": "2026-10-15T09:00:00.000Z"
}
```

| Field | Rule |
| --- | --- |
| `device`, `machine`, `sign` | The certificate it belongs to (4.2): its device, machine and sign key fingerprint. New keys need a new document; one made for earlier keys does not verify. |
| `accounts` | Account uuids (the matrix's ids), sorted, no duplicates, at most 64. |
| `hub` | The hub key's fingerprint. |

- Signed by the owner key only (`by: owner`); never by the hub, so neither a stolen database
  nor the hub key can bind an account to a machine.
- Made by `wy keys certify` (11) for machines and desktops: it lists the accounts the hub sees
  logged in on each machine now (`GET /v1/keys` `devices[].accounts`, from the machine's own
  `slot_info` records) plus those already bound, and signs that list after you confirm it.
  Those come from what the machine reported, so compare them with `wy keys status` on that
  machine (it prints the machine's accounts and which are bound). `--all` picks every machine
  with an account that is not bound yet.
- Uploaded with `POST /v1/devices/{id}/accounts` and kept in `device_accounts` (one row per
  device, the newest replaces the older). The hub accepts it only when it verifies with the
  active owner key against the device's current certificate.
- **Who checks it.** The peer relay (peers.md 8.3) refuses an envelope whose sender account
  is not in the sending device's document (`unbound_account`), and attaches the document to the
  queued `peer_msg`; the receiving agent checks it again with its own owner pin (peers.md 8.4)
  and refuses the same way. A missing document is a refusal.
- **What it does not do.** A machine may still say which of **its bound** accounts a session
  belongs to: a session can run any account folder on its own machine, so that is no
  extra power. Removing an account from a machine's list does not take effect against a
  hub or machine that is handed the older document (there is no version counter); to take an
  account away for certain, renew the machine's keys (`wy keys revoke`, then pair again or
  `wy machine add`), which makes every older document fail.
- **Machines certified before this document existed** have none, so their peer messages to
  other machines are refused (`unbound_account`) until the owner runs `wy keys certify --all`
  once more. No re-pairing is needed. `wy doctor` says so on each such machine ("no accounts are
  bound to this machine"), and `wy peers send` names the fix in its refusal line.

## 5. Who may sign what

### 5.1 Summary

| Change | Owner key (terminal, passphrase) | Owner session (board, desktop app) | Any device with a link or a token |
| --- | --- | --- | --- |
| Certify a machine or desktop | Yes | No | No |
| Bind accounts to a machine (4.3) | Yes | No | No |
| Widen the matrix, resume after pause, raise the loop limit | Yes | Yes, hub-signed (5.4) | No |
| Resume one thread after the loop guard (peers.md 6.3) | No (a human prompt in either session does it) | Yes, hub-signed (5.4; `peer_thread_resume`) | No |
| Narrow the matrix, pause | Yes | Yes | Yes (phones too), hub-signed |
| Revoke a device | Yes | Yes (Devices → Revoke), hub-signed | Only itself ("Unpair this phone", pairing.md 6) |

### 5.2 Narrowing vs widening

A matrix document **widens** compared with the one before it when any of these is true:
an allowed directed pair is added, `same_account` goes from false to true, `paused` goes
from true to false, or `max_turns` grows, where 0 (no limit) counts as the highest value: going
to 0 widens, going from 0 to any number narrows (`keys.TurnsWiden`). Anything else narrows or is neutral. The rule is
computed by one function (`keys.Widens(old, new)`, in `internal/keys` beside the matrix body so
the hub, the agent and the CLI share it without importing `internal/peers`).

### 5.3 Owner-key documents

`wy peers allow …` and the other widening commands (peers.md 7) build the next matrix body,
show the change, ask for the passphrase, sign with `by: owner` and send it with `PUT
/v1/peers/matrix {"doc": …}`. The hub accepts an owner-signed document from **any**
authenticated caller (a machine bearer is enough: the signature is the authority), provided
its `version` is the current one + 1 and the signature verifies against the active owner
key. The hub then queues it to every machine (link.md 6, frame `matrix`).

### 5.4 Hub-signed documents

The hub signs a matrix or revocation document itself (`by: hub`) when the change comes from:

- an owner session on the board or the desktop app (any change;
  either is enough);
- any authenticated device, phones included, when the change narrows (`POST
  /v1/peers/pause`, `PUT /v1/peers/matrix` with an `edit` that narrows; a revocation of
  itself).

`matrix_doc.made_by` records the device that asked; `widens` records the 5.2 result.

Each agent decides whether it trusts a hub-signed **widening**: `config.toml` `[peers]
hub_may_widen = true` (default). With `false`, a machine accepts a hub-signed
document only when `Widens(current, new)` is false for **its own** current document (an agent
with none starts from peers.md 5.1's version-1 baseline), so nothing that holds the hub key can
widen what that machine delivers; widening then needs the owner key. Narrowing documents are
always accepted from either signer. The same setting governs `peer_thread_resume` (peers.md
6.3), which the hub alone signs: with `false` the agent refuses it, tells the hub (the audit
shows the refusal), and the thread stays stopped until a human prompt.

This is the one place the default ("board sign-in is enough") trades against the
principle that the hub can only narrow. Who can widen, per setting:

| Attacker | `hub_may_widen = true` (default) | `false` |
| --- | --- | --- |
| A copy of `hub.db` or a backup (read only) | No | No |
| Write access to `hub.db` only | No: owner-session token hashes are keyed with `pair_pepper` (tokens.md 2), so it cannot mint one; it can mint a `write` token, which cannot ask for a widening | No |
| Code running as the hub's OS user (reads `hub_sign.key`) | **Yes** | No |
| A Claude Code session on the **hub machine**, as the same OS user | **Yes**, the same way | No |
| A session on another machine | No (no owner session, no hub key) | No |

So with the default, "sessions cannot widen" holds only for sessions that do not run on the
hub machine as the hub's user. When sessions run there (the gpu-box as hub, or the laptop in
merged mode), set `hub_may_widen = false` on every machine, or run the hub as its own OS user;
`wy doctor` warns when a slot's sessions run as the hub's user while it is `true`.

## 6. Revocations

Body:

```json
{
  "v": 1,
  "version": 4,
  "hub": "<hub fp>",
  "issued_at": "2026-10-15T09:10:00.000Z",
  "devices": [
    {"device": "dev_01JA…", "keys": ["<sign fp>", "<box fp>"], "at": "2026-10-15T09:10:00.000Z", "reason": "lost"}
  ]
}
```

- Cumulative: version N+1 must contain every entry of version N (a verifier rejects a list
  that drops one). A version not above the verifier's current one is ignored (no replay of an
  older list). Entries are never removed; a re-paired device has new keys and a new
  certificate.
- Made by `wy keys revoke <device>` (owner key) or by the hub on any device revocation
  (`DELETE /v1/devices/{id}`, `wy hub token revoke`): the hub then sets `device.revoked_at`
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
  "pub"}, "auth"?}` (`auth`, 16 bytes, phones only). Used by typed-code claims and by `wy
  connect --code` (machines). Registration `reg = tofu`.
- `keys_sealed`: the same JSON sealed with the QR's `k` (phones scanning the QR):

```
k           = HMAC-SHA256(pair_pepper, "seal:" ‖ secret)[0:16]     (the 16 bytes behind the QR's k=)
nonce       = 12 random bytes
keys_sealed = b64u(nonce ‖ AES-128-GCM(key = k, nonce, plaintext = keys JSON, aad = "wy-reg-v1\n" ‖ secret))
```

`k` is already a fresh pseudo-random key per code, so it is used directly (no extra KDF); the
AAD ties the seal to this code's secret.

Hub steps, inside the claim transaction after pairing.md 3 step 3:

1. Recompute `k`, open `keys_sealed`. Failure → **400** `bad_request` "key registration
   invalid", and the code is **not** consumed, so a tampered request cannot burn the real
   phone's code.
   The seal is opened only after the secret matched a live code, so an unknown secret is
   still 401 and counts against the limiter like any other wrong secret; a typed code with
   `keys_sealed`, or both fields at once, is 400.
   **No downgrade:** a `secret` claim (the QR path) that carries `keys` in the clear is
   refused the same way: clear keys are for typed codes only. A `secret` claim made without
   the QR (`wy machine add` passing a code on stdin) therefore sends no keys; `wy machine
   add` then registers the keys it read over SSH through the hub host's admin channel (`wy
   hub keys register`, `reg = ssh`, machines.md 4.2), not over the network. A proxy can still strip the keys
   altogether; the app then sees a reply without `cert` / `reg = qr` and refuses (7.3).
2. Check the keys: P-256 points on the curve (`ecdh.P256().NewPublicKey`, `ecdsa` parse),
   Ed25519/X25519 32 bytes, `auth` 16 bytes, algorithms matching the code's kind.
3. Create the device (step 4), then `device_key` with `reg = qr | tofu` and the hub-signed
   certificate, and for phones the `link` push target (push.md 13.7), in the same transaction.

A claim without either field works as before (no keys yet; `PUT /v1/devices/{id}/keys`
later, as for devices paired before keys existed).

### 7.2 What the QR seal buys

A proxy that terminates TLS (a misconfigured `tailscale serve`, a Funnel node you do not
trust, a Cloudflare tunnel) sees the claim, the secret and even the bearer, but not `k`
(it is only in the camera image, and needs the pepper to compute). It cannot swap in its
own phone key: the hub refuses a seal it cannot open, and the pairing fails visibly. It
cannot decrypt frames (no private key, no auth secret) and cannot forge them (no hub key).

### 7.3 Claim reply

`PairClaimReply` gains, when the hub has keys (always, once the hub has a key):

```json
"hub_key": {"alg": "ed25519", "pub": "<b64u>", "fp": "<24 chars>"},
"owner_key": {"type": "owner_key", "body": "…", "sigs": [ … ]},
"cert": {"type": "device_cert", "body": "…", "sigs": [ … ]},
"link": {"urls": ["wss://hub.example.ts.net/v1/link"], "ping_s": 180},
"reg": "qr"
```

`owner_key` is the active owner key document (both signatures, 3.3), null until one is active.
`link.urls` comes from the link channel settings (push.md 13.6); a device tries them in order.

The device checks the reply before it keeps anything: the certificate's hub signature with
`hub_key` (and `hub_key.fp` against `h=` when the QR had one), that the certificate holds
**its own** public keys, and, when it sent `keys_sealed`, that `reg` is `qr`. Any mismatch →
it deletes the new device with its token (`DELETE /v1/devices/self`), forgets the token and
shows "Pairing was tampered with; try again on your home network". A machine (`wy connect
--code`, a `write` token) cannot call `DELETE /v1/devices/self` (owner and push scopes only):
it tries, forgets the token whatever the answer, and tells you to revoke the device on the
board if the hub still lists it. A machine that already pins another hub key refuses the
reply the same way (`wy agent enable --repin-hub` after checking the new fingerprint).

## 8. The signing guard

Every command that unseals the owner key or replaces a pin (`wy keys init | certify | revoke |
passwd | recover | trust-owner`, `wy agent enable --repin-hub`, and the widening `wy peers`
commands) first runs `keys.CheckNotUnderClaude()`, and refuses with exit 4 and:

```
wy: refusing to sign: this command runs inside a Claude Code session (pid 4242, claude).
Owner-key changes need you at a terminal that Claude Code did not start, or the board's owner sign-in.
```

The check, in order; the first hit refuses:

1. **Own environment.** `CLAUDECODE` is set (Claude Code sets `CLAUDECODE=1` for the Bash tool
   and hooks), or `CLAUDE_CODE_ENTRYPOINT` is set, or `CLAUDE_CODE_MESSAGING_SOCKET` is set.
   Only the **names** are tested. No value is read, kept or logged, here or in step 2: the
   environment of a session holds `CLAUDE_CODE_MESSAGING_TOKEN`, a credential Watchyard
   treats like `sessions/*.key`.
2. **Ancestors.** Walk from `os.Getppid()` towards pid 1, at most 64 steps, stopping at pid 1
   or a pid that cannot be read. For each process:
   - **Linux:** ppid from `/proc/<pid>/stat` (the field after the last `)`), the executable from
     `readlink /proc/<pid>/exe`, `argv[0]` from `/proc/<pid>/cmdline`, the name from
     `/proc/<pid>/comm`, and the environment from `/proc/<pid>/environ` (readable for your own
     processes; an unreadable one is skipped, not a refusal).
   - **macOS:** `ps -o ppid=,comm=,args= -p <pid>` per step (no environment of other
     processes).
   - **Windows:** owner-key commands are not available ("use the board's owner sign-in");
     they exit 4 before any check.
   - It is Claude Code when any of these hold: the base name of the executable, of `argv[0]`
     or of `comm` is `claude` (or `claude.exe`); the executable path contains
     `/claude/versions/` (the native installer's layout, where the file is named after the
     version) or `/@anthropic-ai/claude-code/`; `argv[0]` or `args` ends in `claude` with any
     directory; the environment holds `CLAUDECODE` (by name).
3. **A terminal.** `/dev/tty` must open; otherwise refuse ("needs a terminal").

Claude Code 2.1.300 (read from the installed program, not documented) starts Bash tool
commands and hooks **detached** (`setsid`: a new session with no controlling terminal) and
with `CLAUDECODE=1`, so steps 1 and 3 each refuse an ordinary session command on their own.

### 8.1 What the guard can and cannot do

The guard stops a session from signing **by accident or because a message told it to**. It is
not a sandbox, and each step can be evaded on purpose by a process running as your user:

| Step | Evaded by |
| --- | --- |
| 1 Environment | `env -u CLAUDECODE …`, `env -i …` |
| 2 Ancestry | Re-parenting: `setsid -f`, `nohup … &` then exit, a double fork, `systemd-run --user`, `at`, cron. The walk then ends at pid 1 or the user's systemd, never at `claude` |
| 3 Terminal | `script -qc …`, a Python `pty`, `systemd-run --user --pty`: a pseudo-terminal the session itself drives |

What none of these gives a session is the **passphrase** (3.1): it is typed by you, it is
never stored, and a session cannot type into your terminal (it has no controlling terminal,
and Linux 6.2+ refuses `TIOCSTI` injection by default). So owner-key signatures, and with
`hub_may_widen = false` every widening, need you. Limits that remain, by design of the OS user
as the trust boundary (as for Claude Code itself):

- A session can read `owner.sealed` and guess passphrases offline; 600 000 PBKDF2 iterations
  and the 12-character minimum make that slow, a four-random-word passphrase makes it hopeless.
- A session can ask you, in its own words, to run a widening command. `wy` prints exactly what
  it will sign before it asks for the passphrase; read it.
- On the hub machine a session can read `hub_sign.key` (5.4's table).
- On its own machine a session can bypass Watchyard entirely: write to another session's
  inbox socket directly, or edit Watchyard's state files (peers.md 5.5).

**Uncertain:** the exact executable and `comm` names of every Claude Code install (native
installer, npm, Homebrew, the desktop app's bundled CLI). The environment marker catches a
renamed binary, the ancestry catches a cleared environment; neither is relied on alone.

The same walk (step 2 only) is how `wy peers send` finds its calling session (peers.md 4.1):
there a hit is required instead of refused, and the environment marker is **not** part of the
test, because every child of a session carries `CLAUDECODE` and the walk would stop at the
Bash tool's shell.

## 9. Schema v7

Migration `internal/hub/store/migrations/0007_keys_link.sql` (the link tables are explained in
link.md 5):

| Table | Holds |
| --- | --- |
| `owner_key` | Owner public keys: `pending` proposals, the one `active` key, `retired` ones after a replacement; each with its self-signed document body and, once confirmed, the hub's signature (3.3). |
| `device_key` | One row per device that has link keys: algorithms, public keys, `reg`, the certificate body bytes, `hub_sig`, and `owner_fp`/`owner_sig` once certified. |
| `revocation_doc` | Every version of the revocation list with its signer and signature. |
| `link_state`, `link_queue` | link.md 5. |

`device_key.cert`, `*_doc.body`, signatures and `link_queue.body` are `BLOB` columns in STRICT
tables: the store binds `[]byte`, never a string.

Schema v14 (`0014_device_accounts.sql`) adds `device_accounts`: per device, the body and owner
signature of its newest `machine_accounts` document (4.3).

## 10. Files (paths.md 3)

```
<state>/keys/                  mode 0700
  device.json                  this device's keys: {"v":1,"sign":{"alg":"ed25519","seed":b64u},"box":{"alg":"x25519","priv":b64u},"device":"dev_…","created_at"}, mode 0600
  owner.sealed                 owner machine only (3.1), mode 0600
  owner.pub                    pinned owner key (3.5)
  hub.pub                      pinned hub key (3.5)
  certs.json                   certificates of other devices seen in peer envelopes, by device id (cache)
  matrix.json                  newest accepted matrix document, as received (wire form); read with keys.LoadAcceptedMatrix (verifies it with the pins), written with keys.SaveAcceptedMatrix (verifies, applies 5.4, atomic, 0600)
  revocations.json             newest accepted revocation document
<state>/hub_sign.key           hub machine only (3.4), mode 0600
```

`wy uninstall --purge` removes `<state>/keys/` and `<state>/hub_sign.key` with the rest of the
state folder; it prints a warning first when `owner.sealed` is present ("this deletes your
owner key; you need the recovery code to sign again").

## 11. CLI

```
wy keys init [--replace]         owner machine: make the owner key, print fp + recovery code, propose it to the hub (3.3)
wy keys status [--json]          pins (hub, owner), this device's keys and certificate state, this machine's
                                 accounts and which are bound (4.3), pending proposals
wy keys certify [<device>…|--all] [--yes]   owner-sign machine/desktop certificates and the accounts each
                                 may speak for (4.3; passphrase once)
wy keys revoke <device> [--reason R] [--yes]  owner-signed revocation, uploaded; the hub revokes the device
wy keys passwd                   new passphrase (and iteration count) for owner.sealed
wy keys recover                  rebuild owner.sealed from the recovery code
wy keys trust-owner <fp>         replace this machine's owner pin by hand (prints old and new)
wy keys show-owner               print the owner public key and fp (used by wy machine add)
wy keys device [--renew] [--json]  this machine's device id, machine id and public link keys,
                                 made at first use (read over SSH by wy machine add)
wy keys pin-ssh [--json]         read {hub_key, owner?, cert} on stdin (wy machine add over SSH):
                                 check the certificate holds this machine's keys, pin the hub
                                 key (how ssh; a different pin is refused) and the owner key
                                 (kept when one is already pinned)
wy agent enable [--repin-hub]    install the agent and register this machine's link keys (3.5,
                                 peers.md 2.1); --repin-hub replaces a hub pin that no longer
                                 matches the hub (guard, TTY confirm, how hand)

wy hub keys show [--json]        hub machine: hub key fp, active owner fp, pending proposals
wy hub keys confirm-owner <fp>   hub machine: activate a pending owner key
wy hub keys register <device> --machine <id> --keys-stdin [--json]   hub machine: reg ssh (4.2 step 4)
wy hub keys certify <device> --fp <fp> --sig-stdin   hub machine: store an owner signature (4.2 step 4)
```

All of them run the signing guard (8) where 8 says so; every command that changes something
prints what it will sign and asks unless `--yes` (setup.md rule 1). `--yes` never skips the
passphrase.

## 12. Tests

- Fingerprint, base32 and recovery-code goldens (`internal/keys/testdata/`, fixed `R`);
  recovery round trip (code → seed → same public key); typo handling and the check group;
  document signature goldens per type with the hub seed of link.md 10.
- Sealed file: wrong passphrase fails, changed `fp` or `pub` fails (AAD), `iter` honoured.
- Document signing and verification for every type; each field of a body changed → reject;
  `by` swapped → reject; a missing or an extra signature → reject; a document signature
  presented as a link frame signature (and the reverse) → reject; a hub signature over a
  `device_cert` checked on link login, with a `device_key.sign_pub` edited in the database →
  login refused; an `owner_key` row inserted as `active` without `hub_sig` → ignored by the hub
  and not pinned by machines; `matrix` / `revocations` with another hub's fp → reject.
- `Widens` table tests (each widening case, each narrowing case, neutral).
- Revocation list that drops an entry → reject; lower version → ignored.
- Signing guard with a fake `/proc` tree: claude parent, grandparent, `versions/2.1.299`
  executable, `CLAUDECODE` in an ancestor's environ, reparented process (ppid 1, no hit: the
  TTY step must then refuse), 64-step cap, unreadable entries, no TTY; environment values never
  read (a recording reader sees names only). macOS path with a fake `ps`.
- QR seal: open with the right `k`; wrong `k` → 400 and the code stays live; a `secret` claim
  with clear `keys` → 400 and the code stays live; typed claim with `keys` → `reg = tofu`;
  algorithm/kind mismatch → 400; `PUT /v1/devices/{id}/keys` on a `qr` device → 409.
- Export/import carries `hub_sign.key`; export without it fails.
- `machine_accounts`: a document for another device, machine or sign key, another hub's, a
  hub signature, unsorted accounts → reject; the hub stores only one by the active owner key;
  `wy keys certify --all` binds the accounts the hub sees and then has nothing left to do.
