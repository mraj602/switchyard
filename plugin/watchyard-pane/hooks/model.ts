// Pure helpers of the pane mod: parsing the board's NDJSON, ordering rows, and the words of the
// band. No `$` calls here, so register.tsx stays the only place that touches the engine.
import type { Row, View } from '../types'

// A View is capped at 512 KiB (T2); a line past this is dropped without parsing.
export const MAX_LINE = 1024 * 1024

// parseLine returns the View a line holds, or null for a line that is not valid JSON, has
// v != 1, or is not shaped like a View (the mod drops it and counts it).
export function parseLine(line: string): View | null {
  if (line.length === 0 || line.length > MAX_LINE) return null
  let v: unknown
  try {
    v = JSON.parse(line)
  } catch {
    return null
  }
  if (typeof v !== 'object' || v === null) return null
  const o = v as Record<string, unknown>
  if (o.v !== 1) return null
  if (typeof o.rows !== 'object' || o.rows === null || Array.isArray(o.rows)) return null
  if (!Array.isArray(o.needs_you) || !Array.isArray(o.groups)) return null
  if (typeof o.counts !== 'object' || o.counts === null) return null
  if (!isRecord(o.hub) || typeof o.hub.state !== 'string') return null
  const counts = o.counts as Record<string, unknown>
  if (typeof counts.sessions !== 'number' || typeof counts.machines !== 'number') return null
  if (!o.needs_you.every(id => typeof id === 'string')) return null
  for (const g of o.groups) {
    if (!isRecord(g)) return null
    if (typeof g.seat !== 'string' || typeof g.label !== 'string' || !Array.isArray(g.rows)) return null
    if (!g.rows.every(id => typeof id === 'string')) return null
    if (!isWindow(g.five_hour) || !isWindow(g.seven_day)) return null
    if (g.colour !== undefined && typeof g.colour !== 'string') return null
  }
  const rows = o.rows as Record<string, unknown>
  for (const id of Object.keys(rows)) {
    if (!isRow(rows[id])) return null
  }
  return v as View
}

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x)
}

// A Window is null (not shown) or an object with a text; an absent one reads as null.
function isWindow(x: unknown): boolean {
  return x === null || x === undefined || (isRecord(x) && typeof x.text === 'string')
}

// isRow checks every field the pane reads, so a View from a newer or older wy that lacks one
// is dropped here instead of throwing while it renders.
function isRow(x: unknown): boolean {
  if (!isRecord(x)) return false
  for (const k of ['id', 'state', 'glyph', 'status', 'project', 'title', 'account', 'machine_name', 'age', 'line2', 'resume']) {
    if (typeof x[k] !== 'string') return false
  }
  for (const k of ['needs_you', 'here', 'this_session', 'can_focus']) {
    if (typeof x[k] !== 'boolean') return false
  }
  for (const k of ['colour', 'detail', 'remote_url']) {
    if (x[k] !== undefined && typeof x[k] !== 'string') return false
  }
  return true
}

// Splitter turns the child's output pieces (a line may span two, a piece may hold several)
// into whole lines.
export class Splitter {
  private buf = ''
  // overflowed counts lines thrown away for having no newline within MAX_LINE.
  overflowed = 0

  push(text: string): string[] {
    this.buf += text
    const out: string[] = []
    let i = this.buf.indexOf('\n')
    while (i >= 0) {
      out.push(this.buf.slice(0, i).replace(/\r$/, ''))
      this.buf = this.buf.slice(i + 1)
      i = this.buf.indexOf('\n')
    }
    if (this.buf.length > MAX_LINE) {
      this.buf = ''
      this.overflowed += 1
    }
    return out.filter(l => l.trim().length > 0)
  }
}

// rowOrder is the order the pane lists rows in: the needs-you rows first (the hub's order,
// oldest waiting first), then each group's rows in order, then any row no group holds.
export function rowOrder(view: View): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  const add = (id: string) => {
    if (!seen.has(id) && view.rows[id] !== undefined) {
      seen.add(id)
      out.push(id)
    }
  }
  view.needs_you.forEach(add)
  view.groups.forEach(g => g.rows.forEach(add))
  Object.keys(view.rows).forEach(add)
  return out
}

// waiting is the needs-you rows that can make a band: not the session this pane is in.
export function waiting(view: View): Row[] {
  const out: Row[] = []
  for (const id of view.needs_you) {
    const r = view.rows[id]
    if (r !== undefined && !r.this_session) out.push(r)
  }
  return out
}

