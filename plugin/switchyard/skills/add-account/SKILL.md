---
name: add-account
description: Add another Claude Code account (a new config folder with the shared settings linked in) with `sy account add`, showing the plan first. Use when the user wants a second or third Claude account, a work/personal split, or runs /switchyard:add-account.
allowed-tools: Bash(command -v sy), Bash(sy --version), Bash(sy account list:*)
---

Add one Claude Code account folder by driving `sy account add`. Plan first, apply only after a
clear yes. Keep each question to one sentence with its default named.

Rules that always hold:

- Never run `claude auth login` yourself and never touch credentials, `.credentials.json`,
  a Keychain item or `oauthAccount`. The new folder is created without a login; the person
  logs in from their own terminal.
- Never ask for, accept or pass a hub token. Hub tokens are entered by the person with
  `sy connect <url> --token-stdin` in their own terminal.
- Do not edit any file yourself.

## Steps

1. `command -v sy`. If it is missing, say Switchyard is not installed and offer to run the
   `/switchyard:setup` skill instead; stop here.
2. Run `sy account list --json` and tell the person, in one or two lines, which accounts exist
   (label, folder, logged in or not). If no accounts are managed yet (setup never ran), suggest
   `/switchyard:setup` first and stop.
3. Ask, one question each, only what is not already clear:
   - **Label**: "What should the new account be called, for example `work` or `personal`?"
     Optionally a colour.
   - **Sharing**: "Share settings, instructions, commands, skills, plugins, MCP servers and
     history with the new account?" Default: yes (the defaults of `sy setup`). Name what is
     never shared: logins and credentials, and per-account usage data. Accept removals such as
     `--share defaults,-history`.
4. Dry run: `sy account add <label> [--colour <c>] [--share <switches>] --no-login --dry-run --json`.
   Show the human text of the plan (printed on stderr) in a code block, unchanged, and take
   the `plan_id` from the JSON. State in one line the new folder name and how many links it
   gets. Ask: "Create this account?"
5. On a clear yes, apply exactly that plan:
   `sy account add <label> [--colour <c>] [--share <switches>] --no-login --yes --json --expect-plan <plan_id>`
   (use the same flags as the dry run; if `sy account add --help` does not list
   `--expect-plan`, leave it out and compare the new plan's folder name with the one shown).
   If it reports that the plan changed (exit 4), show the new plan and ask again. If it
   fails, say it rolled back and show the error.
6. Relay `needs` from the result as commands for the person's own terminal, for example
   `CLAUDE_CONFIG_DIR=~/.claude-2 claude auth login` or the launcher `claude-2` the plan
   names. Logging in is theirs to do. Mention that `sy setup --undo` reverses the change.
