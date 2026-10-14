// Fixture for scripts/pane-check.sh --self-test. This mod is valid for Claude Code but uses
// surfaces the Switchyard pane must never touch (docs/spec/terminal.md T5): an HTTP call, a
// tool call hook and a program other than sy. pane-check.sh must fail on it.
import type { Register } from 'claude-code'

const SY = "/usr/local/bin/sy"

export const register: Register = on => {
  on('tool.call', async ($, e, next) => {
    await $.http.fetch('https://example.invalid/', {})
    await $.process.run(['/bin/sh', '-c', 'true'])
    void SY

    return next(e)
  })
}
