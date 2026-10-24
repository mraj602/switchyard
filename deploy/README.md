# Deploying the hub

The hub is `sy hub serve`: one process, one SQLite file. This folder has what you need to run
it as a container on an always-on server (a small VPS or a home server), or as a user service on your laptop
("merged mode"), and how to point machines at it.

| File | What |
| --- | --- |
| `Dockerfile` | Multi-stage build: static `sy` on Alpine, non-root user (uid 10001), `/data` volume, `HEALTHCHECK` on `/health`. |
| `Dockerfile.dockerignore` | Keeps the build context to the Go sources. |
| `docker-compose.yml` | The hub, published on `127.0.0.1:8787` only. |
| `docker-compose.tailscale.yml` | Override that publishes it on the server's Tailscale address. |
| `Caddyfile` | Optional public HTTPS (VPS mode). |
| `systemd/switchyard-hub.service` | Hub as a systemd **user** service (merged mode). |

The hub port is never published on `0.0.0.0`, and a client refuses plain `http` to a host that
is not loopback or on Tailscale (unless you pass `--insecure-lan`).

Terms: the **server** runs the hub. Every computer that runs Claude Code (the **laptop**, and
the server itself if you use Claude Code there) is a **machine** that sends records to it.

---

## A. Hub on an always-on server, machines over Tailscale

Needs: Docker with the compose plugin on the server, and Tailscale on the server and on every
machine (the free Personal plan is enough).

### 1. Build and start the hub (on the server)

```sh
git clone https://github.com/mraj602/switchyard.git && cd switchyard/deploy

# Publish on the Tailscale address instead of loopback.
echo "SY_BIND=$(tailscale ip -4 | head -n1)" > .env
echo "SY_VERSION=$(git describe --tags --always)" >> .env

docker compose -f docker-compose.yml -f docker-compose.tailscale.yml up -d --build
docker compose ps          # STATUS should become "healthy" within about 30 s
docker compose logs hub
docker compose exec hub sy hub owner-link   # sign in to the board as the owner
```

The logs do not carry the first owner link. The hub writes it to `/data/first-owner-link.txt`
(mode 0600, readable by the hub only) while it has no owner; `sy hub owner-link` makes a
fresh one whenever you need it.

Only machines on your tailnet can reach the port. If you would rather not use the override,
leave `SY_BIND` out: the hub then listens on `127.0.0.1` of the server only, and you can put
HTTPS in front with `tailscale serve --bg 8787` (then use the `https://<name>.<tailnet>.ts.net`
address below instead of `http://<name>.<tailnet>.ts.net:8787`).

The data lives in the Docker volume `switchyard_hub-data` (`/data` in the container): the <!-- rename:keep -->
database, backups made by `sy hub backup`, and the pull key (`/data/state/pull`, see D).
Back that volume up.

### 2. Make a token for each machine (on the server)

A token is tied to one machine id. Get the laptop's id on the **laptop**:

```sh
sy machine          # m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K  laptop
```

Then, on the **server**, in `deploy/`:

```sh
docker compose exec hub sy hub token create --kind machine --name laptop --machine m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K
# prints the token once: sy_m_...   (copy it now; the hub only stores a hash)

docker compose exec hub sy hub token list
docker compose exec hub sy hub token revoke laptop        # a lost or retired machine
```

`--kind` is `machine` (can only send records), `desktop` (also reads), `phone` or `browser`
(read only). Make one token per machine. Instead of copying a token you can pair: on the
server `docker compose exec hub sy hub pair --kind machine` prints a one-time command
`sy connect <url> --code 482-913` to run on the laptop. Phones pair as in section C.

### 3. Connect each machine (on the laptop)

```sh
# Use the server's full Tailscale name (hub-server.<tailnet>.ts.net) or its 100.x address.
# A short name like "hub-server" is refused over plain http; --insecure-lan allows it.
sy connect http://hub-server.<tailnet>.ts.net:8787 --token sy_m_...
# or keep the token out of your shell history:
printf '%s\n' "$TOKEN" | sy connect http://hub-server.<tailnet>.ts.net:8787 --token-stdin
```

`sy connect` checks `GET /health`, checks the token (it stores nothing for a rejected one),
saves the URL in `config.toml` (`hub.url`) and the token in `<state>/token` (mode 0600), sends
everything already queued, and installs the timer that runs `sy flush --quiet` every minute
and at boot:

* **Linux**: `~/.config/systemd/user/switchyard-flush.{service,timer}`. On a machine you reach
  by SSH, run `loginctl enable-linger $USER` once (connect prints this when it is needed) or
  the timer stops at logout.
* **macOS**: `~/Library/LaunchAgents/com.switchyard.flush.plist`.
* **Windows**: connect writes a Task Scheduler file and prints the one `schtasks /Create`
  command to run.

