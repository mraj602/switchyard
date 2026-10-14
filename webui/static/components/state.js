// Session state names, glyphs and colours (docs/spec/states.md). The glyph and the word
// always travel with the colour, so state is never colour-only.

import { h } from './dom.js';
import { humanKey } from '../format.js';

const BASE = {
  working: { label: 'Working', long: 'Working', glyph: '●', cls: 'working' },
  needs_permission: { label: 'Permission', long: 'Needs permission', glyph: '●', cls: 'needs' },
  asked: { label: 'Asked', long: 'Asked a question', glyph: '●', cls: 'needs' },
  done: { label: 'Done', long: 'Done', glyph: '○', cls: 'done' },
  failed: { label: 'Failed', long: 'Failed', glyph: '■', cls: 'failed' },
  ended: { label: 'Ended', long: 'Ended', glyph: '◌', cls: 'ended' },
  gone: { label: 'Gone', long: 'Ended without a goodbye', glyph: '◌', cls: 'ended' },
};

const FAILURES = {
  rate_limit: 'Rate limited',
  authentication_failed: 'Login failed',
  billing_error: 'Billing problem',
  server_error: 'Server error',
  overloaded: 'Overloaded',
  max_output_tokens: 'Output too long',
  model_not_found: 'Model not found',
};

export function stateInfo(session) {
  const base = BASE[session.state] || { label: humanKey(session.state), long: humanKey(session.state), glyph: '○', cls: 'done' };
  const d = session.detail;
  if (session.state === 'done' && d === 'started') return { ...base, label: 'New', long: 'New session' };
  if (session.state === 'done' && d === 'resumed') return { ...base, label: 'Resumed', long: 'Resumed' };
  if (session.state === 'failed') {
    const f = FAILURES[d] || (d ? humanKey(d) : 'Failed');
    return { ...base, label: f, long: f, cls: session.needs_you ? 'failed' : 'ended' };
  }
  return base;
}

// "● Asked" with its colour class.
export function stateTag(session, long = false) {
  const i = stateInfo(session);
  return h('span', { class: `state state-${i.cls}` }, h('span', { 'aria-hidden': 'true' }, i.glyph), ' ', long ? i.long : i.label);
}

export const isLive = (s) => s.state !== 'ended' && s.state !== 'gone';