function what(r: Row): string {
  if (r.state === 'asked' && r.detail === 'question') return 'asked a question'
  if (r.state === 'asked') return 'is waiting for you'
  if (r.state === 'needs_permission') return 'needs permission'
  if (r.state === 'failed' && r.detail === 'rate_limit') return 'hit a limit'
  if (r.state === 'failed') return 'failed'
  return 'needs you'
}

// ago turns the board's age word ("40s", "4m", "3h", "2d") into "4 min ago".
export function ago(age: string): string {
  const m = /^(\d+)([smhd])$/.exec(age)
  if (m === null) return age === '' ? '' : `${age} ago`
  const unit = { s: 's', m: 'min', h: 'h', d: 'd' }[m[2] as 's' | 'm' | 'h' | 'd']
  return `${m[1]} ${unit} ago`
}

// bandText is the one line above the prompt, or null when no other session needs you:
//   work · api (gpu-box) asked a question 4 min ago   +2 more need you
export function bandText(view: View): { lead: string; more: string } | null {
  const w = waiting(view)
  const first = w[0]
  if (first === undefined) return null
  const where = first.here ? '' : ` (${first.machine_name})`
  const when = ago(first.age)
  const lead = `${first.account} · ${first.project}${where} ${what(first)}${when === '' ? '' : ' ' + when}`
  return { lead, more: w.length > 1 ? `+${w.length - 1} more need you` : '' }
}

export function statusText(view: View | null): string | undefined {
  const n = view === null ? 0 : view.needs_you.length
  return n > 0 ? `needs you: ${n}` : undefined
}

const GLYPHS: Record<string, string> = {
  working: '●',
  needs: '▲',
  done: '○',
  failed: '■',
  ended: '·',
  gone: '·',
}

export function glyph(r: Row): string {
  return GLYPHS[r.glyph] ?? '·'
}

// cellWidth is the terminal columns a code point takes: 0 for combining marks, joiners and
// variation selectors, 2 for East Asian wide and emoji, else 1.
export function cellWidth(cp: number): number {
  if (cp === 0 || cp === 0x200b || cp === 0x200d || (cp >= 0x300 && cp <= 0x36f) || (cp >= 0xfe00 && cp <= 0xfe0f) || (cp >= 0x20d0 && cp <= 0x20ff)) return 0
  if (cp < 0x20 || (cp >= 0x7f && cp < 0xa0)) return 0
  if (
    (cp >= 0x1100 && cp <= 0x115f) ||
    (cp >= 0x2e80 && cp <= 0xa4cf && cp !== 0x303f) ||
    (cp >= 0xac00 && cp <= 0xd7a3) ||
    (cp >= 0xf900 && cp <= 0xfaff) ||
    (cp >= 0xfe30 && cp <= 0xfe6f) ||
    (cp >= 0xff00 && cp <= 0xff60) ||
    (cp >= 0xffe0 && cp <= 0xffe6) ||
    (cp >= 0x1f300 && cp <= 0x1f64f) ||
    (cp >= 0x1f900 && cp <= 0x1f9ff) ||
    (cp >= 0x20000 && cp <= 0x3fffd)
  ) {
    return 2
  }
  return 1
}

// width is the terminal columns of s.
export function width(s: string): number {
  let w = 0
  for (const ch of s) w += cellWidth(ch.codePointAt(0) as number)
  return w
}

// cut shortens s to at most n terminal columns with an ellipsis. It walks code points, so a
// surrogate pair is never split, and counts wide characters as two columns.
export function cut(s: string, n: number): string {
  if (n <= 0) return ''
  if (width(s) <= n) return s
  const budget = n <= 1 ? n : n - 1
  let out = ''
  let w = 0
  for (const ch of s) {
    const cw = cellWidth(ch.codePointAt(0) as number)
    if (w + cw > budget) break
    out += ch
    w += cw
  }
  return n <= 1 ? out : out + '…'
}

// resumeRE is the one shape of resume command the pane copies (the same rule wy applies to a
// hub's rows): the pane's copy button is hidden for anything else.
const resumeRE = /^(claude(-[0-9]{1,3})?|CLAUDE_CONFIG_DIR=(~\/)?[A-Za-z0-9._@%+=:,/-]+ claude) --resume [0-9A-Za-z_-]{1,64}$/

export function isResumeCommand(s: unknown): s is string {
  return typeof s === 'string' && resumeRE.test(s) && !s.includes('..')
}

export function isClaudeURL(u: string | undefined): u is string {
  return typeof u === 'string' && u.startsWith('https://claude.ai/') && !/[\s\u0000-\u001f]/.test(u)
}
