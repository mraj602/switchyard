# Spec

The contract every phase builds on. A change to the record format, the API or the schema
goes here first, before any code (plan section 16).

| File | What it fixes |
| --- | --- |
| [record.md](record.md) | Upload batch, record envelope, the seven record kinds, content rules. |
| [schema.sql](schema.sql) | The hub's SQLite schema (version 6; each phase's migration is in its header). |
| [states.md](states.md) | Session state machine, question detection, `gone` and timeouts. |
| [api.yaml](api.yaml) | OpenAPI 3.1 for ingest, the read API, the live stream and reserved paths. |
| [tokens.md](tokens.md) | Token format, hashing, scopes, CLI, pairing codes. |
| [identity.md](identity.md) | Machines, slots, seats, slot logins; how the login is read. |
| [outbox.md](outbox.md) | The local outbox: files, concurrent writers, cursor, cap, crash safety. |
| [paths.md](paths.md) | Config, state and data folders per OS; `config.toml`. |
| [pairing.md](pairing.md) | Pairing codes and QR, claim, rate limits, owner sessions, devices and revoke (phase 4). |
| [push.md](push.md) | Alert rules, scheduler, delivery, Web Push (VAPID, RFC 8291), ntfy, action tokens, schema v3 (phase 4); the Expo channel for the phone app, widget refresh pushes, schema v6 (section 12, phase 8). |
| [pwa.md](pwa.md) | The installable web app: manifest, icons, service worker, offline, install and notification flows (phase 4). |
| [setup.md](setup.md) | Install scripts, `sy setup`, sharing, plugin install, backups and undo, accounts, uninstall (phase 3). |
| [machines.md](machines.md) | `sy machine add`, reach modes (direct, SSH forward, hub pull) and the drain protocol, direct ntfy, the Machines screen, versions and `sy update`, hub export/import, schema v4 (phase 5). |
| [desktop.md](desktop.md) | The desktop app (`app/`): folders, security baseline, IPC allow-list, `sy desktop`, web usage reading, hub connection and view, shell, tray, notifications, schema v5 (phase 6). |
| [terminal.md](terminal.md) | The session board in any terminal (`sy board`), the board view model and token, the Claude Code pane mod, tab titles, click to focus, Warp tab configs and plugin, `sy ssh`, `sy term setup` (phase 7). |
| [phone.md](phone.md) | The phone app (`phone/`, Expo): layout and pinned versions, pairing, keychain token and hub client, screens and routes, polling, notifications (channels, categories, responses, background task), deep links, widgets, the Live Activity, build config, test seams, wave 0 facts (phase 8). |

Checks: `make spec-check` loads `schema.sql` into a temporary SQLite database and validates
`api.yaml` as OpenAPI 3.1. CI runs the same checks.

Implementation notes that apply everywhere:

- Go 1.27, standard library first. SQLite through `modernc.org/sqlite` (pure Go, no cgo), so
  `sy` stays one static binary for Linux, macOS and Windows.
- Words marked **uncertain** are behaviours of Claude Code or claude.ai that were not
  verified against a capture. Each one has a fallback, and the fixtures in `testdata/`
  should settle them.
