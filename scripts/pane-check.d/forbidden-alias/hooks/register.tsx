// Fixture for scripts/pane-check.sh --self-test: a program other than wy, hidden behind an
// alias of $.process. pane-check.sh must fail on it.
import type { Register } from 'claude-code'

const SY = "/usr/local/bin/wy"

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const p = $.process
    await p.run(['/bin/sh', '-c', 'true'])
    void SY

    return next(e)
  })
}
