import { expect, test } from 'claude-code/testing'

import { STREAM } from './fixtures'
import { Splitter, ago, cut, isClaudeURL, parseLine, rowOrder, statusText } from './model'

test('parseLine takes a View and drops everything else', () => {
  expect(parseLine(STREAM[0] as string)).not.toBeNull()
  expect(parseLine(STREAM[1] as string)).toBeNull()
  expect(parseLine(STREAM[3] as string)).toBeNull()
  expect(parseLine('')).toBeNull()
  expect(parseLine('[]')).toBeNull()
  expect(parseLine('{"v":1}')).toBeNull()
  expect(parseLine('{"v":1,"rows":{"a":3},"needs_you":[],"groups":[],"counts":{}}')).toBeNull()
})

test('Splitter joins pieces into lines', () => {
  const s = new Splitter()
  expect(s.push('{"a":')).toEqual([])
  expect(s.push('1}\r\n{"b":2}\n\n{"c"')).toEqual(['{"a":1}', '{"b":2}'])
  expect(s.push(':3}\n')).toEqual(['{"c":3}'])
})

test('Splitter drops an endless line', () => {
  const s = new Splitter()
  s.push('x'.repeat(1024 * 1024 + 1))
  expect(s.overflowed).toBe(1)
  expect(s.push('{"ok":1}\n')).toEqual(['{"ok":1}'])
})

test('rowOrder is needs-you first, then the groups', () => {
  const v = parseLine(STREAM[4] as string)
  expect(v).not.toBeNull()
  if (v === null) return
  expect(rowOrder(v).slice(0, 3)).toEqual(v.needs_you)
  expect(rowOrder(v).length).toBe(Object.keys(v.rows).length)
  expect(statusText(v)).toBe('needs you: 3')
  expect(statusText(null)).toBeUndefined()
})

test('words and links', () => {
  expect(ago('4m')).toBe('4 min ago')
  expect(ago('40s')).toBe('40 s ago')
  expect(cut('abcdef', 4)).toBe('abc…')
  expect(isClaudeURL('https://claude.ai/code/x')).toBe(true)
  expect(isClaudeURL('http://claude.ai/x')).toBe(false)
  expect(isClaudeURL('https://claude.ai.evil.example/x')).toBe(false)
  expect(isClaudeURL('javascript:alert(1)')).toBe(false)
})
