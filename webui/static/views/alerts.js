// Alerts and rules (PhoneAlerts / DeskAlerts): which pushes fire, quiet hours, and the log of
// recent pushes with their outcome.
//
// - An owner session edits the hub's rules (switches and delays) and overrides.
// - A push-scoped device (a paired phone) edits only its own mute list and quiet hours.
// - A plain board login sees everything read-only.

import { h } from '../components/dom.js';
import { toggle, select, section, toast, moreTabs } from '../components/ui.js';
import { quietHours } from '../components/quiet.js';
import { seatIndex, seatName } from '../components/seat.js';
import { clock } from '../format.js';
import { isOwner, canPush } from '../store.js';
import {
  getRules, getSelf, patchDevice, putRule, addRule, deleteRule, putPushSettings, getAlerts, listDevices, testTarget,
} from '../api.js';
import { reasonText } from '../components/alerts.js';

const GROUPS = [
  { id: 'needs_permission', label: 'Needs permission', triggers: ['needs_permission'], delay: true },
  { id: 'asked', label: 'Asked a question', triggers: ['asked'], delay: true },
  { id: 'failed', label: 'Failed or rate limited', triggers: ['failed'] },
  { id: 'limits', label: 'Limits at 80% / 95%', triggers: ['limit_80', 'limit_95'] },
  { id: 'reset', label: 'Limit window reset', triggers: ['reset'] },
  { id: 'machine_silent', label: 'Machine silent with live sessions', triggers: ['machine_silent'], delay: true },
];
const TRIGGER_LABEL = {
  needs_permission: 'Needs permission', asked: 'Asked a question', failed: 'Failed or rate limited',
  limit_80: 'Limit at 80%', limit_95: 'Limit at 95%', reset: 'Limit window reset', machine_silent: 'Machine silent',
};
const DELAYS = [0, 30, 60, 120, 300, 600, 1800, 3600];

export function delayText(s) {
  if (!s) return '';
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  return `${Math.round(s / 3600)} h`;
}
const delayOptions = (cur) => [...new Set([...DELAYS, cur])].sort((a, b) => a - b).map((s) => [s, s ? delayText(s) : 'at once']);

const OUTCOME = { fired: 'open', seen: 'seen', snoozed: 'snoozed', cancelled: 'cancelled', closed: 'closed', pending: 'waiting', skipped: 'skipped' };

const state = { rules: null, settings: null, self: null, alerts: null, devices: null, error: null, loaded: false };
const again = () => window.dispatchEvent(new Event('sy-rerender'));

export async function load() {
  state.error = null;
  const tasks = [
    getRules().then((r) => { state.rules = r.rules; state.settings = r.settings; }),
    getSelf().then((d) => { state.self = d; }),
    getAlerts({ limit: '20' }).then((r) => { state.alerts = r.alerts; }),
  ];
  if (isOwner()) tasks.push(listDevices().then((r) => { state.devices = r.devices; }).catch(() => {}));
  const results = await Promise.allSettled(tasks);
  const bad = results.find((r) => r.status === 'rejected' && r.reason && r.reason.name !== 'AuthError');
  if (bad) state.error = bad.reason.message;
  state.loaded = true;
  again();
}

const base = (trigger) => (state.rules || []).find((r) => r.base && r.trigger === trigger);

async function saveRule(rule, patch) {
  const body = { enabled: rule.enabled, delay_s: rule.delay_s, bypass_quiet: rule.bypass_quiet, scope: rule.scope, ...patch };
  const next = await putRule(rule.id, body);
  Object.assign(rule, next);
}

async function savePrefs(patch) {
  const prefs = { ...(state.self.prefs || {}), ...patch };
  state.self = await patchDevice('self', { prefs });
  again();
}

