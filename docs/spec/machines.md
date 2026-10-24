# More machines

Status: spec v1. Implemented in `internal/sshx` (running `ssh` safely),
`internal/machineadd` (`sy machine add|remove|repoint`), `internal/outbox` (`sy outbox drain`),
`internal/client` (forward dialing, version negotiation), `internal/directpush` (direct ntfy
from `sy`), `internal/update` (`sy update`), `internal/hub/pull` (the hub pulling over SSH),
`internal/hub/ingest` (`machine_info`, `direct_push`, same-machine dedupe, frozen hub),
`internal/hub/store` (schema v4, export/import), `internal/hub/api` (machine endpoints,
direct-push policy), `internal/hub/cli` (`sy hub pull`, `export`, `import`, `unfreeze`) and
`webui` (Machines screen). The Machines screen is "Settings · Hub and machines".

Every rule of the connection model holds: every connection is outgoing from the machine that
has something to say, except the hub's pull over SSH (3.4), which is limited by a forced
command to reading the outbox. The hub never makes a client do anything else: replies are
lists of answered ids, and nothing a hub sends ever changes a client's configuration.

## 1. Overview

| Piece | One line | Section |
| --- | --- | --- |
| `sy machine add user@host` | From the laptop: copy the right `sy` over SSH, join the hub with a one-time pairing secret passed on stdin, log accounts in with the person's own terminal | 4 |
| Reach modes | `direct` (Tailscale or HTTPS), `forward` (SSH `RemoteForward` from the laptop), `pull` (the hub runs `ssh host sy outbox drain`) | 3 |
| Drain protocol | Stop-and-wait batches over the SSH channel, acknowledged with the ingest reply; the outbox cursor moves exactly as for `sy flush` | 3.5 |
| Direct ntfy | A server that cannot reach the hub sends the session pushes itself, with the hub's delays, and tells the hub later so it does not push again | 5 |
| Machines screen | Reach, last seen, backlog and its age, version against the hub, pull health, the silent alert | 6 |
| Versions | `/health` and every ingest reply say which record versions the hub reads; a client never sends a newer one | 7.1, 7.2 |
| `sy update` | Self-update from GitHub releases with SHA-256 verification (install.sh in Go), and `--machines` to update every added machine over SSH | 7.3, 7.4 |
| `sy hub export` / `import` | Move the hub: frozen, consistent snapshot with secrets, VAPID keys, pepper and pull key; import refuses a non-empty hub; tokens keep working | 8 |
| Schema v4 | `machine` gains reach, info and backlog age; new `pull_target` | 9 |

## 2. Words

| Word | Meaning |
| --- | --- |
| **Operator** | The machine the person types on, usually the laptop. It runs `sy machine add`, `sy update --machines` and `sy machine repoint`, with the person's own SSH keys and config. |
| **Remote** | A machine being added or managed over SSH (gpu-box, a home server, a VPS). Linux or macOS. |
| **Hub host** | The machine running `sy hub serve` (or the Docker image). It may be the operator (merged mode). |
| **Reach** | How a machine's records get to the hub: `direct`, `forward` or `pull` (section 3). Stored in the remote's `config.toml` (`hub.reach`) and reported to the hub in `machine_info` (6.3). |
| **Admin channel** | How the operator asks the hub host for things only the hub host may do: mint a machine pairing code, register a pull target. `local`, a command prefix run over SSH, or a person pasting (4.4). |
| **Registry** | `<state>/machines.json` on the operator: the machines it added, with SSH target and reach, used by `sy update --machines` and `sy machine repoint`. Never sent anywhere. |
| **Pull device** | The `device` row the hub creates for a pulled machine. Its token is generated, hashed and thrown away: it exists so pulled records have a device, the same dedupe and `machine_mismatch` rules as uploads, and a row to revoke. |

## 3. Reaching the hub

### 3.1 The three modes

| | `direct` | `forward` | `pull` |
| --- | --- | --- | --- |
| Who connects | Remote → hub over HTTPS (or `http` on a tailnet) | Remote → its own `127.0.0.1:<port>` → SSH channel → operator → hub | Hub → `ssh` → remote's forced command |
| Needs | Tailscale (or a public HTTPS hub) on the remote | An SSH session from the operator to the remote | The hub host can SSH to the remote; one line in the remote's `authorized_keys` |
| Records flow while the laptop is closed | Yes (unless the hub is merged on the laptop) | **No**: queued until the next SSH connection | Yes when the hub is not the laptop; no when merged |
| Delay | Seconds (flush after each hook) | Seconds, while connected | Up to one pull interval (default 60 s) |
| Token on the remote | Machine token (`<state>/token`) | Machine token | **None** |
| Typical use | A remote on a tailnet with the hub | — | Locked-down servers |

`auto` (the default of `sy machine add`) picks `direct` when the remote can reach the hub
(4.2 step 2); otherwise it asks, at a terminal, between `pull` (default when the hub is on the
operator) and `forward`, and refuses under `--yes` (exit 2: "the remote cannot reach the hub;
pass --reach forward or --reach pull").

### 3.2 `direct`

`hub.url` is the hub's address as the remote sees it, normally
`https://<hub>.<tailnet>.ts.net` (`tailscale serve`). `sy machine add` uses, in order:
`--hub-url`; the hub's `hub.public_url` (read through the admin channel); the operator's own
`hub.url` when it is not loopback. A loopback URL is never offered to a remote. A server
without a TUN device can run Tailscale in userspace mode; `sy` then needs `ALL_PROXY` or
`HTTPS_PROXY` set by the person, which Go's HTTP client honours (`sy doctor` prints the hint
when the probe fails with "no route" and `tailscaled --tun=userspace-networking` is running).

### 3.3 `forward`

**On the operator.** `sy machine add --reach forward` writes one block to
`~/.ssh/switchyard.conf` and, once, an `Include switchyard.conf` line as the **first line** of
`~/.ssh/config` (an `Include` after a `Host` line would only apply to that host). Both files
are journaled in a setup backup set (setup.md 7), so `sy machine remove` and
`sy setup --undo` take them out again.

```
# sy-forward v1 machine=m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K name=gpu-box (sy machine add; sy machine remove undoes it)
Host gpu-box
  RemoteForward 127.0.0.1:47830 127.0.0.1:8787
  ExitOnForwardFailure no
  ServerAliveInterval 30
  ServerAliveCountMax 3
# end sy-forward gpu-box
```

- `Host` is exactly the host word the person typed in `sy machine add` (an alias from their own
  config works: OpenSSH merges every matching block, and `RemoteForward` accumulates).
- The **target** (right side) is the hub as the operator reaches it: `127.0.0.1:8787` for a
  merged hub, else the host and port of the operator's `hub.url` (`hub.ts.net:443`).
- The **port** (left side) is chosen once per machine and never changes, so the remote's
  config stays valid: 47830, or the first free port in 47830–47849 on the remote (the probe,
  4.2, reports which are free). Stored in the block, in the registry and in the remote's
  `hub.dial`.
- `ExitOnForwardFailure no`: a second SSH window to the same host still opens when the first
  already holds the port. `ServerAliveInterval 30` makes a dead connection (laptop asleep,
  Wi-Fi gone) release the port within about 90 seconds, so the next connection can bind it.
- Options the person already set for that host are respected: `ssh -G <host>` is read first;
  if it already has a `RemoteForward` to the same target, nothing is written ("adopted").

**On the remote.** `hub.reach = "forward"` and `hub.dial` is the local end:

| Hub on | Remote `hub.url` | Remote `hub.dial` |
| --- | --- | --- |
| Operator, merged (`http://127.0.0.1:8787`) | `http://127.0.0.1:47830` | empty (the URL is already loopback) |
| Elsewhere, HTTPS (`https://hub.ts.net`) | `https://hub.ts.net` | `127.0.0.1:47830` |

`hub.dial` is a new `config.toml` key (paths.md 4): the client opens its TCP connection to
`hub.dial` but keeps the URL for everything else (TLS server name and certificate check,
`Host` header). TLS stays end to end; the SSH channel only carries bytes. `unix:<path>` dials
a Unix socket instead (below). `CheckURL` is unchanged; `hub.dial` must be a loopback address
or a `unix:` path, anything else is refused.

**What happens when the laptop sleeps or the window closes.** The forward exists only while
some SSH connection from the operator with that block is open. When it goes away the
remote's flush gets "connection refused", keeps its records (outbox.md 5 step 4, backoff up to
5 minutes) and sends them, in order, within one timer tick (≤ 1 minute) plus the backoff after
the next connection. While no connection is open: no pushes for that machine's sessions from
the hub, unless direct ntfy is on (section 5); a hub that is not on the laptop raises
`machine_silent` after an hour (push.md 3.1). A session in tmux keeps running and queuing.
`sy doctor` on the remote says "hub reached through an SSH forward from the laptop; last
contact 14:02".

**Other local users on the remote** can connect to `127.0.0.1:47830` too, which lets them
reach the hub's unauthenticated endpoints (`/health`, `/v1/pair/claim`, the board's login
page) exactly as anyone on the tailnet can. Every data endpoint needs a token, and claims are
rate limited (pairing.md 3.3). On a shared multi-user server, `--forward-socket` uses
`RemoteForward <remote state>/hub.sock <target>` with `StreamLocalBindMask 0177` instead, so
only the remote user can connect, and `hub.dial = "unix:<path>"`. Caveat: sshd removes a stale
socket only when its own `StreamLocalBindUnlink` is `yes` (default `no`), so after a dropped
connection the next forward may fail to bind; `sy flush` deletes the socket when it is owned by
the user and refuses connections, so the connection after that works. That is why it is not
the default.

