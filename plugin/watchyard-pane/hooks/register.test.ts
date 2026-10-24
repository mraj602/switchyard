import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import { STREAM } from './fixtures'
import { PANE, WY } from './register'

const PLUGIN = 'watchyard-pane'
const SURFACES = ['terminal', 'desktop'] as const

type Spawn = { argv: readonly string[]; pieces: number }

// The board child, mocked at process.spawn: it writes the given lines in 37-character pieces
// (so a line spans pieces), then ends. The mod then waits its backoff on the mocked clock, which
// no test but the restart ones moves, so the last View stays.
function fakeBoard(on: On, clock: ReturnType<typeof mock.clock>, lines: string[], runs: Spawn[], ends = true): void {
  on('process.spawn', async function* ($, e) {
    runs.push({ argv: e.argv, pieces: 0 })
    const text = lines.map(l => l + '\n').join('')
    for (let i = 0; i < text.length; i += 37) {
      yield { stream: 'stdout' as const, text: text.slice(i, i + 37) }
    }
    return { value: { code: ends ? 1 : 0, signal: null } } as never
  })
}

// What the engine answers beneath the plugin at session start.
function base(on: On): void {
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('command.register', (_$, e) => ({ value: { command: e.name } }))
  // Nothing beneath draws: a band that has nothing to show passes down to this empty Box.
  on('ui.render', ($, e) => {
    const { Box } = $.ui.resolve(e)
    return Box({})
  })
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('session.id', () => ({ value: 's-test' }) as never)
}