function ruleRows(mode) {
  return GROUPS.map((g) => {
    const rules = g.triggers.map(base).filter(Boolean);
    if (!rules.length) return null;
    const first = rules[0];
    const delay = g.delay ? delayText(first.delay_s) : '';
    const label = delay && mode !== 'owner' ? `${g.label} · ${delay}` : g.label;
    if (mode === 'owner') {
      return h('div', { class: 'row rule-row' },
        toggle({
          label, checked: rules.every((r) => r.enabled), key: `rule-${g.id}`,
          onChange: async (v) => { for (const r of rules) await saveRule(r, { enabled: v }); again(); },
        }),
        g.delay ? select({
          label: `${g.label} delay`, value: first.delay_s, options: delayOptions(first.delay_s), key: `delay-${g.id}`,
          onChange: async (v) => { await saveRule(first, { delay_s: Number(v) }); again(); },
        }) : null);
    }
    const mute = (state.self && state.self.prefs && state.self.prefs.mute) || [];
    const ruleOn = rules.every((r) => r.enabled);
    const muted = g.triggers.some((t) => mute.includes(t));
    return h('div', { class: 'row rule-row' }, toggle({
      label, sub: ruleOn ? '' : 'Off on the hub', checked: ruleOn && !muted, key: `rule-${g.id}`,
      disabled: mode === 'read' || !ruleOn,
      onChange: (v) => savePrefs({ mute: v ? mute.filter((t) => !g.triggers.includes(t)) : [...new Set([...mute, ...g.triggers])] }),
    }));
  });
}

function overrides(mode, idx) {
  const list = (state.rules || []).filter((r) => !r.base);
  const seatLabel = (id) => (idx.get(id) ? seatName(idx.get(id).seat) : id);
  const scopeText = (r) => {
    const s = r.scope || {};
    const parts = [...(s.seats || []).map(seatLabel), ...(s.projects || []), ...(s.machines || []), ...(s.windows || [])];
    return parts.length ? parts.join(', ') : 'everything';
  };
  const rows = list.map((r) => h('div', { class: 'row' },
    h('span', { class: 'row-text' }, h('span', {}, `${TRIGGER_LABEL[r.trigger] || r.trigger} · ${scopeText(r)}`),
      h('span', { class: 'muted small' }, r.enabled ? (r.delay_s ? `after ${delayText(r.delay_s)}` : 'at once') : 'off')),
    mode === 'owner' ? h('button', {
      type: 'button', class: 'btn btn-sm', 'data-key': `del-${r.id}`,
      onclick: async (e) => {
        e.currentTarget.disabled = true;
        try { await deleteRule(r.id); state.rules = state.rules.filter((x) => x.id !== r.id); again(); } catch (ex) { toast(ex.message); e.currentTarget.disabled = false; }
      },
    }, 'Remove') : null));
  if (!rows.length && mode !== 'owner') return null;
  const seats = [...idx.values()].map((x) => x.seat);
  const form = mode === 'owner' ? addOverride(seats) : null;
  return section('Overrides', h('div', { class: 'panel' }, rows.length ? rows : h('p', { class: 'muted pad-s' }, 'No overrides.')), form);
}

function addOverride(seats) {
  let trigger = 'asked', seat = '', enabled = 'on', delay = 300;
  const sel = (label, opts, cur, set, key) => h('label', { class: 'field' }, label,
    h('select', { 'data-key': key, onchange: (e) => set(e.target.value) }, opts.map(([v, t]) => h('option', { value: v, selected: String(v) === String(cur) }, t))));
  const form = h('form', {
    class: 'panel override-form',
    onsubmit: async (e) => {
      e.preventDefault();
      const b = form.querySelector('button[type=submit]');
      b.disabled = true;
      try {
        const r = await addRule({ trigger, enabled: enabled === 'on', delay_s: Number(delay), scope: seat ? { seats: [seat] } : {} });
        state.rules = [...state.rules, r];
        again();
      } catch (ex) { toast(ex.message); } finally { b.disabled = false; }
    },
  },
    h('div', { class: 'form-row' },
      sel('When', Object.entries(TRIGGER_LABEL), trigger, (v) => { trigger = v; }, 'ov-trigger'),
      sel('For', [['', 'All accounts'], ...seats.map((s) => [s.id, seatName(s)])], seat, (v) => { seat = v; }, 'ov-seat'),
      sel('Then', [['on', 'Notify'], ['off', 'Do not notify']], enabled, (v) => { enabled = v; }, 'ov-enabled'),
      sel('After', delayOptions(300), delay, (v) => { delay = v; }, 'ov-delay')),
    h('button', { type: 'submit', class: 'btn', 'data-key': 'ov-add' }, 'Add override'));
  return form;
}