### 3.4 `pull`

The hub host runs `ssh` to the remote on a schedule; the remote's `authorized_keys` restricts
that key to one command, `sy outbox drain --stdio`, which streams outbox records and moves the
cursor only for records the hub acknowledged (3.5). The remote holds no hub token and makes
no network connection of its own.

**Hub host side** (`internal/hub/pull`, inside `sy hub serve`):

- **Key.** One key pair per hub, `<state>/pull/id_ed25519` (0600) and `.pub`, made at the
  first `sy hub pull add` with `ssh-keygen -t ed25519 -N '' -C 'switchyard-pull@<hub name>'`
  (the `ssh` client is required on the hub host for pull mode; the Docker image gains
  `openssh-client`). It is used for nothing else and moves with `sy hub export` (8.1).
- **Targets.** `pull_target` rows (section 9): SSH host, port, user, the
  pinned host keys, interval, the bound machine and its pull device.
- **ssh config.** Before each run the hub writes `<state>/pull/ssh_config` and
  `<state>/pull/known_hosts` (0600) from the table, and runs
  `ssh -F <state>/pull/ssh_config -T sy-<target id> .local/bin/sy outbox drain --stdio`
  (the remote command is ignored under the forced command and used only without one). The
  generated block, per target:

  ```
  Host sy-pt_01JA…
    HostName gpu-box.tailnet.ts.net
    Port 22
    User me
    HostKeyAlias sy-pt_01JA…
    UserKnownHostsFile <state>/pull/known_hosts
    GlobalKnownHostsFile /dev/null
    StrictHostKeyChecking yes
    UpdateHostKeys no
    IdentityFile <state>/pull/id_ed25519
    IdentitiesOnly yes
    IdentityAgent none
    BatchMode yes
    ForwardAgent no
    ForwardX11 no
    ClearAllForwardings yes
    RequestTTY no
    ControlMaster no
    ControlPath none
    ConnectTimeout 10
    ServerAliveInterval 15
    ServerAliveCountMax 2
    LogLevel ERROR
  ```

  `-F` makes the person's own `~/.ssh/config` irrelevant (a hub in Docker has none), and
  `HostKeyAlias` keys the pinned host keys by target id, so the hub may reach the remote by
  another name or address than the operator did without weakening the check.
