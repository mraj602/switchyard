# Manual test guide

An ordered walk through your real setup: a laptop with several Claude Code folders
(`~/.claude`, `~/.claude-2`, ...), an SSH worker on Tailscale and an Android phone. Nothing here
has been run by hand yet, so expect to find bugs: that is the point. Do the stages in order;
each step says what to run, what a pass looks like, how to undo it and what to report.

The specs are the contract if a step and a spec disagree: [keys](spec/keys.md),
[link](spec/link.md), [peers](spec/peers.md), [push 13-14](spec/push.md),
[phone](spec/phone.md), [machines](spec/machines.md), [setup](spec/setup.md).

## Rules for the whole guide

- Run `wy keys ...`, `wy peers allow` and the other owner-key commands in a **plain terminal
  that Claude Code did not start**. Inside a Claude session they refuse on purpose (exit 4).
  Never type a passphrase or the recovery code anywhere else, and never paste either into a
  bug report.
- Never copy `.credentials.json` or `sessions/*.key` anywhere. Backups below copy settings only.
- `wy` here means `~/.local/bin/wy` once installed; before that use `./dist/wy`.
- Stop at the first failing step of a stage and report it. Later steps depend on earlier ones.

**Report kit** (attach to every failure, after reading it for secrets; it holds no tokens):

```sh
wy version; wy doctor --json; wy paths      # versions, checks, where everything lives
wy outbox stat                              # records waiting to be sent
```

Plus the step's own output, and the logs: the agent's is `<state>/logs/agent.log`; the hub's, when
run as a user service, is `journalctl --user -u watchyard-hub -n 100`.

## T0. Safety net

### T0.1 Back up settings (not credentials)

```sh
mkdir -p ~/wy-backup && for d in ~/.claude ~/.claude-[0-9]*; do
  n=$(basename "$d"); mkdir -p ~/wy-backup/$n
  for f in settings.json CLAUDE.md; do [ -e "$d/$f" ] && cp -L "$d/$f" ~/wy-backup/$n/; done
done; ls -RA ~/wy-backup
```

`cp -L` copies what a symlinked `settings.json` points at. **Pass:** one folder per config dir,
no `.credentials.json` anywhere (`find ~/wy-backup -name '*.json' | grep -c credentials` prints 0).
**Undo:** `rm -rf ~/wy-backup`. **Report:** nothing; just do not continue without it.

### T0.2 Build `wy`

```sh
export PATH=$HOME/.local/bin:$PATH
make build && ./dist/wy version
```

**Pass:** a version line. Nothing is installed or changed yet. **Undo:** `rm -rf dist`.
**Report:** the build error and `go version`.

### T0.3 `wy doctor`, read only

```sh
./dist/wy doctor
```

**Pass:** it prints checks and fixes and changes nothing in your Claude folders (do not pass
`--rebaseline`); it only creates Watchyard's own state folder (`wy paths`). Warnings
about "not set up yet" are normal. **Report:** anything that crashes or names a folder you do not have.

### T0.4 `wy setup --detect`, then a dry run

```sh
./dist/wy setup --detect            # what wy sees; changes nothing
./dist/wy setup --dry-run           # the plan it would show; changes nothing
```

**Pass:** every `~/.claude*` folder is listed with the right login, and `settings.json` files that
are symlinks are seen as symlinks. The plan names each file it will touch. **Report:** a missing or
mislabelled folder, or a plan that touches something you did not expect (paste the plan).

## T1. One device, local

### T1.1 Install and set up

```sh
mkdir -p ~/.local/bin && cp dist/wy ~/.local/bin/wy
wy setup                            # shows the plan first; nothing changes until you accept it
```

Choose "Just this computer" for the hub (`--hub here`). **Pass:** the plan is accepted, a backup id
is printed, `wy doctor` ends with no errors, and `curl -s http://127.0.0.1:8787/health` answers ok.
**Undo:** `wy setup --undo-list`, then `wy setup --undo` (the last run, byte for byte); `wy uninstall`
removes the plugin, status line, timer and launchers and keeps your folders and logins.
**Report:** the setup output, `wy setup --undo-list`.