Check it:

```sh
sy flush                  # by hand: prints what was sent
systemctl --user list-timers switchyard-flush.timer      # Linux
docker compose logs -f hub                               # on the server
```

Undo with `sy disconnect` (removes the timer, the token and `hub.url`; queued records stay).

Records wait in the local outbox while the hub is unreachable (50 MB cap, oldest token rows
dropped first) and are sent, without duplicates, when it is back.

### 4. Upgrades and backups (on the server)

```sh
cd switchyard && git pull
cd deploy && docker compose -f docker-compose.yml -f docker-compose.tailscale.yml up -d --build
docker compose exec hub sy hub backup          # consistent copy under /data/backups
```

The schema migrates itself at start, after copying the database file.

### Optional: public HTTPS

If you want a public address instead of Tailscale, keep the default loopback bind, install
Caddy, edit the host name in `Caddyfile`, point DNS at the server, open ports 80 and 443 only,
and run `caddy run --config deploy/Caddyfile`. Machines then use `https://hub.example.com`.

---

## B. Merged mode: the hub on your laptop

One computer, no server. The hub listens on `127.0.0.1:8787` and runs as a user service so
it keeps running without a terminal.

```sh
# 1. sy on your PATH, for example ~/.local/bin/sy  (or: make build && cp dist/sy ~/.local/bin/)

# 2. The service.
mkdir -p ~/.config/systemd/user
cp deploy/systemd/switchyard-hub.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now switchyard-hub.service
curl -s http://127.0.0.1:8787/health

# 3. A token for this machine, made directly against the local database.
sy hub token create --kind machine --name "$(hostname -s)" --machine "$(sy machine | cut -d' ' -f1)"

# 4. Connect this machine to its own hub.
sy connect http://127.0.0.1:8787 --token sy_m_...

# 5. Optional: keep the hub (and the timer) running when you are logged out.
loginctl enable-linger "$USER"
```

Open `http://127.0.0.1:8787` in a browser for the board (`sy hub owner-link` prints a sign-in
link for the owner settings). For the phone app on a merged hub, run `tailscale serve --bg 8787`
on the laptop, add `--public-url https://<laptop>.<tailnet>.ts.net` to `ExecStart`, then
`sy phone pair` (section C). Pushes stop while the laptop sleeps. Other machines can send to this hub
over Tailscale: start it with `--listen <tailscale ip>:8787 --insecure-lan` (edit `ExecStart`)
and connect them as in section A, step 3.

On macOS use a LaunchAgent for the hub instead of the systemd unit (`sy setup` does not install
one yet).

### Check list when nothing shows up

1. `curl http://<hub>:8787/health` from the machine: `{"ok":true,...}`.
2. `sy flush` by hand: the error is printed (a rejected token says so).
3. `sy outbox stat`: how many records are waiting.
4. `sy hub token list` on the hub: is the device revoked, and is its machine id the one in
   `sy machine`?

---

## C. The phone app and pushes

The board is also an installable phone app that gets pushes when a session needs you
(pairing.md, push.md, pwa.md). Three things are needed on the server:

1. **HTTPS on the hub's address (required for the phone app).** Service workers, "Add to Home
   Screen" and Web Push only work in a secure context, so the phone must open the hub over
   `https://`. On the server, with the hub published on loopback (leave `SY_BIND` out),
   run once:

   ```sh
   sudo tailscale serve --bg 8787        # https://<name>.<tailnet>.ts.net -> 127.0.0.1:8787
   tailscale serve status
   ```

   (Enable HTTPS certificates for the tailnet in the Tailscale admin console first.) With the
   `docker-compose.tailscale.yml` override the port is plain http on the Tailscale IP: fine
   for machines, not for the phone app, so use `tailscale serve` for phones.

2. **Tell the hub its public address**, so pairing QR codes, push links and the cookie
   `Origin` check use it. Put it in `deploy/.env` and recreate the container:

   ```sh
   echo "SY_PUBLIC_URL=https://<name>.<tailnet>.ts.net" >> .env
   docker compose up -d
   ```

   The compose file passes it as `sy hub serve --public-url …`, which stores it as the
   `hub.public_url` setting (an empty value keeps the stored one). Without Docker:
   `sy hub serve --public-url https://…`.

3. **Outbound HTTPS to the push services.** Pushes leave the hub as HTTPS POSTs to the
   phone's push service (`fcm.googleapis.com`, `*.push.apple.com`,
   `*.push.services.mozilla.com`, `*.notify.windows.com`) and, if you add an ntfy topic, to
   the ntfy server (`ntfy.sh` by default). The Android app's own connection (the `link`
   channel) needs no outbound push service. No inbound port is needed for pushes, but a firewall or egress policy
   must allow these on 443. The phone itself only needs the
   tailnet to open the app, tap a push or use Snooze/Seen.

