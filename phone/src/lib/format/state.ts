// Session state names and colour classes (docs/spec/states.md; webui components/state.js).
// The word always travels with the glyph, so state is never colour-only.
import type { Session } from '../../contract/api';
import { humanKey } from './core';

export interface StateInfo {
  label: string;
  long: string;
  glyph: string;
  cls: 'working' | 'needs' | 'done' | 'failed' | 'ended';
}

const BASE: Record<string, StateInfo> = {
  working: { label: 'Working', long: 'Working', glyph: '●', cls: 'working' },
  needs_permission: { label: 'Permission', long: 'Needs permission', glyph: '●', cls: 'needs' },
  asked: { label: 'Asked', long: 'Asked a question', glyph: '●', cls: 'needs' },
  done: { label: 'Done', long: 'Done', glyph: '○', cls: 'done' },
  failed: { label: 'Failed', long: 'Failed', glyph: '■', cls: 'failed' },
  ended: { label: 'Ended', long: 'Ended', glyph: '◌', cls: 'ended' },
  gone: { label: 'Gone', long: 'Ended without a goodbye', glyph: '◌', cls: 'ended' },
};

const FAILURES: Record<string, string> = {
  rate_limit: 'Rate limited',
  authentication_failed: 'Login failed',
  billing_error: 'Billing problem',
  server_error: 'Server error',
  overloaded: 'Overloaded',
  max_output_tokens: 'Output too long',
  model_not_found: 'Model not found',
};

export function stateInfo(s: Pick<Session, 'state' | 'detail' | 'needs_you'>): StateInfo {
  const base = BASE[s.state] ?? { label: humanKey(s.state), long: humanKey(s.state), glyph: '○', cls: 'done' as const };
  const d = s.detail;
  if (s.state === 'done' && d === 'started') return { ...base, label: 'New', long: 'New session' };
  if (s.state === 'done' && d === 'resumed') return { ...base, label: 'Resumed', long: 'Resumed' };
  if (s.state === 'failed') {
    const f = (d && FAILURES[d]) || (d ? humanKey(d) : 'Failed');
    return { ...base, label: f, long: f, cls: s.needs_you ? 'failed' : 'ended' };
  }
  return base;
}

/** How long a finished turn may sit before the session no longer counts as open (the hub's 12 h mark). */
export const IDLE_MS = 12 * 36e5;

/**
 * Open: not ended, and not a finished session idle for 12 h, nor a stale working one. The
 * board's isLive (webui/static/components/live.js) and the hub's `dormant` use the same rule
 * (states.md 5); the hub's own `dormant` wins when it is sent.
 */
export const isLive = (
  s: Pick<Session, 'state'> & Partial<Pick<Session, 'needs_you' | 'stale' | 'dormant' | 'activity_at' | 'last_event_at' | 'state_since' | 'started_at'>>,
  now: number = Date.now(),
): boolean => {
  if (s.state === 'ended' || s.state === 'gone') return false;
  if (s.needs_you) return true;
  if (typeof s.dormant === 'boolean') return !s.dormant;
  if (s.state !== 'done' && !s.stale) return true;
  const t = Date.parse(s.activity_at || s.last_event_at || s.state_since || s.started_at || '');
  return Number.isNaN(t) || now - t < IDLE_MS;
};

/**
 * The short state word of a session ("Asked", "Permission", "Rate limited", "Done", …): the
 * label of the PWA's state tag. `now` and `tz` are kept for the stub's signature; no state
 * word carries a time today.
 */
export function stateWord(s: Session, now?: number, tz?: string): string {
  void now;
  void tz;
  return stateInfo(s).label;
}