### T1.2 Sessions reach the board

Open a new Claude Code session with the launcher setup made (`claude`, `claude-2`, ...), ask it
something that needs a permission prompt, then:

```sh
wy status                           # local sessions
wy board --once                     # one text frame of the board
wy hub owner-link                   # open the printed link in a browser: the web board
```

**Pass:** the session shows `needs you` while the prompt is open and `working`/`done` after; the
web board shows the same, and each account appears under its own label. **Undo:** none needed.
**Report:** `wy status`, `wy board --once --json`, the hook's folder (`wy paths`).

### T1.3 Undo drill

```sh
wy setup --undo --dry-run
```

**Pass:** it lists exactly the files from T1.1 and changes nothing. Do the real undo only if you
want to start over (`wy setup --undo`); then run `wy setup` again.

## T2. Phone

### T2.1 HTTPS for the hub

A phone can only install the web app and get Web Push from an `https://` address.

```sh
tailscale serve --bg 8787           # https://<laptop>.<tailnet>.ts.net -> 127.0.0.1:8787
tailscale serve status
```

Add `--public-url https://<laptop>.<tailnet>.ts.net` to the `ExecStart` line of
`~/.config/systemd/user/watchyard-hub.service`, then
`systemctl --user daemon-reload && systemctl --user restart watchyard-hub`
(the setting is stored; once is enough). **Pass:** the https address answers `/health` from the phone
browser with Tailscale on. **Undo:** `tailscale serve --https=443 off` and remove the flag.
**Report:** `tailscale serve status`, `wy doctor` (it checks the public URL).

### T2.2 Web app and Web Push

```sh
wy phone pair --url https://<laptop>.<tailnet>.ts.net     # QR plus a 6-digit code
```

Scan it with the phone camera, install the page to the home screen, then Settings → Turn on
notifications → Send test push. Then make a real session ask for a permission.
**Pass:** the test push arrives; a real "needs you" push arrives within the rule's delay; Snooze and
Seen on the notification work with the app closed; Settings → Peers (More → Peers) opens.
**Undo:** Settings → Unpair this phone, or revoke it on the board (Devices).
**Report:** the board's Alerts delivery log row, `wy doctor` push lines, the phone and browser.

### T2.3 Build the Android app locally

Needs JDK 17+, the Android SDK (`ANDROID_HOME`) and Node. The keystore is yours and stays outside
the repo; keep two offline copies (losing it means every phone reinstalls).

```sh
keytool -genkeypair -v -keystore ~/secrets/watchyard-release.jks -alias watchyard \
  -keyalg RSA -keysize 4096 -validity 10000            # once
export WY_ANDROID_KEYSTORE=~/secrets/watchyard-release.jks WY_ANDROID_KEY_ALIAS=watchyard
read -rs WY_ANDROID_KEYSTORE_PASSWORD && export WY_ANDROID_KEYSTORE_PASSWORD
make phone-deps && make phone-check                    # offline checks first
make phone-apk-release                                 # you run it, not an agent
```

**Pass:** `phone/dist/` holds `watchyard-<version>.apk`, `SHA256SUMS` and `manifest.json`, and the
certificate fingerprint is printed. Install: `adb install phone/dist/watchyard-<version>.apk`, or copy
it to the phone and allow installs from that file manager. Then pair: `wy phone pair --url https://...`
and scan it from inside the app (the release build refuses plain `http://`). **Pass:** the app shows
Now with your sessions. **Undo:** uninstall the app; `rm -rf phone/dist phone/android`.
**Report:** the failing Gradle or prebuild lines, `phone/dist/manifest.json`, the phone's Android version.

## T3. SSH worker

Pre-flight: `ssh <worker> true` works with your keys, and `claude` is installed there.

```sh
wy machine add <user@worker> --dry-run          # prints the steps; still runs the read-only probe
wy machine add <user@worker> --name worker      # --reach auto: direct over Tailscale if it can
wy machine list
```

