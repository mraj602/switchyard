// Alert status text and the Seen / Snooze controls shared by Now, Session and Alerts.

import { h } from './dom.js';
import { toast } from './ui.js';
import { clock } from '../format.js';
import { store, markSeen, snooze, canPush, actionable } from '../store.js';

const REASONS = {
  remote_control: 'the Claude app already notifies', rule_off: 'rule is off', stale: 'too old to send',
  superseded: 'replaced by a later alert', baseline: 'existing at first start', answered: 'you answered first',
  ended: 'session ended', recovered: 'recovered', reset_passed: 'window reset', seen: 'seen',
};
export const reasonText = (r) => REASONS[r] || (r ? String(r).replace(/_/g, ' ') : '');

// "notifies in 40 s" counts down; the app refreshes [data-due] every few seconds.
export function untilShort(ts, now = Date.now()) {
  const d = Math.round((Date.parse(ts) - now) / 1000);
  if (Number.isNaN(d)) return '';
  if (d <= 0) return 'now';
  if (d < 90) return `in ${d} s`;
  return `in ${Math.round(d / 60)} min`;
}

// "Notifies in 19 s", then "Notifying now…" once it is due.
export function pendingText(due, now = Date.now()) {
  if (!due) return 'notification waiting';
  const u = untilShort(due, now);
  return !u || u === 'now' ? 'notifying now…' : `notifies ${u}`;
}

// One line for a session's alert brief, or ''.
export function alertText(a, now = Date.now()) {
  if (!a) return '';
  switch (a.status) {
    case 'pending': return pendingText(a.due_at, now);
    case 'fired': return `notified ${clock(a.fired_at, now)}`;
    case 'snoozed': return `snoozed until ${clock(a.snoozed_until, now)}`;
    case 'seen': return a.seen_at ? `seen ${clock(a.seen_at, now)}` : 'seen';
    case 'skipped': return `not notified · ${reasonText(a.reason) || 'skipped'}`;
    case 'cancelled': return `cancelled · ${reasonText(a.reason) || 'answered'}`;
    default: return a.status;
  }
}

// The text element; `data-due` lets the app keep a pending countdown fresh.
export function alertLine(a, now) {
  if (!a) return null;
  return h('span', { class: `alert-line alert-${a.status}`, 'data-due': a.status === 'pending' && a.due_at ? a.due_at : undefined }, alertText(a, now));
}

const MIN = 15;

// Seen and Snooze buttons. Hidden unless the caller may push and the alert can still be
// acted on; disabled while offline.
export function alertButtons(s, { size = '' } = {}) {
  const a = s.alert;
  if (!canPush() || !a) return null;
  const canSeen = actionable(a) || (a.status === 'skipped' && s.needs_you);
  const canSnooze = actionable(a);
  if (!canSeen && !canSnooze) return null;
  const off = !!store.offline;
  const run = (fn) => async (e) => {
    const b = e.currentTarget;
    b.disabled = true;
    try { await fn(); } catch (ex) { toast(ex.status === 409 ? 'That alert already changed' : ex.message); b.disabled = false; }
  };
  return h('div', { class: 'alert-actions' },
    canSeen ? h('button', { type: 'button', class: `btn ${size}`, 'data-key': `seen-${s.id}`, disabled: off, onclick: run(() => markSeen(a.id, s.id)) }, 'Seen') : null,
    canSnooze ? h('button', { type: 'button', class: `btn ${size}`, 'data-key': `snooze-${s.id}`, disabled: off, onclick: run(() => snooze(a.id, s.id)) }, `Snooze ${MIN} min`) : null);
}

export const canAct = (s) => {
  const a = s.alert;
  return canPush() && !!a && (actionable(a) || (a.status === 'skipped' && s.needs_you)) && !store.offline;
};
