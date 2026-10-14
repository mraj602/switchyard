// Small shared controls: switch, select, section card, toast, SVG helper.

import { h } from './dom.js';

const SVG_NS = 'http://www.w3.org/2000/svg';

// An SVG element built with DOM calls (never markup strings).
export function svg(tag, attrs = {}, ...kids) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) if (v != null) el.setAttribute(k, String(v));
  for (const k of kids.flat()) if (k) el.append(k);
  return el;
}

// A labelled on/off switch (a checkbox with role=switch). onChange gets the new value; when
// it rejects, the switch goes back and the error is shown.
export function toggle({ label, sub, checked, disabled, onChange, key }) {
  const input = h('input', { type: 'checkbox', role: 'switch', class: 'sw', 'data-key': key, disabled: disabled ? true : undefined, 'aria-label': label });
  input.checked = !!checked;
  input.addEventListener('change', async () => {
    const want = input.checked;
    input.disabled = true;
    try { await onChange(want); } catch (e) { input.checked = !want; toast(e.message || 'Could not save'); } finally { input.disabled = !!disabled; }
  });
  return h('label', { class: `switch-row${disabled ? ' is-disabled' : ''}` },
    h('span', { class: 'switch-text' }, h('span', {}, label), sub ? h('span', { class: 'muted small' }, sub) : null), input);
}

export function select({ label, value, options, onChange, key, disabled }) {
  const sel = h('select', { 'data-key': key, 'aria-label': label, disabled: disabled ? true : undefined },
    options.map(([v, text]) => h('option', { value: String(v), selected: String(v) === String(value) }, text)));
  sel.addEventListener('change', async () => {
    const prev = value;
    sel.disabled = true;
    try { await onChange(sel.value); value = sel.value; } catch (e) { sel.value = String(prev); toast(e.message || 'Could not save'); } finally { sel.disabled = !!disabled; }
  });
  return sel;
}

// A page section: heading and a panel of rows.
export function section(title, ...kids) {
  return h('section', { class: 'block', 'aria-label': title }, h('h2', {}, title), ...kids);
}

export const panel = (...kids) => h('div', { class: 'panel' }, ...kids);
export const row = (...kids) => h('div', { class: 'row' }, ...kids);

// A short message at the bottom of the screen (errors from actions).
let toastTimer = 0;
export function toast(msg) {
  const el = document.getElementById('toasts');
  if (!el) return;
  el.replaceChildren(h('div', { class: 'toast' }, msg));
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.replaceChildren(), 5000);
}

export const pillNav = (items, current) =>
  h('nav', { class: 'pills phone-only', 'aria-label': 'More' }, items.map(([page, href, label]) =>
    h('a', { class: 'pill', href, 'aria-current': page === current ? 'page' : undefined }, label)));

// Pill row shown under the title of the screens behind the More tab.
export function moreTabs(current, { owner } = {}) {
  const items = [['alerts', '#/more/alerts', 'Alerts'], ['machines', '#/machines', 'Machines'], ['settings', '#/more/settings', 'Settings'], ['peers', '#/more/peers', 'Peers']];
  if (owner) items.push(['devices', '#/more/devices', 'Devices']);
  return pillNav(items, current);
}