You may be asked to log each folder in on the worker (a URL to open, a code to paste back).
**Pass:** each step prints a check mark, `wy machine list` shows the worker, and within a minute the
board lists it as connected; a session started over `ssh <worker>` appears on the board.
If the hub is the laptop and the worker cannot reach it, try `--reach forward`, or `pull`
(machines.md 3). Link keys are added at the end of `machine add`; "run `wy keys certify <device>` on the
owner machine" is expected until section K1 is done. **Undo:** `wy machine remove worker --revoke`.
**Report:** the whole `machine add` output (use `--json` for a file), `ssh <worker> wy doctor`.

## T4. Move the hub to the worker

Use a real move only after T3 works. Keep the archive private: it holds every token hash and key.

```sh
# on the laptop (freezes the old hub: it answers 503 to new records from now on)
wy hub export --moved-to https://<worker>.<tailnet>.ts.net --encrypt --out ~/hub.wyx
scp ~/hub.wyx <user@worker>:
# on the worker (wy is installed from T3; start no hub before importing)
wy hub import ~/hub.wyx --public-url https://<worker>.<tailnet>.ts.net
# then start its hub: copy deploy/systemd/watchyard-hub.service from this repo into
# ~/.config/systemd/user/, add --public-url <the same address> to ExecStart, and
systemctl --user daemon-reload && systemctl --user enable --now watchyard-hub
tailscale serve --bg 8787
# back on the laptop, point every machine at it
wy machine repoint https://<worker>.<tailnet>.ts.net
wy connect https://<worker>.<tailnet>.ts.net --keep-token        # on this machine itself
```

**Pass:** the board at the new address has the old history, the laptop and worker report to it, and
the old hub says it moved. Phones keep working if the public URL stayed the same, otherwise pair
them again. **Undo:** on the old hub `wy hub unfreeze` and `wy connect <old url> --keep-token`
everywhere. Delete `~/hub.wyx` afterwards. **Report:** the import output, `wy doctor` on both
machines (a frozen hub prints the exact repoint line).