function recent() {
  const list = state.alerts;
  return section('Recent pushes',
    !list ? h('p', { class: 'muted' }, state.error || 'Loading…')
      : list.length === 0 ? h('p', { class: 'calm' }, 'No alerts yet. They appear here when something needs you.')
      : h('ul', { class: 'panel log' }, list.map((a) => h('li', { class: 'log-row' },
        h('time', { class: 'mono small log-time', datetime: a.fired_at || a.created_at }, clock(a.fired_at || a.created_at)),
        h('a', { class: 'log-title', href: a.url || '#/' }, a.title || a.trigger),
        h('span', { class: `log-outcome out-${a.status}`, title: reasonText(a.reason) }, OUTCOME[a.status] || a.status)))),
    h('p', { class: 'muted small' }, '"Cancelled": you answered in the terminal before the delay ran out, so nothing was sent.'));
}

function phones() {
  if (!isOwner() || !state.devices) return null;
  const phones = state.devices.filter((d) => d.kind === 'phone' || d.kind === 'browser').filter((d) => !d.revoked_at);
  return h('div', { class: 'cards-2 desk-only' },
    h('div', { class: 'card' }, h('h2', {}, 'Add a phone'),
      h('p', { class: 'muted' }, 'Scan with the phone camera to open the web app, or type the code shown.'),
      h('a', { class: 'btn btn-primary', href: '#/more/devices?add=phone', 'data-key': 'add-phone' }, 'Add phone')),
    h('div', { class: 'card' }, h('h2', {}, 'Paired phones'),
      phones.length ? h('ul', { class: 'plain' }, phones.map((d) => h('li', { class: 'row' }, h('span', {}, d.name),
        (d.targets || []).filter((t) => t.status !== 'gone').slice(0, 1).map((t) => h('button', {
          type: 'button', class: 'linkish', 'data-key': `test-${t.id}`,
          onclick: async () => { try { const r = await testTarget(t.id); toast(r.outcome === 'sent' ? 'Test push sent' : `Test push: ${r.outcome}${r.error ? ' · ' + r.error : ''}`); } catch (ex) { toast(ex.message); } },
        }, 'Test push')))))
        : h('p', { class: 'muted' }, 'None yet.'),
      h('a', { href: '#/more/devices' }, 'All devices')));
}

export function render({ store }) {
  const owner = isOwner();
  const mode = owner ? 'owner' : canPush() ? 'device' : 'read';
  const idx = seatIndex(store.overview.seats);
  const quiet = state.settings && state.self
    ? (owner
      ? quietHours({
        value: state.settings.quiet_hours, hubValue: null, key: 'hub',
        onSave: async (q) => { state.settings = await putPushSettings({ ...state.settings, quiet_hours: q }); again(); },
      })
      : quietHours({
        device: true, value: state.self.prefs && state.self.prefs.quiet_hours, hubValue: state.settings.quiet_hours, disabled: mode === 'read',
        onSave: (q) => savePrefs({ quiet_hours: q }),
      }))
    : null;

  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head' }, h('h1', {}, 'Alerts'),
      h('p', { class: 'muted small head-meta' }, mode === 'owner' ? 'Rules apply to every device.' : mode === 'device' ? 'Switches choose what this phone gets; the hub sets the delays.' : 'Read-only: sign in as owner (sy hub owner-link) to change rules.')),
    moreTabs('alerts', { owner }),
    phones(),
    section('Rules',
      !state.rules ? h('p', { class: 'muted' }, state.error || 'Loading…')
        : h('div', { class: 'panel' }, ruleRows(mode), quiet ? h('div', { class: 'row' }, quiet) : null)),
    state.rules ? overrides(mode, idx) : null,
    recent());
  return { title: 'Alerts · Switchyard', node };
}
