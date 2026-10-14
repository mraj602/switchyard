---
name: status
description: Show the state of every Claude Code session on this machine (account, folder, project, state, context %) by running `sy status`. Use when the user asks what their sessions are doing, which need attention, or runs /switchyard:status.
allowed-tools: Bash(sy status:*)
---

Run `sy status` with the Bash tool and show its output to the user as it is, in a code block.

- If the command is not found, say that `sy` is not on PATH and that Switchyard has to be
  installed first; do not try to install it.
- If the command says `status` is not implemented yet, say so in one line.
- After the table, add at most one sentence naming any session whose state is `asked`,
  `needs_permission` or `failed`, since those wait for the user.
- Do not run any other command and do not change any file.
- Only if the user asks for something live: `sy board` shows the same sessions as a live
  board in any terminal, and `sy board --once` prints one frame. Mention it; do not run it
  (it needs a terminal).
