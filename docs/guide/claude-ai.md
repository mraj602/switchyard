# Privacy, risks and how Switchyard talks to claude.ai

Switchyard is unofficial and not affiliated with Anthropic. Most of it never talks to
claude.ai or to Anthropic at all. This page lists every place where it does, what it sends and
reads, and the risks, so you can decide what to turn on.

## Summary

| Part | Talks to claude.ai? | What | Default |
| --- | --- | --- | --- |
| `sy` (hooks, status line, setup) | No | Reads what Claude Code hands to its hooks and status line on your machine | On after `sy setup` |
| Hub | No | Never contacts Anthropic, never holds a Claude login | - |
| Desktop app, account tabs | Yes | Loads claude.ai's own pages, one local browser session per account | Only when you add a tab |
| Desktop app, usage reading | Yes | Reads the numbers on claude.ai's Settings → Usage page, every 5 minutes | **Off** for each account until you allow it |
| Android app, claude.ai tabs | Yes | Loads claude.ai's own pages; asks which account a tab is signed in to | Only when you open a tab |

## Claude Code integration

`sy` uses Claude Code's documented extension points only: hooks, the status line, plugins,
`CLAUDE_CONFIG_DIR`, `claude auth status` and the session list Claude Code provides. The limit
percentages it records come from the `rate_limits` Claude Code itself passes to the status
line command. Nothing extra is sent to Anthropic. Logging in is always Claude Code's own
`claude auth login`; `sy` never reads, copies, moves or sends a Claude Code credential.

## claude.ai sign-ins stay on the device

- You sign in on Anthropic's own claude.ai page inside a tab. Switchyard shows no login form
  of its own.
- Each tab keeps its sign-in in its own local app storage on that device: an Electron session
  per tab in the desktop app, a WebView profile per account in the Android app.
- Switchyard never reads, exports or sends cookies or tokens. Nothing from a tab's session is
  sent to the hub or to any other machine.
- Removing a tab, or **Sign out (clear this tab)**, deletes that local session.

## The browser identity of the tabs

The desktop app's account tabs send a plain Chrome user agent string for your real operating
system, for example `Mozilla/5.0 (X11; Linux x86_64) ... Chrome/<version> Safari/537.36`,
instead of Electron's default, which adds `Electron/<version>` and the app name. claude.ai's
bot protection rejects Electron's default user agent, so without this the tabs do not load.

What this means:

- claude.ai sees an ordinary Chrome browser, not a Switchyard app. The tabs run the Chromium
  engine that is inside Electron, so the string describes the engine truthfully, but it does
  not say that the browser is Switchyard.
- It changes nothing else: the tab sends what any browser on claude.ai sends, and nothing
  more.
- The Hub view (your own hub's board, not claude.ai) adds `SwitchyardDesktop/<version>` to its
  user agent so your hub can tell it is in the app.

If you would rather not use it, do not add account tabs: the rest of the desktop app (the Hub
view, notifications) works without them, and the browser board works in any browser.

## Usage reading (opt-in per account)

The desktop app can read each account's usage limits from claude.ai. This also covers
accounts you use only on claude.ai, where Claude Code's status line never reports. It is
**off for every account until you allow it**:

1. Add a tab and sign in. Until you decide, the tab's usage bar says **Usage reading not
   enabled** with a **Review** button.
2. The first time a signed-in tab could read, the app shows this warning for that account
   (and shows it again if a later version changes the text):

   > **Read usage for this account?**
   > Every 5 minutes, Switchyard runs a small script inside this tab that reads the same
   > numbers claude.ai shows under Settings → Usage. It reads numbers only: no chats, no
   > cookies. They go to your own hub.
   > This uses an internal claude.ai endpoint, not a public API. It may stop working, and
   > automated access may not be allowed by Anthropic's consumer terms, so it could get this
   > account flagged. Switchyard is unofficial and not affiliated with Anthropic.
   > You can turn reading off for this account at any time in its usage bar or in Settings.
   > **[Read usage for this account]  [Not now]**

3. **Not now** keeps reading off; the tab works normally. **Read usage for this account**
   turns it on for that account only.
4. Change it at any time: the switch in the account's usage bar, or Settings, where each tab
   has **Read usage** and a separate **Send to hub** switch, and **Review** to read the
   warning again. **Pause all reading** in the tray turns reading off for every tab.

What it reads, when on:

- Every 5 minutes, and shortly after the tab loads, a small script inside that account's tab
  calls `/api/organizations`, `/api/organizations/{uuid}/usage` and `/api/account` with the
  tab's own session: the same data claude.ai's Settings → Usage page shows.
- It keeps the usage percentages and reset times per window, the organization's id, name,
  plan fields and capabilities, and the account's id, email and name. It never reads chats,
  projects, files, cookies or tokens.
- The numbers go to `sy outbox append` and then to your own hub. With **Send to hub** off,
  they only update that tab's own usage bar.
- On errors it backs off from 5 minutes up to an hour. A 401 (signed out), 403 (not
  available) or 429 (rate limited) stops reading for that account until you click **Resume**
  in its usage bar (or **Resume reading** in Settings).

The risks, plainly:

- **It is not an official API.** These are claude.ai's internal endpoints. They are
  undocumented and can change or disappear at any time, and reading then stops or shows stale
  numbers.
- **It may go against the spirit of the terms.** Anthropic's consumer terms restrict automated
  access to its services. A periodic script reading a page you could open yourself is light,
  but it is still automated access, and it could get an account flagged. It is your choice.
- **You do not need it for Claude Code.** The status line source covers every account you use
  in Claude Code, with no call to claude.ai. See [usage.md](usage.md).

The Android app does not read usage from claude.ai today. Its tabs ask claude.ai only which
account they are signed in to, so the app can warn about a tab signed in to the wrong account.
If the Android app ever reads usage, it will ask the same per-account permission first, off by
default.

## Your own accounts, no automatic switching

- Use Switchyard only with your own accounts, each within the terms of its plan
  ([Consumer Terms](https://www.anthropic.com/legal/consumer-terms),
  [Usage Policy](https://www.anthropic.com/legal/aup)). Do not share accounts or credentials
  with other people.
- Switchyard never switches or rotates accounts, and never moves a session to another account
  when one reaches a limit. "Most room right now" is a suggestion; you choose.
- Peers lets your own sessions in your own accounts message each other. It is not for pooling
  accounts between people.

## Turning everything off

- Usage reading: per account in its usage bar or in Settings; **Pause all reading** in the
  tray for every account.
- Account tabs: remove the tab, or **Sign out (clear this tab)**.
- The tail of Claude's last message sent to your hub:
  `sy config set privacy.last_message false`.
- Everything else: [uninstall.md](uninstall.md).

More on what the hub stores and the threat model: [security.md](security.md).
