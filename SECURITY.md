# Security policy

## Supported versions

Switchyard has not made a release yet. Until it does, security fixes land on `main` only.
After the first release, the latest release gets security fixes; older releases do not, so
please update (`sy update`, the desktop app's updater, or the Android app's update notice).

| Version | Supported |
| --- | --- |
| `main` | Yes |
| Latest release (after the first one) | Yes |
| Older releases | No |

## Reporting a vulnerability

Please report security problems **privately**, not in a public issue, pull request or
discussion.

1. Open the repository's **Security** tab and choose **Report a vulnerability** (GitHub
   private vulnerability reporting). Only the maintainers see the report.
2. Describe the problem, the component and version or commit, how to reproduce it, and the
   impact you expect.
3. Do not include real credentials, tokens, cookies or personal data. Redact them, or use
   values from a throwaway setup.

What to expect:

- An acknowledgement within 7 days.
- An assessment and a plan, or a request for more detail, within 14 days.
- A fix on `main` and, after the first release, a patch release, as fast as the severity
  needs. We will agree a disclosure date with you; 90 days is the default upper bound.
- Credit in the release notes and the advisory, if you want it.

Switchyard is a volunteer project and has no bug bounty.

## Scope

In scope: everything in this repository that ships, namely

- `sy` and the hub (`cmd/`, `internal/`), including the API, pairing, tokens, the link, keys,
  peers, the outbox, setup and the install and update paths;
- the board (`webui/`);
- the desktop app (`app/`), including its Electron security settings and IPC;
- the Android app (`phone/`), including its own notification link and updater;
- the Claude Code plugin and pane mod (`plugin/`);
- the deploy files (`deploy/`) and CI workflows (`.github/`).

Especially interesting:

- A way to read another device's data or act as another device without its token.
- A way for the hub, or someone with its database, to make a client run something, forge a
  signed document, or widen the peers matrix without the owner key.
- Anything that sends Claude Code credentials, claude.ai cookies or tokens, prompts, code or
  tool output off a machine.
- Bypasses of the owner-key signing guard that work without the person's help.
- Weaknesses in the desktop app's sandbox, context isolation, CSP or IPC allow-list.

Out of scope:

- The legacy prototype files at the repository root (`main.js`, `core/`, `server/`, `shell/`,
  `web/` and the others listed in [ARCHITECTURE.md](ARCHITECTURE.md#repository-layout)). They
  are not part of the product and are pending removal.
- claude.ai, Claude Code and Anthropic's services. Report problems there to Anthropic.
- Attacks that need full control of the user's own account or machine already, beyond what
  the threat model says Switchyard defends against.
- A hub deliberately exposed without HTTPS using `--insecure-lan`.
- Findings from automated scanners without a demonstrated impact.

## Threat model

What Switchyard protects, against whom, and what it does not try to stop are described in:

- [docs/spec/keys.md](docs/spec/keys.md): keys, signatures, the owner key, the signing guard,
  and the threat model.
- [docs/spec/tokens.md](docs/spec/tokens.md) and [docs/spec/pairing.md](docs/spec/pairing.md):
  device tokens and pairing.
- [docs/guide/security.md](docs/guide/security.md): a summary for users, including what leaves
  each machine and what the hub stores.
- [docs/guide/claude-ai.md](docs/guide/claude-ai.md): every place Switchyard talks to
  claude.ai.
