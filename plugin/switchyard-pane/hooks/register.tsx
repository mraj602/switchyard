// Switchyard pane mod (docs/spec/terminal.md T5): the session board as a side pane, a band
// above the prompt when another session needs you, and a status entry.
//
// Read only. The mod never touches a prompt, a tool, a model, an agent, HTTP or a file. It runs
// one program, sy, which it names by absolute path: the board follower (a child it reads
// NDJSON from), `sy term focus` (brings a tab forward) and `sy config get` (two booleans).
// It never sees the board token: sy reads it.
import { atom, read, update } from 'claude-code'
import type { Hook, Register } from 'claude-code'

import type { Row, View } from '../types'
import {
  Splitter,
  bandText,
  cut,
  glyph,
  isClaudeURL,
  isResumeCommand,
  parseLine,
  rowOrder,
  statusText,
  width as cells,
} from './model'

// SY is the absolute path of sy; internal/setup/pluginmkt replaces the placeholder string
// when it writes the marketplace copy (and refuses a path holding a quote, a backslash or a
// line break).
export const SY = "__SY_PATH__"

export const PANE = 'switchyard-board'
const BACKOFF_FIRST_MS = 2_000
const BACKOFF_MAX_MS = 60_000

type Dollar = Parameters<Hook<'session.start'>>[0]

const view = atom({ plugin: 'switchyard-pane', key: 'view' } as const, null)
const selected = atom({ plugin: 'switchyard-pane', key: 'selected' } as const, null)
const dropped = atom({ plugin: 'switchyard-pane', key: 'dropped' } as const, 0)
const restarts = atom({ plugin: 'switchyard-pane', key: 'restarts' } as const, 0)

// ownRow is the row the `c` key copies: the selected one, else the first listed.
function ownRow(v: View, sel: string | null): Row | undefined {
  const id = sel !== null && v.rows[sel] !== undefined ? sel : rowOrder(v)[0]
  return id === undefined ? undefined : v.rows[id]
}

// Module variables start over on a reload; session.start fires again then.
let generation = 0
let child: AsyncGenerator<unknown, unknown> | null = null
let isBandOn = true
let isAutoOpen = true
let hasAutoOpened = false
let lastStatus: string | undefined

// config reads one boolean through sy; anything but "false" keeps the default (true).
async function config($: Dollar, key: string): Promise<boolean> {
  try {
    const out = await $.process.run([SY, 'config', 'get', key])
    return !(out.exitCode === 0 && out.stdout.trim() === 'false')
  } catch {
    return true
  }
}

// wake ends the wait of the loop that is reading the child, so a stop does not wait for the
// child's next line. It is replaced for every child.
let wake: (() => void) | null = null

// stop ends the current follower now: the generation moves on (a late line is ignored), the
// reading loop is woken, and the child is told to end without waiting for it to speak.
function stop(): void {
  generation += 1
  const c = child
  child = null
  const w = wake
  wake = null
  if (w !== null) w()
  if (c !== null) void c.return(undefined).catch(() => {})
}

// follow runs the board child for the session's life: it parses each line into the atom, and
// when the child ends it starts it again after 2, 4 ... 60 s. A good line resets the wait.
async function follow($: Dollar, sessionId: string, gen: number): Promise<void> {
  let wait = BACKOFF_FIRST_MS
  let isFirst = true
  while (gen === generation) {
    if (!isFirst) {
      await update($, restarts, n => n + 1)
    }
    isFirst = false
    const split = new Splitter()
    try {
      const stream = $.process.spawn({
        argv: [SY, 'board', '--follow', '--ndjson', '--session', sessionId],
      })
      child = stream
      const stopped = new Promise<'stopped'>(resolve => {
        wake = () => resolve('stopped')
      })
      const it = stream[Symbol.asyncIterator]()
      for (;;) {
        const pending = it.next()
        pending.catch(() => {}) // a read that loses the race to stop() must not reject unseen
        const next = await Promise.race([pending, stopped])
        if (next === 'stopped' || next.done === true || gen !== generation) break
        const piece = next.value as { stream: string; text: string }
        if (piece.stream !== 'stdout') continue
        for (const line of split.push(piece.text)) {
          const v = parseLine(line)
          if (v === null) {
            await update($, dropped, n => n + 1)
            continue
          }
          wait = BACKOFF_FIRST_MS
          await update($, view, () => v)
          const text = statusText(v)
          if (text !== lastStatus) {
            lastStatus = text
            $.ui.status(text)
          }
        }
        if (split.overflowed > 0) {
          const n = split.overflowed
          split.overflowed = 0
          await update($, dropped, c => c + n)
        }
      }
    } catch {
      // the child could not start or died badly: the wait below is the whole handling
    }
    if (gen !== generation) return
    child = null
    await $.clock.sleep(wait)
    wait = Math.min(wait * 2, BACKOFF_MAX_MS)
  }
}


