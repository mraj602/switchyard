import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'

import { STREAM } from './fixtures'
import { SY } from './register'

// The engine beneath the plugin: session.id answers the ids in order, config answers true.
function base(on: On, ids: string[]): void {
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('command.register', (_$, e) => ({ value: { command: e.name } }))
  on('ui.render', ($, e) => {
    const { Box } = $.ui.resolve(e)
    return Box({})
  })
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  let n = 0
  on('session.id', () => ({ value: ids[Math.min(n++, ids.length - 1)] }) as never)
  on('process.run', () => ({ value: { exitCode: 0, stdout: 'true\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }))
}

// A board child that writes one View and then blocks until released.
function blockingBoard(on: On, argvs: (readonly string[])[], returned: { n: number }): void {
  on('process.spawn', async function* (_$, e) {
    argvs.push(e.argv)
    yield { stream: 'stdout' as const, text: (STREAM[0] as string) + '\n' }
    try {
      await new Promise(() => {})
    } finally {
      returned.n += 1
    }
    return { value: { code: 0, signal: null } } as never
  })
}

test('/clear restarts the follower under the new session id', async ($, on) => {
  const clock = mock.clock(on)
  const argvs: (readonly string[])[] = []
  base(on, ['old', 'old', 'new'])
  blockingBoard(on, argvs, { n: 0 })
  await $.session.start({ cwd: '/work', surface: 'terminal', isInteractive: true })
  await clock.settle()
  expect(argvs.length).toBe(1)
  expect(argvs[0]).toEqual([SY, 'board', '--follow', '--ndjson', '--session', 'old'])

  await $.session.end({ reason: 'clear', sessionId: 'old', resume: { id: 'old' } } as never)
  await clock.advance(500)
  expect(argvs.length).toBe(2)
  expect(argvs[1]).toEqual([SY, 'board', '--follow', '--ndjson', '--session', 'new'])
})

test('session.end does not wait for the child to speak again', async ($, on) => {
  const clock = mock.clock(on)
  const argvs: (readonly string[])[] = []
  base(on, ['s1'])
  blockingBoard(on, argvs, { n: 0 })
  await $.session.start({ cwd: '/work', surface: 'terminal', isInteractive: true })
  await clock.settle()
  await $.session.end({ reason: 'prompt_input_exit', sessionId: 's1', resume: { id: 's1' } } as never)
  await clock.advance(120_000)
  expect(argvs.length).toBe(1)
})
