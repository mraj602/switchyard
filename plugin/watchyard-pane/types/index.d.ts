// The pane mod's $.state contract (docs/spec/terminal.md T5).
//
// The View types are the JSON of `wy board --follow --ndjson` (T2, internal/board/view.go),
// reduced to the fields the mod reads; unknown fields are ignored.

export type HubState = 'live' | 'offline' | 'none' | 'unpaired'
export type LineKind = 'question' | 'recap' | 'error' | 'none'
export type Glyph = 'working' | 'needs' | 'done' | 'failed' | 'ended' | 'gone'

export type Row = {
  id: string
  state: string
  detail?: string
  needs_you: boolean
  glyph: Glyph
  status: string
  project: string
  title: string
  account: string
  colour?: string
  machine_name: string
  here: boolean
  this_session: boolean
  context_pct: number | null
  context_level?: 'ok' | 'warn' | 'high'
  age: string
  line2: string
  line2_kind: LineKind
  can_focus: boolean
  resume: string
  remote_url?: string
}

export type Window = { pct: number; text: string; resets_at?: string }

export type Group = {
  seat: string
  label: string
  colour: string
  five_hour: Window | null
  seven_day: Window | null
  limited: boolean
  rows: string[]
}

export type View = {
  v: 1
  generated_at: string
  source: 'hub' | 'local' | 'mixed'
  hub: { state: HubState; url?: string; offline_since: string | null }
  machine: { id: string; name: string }
  counts: { sessions: number; machines: number; needs_you: number }
  needs_you: string[]
  groups: Group[]
  rows: Record<string, Row>
  truncated?: number
}

declare module 'claude-code' {
  interface PluginState {
    'watchyard-pane': {
      // The latest View the board child wrote; null until the first one.
      view: View | null
      // The row the `c` hotkey copies the resume command of; null means the first row.
      selected: string | null
      // Lines dropped since the child started: not JSON, v != 1, or not shaped like a View.
      dropped: number
      // Child restarts since the session started.
      restarts: number
    }
  }
}
