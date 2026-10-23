---
name: setup
description: Set up Switchyard on this machine by running `sy setup`, with a plan shown and confirmed first. Installs `sy` if missing, detects the Claude Code config folders, sets up sharing between accounts, labels, new accounts and the optional hub. Use when the user asks to set up, install or configure Switchyard, share settings between Claude accounts, or runs /switchyard:setup.
allowed-tools: Bash(command -v sy), Bash(sy --version), Bash(sy setup --detect:*), Bash(sy setup --dry-run:*)
---

Set up Switchyard for this person by driving `sy setup`. Follow the steps in order. Never
skip the dry run, never apply without an explicit yes, and keep every question to one
sentence with the default named.

Rules that always hold:

- Never ask for a hub token, device token, API key or password in the chat, and never put
  one in a command line. If a hub token is needed, tell the person to run
  `sy connect <url> --token-stdin` themselves in their own terminal and paste the token
  there. (A short pairing code, such as `482-913`, may be typed in the chat.)
- Never pass `--conflicts` with any value other than `keep` (or any `--resolve`) unless the
  person chose that for that item after seeing the conflict.
- Never run `claude auth login` yourself and never touch `.credentials.json`, a Keychain item
  or `oauthAccount`. Logins are done by the person in their terminal.
- Do not edit any file yourself. Everything goes through `sy setup`.

## 1. Is `sy` installed?

Run `command -v sy`. If it prints a path, run `sy --version` and continue.

If `sy` is not found, tell the person you are about to install it and ask for a yes. The
installer only puts a checksum-verified `sy` in `~/.local/bin`; it edits no shell profile.
On yes, run this with the Bash tool (the person may be asked to approve it):

```
curl -fsSL https://github.com/mraj602/switchyard/releases/latest/download/install.sh | sh -s -- --no-setup
```

On Windows without a POSIX shell, tell the person to run the PowerShell line from the README
themselves instead. If `~/.local/bin` is not on `PATH`, the installer prints the line to add;
relay it to the person and use `~/.local/bin/sy` for the rest of this session.

## 2. Detect

Run `sy setup --detect --json`. Read the JSON and tell the person, in one paragraph: the
Claude Code version, how many config folders were found and which are logged in (by email, if
shown), what is already shared or set up by hand (links, launchers, MCP sync, a status line),
whether a hub is already connected, and the Tailscale name if one was found. Do not print the
JSON.

If `claude` was not found (exit code 7), say so and stop: Claude Code has to be installed first.

## 3. Ask, one question at a time

Ask only what the detect output leaves open, one question per message, each with its default:

1. **Hub.** "Do you want a hub, so every machine and your phone see all sessions in one
   place?" Offer: `later` (default), `here` (this machine runs it), or `join:<url>` with the
   Tailscale name from detect if there is one, for example
   `join:https://<name>.<tailnet>.ts.net`. If the person picks `join`, remember that the
   token is theirs to enter later (step 6); do not ask for it.
2. **Sharing.** "Share these between your accounts, with the first folder as the primary:
   settings, instructions (`CLAUDE.md`), commands/skills/agents/rules, plugins, MCP servers,
   history and plans?" The default is all of them (`--share defaults`). Say plainly what is
   never shared: logins and credentials, `oauthAccount`, per-account usage and caches, and
   anything outside the items named. Accept removals such as `--share defaults,-history`.
3. **Labels.** "Do you want names for your accounts, for example work and personal?" Default:
   keep what exists. Build `--labels` as `'<folder or email>=<label>[:<colour>],...'`.
4. **New accounts.** "Add another account (a new folder to log in to later)?" Default: no.
   Build `--add <label>[:<colour>],...`. New folders are created, never logged in.

If detect found conflicts (an item that differs from the primary), show them and ask for each:
keep (default), use the primary's version, or merge. Only then use `--conflicts` or
`--resolve`.

Keep the answers as flags; call the final list `<flags>`. Always include `--hub <value>`
(default `later`).

## 4. Dry run, then show the plan

Run `sy setup --dry-run --json <flags>`. The JSON on stdout holds the plan (`plan_id`,
`actions`, `summary`, `needs`); the human text form is on stderr, which the Bash tool shows.
Show the person that text form of the plan in a code block, unchanged, then state in one line:
the number of changes, files backed up, links changed, and that no login is touched. Take the
`plan_id` from the JSON.

If the plan has no actions, say "nothing to do" and go to step 6.

Then ask: "Apply this plan?" Do not apply on anything but a clear yes. If the person changes an
answer, go back to step 3 for that answer and repeat this step with a new dry run.

## 5. Apply exactly that plan

On yes, run:

```
sy setup --yes --json --expect-plan <plan_id> <flags>
```

with the same `<flags>` as the dry run and the `plan_id` from it. Never apply without
`--expect-plan`. If it exits 4 (the plan changed), show the new plan it printed, say what is
different, and ask again; do not retry on your own. Exit 1 means it failed and rolled back:
say so and show the error. Exit 6 means local setup is done but the hub step failed: say so.
Exit 3 means it needs the person (no terminal); say so and do not retry with other flags.

On success say what changed in two or three lines, and that every change can be reversed with
`sy setup --undo`.

## 6. Relay what is left

Read `needs` from the JSON result and give the person the commands to run in their own
terminal, one per line:

- Folders not logged in: `claude auth login` with the folder, for example
  `CLAUDE_CONFIG_DIR=~/.claude-2 claude auth login`, or the launcher the plan named. Say that
  logging in is theirs to do and optional.
- A hub token (when they chose `join:<url>`): `sy connect <url> --token-stdin`, run by them in
  a terminal, then paste the token. Never ask for the token here.
- Any other `needs` entry, as written.

If the person also runs Claude Code on a server they reach over SSH, say that one command
sets it up from here, once this machine has a hub: `sy machine add <user@host>` (it installs
`sy` there, runs `sy setup` on it and connects it; `--reach forward` when the server cannot
reach the hub but this laptop can, `--reach pull` when the hub should fetch from it over SSH).
Do not run it for them: it asks about the server's host key and logins itself.

Finish with: how to undo (`sy setup --undo`) and how to check (`sy doctor`).