// wy config get ... answers true; every other wy run is recorded.
function fakeRun(on: On, runs: (readonly string[])[], band = 'true'): void {
  base(on)
  on('process.run', (_$, e) => {
    runs.push(e.argv)
    const isConfig = e.argv[1] === 'config'
    return { value: { exitCode: 0, stdout: isConfig ? band + '\n' : '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
}

const paneProps = { title: 'Watchyard', isFocused: false, bodyColumns: 100, placement: 'dock', scroll: { offset: 0, bodyRows: 40 }, view: {} } as never
const bandProps = { hasSurvey: false, isWorking: false, maxRows: 5, bodyColumns: 100, scroll: { offset: 0, bodyRows: 5 }, view: {} } as never

// The View's own order, computed here from the JSON: needs-you rows, then each group's rows.
function expectedIds(line: string): string[] {
  const v = JSON.parse(line)
  const out: string[] = []
  for (const id of [...v.needs_you, ...v.groups.flatMap((g: { rows: string[] }) => g.rows)]) {
    if (!out.includes(id)) out.push(id)
  }
  return out
}

async function start($: Engine, surface: (typeof SURFACES)[number]): Promise<void> {
  await $.session.start({ cwd: '/work', surface, isInteractive: true })
}

for (const surface of SURFACES) {
  test(`the pane lists the View's rows in order, the band and the status follow it (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    const spawns: Spawn[] = []
    const runs: (readonly string[])[] = []
    fakeRun(on, runs)

    // Stage 1: the first View, one other session waits.
    fakeBoard(on, clock, [STREAM[0] as string], spawns)
    await start($, surface)
    await clock.settle()
    expect(spawns.length).toBe(1)
    expect(spawns[0]?.argv.slice(0, 5)).toEqual([WY, 'board', '--follow', '--ndjson', '--session'])

    let pane = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', props: paneProps, requestId: PANE })
    const rowKeys = async () => (await pane.findAll({ type: 'Button' })).map(b => b.key ?? '').filter(k => k.startsWith('row:')).map(k => k.slice(4))
    expect(await rowKeys()).toEqual(expectedIds(STREAM[0] as string))
    await pane.unmount()

    const band = await $.ui.mount({ plugin: PLUGIN, surface, component: 'AbovePrompt', props: bandProps })
    expect((await band.find({ type: 'Text', text: /api \(gpu-box\) asked a question/ }))).toBeDefined()
    await band.unmount()
  })

  test(`this_session never makes a band (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    fakeRun(on, [])
    // Lines 1 and 3 are bad and dropped; the View where only this session waits is applied.
    fakeBoard(on, clock, [STREAM[1] as string, STREAM[2] as string, STREAM[3] as string], [])
    await start($, surface)
    await clock.settle()

    const quiet = await $.ui.mount({ plugin: PLUGIN, surface, component: 'AbovePrompt', props: bandProps })
    expect(await quiet.find({ type: 'Text', text: /Watchyard/ })).toBeUndefined()
    await quiet.unmount()
  })

  test(`three waiting: the status entry and the band (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    const runs: (readonly string[])[] = []
    fakeRun(on, runs)
    const status: (string | undefined)[] = []
    on('ui.status', (_$, e, next) => {
      status.push(e.text)
      return next(e)
    })
    fakeBoard(on, clock, STREAM, [])
    await start($, surface)
    await clock.settle()
    expect(status).toEqual(['needs you: 1', 'needs you: 3'])

    const band = await $.ui.mount({ plugin: PLUGIN, surface, component: 'AbovePrompt', props: bandProps })
    expect(await band.find({ type: 'Text', text: /personal · notes needs permission 9 min ago/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /\+1 more need you/ })).toBeDefined()
    await band.unmount()

    const pane = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', props: paneProps, requestId: PANE })
    const keys = (await pane.findAll({ type: 'Button' })).map(b => b.key ?? '').filter(k => k.startsWith('row:')).map(k => k.slice(4))
    expect(keys).toEqual(expectedIds(STREAM[4] as string))
    await pane.unmount()
  })

  test(`a row press runs wy term focus for exactly that session, and c copies (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    const runs: (readonly string[])[] = []
    fakeRun(on, runs)
    const copies: string[] = []
    on('ui.copy', (_$, e) => {
      copies.push(e.text)
      return { value: { isCopied: true as const } } as never
    })
    fakeBoard(on, clock, [STREAM[0] as string], [])
    await start($, surface)
    await clock.settle()

    const view = JSON.parse(STREAM[0] as string)
    const pane = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', props: paneProps, requestId: PANE })
    const focusable = Object.values(view.rows).find((r: any) => r.can_focus) as { id: string; resume: string }
    const before = runs.length
    await pane.press({ key: `row:${focusable.id}` })
    expect(runs.slice(before)).toEqual([[WY, 'term', 'focus', '--session', focusable.id]])

    // A row that cannot be focused runs nothing.
    const stuck = Object.values(view.rows).find((r: any) => !r.can_focus) as { id: string }
    const n = runs.length
    await pane.press({ key: `row:${stuck.id}` })
    expect(runs.length).toBe(n)

    // c copies the selected row's resume command: here the one pressed last.
    await pane.press({ key: 'copy' })
    expect(copies.length).toBe(1)
    expect(copies[0]).toContain('--resume')
    await pane.unmount()
  })

  test(`a bad line is dropped and the child restarts with backoff (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    const spawns: Spawn[] = []
    const runs: (readonly string[])[] = []
    fakeRun(on, runs)
    fakeBoard(on, clock, [STREAM[1] as string, STREAM[3] as string, STREAM[0] as string], spawns, true)
    await start($, surface)
    await clock.settle()
    expect(spawns.length).toBe(1)

    // The first View applied, so the wait is 2 s; the child ended and starts again then.
    await clock.advance(1_999)
    expect(spawns.length).toBe(1)
    await clock.advance(1)
    expect(spawns.length).toBe(2)
    expect(spawns[1]?.argv).toEqual(spawns[0]?.argv)

    const pane = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', props: paneProps, requestId: PANE })
    expect((await pane.findAll({ type: 'Button' })).some(b => (b.key ?? '').startsWith('row:'))).toBe(true)
    await pane.unmount()
  })

  test(`a child that only fails backs off 2, 4, 8 ... seconds (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    const spawns: Spawn[] = []
    fakeRun(on, [])
    fakeBoard(on, clock, [STREAM[1] as string], spawns, true)
    await start($, surface)
    await clock.settle()
    expect(spawns.length).toBe(1)
    await clock.advance(2_000)
    expect(spawns.length).toBe(2)
    await clock.advance(3_999)
    expect(spawns.length).toBe(2)
    await clock.advance(1)
    expect(spawns.length).toBe(3)
    await clock.advance(8_000)
    expect(spawns.length).toBe(4)
  })

  test(`terminal.band = false hides the band (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    fakeRun(on, [], 'false')
    fakeBoard(on, clock, [STREAM[0] as string], [])
    await start($, surface)
    await clock.settle()
    const band = await $.ui.mount({ plugin: PLUGIN, surface, component: 'AbovePrompt', props: bandProps })
    expect(await band.find({ type: 'Text', text: /Watchyard/ })).toBeUndefined()
    await band.unmount()
  })

  test(`session.end stops the child for good (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    const spawns: Spawn[] = []
    fakeRun(on, [])
    fakeBoard(on, clock, [STREAM[1] as string], spawns)
    await start($, surface)
    await clock.settle()
    expect(spawns.length).toBe(1)
    await $.session.end({ reason: 'prompt_input_exit', sessionId: 's-test', resume: { id: 's-test' } } as never)
    await clock.advance(120_000)
    expect(spawns.length).toBe(1)
  })

  test(`the pane opens unasked only in a fullscreen session, once (${surface})`, async ($, on) => {
    const clock = mock.clock(on)
    fakeRun(on, [])
    const opened: string[] = []
    on('ui.open', (_$, e) => {
      opened.push(e.id)
      return { value: { isPlaced: true } } as never
    })
    fakeBoard(on, clock, [STREAM[0] as string], [])
    await start($, surface)
    await clock.settle()
    const plain = await $.ui.mount({ plugin: PLUGIN, surface, component: 'AbovePrompt', props: bandProps, viewport: { columns: 200, rows: 50, isFullscreen: false } })
    await plain.unmount()
    expect(opened).toEqual([])
    const wide = await $.ui.mount({ plugin: PLUGIN, surface, component: 'AbovePrompt', props: bandProps, viewport: { columns: 200, rows: 50, isFullscreen: true } })
    await wide.unmount()
    await clock.settle()
    expect(opened).toEqual([PANE])
  })
}