Then pair a phone:

```sh
docker compose exec hub sy hub owner-link     # one-time sign-in link for the owner settings
docker compose exec -it hub sy phone pair     # QR + 6-digit code, waits until claimed
```

Scan the QR with the phone's camera (Android claims at once; on iPhone add the page to the
Home Screen first, open the installed app and type the code), then Settings → Turn on
notifications → Send test push. `sy doctor` on the server checks the public URL, the VAPID
keys, that at least one phone can receive pushes, and the last delivery error.

`sy hub serve --no-push` runs the hub without the alert engine (the alert endpoints answer
503). The VAPID keys are made at the first start and live in the database, so backups keep
every phone subscription valid.

**Caddy (VPS mode).** The `Caddyfile` already serves HTTPS and does not buffer the stream;
start the hub with `--public-url https://hub.example.com` so the phone app, the QR codes
and the cookie `Origin` check use that address. Caddy sets `X-Forwarded-Proto`, and the hub
trusts it only from loopback and from `--trusted-proxy`.

**Forwarded headers in Docker.** With a published port, the hub sees the Docker bridge
gateway (for example `172.17.0.1`) as the caller, not loopback. The compose file therefore
starts it with `--trusted-proxy 172.16.0.0/12` (override with `SY_TRUSTED_PROXY` in `.env`; a
comma-separated list of CIDRs). Without it, every client shares the bridge address, so the
per-client rate limits become one shared limit and the session cookie loses its `Secure` flag
behind `tailscale serve`. Only list ranges that carry your own proxy: a caller from a trusted
range can set `X-Forwarded-For` to anything. If you publish the hub directly on the Tailscale
address (no proxy), tailnet peers arrive from the bridge too, so set `SY_TRUSTED_PROXY` to an
unused range such as `192.0.2.1/32` to turn this off.

---

## D. Pulling servers from a Docker hub

A server that cannot reach the hub (no Tailscale, a locked-down network) can be **pulled**:
the hub opens an SSH connection to it every minute and drains its outbox (machines.md 3.4).
The image has `openssh-client` for this. The hub's pull key is made at the first start and
lives in the state volume, `/data/state/pull/id_ed25519` (with the pinned host keys next to
it), so it survives rebuilds and moves with `sy hub export`.

From the laptop, with SSH to both the hub host and the server:

```sh
sy machine add dev@gpu-box --reach pull \
  --hub-admin "ssh hub-server docker compose -f $HOME/switchyard/deploy/docker-compose.yml exec -T hub sy"
```

That installs `sy` on the server, adds one `restrict,command="… sy outbox drain --stdio"`
line for the hub's key to its `~/.ssh/authorized_keys` (the key can run nothing else), pins
the server's host key on the hub, and runs a first pull. The hub container must be able to
reach the server's SSH port (it uses the container's network: the server's Tailscale name only
works if the Docker host's resolver and routes reach it).

```sh
docker compose exec hub sy hub pull list           # targets, state, last pull
docker compose exec hub sy hub pull run gpu-box --wait
docker compose exec hub sy doctor                  # pull targets and the pull key
```

A target in `host_key` stops until you check the server really changed its key and run
`sy hub pull trust gpu-box`. `sy machine remove gpu-box` takes the `authorized_keys` line out
again.

## E. Moving the hub

To move the hub to another server (or from the laptop to a container), export it, import it
on the new host, and point the machines at the new address (machines.md 8):

```sh
# Old hub: freeze it and write an archive. Ingest answers 503 from now on; machines keep
# their records until they are re-pointed.
docker compose exec -it hub sy hub export --moved-to https://new.<tailnet>.ts.net --encrypt \
  --out /data/exports/hub.syx
docker compose cp hub:/data/exports/hub.syx .

# New hub (same deploy folder, a fresh volume; the hub not started yet):
docker compose create
docker compose cp hub.syx hub:/data/hub.syx
docker compose run --rm -it hub hub import /data/hub.syx --public-url https://new.<tailnet>.ts.net
docker compose up -d
```

The archive holds every token hash, the VAPID private key, the pairing pepper and the pull
key: keep it like a password and delete it after the import.

Then, on each machine (or for all of them from the laptop with `sy machine repoint
https://new.<tailnet>.ts.net`):

```sh
sy connect https://new.<tailnet>.ts.net --keep-token
```

Tokens keep working; pulled servers keep working if the new host can reach them over SSH.
Phones re-pair unless the public URL stayed the same. `sy doctor` on a machine whose hub is
frozen prints the exact `sy connect … --keep-token` line. `sy hub unfreeze` on the old hub
calls a move off.