- **Host keys are pinned at registration, never learned.** `sy hub pull add` takes the
  remote's public host keys on stdin (`--host-keys-stdin`, lines of `<type> <base64>`).
  `sy machine add` reads them on the remote (4.2 step 2: the probe prints
  `/etc/ssh/ssh_host_*_key.pub`) over the operator's own SSH connection, which the operator's
  `ssh` has already verified against the person's `known_hosts`, so the keys come through an
  authenticated channel. Typed by hand instead: `sy hub pull add … --fingerprint SHA256:…`
  runs `ssh-keyscan` from the hub host and pins only keys whose fingerprint matches; at a
  terminal without either flag it shows the scanned fingerprints and asks, as `ssh` does,
  and under `--yes` it refuses. `StrictHostKeyChecking yes` then fails any changed key: the
  target goes to state `host_key` (no retries; the Machines screen shows "host key changed:
  check the server, then `sy hub pull trust <name>`").
- **Schedule.** Each enabled target runs every `interval_s` (default 60, 15–3600) with ±10 %
  jitter; at most one run per target and 4 runs at once per hub. A run that ended with
  `more` (budget used up, 3.5) is followed by the next run after 1 s. Failures back off:
  30 s, 1, 2, 5, 10, then 15 minutes. `host_key` and `refused` (3.5 `refuse`) stop
  retrying until the owner acts; `auth` ("Permission denied") retries every 15 minutes.
  The scheduler pauses while the hub is frozen (8.1). `sy hub pull run [<name>]` runs one
  now, in the hub process if it is running (through the database: it sets `next_at = now`),
  else in-process; with no name it does that for every due target. A "Pull now" that lands
  while the target is running survives the run: the run's own next time applies only if
  `next_at` is still what the run started with. The scheduler runs ssh with a context and a
  5 s `WaitDelay`, so stopping the hub never hangs on an ssh whose children hold its pipes.
- **Ingest.** Each batch goes through the same `ingest.Ingester.Ingest` as `POST /v1/ingest`,
  with the target's pull device, inside the hub process, so typing, dedupe, the machine
  heartbeat, the stream and the alert engine behave exactly as for uploads.

**Hub CLI** (`internal/hub/cli/pull.go`; like `sy hub pair`, it works on the database
directly, so it runs on the hub host or through the admin channel, 4.4):

```
sy hub pull add <name> --ssh [user@]host [--port 22] [--machine <m_…>]
                [--interval 60s] (--host-keys-stdin | --fingerprint SHA256:…) [--yes] [--json]
    Creates the target (and, with --machine, its pull device), makes the hub key pair if
    missing. --json: {"target": "pt_…", "name", "public_key": "ssh-ed25519 AAAA… switchyard-pull@hub"}.
sy hub pull list [--json]                  Targets with state, last ok, last error, next run.
sy hub pull run [<name>] [--wait] [--json] One session now (all due targets without a name).
sy hub pull trust <name> (--host-keys-stdin | --fingerprint SHA256:…)   Re-pin after a key change.
sy hub pull disable|enable <name>
sy hub pull remove <name> [--revoke]       Deletes the target; --revoke also revokes its device.
sy hub pull key [--json]                   Prints the hub's pull public key.
```

**Remote side.** `sy setup --hub pull` (or `sy machine add --reach pull`) sets
`hub.reach = "pull"`, `hub.url = ""` and no token, installs the timer (scan, agents, cap) and
adds the hub's key with `sy machine authorize-pull --key-stdin`:

```
restrict,command="/home/me/.local/bin/sy outbox drain --stdio" ssh-ed25519 AAAA… switchyard-pull@hub sy-pull:pt_01JA…
```

- `restrict` turns off port, agent and X11 forwarding, PTY allocation and `~/.ssh/rc`
  (OpenSSH 7.2+; older servers get the explicit `no-port-forwarding,no-agent-forwarding,
  no-X11-forwarding,no-pty,no-user-rc` list, chosen by the probe from `ssh -V`).
- `command=` is the absolute path of the installed `sy`; the client's requested command is
  ignored. `sy outbox drain --stdio` accepts no other arguments when started this way
  (`SSH_ORIGINAL_COMMAND` is set and ignored), reads only the outbox folder and writes only
  `cursor.json`, `flush.status.json`, `rejected.jsonl` and answered spill files.
- `--pull-from <cidr,…>` adds `from="…"` (the hub's tailnet address, say).
- The trailing comment `sy-pull:<target id>` is how `sy machine remove` and
  `authorize-pull --remove` find the line. The file is edited with the same atomic,
  backed-up write as setup (mode kept, `0600` if created, `~/.ssh` `0700`). A line with the
  same key and a different command is replaced; any other line is never touched.

### 3.5 The drain protocol (`sy outbox drain --stdio`, proto 1)

One SSH session carries one **drain session**: the remote writes frames to stdout, the hub
writes frames to the remote's stdin. stderr is for people (the hub logs its first 4 KiB).

**Framing.** One frame = one line of compact UTF-8 JSON ending in `\n`, at most 9 MiB. Every
frame has `t` (its type). Records travel embedded raw: each outbox line is already one compact
JSON object, and it is copied into the frame byte for byte (never decoded and re-encoded), so
the hub stores exactly what was written. Unknown fields are ignored on both sides; an
unknown `t` is a protocol error.

| # | Direction | Frame | Fields |
| --- | --- | --- | --- |
| 1 | remote → hub | `busy` | — The remote could not take `flush.lock` (a local `sy flush` runs). Exit 0; the hub retries after 5 s once, then at the next interval. |
| 1 | remote → hub | `hello` | `proto` (1), `machine`, `name`, `sy` (version), `os`, `arch`, `record_v` (highest record `v` this `sy` writes), `backlog` (records past the cursor, estimate), `oldest` (ts of the oldest unanswered record or null) |
| 2 | hub → remote | `start` | `proto` (1), `hub` (version), `record_v_max`, `max_records` (≤ 500), `max_bytes` (≤ 4 MiB), `budget_s` (≤ 30) |
| 2 | hub → remote | `refuse` | `code`: `proto` (unknown proto), `machine_mismatch` (hello `machine` is not the target's bound machine), `disabled` (target or its device revoked), `frozen` (hub frozen for a move, 8.1); `message` |
| 3 | remote → hub | `batch` | `seq` (1, 2, …), `records` (1..`max_records` raw records, total ≤ `max_bytes`), `more` (bool: records remain past this batch) |
| 4 | hub → remote | `ack` | `seq`, then exactly the `IngestReply` fields: `accepted`, `duplicates`, `rejected` (`{id, reason, detail?}`) |
| 4 | hub → remote | `nack` | `seq`, `code` (`unavailable`), `retry_after_s` — the hub could not store the batch |
| 5 | remote → hub | `done` | `reason`: `drained`, `budget`, `held` (next record's `v` is above `record_v_max`, 7.2), `error`; `backlog`, `held` (count) |
| any | hub → remote | `bye` | The hub ends the session (its budget, shutdown). |

**Remote rules.**

1. Take `flush.lock` (non-blocking); if busy send `busy` and exit 0. Then send `hello` and wait
   up to 30 s for `start` or `refuse`. `refuse` → write `flush.status.json` (`error`,
   `pull_refused: <code>`) and exit 0.
2. Read the next batch from the cursor exactly as `sy flush` does (`outbox.Read`: day files
   then spill files, at most `max_records` / `max_bytes`, stopping before the first record
   whose `v` exceeds `record_v_max`). None → send `done` (`drained` or `held`) and exit 0.
3. Send `batch` with the next `seq` and wait for `ack`/`nack` for up to 60 s. **Stop-and-wait:
   exactly one batch is ever unacknowledged.**
4. On `ack`: it must carry the outstanding `seq`, and every id it names must be in that batch;
   otherwise it is a protocol error (step 6). Apply it with the **same function as
   `sy flush`** (outbox.md 5 step 3: rejected → `rejected.jsonl`; answered spill files
   deleted; cursor moved past the longest answered prefix of day-file records, written
   atomically). An id of the batch missing from the ack means the batch failed: nothing more
   is applied, `done` (`error`), exit 0. Record `last_contact` and `last_success` in
   `flush.status.json` (with `via: "pull"`).
5. Repeat from 2 until the hub's `budget_s` is used (then `done`, `budget`). Before reading
   each batch, check without blocking for a `bye` already waiting: the hub sends `ack` then
   `bye` when its budget (30 s or 20 batches) is up and then reads nothing but EOF, so a
   `bye` after at least one applied batch is the **clean end** of the session (exit 0, no
   error in `flush.status.json`), also when it arrives while a further batch is in flight
   (that batch was not stored and nothing of it is applied).
6. `nack`, a `bye` before any batch was applied, a timeout, EOF on stdin or a protocol error:
   stop without applying anything more and exit (0 for all but a protocol error, 3); the
   cursor stays where the last applied `ack` left it.
7. At the end of a session that applied a batch or found nothing to send, still holding
   `flush.lock`, do what `sy flush` does after a run: enforce `outbox.max_mb` and prune day
   files past `outbox.keep_days` (a pulled machine never runs `sy flush` against a hub).
   `sy flush` on a machine with no hub URL also prunes. `sy outbox drain --peek` takes
   `flush.lock` too (reading quarantines corrupt lines) and refuses while it is held.

**Hub rules.**

1. Read `hello` (10 s from the moment the session is open; the hub adds 20 s for ssh's own connect and key exchange, which run before the remote can speak). Refuse when `proto` ≠ 1, when the target or its device is revoked or
   disabled, when frozen, or when `machine` differs from the target's bound machine. A target
   with no bound machine yet (registered by hand without `--machine`) binds to the first
   `hello`'s machine after its host key check passed (trust on the verified host, not on the
   claim alone), and creates the pull device then. That first bind is refused
   (`machine_mismatch`, "that machine uploads to the hub directly") when the machine had
   contact with another reach than `pull` in the last 10 minutes: the claim alone must not
   take over a machine that already uploads. Refusals are only for a target that is gone,
   disabled or revoked, a frozen hub, a mismatch or such a bind; any other store error ends
   the session as a failed run and backs off.
2. Count the `hello` as contact: machine heartbeat with `last_seen = now`, `backlog` and
   `backlog_oldest` from `hello`, `sy_version`, `reach = 'pull'`, even when nothing follows.
3. For each `batch`: ingest `{"v":1,"client":"sy/<hello.sy>","records":[…]}` with the pull
   device and `backlog` = the remote's `more`-adjusted count. Only **after** the ingest
   transaction committed, send `ack` with the ingest reply. A store error → `nack`, end the
   session, back off. A batch whose records are all `machine_mismatch` is acked like any
   other (the ids are rejected, the remote logs them), and the target goes to state
   `refused` because the remote's state folder changed.
4. Budget: 30 s or 20 batches per session; then `bye`. `more` on the last batch schedules
   the next session after 1 s.
5. ssh exit status, the first stderr line and the outcome go to the target row
   (`last_try_at`, `last_ok_at`, `last_error`, `fail_count`, `state`) and the log.

**Why nothing is lost or duplicated.** The hub commits before it acks; the remote moves its
cursor only after an ack for exactly that batch. A crash, timeout or dropped connection at any
point leaves the cursor before the unacknowledged batch, which is sent again in the next
session; the hub then answers those ids as duplicates (same pull device), so each record is
stored once. Records go in outbox order, as with `sy flush`. Only one drain or flush runs at a
time on the remote (`flush.lock`), and only one session per target on the hub. A hub that
acks ids it did not store could make the remote forget records, which is the same trust a
remote already places in a hub's ingest reply.

**Without SSH** (tests, debugging): `sy outbox drain --stdio` works on any pipe, e.g. the
Go tests connect it to an in-process hub with `io.Pipe`. `--peek` prints the next
`max_records` records as JSONL and changes nothing (for people; no protocol).

### 3.6 Changing reach later

`sy connect <url> --keep-token` (direct), `sy machine add <host> --reach …` again (it is
idempotent and only changes what differs: removes a forward block when leaving `forward`,
the `authorized_keys` line when leaving `pull`), or by hand on the remote with
`sy setup --hub …`. The hub learns the new reach from the next `machine_info` (6.3) or from
the first pull. A machine that switches from `pull` to `direct` gets a new device (its own
token); records already stored through the pull device are answered as duplicates, not
`id_conflict`, because the dedupe rule compares machines (record.md 3.1, ).

## 4. `sy machine add`

### 4.1 Command

```
sy machine add <[user@]host> [--name <name>] [--reach auto|direct|forward|pull]
               [--hub-url <url>] [--insecure-lan] [--port <n>] [--forward-socket] [--pull-from <cidr,…>]
               [--hub-admin local|<command prefix>] [--code-stdin]
               [--version <v> | --binary <path>]
               [--accounts <labels>] [--share <switches>] [--login ask|never]
               [--direct-push] [--ssh-option K=V]… [--yes] [--dry-run] [--json]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `host` | | Anything `ssh` accepts: `me@gpu-box`, an alias from `~/.ssh/config`, `host:port` is **not** accepted (use the config or `--ssh-option Port=…`) |
| `--name` | the remote's short hostname | Machine name (identity.md 2). |
| `--reach` | `auto` | 3.1. |
| `--hub-url` | 3.2 | The hub address as the remote will use it (direct). |
| `--insecure-lan` | off | Passed to the remote's `sy setup` (plain `http` to a LAN host, tokens.md 4); the e2e tests use it. |
| `--port` | 47830 or the first free one | Forward port on the remote (3.3). |
| `--hub-admin` | `machines.hub_admin` in `config.toml`, else `local` when `<data>/hub.db` exists here, else ask | 4.4. Saved after first successful use. |
| `--code-stdin` | | Read a machine pairing code (digits from the board's **Add machine**) from stdin instead of using the admin channel. |
| `--version` | this `sy`'s version | Release to install on the remote (7.3). |
| `--binary` | | Install this file instead of a release (development builds; the e2e tests). Its platform must match the remote (checked with `go version -m`-style build info read from the file, else by running it). |
| `--accounts`, `--share` | as `sy setup` | Passed to the remote's `sy setup`. |
| `--login` | `ask` at a terminal, else `never` | 4.2 step 8. |
| `--direct-push` | off | Also run `sy direct-push on` on the remote with the operator's direct-push settings (section 5), topic and token on stdin. |
| `--yes`, `--dry-run`, `--json` | | As `sy setup`: `--dry-run` prints the steps and changes nothing anywhere (it still runs the read-only probe). |

`SY_SSH_COMMAND` (words, no shell; default `ssh`) replaces the `ssh` program, like
`GIT_SSH_COMMAND`: the e2e tests use `SY_SSH_COMMAND="ssh -F <test config>"` because OpenSSH
finds `~/.ssh` from the password database, not `$HOME`. `--ssh-option K=V` (repeatable) adds
`-o K=V` except for the forbidden options in 10.1.

### 4.2 Steps

Each step prints one line (screen "Adding a machine": `✓ probed gpu-box: linux/arm64, sy not
installed`), `--json` prints one object per step and a final summary. The command is
idempotent: rerunning after a failure redoes only what is missing.

1. **Connect.** `ssh <host> -- uname -sm` through `internal/sshx` with the person's config,
   agent and `known_hosts`. Host keys follow 10.1: at a terminal an unknown key gets
   OpenSSH's own prompt (stdin inherited for this one call); without a terminal
   `BatchMode=yes`, and a failure prints "ssh \<host> once by hand to check and accept its
   host key". `Windows_NT` / `MINGW*` / `CYGWIN*` → exit 2 "use install.ps1 on that machine".
2. **Probe.** If `~/.local/bin/sy` exists and its version is ≥ 0.5, run
   `sy machine probe --json [--hub-url U] [--ports 47830-47849]`; otherwise a POSIX `sh`
   script does the read-only parts (home folder, `$SHELL`, `ssh -V`, whether `claude` is on
   the login PATH and its version, Darwin Rosetta check, existing `sy` version, host public
   keys). After the binary step the probe runs again with the new `sy`, which adds: machine
   id (creating `machine.json`), Claude config folders and who is logged in, free forward
   ports, linger, and a `GET <hub-url>/health` from the remote with a 5 s timeout
   (reachable, TLS ok, hub version). Nothing in the probe output is secret.
3. **Binary.** Unless the remote already runs the target version: obtain the archive for
   the remote's `os/arch` (7.3 steps 1–4, cached in `<state>/update/cache/`, verified against
   `SHA256SUMS`), extract `sy`, and stream it over SSH into `~/.local/bin/.sy.<pid>.tmp` with
   a fixed script that writes, `chmod 0755`s and prints its SHA-256 (`sha256sum`, else
   `shasum -a 256`, else `openssl dgst -sha256 -r`). The operator compares it with the
   archive's binary; only on a match does a second call `mv -f` it over `~/.local/bin/sy`
   (atomic; running hooks keep the old inode). No hash tool on the remote: the new file must
   print exactly the target version (`version --short`), and a warning says the hash was not
   checked remotely. Development builds: `--binary`, or this `sy` itself when its platform
   matches, with "not a release build" in the summary.
4. **Hub admin** (4.4), once per run in every mode: `direct` and `forward` mint a **machine
   pairing code** bound to the machine id from step 2 (`sy hub pair --kind machine --name
   <name> --machine <id> --url <hub url as the remote sees it> --ttl 10m --json`); `pull`
   registers the target instead (`sy hub pull add … --json`, host keys on stdin) and reads
   the hub's pull public key from its reply. With `--code-stdin` (direct and forward only)
   this step is skipped.
5. **Forward only:** write the `~/.ssh/switchyard.conf` block (3.3). The next steps open
   their SSH connection with `-o ExitOnForwardFailure=yes -R 127.0.0.1:<port>:<target>` so
   the claim works now; if the port is busy because another connection already forwards it,
   the step retries without `-R`.
6. **Setup on the remote** (no TTY; secrets on stdin only):

   | Reach | Remote command | stdin |
   | --- | --- | --- |
   | direct | `sy setup --yes --json --login never --hub join:<url> --code-stdin [--accounts …] [--share …]` | the pairing **secret** (22 characters) |
   | forward | same, plus `--hub-dial 127.0.0.1:<port>` (or `unix:<path>`), `--reach forward` | the secret |
   | pull | `sy setup --yes --json --login never --hub pull [--accounts …] [--share …]`, then `sy machine authorize-pull --key-stdin [--from …] --target <id>` | the hub's public key |

   The operator mints the code through the admin channel and passes the 128-bit **secret**,
   not the six digits: claims by secret are not subject to the typed-code limits (pairing.md
   3.3), and nothing typed by a person is involved. With `--code-stdin` the digits the person
   pasted are passed instead. `sy setup --code-stdin` accepts either shape (6 digits with
   optional separators, or 22 base64url characters) and claims with `code` or `secret`
   accordingly. Exit 7 ("Claude Code not found") stops here with "install Claude Code on
   \<host>, then run this again"; the binary stays.
7. **Verify.** `direct`/`forward`: the remote's setup ran `sy connect`, which sent one
   `slot_info` per folder; `sy machine add` then asks the hub (admin channel:
   `sy hub machine show <id> --json`) for `last_seen` within the last minute. `pull`: it runs
   `sy hub pull run <name> --wait --json` through the admin channel and expects `ok`. A
   failure here is reported with the fix but nothing is rolled back.
8. **Logins.** For each folder the remote setup listed in `needs` (not logged in), at a
   terminal and with `--login ask`: "Log in ~/.claude-2 (work) on gpu-box now? [Y/n]", then
   `ssh -t <host> .local/bin/sy account login <folder>` with the person's terminal inherited
   (setup.md 8.2: Claude Code prints the URL, the person opens it anywhere and pastes the
   code into this terminal). `sy` never sees the code: it passes through the SSH PTY to the
   remote `claude`. Without a terminal or with `--login never`, the summary lists the exact
   commands (`ssh -t gpu-box sy account login ~/.claude-2`).
9. **Direct push** (`--direct-push`): `sy direct-push on --server <s> --private <bool>
   --secret-stdin` on the remote, with topic and token from the operator's
   `<state>/push/direct.json` on stdin (section 5).
10. **Link keys** (`direct` and `forward`; keys.md 4.2, peers.md 2.1). `sy keys device
    --json` on the remote makes (first use) and prints its device id, machine id and public
    link keys; `sy hub keys register <device> --machine <id> --keys-stdin --json` through the
    admin channel signs them (`reg = ssh`) and returns the certificate, the hub key and the
    active owner key. The operator checks the certificate with that hub key (this device,
    machine and keys) and refuses a hub key other than the one its own `keys/hub.pub` pins.
    `sy keys pin-ssh --json` on the remote (stdin: hub key, owner key, certificate) checks the
    certificate against its own keys and pins both keys (`how = ssh`). Last, on the owner
    machine at a terminal, the operator signs the certificate with the owner key (passphrase,
    signing guard) and stores the signature with `sy hub keys certify`; elsewhere the summary
    says "run `sy keys certify <device>` on the owner machine". Keys on the revocation list
    are renewed once (`sy keys device --renew`). With no admin channel the step is skipped
    with "run `sy agent enable` on \<host>" (`reg = tofu`, then `sy keys certify`). `pull`
    machines have no route to the hub and hold no link, so they get no link keys: peers there
    reach only that machine's own sessions. Failures here do not undo the join; running the
    add again is idempotent (same keys, same pins).
11. **Registry.** Write `{name, machine, ssh: host, reach, port?, pull_target?, os, arch,
    sy_version, added_at, updated_at}` to `<state>/machines.json` (atomic, 0600).

Summary (screen `SetupDone` style): one line per step, then "gpu-box reports to
https://hub.ts.net (direct). Next: `ssh gpu-box` and start Claude with `claude-2`."

Exit codes: 0 done; 1 a step failed (the line says which and how to continue); 2 usage or
unsupported remote; 3 needs a person (no terminal, no `--yes`); 6 joined but verification
failed; 7 Claude Code missing on the remote.

### 4.3 Remote commands

- Every remote call is `ssh [opts] <host> -- <words…>`. The words are either fixed program
  text or values that match `^[A-Za-z0-9_./:@%+=,-]+$`; anything else (a folder with a
  space, an odd label) is passed on stdin as JSON instead. They are single-quoted anyway,
  so a remote login shell of `bash`, `zsh`, `fish` or `dash` reads them the same. No call
  depends on the remote PATH: `sy` is `.local/bin/sy` relative to the home folder (ssh
  starts there), and the probe resolves `claude` through `$SHELL -lc 'command -v claude'`.
- Fixed scripts run as `sh -c '<script>' sy-remote <args…>`; scripts contain no `'`, `!` or
  backquote, so `csh`-family login shells pass them through unharmed.
- **No secret is ever an argument** (argv is visible to every user in `ps`): pairing codes and
  secrets, ntfy topics and tokens, and the pull public key (not secret, but uniform) travel on
  stdin; tokens never leave the remote.
- `ssh` gets `-o ConnectTimeout=15` and `-o ServerAliveInterval=15` unless the person's config
  sets them; nothing else is overridden.

### 4.4 The admin channel

Minting machine codes and registering pull targets need the hub host (pairing.md 2.4: a
machine token can never make codes). `--hub-admin`:

| Value | How | When |
| --- | --- | --- |
| `local` | In-process against `<data>/hub.db` and `<state>`, as `sy hub pair` does | Merged hub on the operator |
| a command prefix | The prefix's words followed by `hub …`, e.g. `ssh hub sy` → `ssh hub sy hub pair --kind machine … --json`; `ssh hub docker compose -f /srv/sy/compose.yml exec -T hub sy` for the Docker hub | Hub on another machine the person can SSH to |
| (none) | Interactive: "Open \<hub>/#/more/devices → Add machine, and paste the code here" (echo off); then the code goes to the remote as with `--code-stdin`. `pull` cannot work without a channel and says so. | No SSH to the hub |

The prefix is split into words like `SY_SSH_COMMAND` (no shell); `sy` itself never interprets
it beyond appending arguments. Commands used: `sy hub pair --kind machine … --json`,
`sy hub pull add … --json`, `sy hub pull run <name> --wait --json`, `sy hub machine show
<id> --json`, `sy hub keys register <device> --machine <id> --keys-stdin --json`, `sy hub
keys certify <device> --fp <fp> --sig-stdin`, `sy hub pull remove <name>`, `sy hub token
revoke <device>`.

### 4.5 Other machine commands

| Command | Runs on | Does |
| --- | --- | --- |
| `sy machine [show] [--json]` | any | Unchanged: id and name. |
| `sy machine rename <name>` | any | identity.md 2: changes `machine.json` and sends a `slot_info` and a `machine_info` with the new `name` (6.3); the hub's name follows with the next flush. |
| `sy machine list [--json]` | operator | The registry, with each machine's hub view when the hub is reachable (reach, last seen, backlog, version). |
| `sy machine remove <name> [--revoke] [--keep-remote]` | operator | Removes the forward block, the remote's `authorized_keys` line and pull target, and the registry entry. `--revoke` also revokes the machine's devices through the admin channel (the hub keeps its history; the board hides removed machines). The remote's `sy`, outbox and Claude folders are left; `ssh host sy uninstall` is printed as the next step. |
| `sy machine repoint <url> [--machines all\|a,b]` | operator | After a hub move (8.3): `ssh host .local/bin/sy connect <url> --keep-token` for each registered `direct`/`forward` machine, 4 at a time; pull targets need nothing. |
| `sy machine probe --json [--hub-url U] [--ports A-B]` | remote | 4.2 step 2. Read-only except creating `machine.json`. |
| `sy machine authorize-pull --key-stdin --target <id> [--from cidrs] \| --remove --target <id>` | remote | 3.4. |
| `sy hub machine show <id> [--json]` | hub host | One machine's read model (6.1), for the admin channel. |

## 5. Direct ntfy from `sy`

Off by default. For machines whose hub may be unreachable while sessions run (`forward`,
`pull` with a merged hub, a hub that is down). Only ntfy: Web Push needs the hub's VAPID keys.

### 5.1 What a machine can compute, and what it cannot

| Trigger | On the machine? | Why |
| --- | --- | --- |
| `needs_permission`, `asked`, `failed` | **Yes** | The machine folds its own outbox with the same pure state fold as the hub (`internal/hub/state`, the one client exception in ARCHITECTURE.md), so it knows each session's state and the record `ts` that entered it: the episode key `s:<session>:<state>:<since_at ms>` (push.md 3.2) is the same string the hub computes. |
| `limit_80`, `limit_95`, `reset` | No | Their keys use hub seat ids and the hub's window model; a machine sees only its own samples. |
| `machine_silent` | No | It is about the machine itself. |
| summaries, snooze re-pushes, Snooze/Seen buttons | No | They need the hub's alert rows and its action key. |

### 5.2 Setup

```
sy direct-push on [--server https://ntfy.sh] [--private=true] [--after 2m] --secret-stdin
sy direct-push off | status [--json] | test
```

- stdin: one JSON object `{"topic": "…", "token": "tk_…"}` (token optional), or at a terminal
  two prompts (echo off). Stored in `<state>/push/direct.json` (0600); `config.toml` keeps only
  `push.direct.enabled`, `server`, `private`, `after_s` (paths.md 4), so `config.toml` still
  holds nothing secret. The topic is the secret on ntfy.sh, which is why it is never an
  argument. The person normally uses the same topic as a hub ntfy target, so the phone
  already listens.
- `private` defaults to true when the server is `ntfy.sh` (push.md 5.5).
- `test` sends "Switchyard direct push test from gpu-box".

### 5.3 The policy (consistent with the hub's rules)

When the hub is reachable, `sy tick` fetches `GET /v1/push/direct-policy` (6.2) at most once an
hour and stores it in `<state>/push/policy.json`; a pull-mode machine has no token and uses the
defaults below unless the person copies a policy file (`sy direct-push policy --file`). The
reply is the hub's rules **resolved for this machine**:

```json
{"v": 1, "hub": "0.5.0", "public_url": "https://hub.example.ts.net", "machine": "m_01JA…",
 "settings": {"quiet_hours": {"enabled": true, "start": "23:00", "end": "08:00", "tz": "Europe/Berlin"},
              "skip_remote": false, "skip_peers": true, "max_age_s": 900},
 "rules": [
   {"trigger": "asked", "enabled": true, "delay_s": 300, "bypass_quiet": false,
    "seats": [{"account": "3f1c…", "org": "9b20…"}], "projects": ["infra"]},
   {"trigger": "needs_permission", "enabled": true, "delay_s": 30, "bypass_quiet": false, "seats": [], "projects": []},
   {"trigger": "asked", "enabled": true, "delay_s": 60, "bypass_quiet": false, "seats": [], "projects": []},
   {"trigger": "failed", "enabled": true, "delay_s": 0, "bypass_quiet": true, "seats": [], "projects": []}
 ]}
```

- Only the three session triggers. Overrides scoped to other machines are dropped, the
  `machines` list is resolved away, and seat ids become `(account, org)` pairs (the machine
  knows logins, not hub seat ids). Rules are ordered by push.md 3.4's tie-breaks (most
  non-empty lists, overrides before base, newest, lowest id); **the client takes the first
  rule whose trigger matches and whose `seats` and `projects` lists are empty or contain the
  session's seat and project.** `project` is the session's `cwd` base name, as the hub
  derives it. So the machine reaches the same rule the hub would.
- No policy yet: the base defaults of push.md 3.1 (30 s, 60 s, 0 with bypass), no quiet
  hours, `max_age_s` 900, `skip_peers` on. A policy without `skip_peers` (from a hub that
  predates it) also means on, the hub's default.
- No device prefs apply (the ntfy topic is not a hub device), only the hub-wide quiet hours
  and the direct-push `private` switch.

### 5.4 When the machine pushes

Checked by `sy tick` (every minute) and by the detached `sy flush` that `sy hook` starts:
when that flush fails and direct push is on, it computes the next due episode and, if one is
due within 90 seconds, waits for it (holding no lock), flushes once more, then decides. For
every session of this machine with records in the last 24 hours, folded from the outbox:

| # | Condition |
| --- | --- |
| 1 | State is `needs_permission`, `asked`, or `failed` with `needs_you`; the rule (5.3) is enabled; not (`skip_remote` and the session has a Remote Control link and trigger ≠ `failed`); not (`skip_peers` and the state is `asked:question` in a turn of coordination between sessions, push.md 3.5). |
| 2 | `now ≥ since_at + delay_s + 15 s` (the 15 s grace lets a hub that is just slow win). |
| 3 | **The hub has not answered the record that entered the state**: it is at or after the cursor (or still in a spill file). This is the dedupe boundary: once the hub has the record, the alert is the hub's. |
| 4 | **The hub is unreachable**: `flush.status.json` has no `last_contact` since that record was written, or the last one is older than `after_s` (default 120 s; in pull mode 180 s). |
| 5 | `now ≤ since_at + delay_s + max_age_s` (the hub's stale guard, push.md 3.3). |
| 6 | Not in quiet hours, unless `bypass_quiet`. |
| 7 | No marker for the episode: `<state>/push/direct/<sha256(episode)[:16]>.json`, created with `O_EXCL`, so two processes never both send. |
| 8 | At most 6 direct pushes per 10 minutes (push.md's flood default); the rest are recorded as skipped. |

Then it POSTs the ntfy message (10 s timeout, no redirects) and writes the marker with the
outcome. The message follows push.md 5.5 and 7.2 for session triggers, with these
differences: the seat label is the slot's local label (`slots.labels`), else the email's local
part; actions are **Open in Claude** (Remote Control link) when known and **Open** (the
policy's `public_url` + `/#/sessions/<id>`) when a public URL is known; no Snooze/Seen; tag and
priority as push.md 7.2; private mode as 5.5. Title suffix `· direct` so the person can tell.

### 5.5 Telling the hub: the `direct_push` record

Right after sending, `sy` appends a `direct_push` record (record.md 4.9) to the outbox. When it
reaches the hub (the record that entered the state is necessarily earlier in the outbox):

- If no alert exists for the episode: create it as `fired`, reason `direct`, `fired_at` =
  `sent_at`, `fire_count = 1`. Nothing is sent.
- If one exists as `pending`, or `skipped` with reason `stale`: set it to `fired` / `direct` the
  same way. A `pending` alert never fires afterwards.
- If one exists as `fired`, `snoozed`, `seen`, `cancelled` or `closed`: nothing changes; a fired
  one means both pushed (the hub got the record but its reply was lost); logged as
  `direct_push duplicate`.
- `ok: false` changes nothing (the hub may still push).
- A `direct_push` for a session that is not on the sending machine is stored and ignored.

**Catch-up hold** (push.md 3.3): when the hub creates a session alert whose due time
has already passed and the batch came from a machine that reported more records queued
(`X-Sy-Backlog` above the batch size, or a drain batch with `more`), `due` becomes `now + 10 s`,
and at fire time the scheduler postpones it by another 10 s while that machine's last
heartbeat (≤ 10 s old) still reports a backlog, up to 2 minutes. That gives the machine's
`direct_push` record, a few batches behind, time to arrive and mark the episode fired. The
stale guard still applies.

The episode keys match unless the hub clamped the record's `ts` (more than 5 minutes ahead
of the hub clock, record.md 1.6); then the hub's own alert is a different episode, usually
skipped as stale. That is the only known duplicate path, and it needs a badly wrong clock.

## 6. The Machines screen and its API

### 6.1 `Machine` read model (api.yaml `Machine`, fields added for machine management)

| Field | Type | Meaning, and where it comes from |
| --- | --- | --- |
| `reach` | `direct` \| `forward` \| `pull` \| null | Last `machine_info.reach`, or `pull` after a pull |
| `last_seen` | time | Unchanged: last request from it, **or the last drain `hello`** |
| `last_record` | time | Unchanged |
| `backlog` | integer \| null | Records still queued: `X-Sy-Backlog` or the drain `batch` count minus the batch's own records (they are answered by that ingest, so a flush that sent everything leaves 0), or drain `hello` |
| `backlog_oldest` | time \| null | Ts of the oldest unanswered record: `X-Sy-Backlog-Oldest` (new, sent with `X-Sy-Backlog` when the backlog is more than one batch), drain `hello.oldest`, or `machine_info` |
| `backlog_at` | time \| null | When `backlog` was reported |
| `sy_version` | string \| null | Unchanged |
| `version_status` | `same` \| `older` \| `newer` \| `unknown` | `sy_version` compared with the hub's version: the same string (after a leading `v` and `+build` are dropped) is `same`, so two builds of one untagged commit (`104cd7f`) match; otherwise by semver (pre-release, commit names and `dev` → `unknown`; `dev` never matches itself) |
| `record_v` | integer \| null | Highest record `v` the machine writes (`machine_info`) |
| `held` | integer | Records the machine holds back because the hub is too old (7.2), from `machine_info`; 0 normally |
| `os`, `arch` | string \| null | Now filled, from `machine_info` |
| `direct_push` | boolean | From `machine_info` |
| `timer_ok`, `linger` | boolean \| null | From `machine_info` (`sy doctor`'s checks) |
| `pull` | object \| null | Pull targets only: `{name, target: "me@gpu-box", interval_s, state: ok\|failing\|host_key\|auth\|refused\|disabled\|frozen, last_try, last_ok, last_error, next_at}` |
| `alert` | AlertBrief \| null | The open `machine_silent` alert, if any (push.md 4) |
| `silent`, `live_sessions`, `slots` | | Unchanged |

The screen ("Settings · Hub and machines"): one card per
machine, sorted silent first, then by name. Each card: name, `os/arch`, reach chip
("Tailscale", "via laptop SSH", "pulled by hub every 1 min"), status line (`Live`,
`Quiet 1 h 12 min` with the alert, `Seen 3 min ago`), backlog ("1 240 queued, oldest 14:02")
shown when above 0, version ("0.5.0 · same as hub", amber "0.4.2 · older than hub 0.5.0", red
"holding 37 records: hub too old"), direct push on/off, pull state with **Pull now** (owner),
and its slots. Owner actions: **Rename**, **Remove** (6.2), **Pull now**. The **Add machine**
dialog (pairing.md 2.4) now shows two commands: `sy machine add user@host` (run on your laptop)
and the one-line install for the server itself. Deep link `/#/machines?machine=<id>`
(push.md 5.8) is unchanged.

### 6.2 New operations

All four are in api.yaml (`internal/hub/api/routes.go` lists them with their scopes).
The schemas they use are `MachinePatch`, `PullStatus`, `DirectPolicy` and `DirectPolicyRule`.

| Method and path | Scopes | Body → reply | Errors |
| --- | --- | --- | --- |
| `PATCH /v1/machines/{id}` | owner | `MachinePatch` `{name?}` → `Machine` | 400 name not 1–60 chars; 404 |
| `DELETE /v1/machines/{id}` | owner | — → 204. Sets `machine.removed_at`, revokes every device bound to it (pull device included, with the board sessions opened from them), disables its pull target, ends its open alerts with reason `ended` (`pending` ones become `cancelled`, as they never pushed (push.md 4); fired, snoozed and seen ones `closed`). History stays; removed machines are left out of `/v1/machines` and `/v1/overview` unless `?all=true`. A later record from a new device for the same machine id clears `removed_at`. | 404; 409 when it is the machine the caller's own device is bound to (for a board session: the device it was opened from) |
| `POST /v1/machines/{id}/pull` | owner | — → 202 `PullStatus` (sets `next_at = now`) | 404 no pull target; 409 target in `host_key` or `refused` state (fix first) |
| `GET /v1/push/direct-policy` | write | — → `DirectPolicy` (5.3) for the machine the token is bound to | 403 for a device without a machine |

`GET /v1/machines` keeps its path and scopes; its `Machine` items gain 6.1's fields, and
`?all=true` includes removed machines (with `removed_at`). The stream's `machine` event
(api.yaml `StreamMachine`) carries the same object.

Other contract changes, no new path:

- `GET /health` gains `record_v_max` (integer), `batch_v` (integers), `drain_proto` (integer),
  `frozen` (`{since, moved_to}` or absent). Already in api.yaml (`Health`).
- `POST /v1/ingest` replies gain `hub: {version, record_v_max}` (`IngestReply.hub`) and accept
  the request header `X-Sy-Backlog-Oldest` (RFC 3339). While frozen (8.1) ingest answers `503`
  with `code: unavailable`, message "hub is frozen for a move", `Retry-After: 300`.

### 6.3 `machine_info` (record.md 4.8)

Sent by `sy tick` when its content (minus counters and times) changed since the last one sent,
and otherwise every 6 hours; also by `sy connect`, `sy setup`, `sy update` and
`sy machine rename`. The last one sent is remembered in `<state>/machine-info.json`.
It carries the machine's own name (`name`, from `machine.json`), so a rename is a change
even for `sy tick`. The hub's `machine.name` follows a reported name that differs from the
one in the last stored `machine_info` (record.md 4.8): `sy machine rename` reaches the board
and the CLI with the next flush, while a rename on the board (`PATCH /v1/machines/{id}`)
stays until the machine itself is renamed again.

## 7. Versions and `sy update`

### 7.1 The version numbers that matter

| Number | Where | Changes when | Current value |
| --- | --- | --- | --- |
| `sy` semver | `version.Version`, batch `client`, record `client`, `/health.version` | Every release | 0.5.x |
| Record `v` | Every record (record.md 3) | A kind's envelope or payload changes incompatibly | 1 (all kinds, including the two new ones) |
| Batch `v` | Upload body (record.md 2) | The batch shape changes | 1 |
| Drain `proto` | 3.5 | The drain frames change incompatibly | 1 |
| Schema | `schema_version`, `/health.schema` | A migration | 4 |

Only record `v` needs negotiation: the others are fixed today, and a hub that sees an
unknown batch `v` answers 400, which the client keeps retrying without loss.

### 7.2 Mixed versions

- **The hub accepts every older record `v`** forever: parsers for old versions are never
  removed. It still stores a record with a higher `v` raw (`typed = 0`, record.md 5), as a
  second line of defence, and retypes such records at start when it has learned that `v`
  (an upgraded hub types what an older one stored).
- **The hub says what it reads.** `/health.record_v_max` and every ingest reply's
  `hub.record_v_max` (and the drain `start` frame). A hub without the field (older hubs) reads exactly `1`.
- **The client never sends a record whose `v` is above the hub's `record_v_max`.** It caches
  the last value in `<state>/hub.json` (`{url, version, record_v_max, at}`, written by
  `sy connect`, by every ingest reply that carries `hub`, and by a `/health` check in
  `sy tick` every 6 hours). The flusher (and the drain, 3.5) stops at the first record above
  it, as at the end of the outbox: everything before is sent, the cursor stays before that
  record, and `flush.status.json` gets `held: <count>` and `hub_too_old: {hub, needs}`.
  `sy doctor` says "the hub (0.4.2) reads records up to v1, this machine writes v2: update
  the hub". Held records are kept under the size cap like any other (outbox.md 6). When a
  reply shows a higher `record_v_max`, the next flush continues.
- **Writers prefer what the hub reads.** A future change that bumps a kind's `v` must keep
  writing the previous version for one minor release whenever `<state>/hub.json` says the hub
  is older and the fact can be expressed in it; only facts that cannot be are held. Today
  every `v` is 1, so this is the rule for later versions.
- **Order of updates** therefore does not matter for correctness; `sy update --machines`
  still updates the hub host first (7.4), so nothing is held.
- Unknown reply fields are ignored by every client (api.yaml rule), so newer hubs never break
  older clients.

### 7.3 `sy update` (one machine)

```
sy update [--version <v>] [--check] [--yes] [--json] [--allow-downgrade]
          [--from-file <archive> --sums-file <SHA256SUMS>] [--rollback]
```

`internal/update` implements install.sh (setup.md 1.2) in Go, so every platform verifies the
same way:

1. **Source.** `SY_INSTALL_BASE_URL`, else `update.base_url`, else
   `https://github.com/<owner>/switchyard/releases`. `latest` → `<base>/latest/download/SHA256SUMS`
   (the version is read from the archive names in it); a version → `<base>/download/v<v>/SHA256SUMS`.
   No GitHub API call. HTTPS only (a `http://` base is accepted only for a loopback host, for
   tests). Redirects are followed only to `https://` (GitHub serves downloads from
   `objects.githubusercontent.com`).
2. **Pick** `sy_<v>_<os>_<arch>.tar.gz` (`.zip` on Windows) for this machine (`runtime.GOOS`,
   `GOARCH`; on Darwin under Rosetta, arm64). Its line must be in `SHA256SUMS`.
3. **Download** to `<state>/update/` (0700), at most 200 MB, then compare SHA-256 with the
   line. Mismatch or missing line: exit 3, nothing changed. If `gh` is installed
   and the check is not off (`update.verify_attestation`, default true; `SY_VERIFY_ATTESTATION=0`
   or `--no-attestation` turn it off): `gh attestation verify <archive> --repo
   <owner>/switchyard`; failure: exit 3. Without `gh` the update goes on with the checksum
   alone and prints a note saying the attestation was not checked; `SY_VERIFY_ATTESTATION=1`
   makes a missing `gh` exit 3. On the fleet path (7.4) the operator checks the archive it
   downloaded and the remote update runs with `--no-attestation` (the remote has no internet).
4. **Extract** only the `sy` (`sy.exe`) entry: a regular file, no path components, at most
   100 MB. Run it with `version --short`: it must print the target version (proves it runs
   here).
5. **Refuse** when the target is older than the current version without `--allow-downgrade`;
   when the current binary is owned by a package manager (resolved path under
   `/opt/homebrew`, `/usr/local/Cellar`, `/home/linuxbrew`, `/nix/store`, `/usr/bin`,
   `/usr/sbin`, or a WinGet `Packages` folder: "update with brew/winget/your package
   manager"); on a hub host, when the new binary's `sy hub schema --max` is below the
   database's schema ("this version cannot open the hub database (v4)").
6. **Replace** the resolved `os.Executable()` atomically: Unix: copy the current file to
   `<dir>/sy.old`, write `<dir>/.sy.<pid>.tmp` (0755), `rename` over `sy` (running hooks keep
   the old inode). Windows: rename `sy.exe` to `sy.exe.old` (allowed while it runs), move the
   new file in; `sy.exe.old` is deleted by the next `sy` run that can.
7. **After**, with the new binary (`sy update --post-update <old version>`, an internal step):
   after a rename of the command, give the binary its current name and leave the old name as
   a link to it (rename.md 2.13); move what an older release installed under the old project
   name (rename.md 2.2 to 2.4); refresh the plugin marketplace and run `claude plugin marketplace update sy-local` and
   `claude plugin update switchyard@sy-local` per plugin root (setup.md 5.2); rewrite the
   timer unit and the hub service unit if their content changed; restart the hub service if
   it is installed here (`internal/service`: systemd user unit, launchd agent, scheduled
   task); send `machine_info`; print the `sy doctor` summary line.
8. `--rollback` swaps `sy.old` back (and runs step 7 for it). `--check` does steps 1–2 only and
   prints `current`, `latest`, and, when connected, the hub's version and whether it reads this
   version's records. `--from-file` with `--sums-file` skips the download (used by 7.4's push
   path); the checks of steps 2–5 still run.

Exit codes: 0 updated or already current; 2 usage; 3 verification failed; 4 download failed;
5 refused (step 5); 1 replace or post-update failed (the message says whether `sy.old` was
restored).

### 7.4 `sy update --machines` (every added machine)

```
sy update --machines all|<name,…> [--version <v>] [--yes] [--json]
```

Run on the operator. It first updates itself (unless `--version` is given and it is already
there), then brings every named registry machine to **the operator's version**, so the whole
fleet matches:

1. Order: the hub host first (the registry entry marked `hub`, or the machine named by the
   admin channel's prefix), then the others, 4 at a time.
2. Per machine: `ssh <host> .local/bin/sy update --yes --json --version <v>`.
3. Exit 4 there (no internet on the server): the operator downloads that platform's archive and
   `SHA256SUMS` (7.3 steps 1–3), uploads both with 4.2 step 3's hash-checked transfer to
   `<remote state>/update/`, and runs `sy update --yes --json --version <v> --from-file …
   --sums-file …` there; the remote verifies again before replacing.
4. A table: machine, old → new, outcome. One failure does not stop the others; exit 1 if any
   failed.

Nothing here is started by the hub: the rule that the hub cannot command clients holds.
The operator uses the person's SSH access, as they would by hand.

## 8. Moving the hub: `sy hub export` and `sy hub import`

### 8.1 Export

```
sy hub export [--out <file>] [--moved-to <url>] [--no-freeze] [--encrypt] [--passphrase-file <f>]
              [--pull-dir <d>] [--json]
```

1. **Freeze** (default; `--no-freeze` only for a copy that is not a move, which is then just
   `sy hub backup` plus the state files): in one write transaction set the setting
   `hub.frozen = {"since": <ms>, "moved_to": <url or null>}`. From then on:
   - ingest re-reads `hub.frozen` **inside its write transaction** (the store has one writer,
     so every ingest either committed before the freeze, and is in the snapshot, or sees it and
     answers `503`). Clients keep their records and retry; nothing is accepted that the
     snapshot lacks.
   - the pull scheduler stops starting sessions, and a running drain gets `refuse` /
     `frozen` at its next batch;
   - the alert engine stops sending (deliveries stay `pending`), so the old and the new hub
     never both push;
   - `/health` reports `frozen`; the board shows "This hub is frozen: it moved to \<url>".
     An unparseable `hub.frozen` counts as frozen everywhere (ingest, pulls, `/health`).
   - writes that would change what the snapshot must hold are refused too, with the same
     `503` (`unavailable`, `Retry-After: 300`): pairing claims and owner sign-in, token
     creation (every device insert), binding a pull target on a first hello, and the
     scheduler's bookkeeping (`RecordPullRun`, `SetMachineHeld`).
2. **Snapshot**: `VACUUM INTO` a temporary file in a private folder (mode 0700,
   `<data>/.export-<random>/`) in the hub's own data folder, never next to `--out`: the
   snapshot is plaintext and `--out` may be a shared or synced place, so with `--encrypt` no
   unencrypted copy of the database ever lies beside the archive. The folder is removed when
   the export ends, whatever way it ends; stale ones of a killed export are swept at the next
   export. Then `PRAGMA integrity_check` on it. Ctrl-C or SIGTERM cancels the export: it
   removes the snapshot and unfreezes a hub it froze itself; if it cannot, it says so and
   tells you to run `sy hub unfreeze` (and to delete the snapshot folder it names).
3. **State files**: `<state>/pair_pepper` (required: the export fails when it is missing,
   because the pairing codes in the database cannot be checked without it) and, when present,
   `<pull dir>/id_ed25519` and `.pub`, where the pull dir is `--pull-dir` (the one `sy hub
   serve --pull-dir` runs with), default `<state>/pull`. The state folder does not depend on
   `--db`: serve and `sy hub pair` read the pepper from `<state>` whatever database they open. (VAPID keys and the action key are in `hub_secret`, inside the database; the pinned
   pull host keys are in `pull_target`.) `<state>/hub_sign.key` is required in the
   same way (keys.md 3.4: without it every device must pair again); it goes into the archive
   as `state/hub_sign.key` and step 5 of the import writes it back like the pepper.
4. **Archive** (`.syx`, mode 0600, folder 0700; default `<data>/exports/hub-<UTC time>.syx`):
   a gzip-compressed tar with `manifest.json`, `hub.db`, `state/pair_pepper`,
   `state/pull/id_ed25519`, `state/pull/id_ed25519.pub`. Manifest:
   `{"format": 1, "kind": "switchyard-hub-export", "sy": "0.5.0", "schema": 4, "exported_at",
   "frozen": true, "moved_to", "public_url", "counts": {"devices", "machines", "records",
   "sessions", "push_targets", "pull_targets"}, "files": [{"path", "size", "sha256"}]}`.
5. `--encrypt`: the whole archive is encrypted with a passphrase read from the terminal twice
   (or the first line of `--passphrase-file`; never an argument). Format: `SYXE` magic, `1`,
   KDF id `1` = PBKDF2-HMAC-SHA256 (`crypto/pbkdf2`), uint32 iterations (600 000), 16-byte
   salt, 8-byte nonce prefix; then chunks of up to 64 KiB, each `uint32 length ‖
   AES-256-GCM(key, nonce = prefix ‖ uint32 index, aad = "syxe1" ‖ index ‖ last-flag)`.
   Truncation fails on the missing last chunk. Standard library only.
6. Print what it holds and that it is secret: "This file holds every device's token hash,
   the VAPID private key, ntfy access tokens, the pairing pepper and the pull key. Anyone with
   it can impersonate this hub to your phones. Keep it like a password; delete it after the
   import."

`sy hub unfreeze` clears `hub.frozen` (a move called off). The old hub otherwise stays frozen
forever: machines not yet re-pointed keep their records instead of losing them to it.

### 8.2 Import

```
sy hub import <file> [--public-url <url>] [--passphrase-file <f>] [--pull-dir <d>] [--yes] [--json]
```

1. Refuse when a hub runs on this data folder: `sy hub serve` holds an OS file lock on
   `<data>/hub.lock` for as long as it runs (released by the OS if it dies, so there is no
   stale pidfile; `flock` on Unix, `LockFileEx` on Windows), and a second `serve` on the same
   folder refuses to start. Import also refuses when the database itself is locked or `/health`
   answers on the configured listen address: "stop the hub first".
2. Read (decrypt), check `format`, every file's size and SHA-256, and that `schema` is at most
   this binary's newest schema (else "update sy here first"). The archive's database is
   untrusted: before anything opens it for writing, an archive whose schema holds a trigger,
   a view or a virtual table is refused (no Switchyard schema has one, and a trigger would run
   during the migration in step 4).
3. **Refuse a non-empty hub.** If `<data>/hub.db` exists and has any row in `device`,
   `record`, `machine`, `push_target` or `pull_target`, exit 5 with the counts: "this hub
   already has data; move \<path> away first". There is no `--force`. An existing empty
   database (a hub started once and never used) is replaced.
4. In the private work folder, open the unpacked `hub.db` through the store (migrations and
   their pre-migration copy run as usual), clear `hub.frozen`, set `hub.public_url` from
   `--public-url` (else keep the exported one, and warn if it is not this host's address),
   and record `hub.imported = {exported_at, imported_at, sy}`. After the migration the
   database must hold no table, index or other object that a freshly made database of this
   sy lacks (else the import is refused), and `setting` rows whose key this hub never writes
   are dropped and logged (kept: `hub.imported`, `hub.public_url`, `hub.tz`, `push.settings`,
   `push.baseline_done`, `push.vapid_public`, `pair.ttl_s`, `app.android_mirror`,
   `app.android_repo`). Everything that can fail on
   the data fails here, before any file of the hub is touched.
5. Swap, state files last: move an existing empty `hub.db` (and `-wal`/`-shm`) aside, rename
   the prepared database into place (0600), then back up any existing `<state>/pair_pepper`
   and `<pull dir>/id_ed25519*` to `*.pre-import-<UTC time>` and write the imported ones
   (0600). If any step fails, everything done so far is undone (the database and the backups
   go back where they were), so a failed import leaves the hub as it was.
6. Print the next steps (8.3).

### 8.3 Token continuity

What moves, and what each device has to do:

| Thing | Moves with the file | After the move |
| --- | --- | --- |
| Machine and desktop tokens | Hashes in `device`, machine bindings | **Keep working.** Each machine runs `sy connect <new url> --keep-token` (or the operator runs `sy machine repoint <new url>`), which checks `/health` and proves the token with `slot_info` records (tokens.md 4), then changes only `hub.url`. Until then its records wait (the old hub is frozen). |
| Pull targets | Rows, pinned host keys, the pull key file | Keep working if the new hub host can reach the remotes over SSH; nothing changes on the remotes. |
| Phones and browsers | Their device rows and Web Push subscriptions (VAPID keys move) | Their web app and cookie belong to the old origin: **re-pair**, unless the new hub keeps the same public URL (the tailnet name moved with it), in which case nothing changes at all. |
| Owner sessions | Rows | Cookies are per origin: sign in again unless the URL is the same: an owner device already on the new origin makes a link (More, Devices, Make another device owner), else `sy hub owner-link` on the new hub. |
| Pairing codes | Rows and the pepper | Live codes keep working at the new address. |
| Alerts, rules, history | Everything | The engine starts with `awake_at = now` (no silent-machine burst) and the stale guard drops anything overdue by more than 15 minutes. |

The client never follows `moved_to` by itself: a hub cannot change a client's configuration.
`sy doctor` on a machine whose hub is frozen prints the exact
`sy connect <moved_to> --keep-token` line.
The phone app does not follow it either. `/health` is not signed, so the app shows the new
address on a card ("Pair again at the new address"), unpairs, and asks for a new pairing from
the new hub's own QR code. It never sends its token to a `moved_to`.

## 9. Schema v4

The implementer adds `internal/hub/store/migrations/0004_machines.sql` with exactly this,
updates `docs/spec/schema.sql` to v4 (header line `v4`) and the schema test.

```sql
-- Version 4: reach, machine info, backlog age; pull targets. machines.md section 9.

ALTER TABLE machine ADD COLUMN reach TEXT CHECK (reach IS NULL OR reach IN ('direct', 'forward', 'pull'));
ALTER TABLE machine ADD COLUMN record_v INTEGER;
ALTER TABLE machine ADD COLUMN held INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machine ADD COLUMN backlog_oldest INTEGER;          -- client ts of the oldest unanswered record
ALTER TABLE machine ADD COLUMN backlog_at INTEGER;              -- hub time backlog was reported
ALTER TABLE machine ADD COLUMN info TEXT CHECK (info IS NULL OR json_valid(info));  -- last machine_info payload
ALTER TABLE machine ADD COLUMN info_at INTEGER;

-- One remote the hub pulls over SSH (machines.md 3.4).
CREATE TABLE pull_target (
  id            TEXT    NOT NULL PRIMARY KEY,               -- 'pt_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  name          TEXT    NOT NULL UNIQUE,                    -- machine name, used by the CLI
  machine_id    TEXT    UNIQUE REFERENCES machine(id),      -- NULL until bound (first hello)
  device_id     TEXT    REFERENCES device(id),              -- the pull device; NULL until bound
  host          TEXT    NOT NULL,                           -- HostName
  port          INTEGER NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
  user          TEXT    NOT NULL,
  jump          TEXT,                                       -- unused: ProxyJump is refused (the hop's host key cannot be pinned)
  remote_sy     TEXT    NOT NULL DEFAULT '.local/bin/sy',   -- used only without a forced command
  host_keys     TEXT    NOT NULL CHECK (json_valid(host_keys)),  -- ["ssh-ed25519 AAAA…", …] pinned public keys
  interval_s    INTEGER NOT NULL DEFAULT 60 CHECK (interval_s BETWEEN 15 AND 3600),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  state         TEXT    NOT NULL DEFAULT 'new'
                CHECK (state IN ('new', 'ok', 'failing', 'host_key', 'auth', 'refused', 'disabled')),
  created_at    INTEGER NOT NULL,
  last_try_at   INTEGER,
  last_ok_at    INTEGER,
  last_error    TEXT,                                       -- one line, no secrets
  fail_count    INTEGER NOT NULL DEFAULT 0,
  next_at       INTEGER                                      -- scheduler: next run
) STRICT;
CREATE INDEX pull_target_next ON pull_target(enabled, next_at);
```

Settings added (table `setting`, not secrets): `hub.frozen` (8.1), `hub.imported` (8.2).
`device.created_by` gains the value `pull:<target id>`.

## 10. Security

### 10.1 SSH host keys

- `sy` never passes `StrictHostKeyChecking=no` or `accept-new`, `UserKnownHostsFile=/dev/null`,
  or `CheckHostIP=no` to the person's `ssh`, and refuses them in `--ssh-option` and
  `SY_SSH_COMMAND` (exit 2: "sy never turns host key checking off; check the key by hand").
  The person's own config may set them; `sy` does not override the person's config, but
  `sy machine add` warns when `ssh -G` shows checking off for that host.
- First contact with an unknown host happens only at a terminal, through OpenSSH's own
  prompt; scripted runs (`--yes` without a terminal) use `BatchMode=yes` and fail.
- The hub's pull uses its own `known_hosts`, filled only from keys pinned at registration over
  an authenticated channel or by fingerprint (3.4); a changed key stops the target until the
  owner runs `sy hub pull trust <name>` (which repeats the pinning).

### 10.2 Secrets and the process list

| Secret | Never in | Travels as |
| --- | --- | --- |
| Pairing code or secret | argv, logs, the registry, `--json` output | stdin of the remote `sy setup` / `sy connect` (`--code-stdin`) |
| Device tokens | anywhere off the machine that claimed them | never sent by `sy machine add`; pull mode has none |
| ntfy topic and token | argv, `config.toml` | stdin; `<state>/push/direct.json` (0600) |
| Export passphrase | argv, environment | terminal or `--passphrase-file` |
| Pull private key | anywhere but the hub host's `<state>/pull/` and export files | — |

Every remote command line is fixed text plus plain values (4.3), so `ps` on either side shows
nothing that would let another user act as the person or the machine.

### 10.3 Least privilege of each path

| Path | Can | Cannot |
| --- | --- | --- |
| Pull key on the remote | Run `sy outbox drain --stdio`: read unanswered records, advance the cursor for acked ones | Get a shell, a PTY, forwarding, run any other command; reach anything else on the remote |
| Pull device on the hub | Be the device of pulled records | Be used over HTTP (its token was never kept) |
| Forward port on the remote | Reach the hub's HTTP API as the tailnet can | Read anything without a token |
| Admin channel | What the person can already do on the hub host by hand | Anything new: it is the person's own SSH |
| `sy update` | Replace `sy` with a checksum-verified release | Run anything downloaded before verification |
| A compromised hub | Ack records it did not store (data loss for that machine); show false data | Make any client run a command, change its config, or follow a move |

## 11. Tests

The spec-level
properties every implementation must prove:

- Drain: for every crash point (before ack, after the hub commit, after the ack, during the
  cursor write), resending yields each record stored exactly once and the cursor at the end;
  a forged `ack` (wrong `seq`, unknown id) changes nothing; `held` stops before a
  too-new record.
- Direct push: the client's episode key equals the hub's for the same records; the client
  picks the same rule as push.md 3.4 for every generated rule set (property test against
  `push/rules`); `direct_push` turns a pending hub alert into `fired`/`direct` and nothing is
  sent; the catch-up hold covers a direct_push record arriving two batches later.
- Versions: a hub reporting `record_v_max = 1` and an outbox with a `v = 2` record in the
  middle: everything before is sent, nothing after, `held` reported; raising the hub's value
  resumes in order.
- Export/import: snapshot under concurrent ingest loses nothing (every record a client saw
  accepted is in the import or the client still has it); import refuses non-empty; machine
  tokens work against the imported hub; encrypted archives round-trip and a flipped byte fails.
- SSH: no `sy`-built argv contains a secret; forbidden options are refused; a changed host key
  fails both `sy machine add` and the pull.

## 12. Unverified assumptions

1. **Remote unix-socket forwards** (`--forward-socket`): stale socket handling depends on the
   server's sshd `StreamLocalBindUnlink`; to be confirmed on Fedora and Ubuntu sshd defaults.
2. **`restrict` on old servers**: OpenSSH before 7.2 (2016) lacks it; the explicit option list
   is used instead, untested on such a server.
3. **`claude auth login` over `ssh -t`**: expected to behave as in a local terminal (setup.md
   8.2); verified only with the fake claude in tests until the manual check.
4. **Tailscale userspace networking** needs a proxy variable for `sy`; the probe's hint is
   heuristic.
5. **GitHub download redirects** go to `objects.githubusercontent.com` today; the HTTPS-only
   redirect rule copes with a host change, not with GitHub serving downloads over another
   scheme.