// followNewSession starts the follower again after /clear, once the session id has changed.
async function followNewSession($: Dollar, oldId: string, gen: number): Promise<void> {
  let id = oldId
  for (let i = 0; i < 10 && gen === generation; i++) {
    id = await $.session.id()
    if (id !== oldId) break
    await $.clock.sleep(100)
  }
  if (gen !== generation) return
  await follow($, id, gen)
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'board',
      description: 'Show or hide the Switchyard session board pane',
    })
    isBandOn = await config($, 'terminal.band')
    isAutoOpen = await config($, 'terminal.pane_auto_open')
    stop()
    hasAutoOpened = false
    lastStatus = undefined
    const gen = generation
    const id = await $.session.id()
    void follow($, id, gen).catch(() => {})

    return next(e)
  })

  on('session.end', async ($, e, next) => {
    stop()
    lastStatus = undefined
    $.ui.status(undefined)
    if (e.reason === 'clear') {
      // The process goes on under a new session id and no session.start fires: follow again,
      // under the new id. It may not be visible yet when session.end runs, so ask until it
      // differs from the old one (a bounded wait; then follow whatever it says).
      void followNewSession($, e.sessionId, generation).catch(() => {})
    }

    return next(e)
  })

  on('command.run', { command: 'board' }, async $ => {
    const panes = await $.ui.panes()
    if (panes.some(p => p.id === PANE)) {
      await $.ui.close({ id: PANE })

      return { text: 'Switchyard board hidden.' }
    }
    const opened = await $.ui.open({ id: PANE, title: 'Switchyard' })

    return {
      text: opened.isPlaced ? 'Switchyard board opened.' : 'Switchyard board: widen the terminal to see it.',
    }
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Button, Link, Text } = $.ui.resolve(e)
    const v = await read($, view)
    const sel = await read($, selected)
    if (v === null) {
      return <Text dimColor>Waiting for the board…</Text>
    }
    const width = Math.max(20, e.props.bodyColumns)
    const full = width > 60
    const order = rowOrder(v)
    const own = ownRow(v, sel)
    const needs = new Set(v.needs_you)

    const focus = (r: Row) => async () => {
      await update($, selected, () => r.id)
      if (r.can_focus) {
        await $.process.run([SY, 'term', 'focus', '--session', r.id])
      } else {
        $.ui.toast(r.here ? 'Cannot bring that tab forward.' : `That session runs on ${r.machine_name}.`)
      }
    }

    const rowView = (r: Row) => {
      const mark = own !== undefined && own.id === r.id ? '›' : ' '
      const chip = r.here ? '' : ` ${r.machine_name}`
      const tail = `${chip}  ${r.status}${full ? '  ' + r.age : ''}`
      const head = cut(`${r.project} · ${r.title}`, Math.max(8, width - 8 - cells(tail)))
      return (
        <Box key={`box:${r.id}`} flexDirection="column">
          <Box flexDirection="row">
            <Button key={`row:${r.id}`} plain onPress={focus(r)}>
              <Text>{mark} </Text>
              <Text color={r.colour}>{glyph(r)}</Text>
              <Text bold={r.needs_you}> {head}</Text>
              <Text dimColor>{tail}</Text>
            </Button>
            {isClaudeURL(r.remote_url) ? <Link href={r.remote_url} label=" ↗" /> : null}
          </Box>
          {full && r.line2 !== '' ? <Text dimColor wrap="truncate-end">{'    ' + cut(r.line2, width - 6)}</Text> : null}
        </Box>
      )
    }

    const hub = v.hub.state === 'live' ? 'hub live' : `hub ${v.hub.state}`
    const sections = []
    const needIds = order.filter(id => needs.has(id))
    if (needIds.length > 0) {
      sections.push(
        <Box key="needs" flexDirection="column">
          <Text bold color="yellow">NEEDS YOU</Text>
          {needIds.map(id => rowView(v.rows[id] as Row))}
        </Box>,
      )
    }
    const placed = new Set(needIds)
    for (const g of v.groups) {
      const ids = g.rows.filter(id => v.rows[id] !== undefined && !needs.has(id))
      ids.forEach(id => placed.add(id))
      if (ids.length === 0) continue
      const limits = [g.five_hour === null ? '' : `5h ${g.five_hour.text}`, g.seven_day === null ? '' : `7d ${g.seven_day.text}`]
        .filter(s => s !== '')
        .join('  ')
      sections.push(
        <Box key={`g:${g.seat}`} flexDirection="column">
          <Text>
            <Text bold color={g.colour}>{g.label}</Text>
            <Text dimColor>{limits === '' ? '' : '  ' + limits}</Text>
          </Text>
          {ids.map(id => rowView(v.rows[id] as Row))}
        </Box>,
      )
    }
    const rest = order.filter(id => !placed.has(id))
    if (rest.length > 0) {
      sections.push(
        <Box key="other" flexDirection="column">
          <Text bold>Other</Text>
          {rest.map(id => rowView(v.rows[id] as Row))}
        </Box>,
      )
    }

    return (
      <Box flexDirection="column">
        <Text bold>
          Switchyard <Text dimColor>{` ${v.counts.sessions} sessions · ${v.counts.machines} machines · ${hub}`}</Text>
        </Text>
        {sections}
        {own === undefined || !isResumeCommand(own.resume) ? null : (
          <Button
            key="copy"
            plain
            hotkey="c"
            label="copy resume command"
            onPress={press => $.ui.copy({ text: own.resume, surface: press.surface })}
          />
        )}
      </Box>
    )
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    // The pane opens unasked only in a fullscreen session, once (the engine then seats it
    // from 144 columns); /board opens it at any width.
    if (isAutoOpen && !hasAutoOpened && e.viewport?.isFullscreen === true) {
      hasAutoOpened = true
      void $.ui.open({ id: PANE, title: 'Switchyard' })
    }
    const v = await read($, view)
    const band = v === null || !isBandOn || e.props.hasSurvey ? null : bandText(v)
    if (band === null) {
      return next(e)
    }
    const { Box, Button, Text } = $.ui.resolve(e)

    return (
      <Box flexDirection="row">
        <Text color="yellow">▲ </Text>
        <Text bold>Switchyard </Text>
        <Text> {band.lead}</Text>
        {band.more === '' ? null : <Text dimColor>{'   ' + band.more}</Text>}
        <Button key="band:board" dimColor label=" board" onPress={() => $.ui.open({ id: PANE, title: 'Switchyard', focus: true })} />
      </Box>
    )
  })
}
