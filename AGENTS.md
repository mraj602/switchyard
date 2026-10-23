# AGENTS.md

Instructions for AI coding agents. Two kinds of task bring an agent here:

- **Helping someone install or set up Switchyard** on their machines: follow
  [docs/guide/agents.md](docs/guide/agents.md). It points to the setup procedure and lists what
  stays with the person (logins, tokens, the owner key, their network).
- **Changing Switchyard's code or docs:** read [CONTRIBUTING.md](CONTRIBUTING.md). It is the
  single source for the repository tour, development setup, the checks each component needs,
  style and commits. The rules below are the ones an agent most often gets wrong; they do not
  replace it.

## Rules for changes

- **Specs first.** [docs/spec/](docs/spec/README.md) is the source of truth for behaviour. A
  change in behaviour updates the spec in the same commit; when code and spec disagree, the
  spec is right.
- **Run the checks for what you touched** (the table in
  [CONTRIBUTING.md](CONTRIBUTING.md#checks-a-change-must-pass)), for example `make check` for
  Go, `make app-test` for `app/`, `make phone-check` for `phone/`.
- **Never use the real home folder, Claude Code config or credentials** in tests or while
  working: suites run on temporary folders, fake `claude` binaries and fixtures. Never read
  `.credentials.json` or any token, and never log or store credentials, cookies, prompts or
  code.
- **Sign off every commit** (`git commit -s`): the `dco` check refuses commits without a
  `Signed-off-by` line that matches the author.
- **No new dependency without a reason.** Go prefers the standard library and builds with
  `CGO_ENABLED=0`; the Android app ships no proprietary Google code.
