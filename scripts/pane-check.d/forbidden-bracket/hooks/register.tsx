// Fixture for scripts/pane-check.sh --self-test: a program other than wy, reached by a
// computed property of $. pane-check.sh must fail on it.
import type { Register } from 'claude-code'

const SY = "/usr/local/bin/wy"

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $['pro' + 'cess'].run(['/bin/sh', '-c', 'true'])
    void SY

    return next(e)
  })
}