When the worker is the hub and its sessions run as the hub's OS user, set `[peers]
hub_may_widen = false` on every machine (keys.md 5.4); `wy doctor` warns about it.

## K. The new features

Do these with the hub wherever it now runs. Section K1 first: the owner key is what lets
other machines trust yours.

### K1. Owner key (`wy keys`, `wy hub keys`)

On the **laptop, in a plain terminal**:

```sh
wy keys init            # passphrase twice (12+ characters), then the recovery code
wy keys status
```

Write the recovery code down offline; it asks you to type the last two groups back. The key is
proposed to the hub, and is not active yet. Confirm it with the fingerprint your terminal printed,
either on the board (Settings → Keys, owner sign-in) or on the hub machine:

```sh
wy hub keys show                                 # pending proposal and its fingerprint
wy hub keys confirm-owner <fingerprint>          # only if it matches what init printed
wy keys certify --all                            # owner-sign the machines (passphrase once)
wy keys status                                   # this device: certified
```

**Pass:** `wy hub keys show` lists the active owner key with the same fingerprint, and every machine
has an owner-signed certificate. A wrong fingerprint on `confirm-owner` must be refused: try one.
Optional drill: `wy keys passwd`; or move `<state>/keys/owner.sealed` aside and run `wy keys recover`
with the code, then compare `wy keys show-owner` (same fingerprint, nothing to re-certify).
**Undo:** the key cannot be un-made; `wy keys init --replace` proposes another (every machine then
needs `wy keys trust-owner <fp>` and certifying again). **Report:** `wy keys status --json`,
`wy hub keys show --json`, never the code or the passphrase.

### K2. The agent and the link

On each machine (the worker too, over `ssh`):

```sh
wy agent enable          # makes keys if needed, pins the hub key, installs the service
wy agent status
wy doctor                # agent and "link keys" lines
```

If it stops at a hub key mismatch it prints both fingerprints; compare them on the hub
(`wy hub keys show`) and only then `wy agent enable --repin-hub`.
**Pass:** the service runs, `status` shows the link connected, `doctor` shows `link keys ... certified`,
and the board's Devices list shows the machine with its link state. Restart the hub (`systemctl --user restart watchyard-hub`): the link reconnects on its own. **Undo:**
`wy agent disable` (keys stay). **Report:** `wy agent status`, `<state>/logs/agent.log`, `wy doctor`.

### K3. Peers between two accounts on one machine

You need two live sessions in two different accounts on the same machine (say `claude` and
`claude-2`), with the Watchyard plugin so the `peers` skill is there. Start with both in the same
permission mode (both default prompting), because a pair whose modes differ may ask for approval.

```sh
wy peers enable          # the agent plus matrix version 1 (nothing allowed between accounts)
wy peers matrix          # numbered grid; note the numbers of the two accounts
```

1. **Denied first.** In session A tell Claude: "Use the peers skill to list the sessions you can
   message." **Pass:** the other account is absent, or `wy peers list --all` at a terminal shows it as
   `can: none`.
2. **Allow it** (terminal, owner key, passphrase):

   ```sh
   wy peers allow 1 2 --both          # numbers or labels from the matrix
   wy peers matrix                    # the cell is now the two-way arrow; version went up
   ```

3. **Send.** In session A: "Ask the session in \<account B> what folder it is in." **Pass:** A prints
   `delivered to <name>@<label>.<machine>`; B sees a `<cross-session-message>` and answers with
   `wy peers send`; the answer reaches A. Check from a terminal: `wy peers status`,
   `wy peers audit --since 1h` (metadata only, never text).
4. **Limits and switches.** `wy peers deny 2 1` then retry the reply: refused `not_allowed`.
   `wy peers pause`: sends refuse with `paused`; `wy peers resume` (owner key). `wy peers off` and
   `wy peers on` (this machine only). Ask Claude to send a made-up string that starts with `sk-ant-`: it
   must be refused (`secret`). Ask it to loop two sessions endlessly: with the default turn limit
   (none, `max_turns` 0) only the rates stop it (`rate_limited ... retry in`, 20 quick messages per
   session, 60 an hour per pair); set `wy peers max-turns 4` to see the loop guard stop it at 4,
   then `wy peers max-turns none` to go back.
5. **A session cannot widen.** From inside a session ask Claude to run `wy peers allow 1 2`: it must
   refuse with the signing-guard message (exit 4).

**Undo:** `wy peers deny 1 '*'`, `wy peers pause`, or `wy peers disable`. **Report:** the
send's one-line output, `wy peers matrix --json`, `wy peers audit --json`, `agent.log`, and the
Claude Code version (`claude --version`) since delivery depends on it.

### K4. Peers across machines

Both machines need K1 and K2 done. In a session on the laptop with a session open on the worker
(a different account is the stronger test):

```sh
wy peers list --all                  # the worker's sessions appear, with their handles
```

If the worker's sessions show `start (blocked: ...)`, the link keys are not owner-certified yet
(K1 not done): the line under the table names the steps, and a send is refused with the same
reason without reaching the hub. Same-machine messages (K3) work without certified keys.

Handles are the same on both machines: run `wy peers list --all` on the worker too and check the
laptop's sessions carry the handles the laptop itself shows (the account part is the hub's label
for the account, not each machine's folder name; the folder-name forms appear as `aliases` in
`--json` and still work). A reply sent with the `Reply with:` line of a received message must be
`delivered`.

Ask Claude on the laptop to message that handle. **Pass:** `delivered`; the worker's agent log shows
one line with ids and sizes and no text; the hub's audit (`wy peers audit`) has the metadata and no
text. Then: stop the worker's agent (`wy agent disable` there), send again and expect `queued ...
offline; expires <time>` (exit 8); run `wy agent enable` there and the message arrives. Revoke test
(optional, on a throwaway machine): `wy keys revoke <device>`; its messages are refused everywhere.
**Report:** both sides' `agent.log`, `wy peers status`, `wy peers audit --json`.

### K5. Settings → Peers on the board and the phone

Open the board with an owner link (`wy hub owner-link`), then More → Peers (phone) or Peers in the
sidebar. **Pass:** the grid matches `wy peers matrix`; with an owner sign-in the cells are editable
(a widening is hub-signed), with a read-only sign-in every control is off with its reason; Pause all
peers works from the phone and a message refuses at once; Resume is offered only to an owner session;
the audit list shows the K3 changes with no message text. Check that the board change reaches the
laptop: `wy peers matrix` shows the new version. **Report:** a screenshot and `wy peers matrix --json`.

### K6. Android link notifications

Needs T2.3 (app installed and paired) and K1. In the app: Settings → Notifications.

1. **Instant delivery** is on and **Connection** reads "Connected" with a transport. **Encryption**
   says "Verified by QR" (paired by QR) or shows the 8-character check code, which must equal the one
   on the board's Devices list.
2. **Battery:** tap Fix and allow the exemption (your phone maker's guide shows if needed).
3. **Send test notification.** **Pass:** it arrives in seconds (the app uses no Expo or Google push
   at all; Settings → Channels on the board lists only Web Push, ntfy and the Android app's own link).
4. **Real alert with the app swiped away:** make a session ask for a permission. **Pass:** the
   notification arrives with Open, Snooze and Seen; Seen clears it on the phone and on other devices,
   without opening the app.
5. **Offline replay:** airplane mode, trigger two alerts, turn it off. **Pass:** they arrive in order
   and the board's delivery log shows "on phone \<time>".
6. Reboot the phone: the persistent "Connected to hub" notification returns by itself.
7. **Revoke:** on the board Devices, revoke the phone. **Pass:** the app says it was removed and asks
   to pair again.

**Undo:** switch Instant delivery off (checks every 15 minutes) or unpair. **Report:** the
Notifications screen (screenshot), the board's delivery log row, the phone model and Android version,
and `adb logcat -s WyLink` if you have it.

### K7. The updater

Without a published release, only the empty paths can be tested: Settings → App version → Check for
update should say "Up to date" or a clear "no release" message, never crash. `GET /v1/app/android`
answering 404 on the hub is expected. For the full path you must publish a second release yourself:
bump `version` in `phone/app.config.ts` and `phone/package.json`, rebuild with
`make phone-apk-release` using **the same keystore**, publish the three files from `phone/dist/` as a
GitHub release tagged `phone-vX.Y.Z` (not a draft or pre-release), and restart the hub or wait a day
for the mirror. Then in the old app: Check for update → notes, source ("your hub" or "GitHub") →
Install. **Pass:** Android asks to confirm, the app restarts on the new version, pairing and data
survive, and the link service reconnects. **Undo:** `adb install -r` the older
APK of the same key. **Report:** the Updates row text, `curl` of `/v1/app/android` with a device
token (do not paste the token), the hub log lines about the mirror.

## PE0 lab (needs live Claude Code sessions: you run it)

This settles the things the peers design could only read from Claude Code's program
(peers.md 13, 14). It posts frames by hand to **throwaway** sessions. Do it after K3 works, once per
Claude Code version you care about, and again after an update.

Hard rules (peers.md 13.1): start every lab session yourself in `/tmp/wy-lab/work`, named `lab-...`,
with `--messaging-socket-path /tmp/wy-lab/socks/<x>.sock`; never connect to, write to or list any socket
in the real `cc-socks` folder; never post to a session you did not start for the lab; never open
`sessions/*.key` or `.credentials.json`; do not run the lab in or from a session that matters.
`scripts/pe0-lab.py` enforces the first three: it posts only to a socket under `/tmp/wy-lab/socks/`
whose session's registry file has a `lab-` name and `/tmp/wy-lab/work` as its directory, and reads
only that one registry file.

Setup (two folders logged in to **different** accounts, started directly, not through a launcher):

```sh
mkdir -p /tmp/wy-lab/{socks,work,reply}
cd /tmp/wy-lab/work
CLAUDE_CONFIG_DIR=~/.claude   claude --name lab-a --debug --messaging-socket-path /tmp/wy-lab/socks/a.sock
CLAUDE_CONFIG_DIR=~/.claude-2 claude --name lab-b --debug --permission-mode bypassPermissions \
  --messaging-socket-path /tmp/wy-lab/socks/b.sock          # second terminal
```

Helpers (run from the repo, in a third terminal; the pid comes from `pgrep -f 'name lab-a'`):

```sh
L=scripts/pe0-lab.py
$L info <pid>                                   # session id and socket of a lab session
$L frame <plain|bypass|prompting|none> <session id | -> "<text>" [<from> [<hop tokens>]] > /tmp/wy-lab/f.jsonl
$L post /tmp/wy-lab/socks/a.sock /tmp/wy-lab/f.jsonl <pid>
$L listen /tmp/wy-lab/reply/r1.sock             # a listener, for E10 and E11
```

`frame` builds the canonical wrapper of peers.md 8.5 (`bypass` and `prompting` set `from-mode`, `none`
leaves it out, `plain` sends the bare text). The default `from` is `uds:/tmp/wy-lab/reply/r1.sock`.

Record for each experiment: admitted, held with a dialog, refused or dropped; the `--debug` log's
inbound lines and cause; latency; anything a listener captured.

| # | Do | Expect |
| --- | --- | --- |
| E1 | `frame plain <a's id> hi`, post to lab-a | Admitted |
| E2 | `frame none <b's id> hi`, post to lab-b (bypass) | Held (a dialog) |
| E3 | `frame bypass <b's id> hi`, post to lab-b | Admitted |
| E4 | `frame prompting <b's id> hi`, post to lab-b | Held (mode mismatch) |
| E5 | E3 with the **other** session's id | Dropped |
| E6 | E3 posted from a detached process: `setsid python3 scripts/pe0-lab.py post ...` | Admitted |
| E7 | E3 with 3 hop tokens (24 hex characters each, comma separated), then 30 | Admitted, then dropped |
| E8 | A stdio MCP server in `/tmp/wy-lab/work/.mcp.json` that posts to its parent's socket | Admitted in bypass (needs you to write it; skip if you do not want the fallback) |
| E9 | `/peers` in lab-a and in lab-b | Neither lists the other |
| E10 | After E3, ask lab-b to reply to the sender, with `listen` running on `r1.sock` | A frame arrives; keep it, it is the golden wrapper |
| E11 | E2 with `from` a 16-hex-named socket in the lab socket folder, listener on it; deny the dialog | `held`, then `denied` receipts arrive |
| E12 | A scratch `UserPromptSubmit` hook in `/tmp/wy-lab/work/.claude/settings.json` that logs the first 40 characters of the prompt; deliver to idle lab-a | Note whether it fires and what the prompt starts with |
| E13 | 35 quick posts to lab-a; the same body twice in 30 s | Drops after about 30; the duplicate dropped |
| E14 | A third lab session in bypass with `--settings '{"crossSessionInbound":"accept"}'`; do E2 | Admitted |
| E15 | `claude -p --permission-mode bypassPermissions ...` as a lab session; E3 and E2 | E3 admitted; E2 held then expired |
| E16 | E1-E4 on the worker, posting there (same recipe, a lab folder there) | Same outcomes |
| E17 | E3 to a session whose registry file is gone (exit it first) | Connection refused |

Cleanup: exit the lab sessions, `rm -rf /tmp/wy-lab`.

**Report:** the table filled in (outcome and cause per row), `claude --version`, the E10 frame (it has
lab ids and paths only), and which rows differed from "Expect". The delivery mode follows peers.md
13.3: `honest` if E3 and E4 hold; else `accept` if E14 holds; else `courier` if E8 holds.

## Finishing

When a stage passes, note the date and versions (`wy version`, `claude --version`, the app version)
next to it. Leave the backup from T0.1 in place until you trust the setup; delete `~/hub.wyx` as soon as
T4 is done.
