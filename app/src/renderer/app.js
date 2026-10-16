// The shell renderer (docs/spec/desktop.md C8). Plain ES modules and DOM APIs, no framework,
// no network (CSP connect-src 'none'): everything goes through window.sy (the preload,
// shared/ipc.js names). Never innerHTML: elements are made with createElement and textContent.
// Owned by WP-D. Decisions live in view.js (pure, unit-tested); this file only draws and wires.
//
// Layout contract with window.js (WP-A): top bar 48 px; strip 40 px, only under an account
// tab; the active native view (account tab or hub) covers the rest. Native views sit above this
// page, so anything the person must see while a tab is selected (dialogs) first asks main to
// show the shell (selectView 'settings'), and restores the view when the dialog closes.

import { WARNING_VERSION } from '../shared/constants.js';
import { barWidth, pct, bytes, plural } from '../shared/format.js';
import {
  tabTitle, tabColour, badgeText, nameError, labelError, viewOrder, cycleView, viewForDigit, movedIds, droppedIds,
  effectiveView, windowRows, readingStatus, lastReadTitle, hubBanner, hubUsable, hubSummary, hubChip, ownerPill, fitTabs, progressText,
  SHARE_SWITCHES, foldersOf, isPrimary, switchState, shareSpec, loginCommand, COLOUR_RE, PAIR_CODE_RE,
} from './view.js';

const SVG_NS = 'http://www.w3.org/2000/svg';
const TICK_MS = 30_000;
const TOAST_MS = 6_000;

// ---- tiny DOM helpers

/**
 * h('button', {class: 'btn', text: 'Go', onclick: fn, 'aria-label': 'x', fk: 'go'}, ...children)
 * Strings become text nodes. `style` is an object applied through CSSOM (the CSP forbids the
 * style attribute). `fk` is a focus key that survives a re-render.
 */
function h(tag, props = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k === 'fk') el.dataset.fk = v;
    else if (k === 'style') for (const [p, val] of Object.entries(v)) el.style.setProperty(p, val);
    else if (k === 'value' || k === 'checked' || k === 'disabled' || k === 'hidden' || k === 'readOnly') el[k] = v;
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  add(el, kids);
  return el;
}
function add(el, kids) {
  for (const k of kids) {
    if (k === null || k === undefined || k === false) continue;
    if (Array.isArray(k)) add(el, k);
    else el.append(typeof k === 'string' || typeof k === 'number' ? document.createTextNode(String(k)) : k);
  }
}
function svg(tag, attrs) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  return el;
}
// The same tile mark as the icon (app/build/icons-src/mark-small.svg).
const logo = () => {
  const s = svg('svg', { width: 22, height: 22, viewBox: '0 0 22 22', 'aria-hidden': 'true' });
  const g = svg('g', { transform: 'translate(2.5 2.5) scale(0.7727)', fill: 'none', stroke: '#E8B04B', 'stroke-width': 2.6 });
  g.append(svg('path', { d: 'M3 18 L11 4 L19 18' }), svg('path', { d: 'M7 18 L11 11 L15 18' }));
  s.append(svg('rect', { width: 22, height: 22, rx: 5, fill: '#17191B' }), g);
  return s;
};
const errText = (e) => String((e && e.message) || e || 'Something went wrong').replace(/^Error invoking remote method '[^']*': /, '').replace(/^(Error: )+/, '');

/**
 * Renders the shell into root from window.sy.state() and keeps it current with
 * window.sy.on('state', …).
 * @param {{sy: object, root: HTMLElement}} deps
 */
export function start({ sy, root }) {
  let state = emptyState();
  const ui = {
    v: 0, overlayFor: null, returnView: null, dialog: null, drafts: {}, errors: {}, busy: new Set(),
    dismissed: new Set(), toast: null, folders: null, foldersError: '', foldersLoading: false,
    section: 'computer', pairCode: '', pairError: '', useCode: false, connectUrl: '', connectCode: '', connectError: '',
  };
  const last = {};
  let ready = false;

  const bar = h('header', { class: 'topbar' });
  const stripHost = h('div', { class: 'strip-host' });
  const content = h('div', { class: 'content', id: 'content' });
  root.replaceChildren(bar, stripHost, content);

  // ---- state

  function emptyState() {
    return { tabs: [], view: 'hub', hub: { status: 'not_connected', needs_you: 0 }, settings: {}, banners: [] };
  }
  function apply(s) {
    if (!s || typeof s !== 'object') return;
    ready = true;
    state = { ...emptyState(), ...s, tabs: Array.isArray(s.tabs) ? s.tabs : [], hub: { ...emptyState().hub, ...(s.hub || {}) }, settings: s.settings || {} };
    if (ui.overlayFor && state.view !== 'settings') {
      // The view changed under an open dialog (a menu shortcut): the dialog no longer owns the screen.
      const d = ui.dialog;
      ui.overlayFor = null;
      if (d) d.close('view-changed');
    }
    render();
  }
  const tabById = (id) => state.tabs.find((t) => t.id === id) || null;
  const view = () => effectiveView(state, ui.overlayFor);

  async function act(key, fn) {
    if (ui.busy.has(key)) return undefined;
    ui.busy.add(key);
    ui.v++;
    render();
    try {
      return await fn();
    } catch (e) {
      toast('error', errText(e));
      return undefined;
    } finally {
      ui.busy.delete(key);
      ui.v++;
      render();
    }
  }
  const busy = (key) => ui.busy.has(key);
  // A spinner and a line for a call that takes a while (pairing / owner sign-in through SSH).
  const progress = (kind) => h('span', { class: 'progress', role: 'status', 'aria-live': 'polite' },
    h('span', { class: 'spinner', 'aria-hidden': 'true' }), h('span', { text: progressText(state.hub, kind) }));

  function toast(level, text) {
    ui.toast = { level, text };
    const mine = ui.toast;
    ui.v++;
    render();
    setTimeout(() => {
      if (ui.toast === mine) {
        ui.toast = null;
        ui.v++;
        render();
      }
    }, TOAST_MS);
  }

  async function select(id) {
    if (!id) return;
    state = { ...state, view: id };
    ui.overlayFor = null;
    render();
    try {
      await sy.selectView({ view: id });
    } catch (e) {
      toast('error', errText(e));
    }
  }

  // ---- rendering

  function paint(name, sig, host, build) {
    const key = JSON.stringify(sig);
    if (last[name] === key) return;
    last[name] = key;
    const a = document.activeElement;
    const fk = a && a.dataset ? a.dataset.fk : null;
    const sel = a && typeof a.selectionStart === 'number' ? [a.selectionStart, a.selectionEnd] : null;
    const top = host === content ? content.scrollTop : 0;
    const inner = build();
    if (host === bar) host.replaceChildren(...inner.filter(Boolean));
    else host.replaceChildren(inner);
    if (host === content) content.scrollTop = top;
    if (fk && a && !a.isConnected) {
      const n = root.querySelector(`[data-fk="${CSS.escape(fk)}"]`);
      if (n) {
        n.focus({ preventScroll: true });
        if (sel && n.setSelectionRange) {
          try { n.setSelectionRange(sel[0], sel[1]); } catch { /* not a text input */ }
        }
      }
    }
  }

  function render() {
    if (!ready) return;
    const v = view();
    const minute = Math.floor(Date.now() / TICK_MS);
    const barBefore = last.bar;
    paint('bar', [state.tabs.map((t) => [t.id, t.name, t.label, t.colour]), state.hub.status, state.hub.needs_you, state.hub.owner, state.hub.owner_until, state.hub.removed, state.hub.board_blocked, v, ui.toast, state.view], bar, buildBar);
    if (last.bar !== barBefore) fitBar();
    const t = tabById(v);
    paint('strip', [t, minute, ui.v], stripHost, () => (t ? buildStrip(t) : h('div', { hidden: true })));
    paint('content', [v, state, ui.v, ui.folders, ui.foldersError, ui.section, minute], content, () => buildContent(v));
    maybeWarn();
  }

  // ---- top bar

  function buildBar() {
    const v = view();
    const order = viewOrder(state);
    const selectedId = order.includes(v) ? v : null;
    const need = badgeText(state.hub.needs_you);

    const hubTab = h('button', {
      class: 'tab', role: 'tab', id: 'tab-hub', fk: 'tab-hub', 'aria-selected': String(v === 'hub'), tabindex: v === 'hub' || !selectedId ? '0' : '-1',
      'aria-controls': 'content', title: 'Hub (Ctrl+0)', onclick: () => select('hub'),
    }, h('span', { text: 'Hub' }), need ? h('span', { class: 'count', text: need, 'aria-label': `${state.hub.needs_you} need you` }) : null);

    const tabEls = state.tabs.map((t, i) => {
      const sel = v === t.id;
      return h('button', {
        class: 'tab', role: 'tab', id: `tab-${t.id}`, fk: `tab-${t.id}`, 'aria-selected': String(sel), tabindex: sel ? '0' : '-1',
        'aria-controls': 'content', title: `${tabTitle(t)}${i < 9 ? ` (Ctrl+${i + 1})` : ''}`, 'data-id': t.id,
        onclick: () => select(t.id),
        draggable: 'true',
        ondragstart: (e) => { drag.id = t.id; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', t.id); },
        ondragover: (e) => dragOver(e, t.id),
        ondragleave: (e) => e.currentTarget.classList.remove('drop-before', 'drop-after'),
        ondrop: (e) => dropOn(e, t.id),
        ondragend: () => { drag.id = null; clearDropMarks(); },
      }, h('span', { class: 'dot', style: { background: tabColour(t, i) } }), h('span', { class: 'tab-name', text: tabTitle(t) }));
    });

    const list = h('div', {
      class: 'tabs', role: 'tablist', 'aria-label': 'Hub and account tabs', 'aria-orientation': 'horizontal',
      onkeydown: onTabKeys,
    }, hubTab, tabEls);

    const hc = hubChip(state.hub);
    const chip = h('button', { class: `hub-chip ${hc.level}`, fk: 'hub-chip', 'aria-label': hc.text, title: `${hc.text}. Open the Hub tab`, onclick: () => select('hub') },
      h('span', { class: 'chip-dot', 'aria-hidden': 'true' }), h('span', { class: 'chip-full', text: hc.text }), h('span', { class: 'chip-short', 'aria-hidden': 'true', text: 'Hub' }));
    const more = h('button', {
      class: 'icon-btn more-btn', fk: 'more-tabs', hidden: true, 'aria-haspopup': 'dialog', title: 'Tabs that do not fit', onclick: () => openMoreTabs(),
    }, h('span', { class: 'more-label' }), h('span', { class: 'chev', text: '\u25BE', 'aria-hidden': 'true' }));
    const pill = ownerPill(state.hub);
    const pillEl = pill ? h('span', { class: `owner-pill ${pill.owner ? 'owner' : ''}`.trim(), fk: 'owner-pill', text: pill.text, title: pill.owner ? 'Signed in as owner on this computer' : 'This computer can view, not change' }) : null;
    const tt = ui.toast ? h('div', { class: `toast ${ui.toast.level === 'error' ? 'error' : ui.toast.level === 'warn' ? 'warn' : ''}`.trim(), role: 'status', 'aria-live': 'polite', text: ui.toast.text, title: ui.toast.text }) : null;

    return [
      h('div', { class: 'brand' }, logo(), h('span', { class: 'brand-name', text: 'Switchyard' })),
      list,
      more,
      h('button', { class: 'icon-btn', fk: 'add-tab', 'aria-label': 'Add account tab', title: 'Add account tab', onclick: () => openAddTab() }, h('span', { text: '+', 'aria-hidden': 'true' })),
      h('span', { class: 'spacer' }),
      tt,
      chip,
      pillEl,
      h('button', {
        class: 'icon-btn settings-btn', fk: 'settings', title: 'App settings (Ctrl+,)', 'aria-label': 'App settings', 'aria-current': v === 'settings' ? 'page' : null,
        onclick: () => select('settings'),
      }, h('span', { class: 'gear', text: '\u2699', 'aria-hidden': 'true' }), h('span', { class: 'settings-label', text: 'App settings' })),
    ];
  }

  // Tabs that do not fit leave the strip and wait in the "N more" menu; the shown tab stays.
  function fitBar() {
    const tabsEl = bar.querySelector('.tabs');
    const more = bar.querySelector('.more-btn');
    if (!tabsEl || !more) return;
    const els = [...tabsEl.children];
    for (const el of els) el.hidden = false;
    more.hidden = true;
    const available = tabsEl.clientWidth;
    if (!available || tabsEl.scrollWidth <= available) return;
    more.querySelector('.more-label').textContent = `${els.length} more`;
    more.hidden = false;
    const moreWidth = more.offsetWidth + 4;
    const sel = state.tabs.findIndex((t) => t.id === view()) + 1;
    const keep = fitTabs(els.map((el) => el.offsetWidth), available, moreWidth, sel > 0 ? sel : -1);
    const n = keep.filter((k) => !k).length;
    els.forEach((el, i) => { el.hidden = !keep[i]; });
    more.hidden = n === 0;
    more.querySelector('.more-label').textContent = `${n} more`;
    more.setAttribute('aria-label', `${n} more ${n === 1 ? 'tab' : 'tabs'}`);
  }
  if (typeof ResizeObserver === 'function') new ResizeObserver(() => fitBar()).observe(bar);

  function openMoreTabs() {
    const hidden = [...bar.querySelectorAll('.tabs [role="tab"][hidden]')].map((el) => el.dataset.id).filter(Boolean);
    const list = h('div', { class: 'more-list', role: 'menu', 'aria-label': 'Tabs that do not fit' });
    state.tabs.forEach((t, i) => {
      if (!hidden.includes(t.id)) return;
      list.append(h('button', {
        class: 'btn more-item', role: 'menuitem', fk: `more-${t.id}`, onclick: () => { ui.dialog.close('selected'); select(t.id); },
      }, h('span', { class: 'dot', style: { background: tabColour(t, i) } }), h('span', { class: 'tab-name', text: tabTitle(t) })));
    });
    openDialog({
      title: 'More tabs', body: list,
      actions: (d) => [h('button', { class: 'btn btn-lg', text: 'Close', onclick: () => d.close('cancel') })],
      initialFocus: '.more-item',
    });
  }

  function onTabKeys(e) {
    const ids = viewOrder(state);
    const cur = e.target.closest('[role="tab"]');
    if (!cur) return;
    const id = cur.id === 'tab-hub' ? 'hub' : cur.dataset.id;
    if (e.altKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight') && id !== 'hub') {
      e.preventDefault();
      moveTab(id, e.key === 'ArrowLeft' ? -1 : 1);
      return;
    }
    let target = null;
    if (e.key === 'ArrowRight') target = cycleView(ids, id, 1);
    else if (e.key === 'ArrowLeft') target = cycleView(ids, id, -1);
    else if (e.key === 'Home') target = ids[0];
    else if (e.key === 'End') target = ids[ids.length - 1];
    if (!target) return;
    e.preventDefault();
    select(target).then(() => {
      const n = root.querySelector(`[data-fk="tab-${target}"]`);
      if (n) n.focus();
    });
  }

  // Drag to reorder: only account tabs are draggable and drop targets, so the Hub stays first and
  // "+" last. The order goes through the same tab:reorder as the keyboard and Settings moves.
  const drag = { id: null };
  const clearDropMarks = () => root.querySelectorAll('.tab.drop-before, .tab.drop-after').forEach((n) => n.classList.remove('drop-before', 'drop-after'));
  const dropAfter = (e) => {
    const r = e.currentTarget.getBoundingClientRect();
    return e.clientX > r.left + r.width / 2;
  };
  function dragOver(e, overId) {
    if (!drag.id || drag.id === overId) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    clearDropMarks();
    e.currentTarget.classList.add(dropAfter(e) ? 'drop-after' : 'drop-before');
  }
  async function dropOn(e, overId) {
    e.preventDefault();
    const id = drag.id;
    const after = dropAfter(e);
    drag.id = null;
    clearDropMarks();
    if (!id) return;
    const next = droppedIds(state.tabs.map((t) => t.id), id, overId, after);
    if (!next) return;
    await act('reorder', () => sy.reorderTabs({ ids: next }));
  }

  async function moveTab(id, delta) {
    const next = movedIds(state.tabs.map((t) => t.id), id, delta);
    if (!next) return;
    await act('reorder', () => sy.reorderTabs({ ids: next }));
    const n = root.querySelector(`[data-fk="${CSS.escape(`tab-${id}`)}"]`) || root.querySelector(`[data-fk="${CSS.escape(`move-${id}`)}"]`);
    if (n) n.focus();
  }

  function openSettings(section) {
    if (section) ui.section = section;
    ui.v++;
    select('settings').then(() => {
      const el = content.querySelector(`#sec-${section}`);
      if (el) el.scrollIntoView({ block: 'start' });
    });
  }

  // ---- strip

  function buildStrip(t) {
    const idx = state.tabs.findIndex((x) => x.id === t.id);
    const colour = tabColour(t, idx);
    const st = readingStatus(t);
    const rows = windowRows(t);
    const on = t.reading === 'on' && !t.needs_warning;
    const wins = rows.length
      ? rows.map((r) => h('span', { class: `win${r.primary ? ' win-primary' : ' win-extra'}` },
        h('span', { class: 'win-label', text: r.label }),
        h('span', { class: 'meter', role: 'img', 'aria-label': `${r.label} ${pct(r.used)} used` },
          h('span', { class: `meter-fill${r.used !== null && r.used >= 90 ? ' hot' : ''}`, style: { width: `${barWidth(r.used)}%`, '--tab-colour': colour } })),
        h('span', { class: 'win-num', text: r.reset ? `${pct(r.used)} · ${r.reset}` : pct(r.used) })))
      : [h('span', { class: 'status', text: t.signed_in ? 'No usage numbers yet' : 'Not signed in' })];

    const iconBtn = (key, ico, label, fn, extra = {}) => h('button', {
      class: 'btn', fk: `strip-${key}`, 'aria-label': label, title: label, onclick: fn, ...extra,
    }, h('span', { class: 'ico', 'aria-hidden': 'true', text: ico }), h('span', { class: 'lbl', text: label }));

    return h('section', { class: 'strip', 'aria-label': `Usage for ${tabTitle(t)}` },
      h('span', { class: 'strip-id', title: t.name },
        h('span', { class: 'dot', style: { background: colour } }), h('span', { text: tabTitle(t) })),
      h('span', { class: 'strip-wins' }, wins, t.windows_source === 'local' && rows.length ? h('span', { class: 'tag', text: 'local', title: 'Numbers read in this tab; the hub has none for this account yet' }) : null),
      h('span', { class: `status ${st.kind === 'stopped' ? 'stopped' : st.kind === 'retrying' ? 'retrying' : ''}`.trim(), role: 'status', 'aria-live': 'polite', title: lastReadTitle(t), text: st.text }),
      st.action === 'resume' ? h('button', { class: 'btn btn-primary', fk: 'strip-resume', text: 'Resume', disabled: busy(`resume:${t.id}`), onclick: () => act(`resume:${t.id}`, () => sy.resumeReading({ id: t.id })) }) : null,
      st.action === 'review' ? h('button', { class: 'btn btn-primary', fk: 'strip-review', text: st.actionLabel, onclick: () => openWarning(t, true) }) : null,
      h('span', { class: 'strip-actions' },
        h('button', {
          class: 'switch', role: 'switch', fk: 'strip-reading', 'aria-checked': String(on), 'aria-label': `Read usage for ${tabTitle(t)}`, title: 'Read usage for this account',
          onclick: () => toggleReading(t, !on),
        }, h('span', { class: 'track', 'aria-hidden': 'true' }), h('span', { class: 'lbl', text: 'Read usage' })),
        iconBtn('refresh', '↻', 'Refresh', () => act(`refresh:${t.id}`, () => sy.refreshUsage({ id: t.id })), { disabled: !on || busy(`refresh:${t.id}`) }),
        iconBtn('reload', '⟳', 'Reload tab', () => act(`reload:${t.id}`, () => sy.reloadTab({ id: t.id }))),
        iconBtn('home', '⌂', 'Home', () => act(`home:${t.id}`, () => sy.homeTab({ id: t.id }))),
        iconBtn('external', '↗', 'Open in browser', () => act(`ext:${t.id}`, () => sy.openTabExternal({ id: t.id }))),
      ));
  }

  function toggleReading(t, on) {
    if (on && (t.needs_warning || t.reading === 'never_asked')) {
      openWarning(t, true);
      return;
    }
    act(`reading:${t.id}`, () => sy.setReading({ id: t.id, on }));
  }

  // ---- content

  function buildContent(v) {
    if (v === 'settings') return settingsPage();
    if (v === 'hub') return hubPanel();
    // An account tab: the native view covers this area. The slot is only a placeholder for
    // the plain-browser dev page and for the moment before the view is attached.
    return h('div', { class: 'view-slot', role: 'tabpanel', 'aria-labelledby': `tab-${v}`, 'data-slot': 'tab' });
  }

  function hubPanel() {
    const hub = state.hub;
    const hb = hubBanner(hub);
    if (!hb || (hub.status === 'ok' && !hub.board_blocked)) {
      // The board covers this panel (status ok). Read-only: a strip above it with one visible button.
      const strip = hub.status === 'ok' && !hub.owner
        ? h('div', { class: 'hub-strip', role: 'status', fk: 'hub-strip' },
          busy('owner') ? progress('owner') : h('span', { text: 'Read-only. Sign in as owner to change alerts, devices and accounts.' }),
          h('button', { class: 'btn btn-primary', fk: 'owner-sign-in', text: busy('owner') ? 'Signing in…' : 'Sign in as owner', disabled: busy('owner'), onclick: ownerSignInClick }))
        : null;
      const err = hb ? bannerEl(hb, 'hub') : null;
      return h('div', { class: 'hub-panel ok', role: 'tabpanel', 'aria-labelledby': 'tab-hub' }, strip,
        h('div', { class: 'inner' }, err, h('p', { class: 'hint', text: hub.url ? `Loading the hub board from ${hub.url}…` : 'Loading the hub board…' })));
    }
    const inner = h('div', { class: 'inner' }, h('h1', { text: 'Hub' }), bannerEl(hb, 'hub'));
    for (const b of state.banners || []) inner.append(h('div', { class: `banner ${b.level === 'warn' ? 'warn' : b.level === 'error' ? 'error' : ''}`.trim(), role: 'status', text: b.text }));
    if (hub.direct && hub.status === 'needs_pairing') {
      inner.append(h('p', { class: 'hint' }, `Hub: ${hub.url}. `, h('button', { class: 'linkish', fk: 'hub-other', text: 'Use another hub', onclick: () => act('unpair', () => sy.hubUnpair({ forget: true })) })));
    }
    inner.append(h('p', { class: 'hint' }, 'Account tabs work without a hub. ', h('button', { class: 'linkish', fk: 'hub-open-settings', text: 'Open App settings', onclick: () => openSettings('computer') })));
    return h('div', { class: 'hub-panel', role: 'tabpanel', 'aria-labelledby': 'tab-hub' }, inner);
  }

  function bannerEl(hb, where) {
    const el = h('div', { class: `banner ${hb.level === 'warn' ? 'warn' : hb.level === 'error' ? 'error' : ''}`.trim(), role: 'status' },
      hb.title ? h('div', { class: 'banner-title', text: hb.title }) : null, h('div', { text: hb.text }));
    const actions = h('div', { class: 'banner-actions' });
    if (hb.command) actions.append(cmdRow(hb.command, `cmd-${where}-${hb.command}`));
    if (hb.action === 'connect') actions.append(connectForm(where));
    if (hb.action === 'pair' || (hb.action === 'pair-click' && ui.useCode)) actions.append(pairForm(where));
    if (hb.action === 'pair-click' && ui.useCode) actions.append(h('button', { class: 'linkish', fk: `pair-no-code-${where}`, text: 'Pair without a code', onclick: () => { ui.useCode = false; ui.pairError = ''; ui.v++; render(); } }));
    if (hb.action === 'pair-click') {
      if (!ui.useCode) {
        actions.append(h('button', { class: 'btn btn-primary', fk: `pair-click-${where}`, text: busy('pair') ? 'Pairing…' : 'Pair this computer', disabled: busy('pair'), onclick: () => doPair('') }));
        if (busy('pair')) actions.append(progress('pair'));
        else actions.append(h('button', { class: 'linkish', fk: `pair-use-code-${where}`, text: 'Use a code instead', onclick: () => { ui.useCode = true; ui.pairError = ''; ui.v++; render(); } }));
      }
      if (ui.pairError && ui.useCode === false) actions.append(h('span', { class: 'err', text: ui.pairError }));
    }
    if (hb.action === 'retry' || hb.action === 'retry-service') {
      actions.append(h('button', { class: 'btn btn-primary', fk: `retry-${where}`, text: 'Try again', disabled: busy('retry'), onclick: () => act('retry', () => sy.hubRetry()) }));
      if (hb.action === 'retry-service' && state.hub.service && state.hub.service !== 'missing' && state.hub.service !== 'running') {
        actions.append(h('button', { class: 'btn', fk: `start-hub-${where}`, text: 'Start hub', disabled: busy('service'), onclick: () => act('service', () => sy.hubService({ action: 'start' })) }));
      }
    }
    if (actions.childNodes.length) el.append(actions);
    return el;
  }

  function connectForm(where) {
    return h('div', { class: 'connect-form' },
      h('label', { for: `connect-url-${where}`, text: 'Hub address' }),
      h('input', {
        type: 'text', id: `connect-url-${where}`, fk: `connect-url-${where}`, placeholder: 'https://hub.example.com', autocomplete: 'off', spellcheck: 'false',
        value: ui.connectUrl, oninput: (e) => { ui.connectUrl = e.target.value; }, onkeydown: (e) => { if (e.key === 'Enter') doConnect(); },
      }),
      h('label', { for: `connect-code-${where}`, text: 'Pairing code' }),
      h('input', {
        type: 'text', id: `connect-code-${where}`, fk: `connect-code-${where}`, placeholder: '123-456', inputmode: 'numeric', autocomplete: 'off', maxlength: '7',
        value: ui.connectCode, oninput: (e) => { ui.connectCode = e.target.value; }, onkeydown: (e) => { if (e.key === 'Enter') doConnect(); },
      }),
      h('div', { class: 'hint small', text: 'Get a code on any owner device: More, Devices, Add desktop app.' }),
      h('div', { class: 'row-actions' },
        h('button', { class: 'btn btn-primary', fk: `connect-btn-${where}`, text: 'Connect', disabled: busy('connect'), onclick: () => doConnect() }),
        ui.connectError ? h('span', { class: 'err', role: 'alert', text: ui.connectError }) : null));
  }

  async function doConnect() {
    const url = ui.connectUrl.trim();
    const code = ui.connectCode.trim();
    if (!url) return connectFail('Enter the hub address, for example https://hub.example.com.');
    if (code && !PAIR_CODE_RE.test(code)) return connectFail('The code is 6 digits, like 123-456.');
    if (!code) return connectFail('Type the pairing code. Get one on any owner device: More, Devices, Add desktop app.');
    ui.connectError = '';
    const r = await act('connect', () => sy.hubConnect({ url, code }));
    if (r && r.ok) {
      ui.connectCode = '';
      toast('info', 'Paired with the hub.');
    } else if (r) connectFail(r.error || 'Could not connect.');
    return undefined;
  }

  function connectFail(text) {
    ui.connectError = text;
    ui.v++;
    render();
  }

  function pairForm(where) {
    return h('div', { class: 'cmd' },
      h('label', { class: 'sr-only', for: `pair-code-${where}`, text: 'Pairing code' }),
      h('input', {
        type: 'text', id: `pair-code-${where}`, fk: `pair-code-${where}`, placeholder: '123-456', inputmode: 'numeric', autocomplete: 'off', maxlength: '7',
        value: ui.pairCode, 'aria-label': 'Pairing code', oninput: (e) => { ui.pairCode = e.target.value; }, onkeydown: (e) => { if (e.key === 'Enter') doPair(ui.pairCode); },
      }),
      h('button', { class: 'btn btn-primary', fk: `pair-btn-${where}`, text: 'Pair', disabled: busy('pair'), onclick: () => doPair(ui.pairCode) }),
      busy('pair') ? progress('pair') : null,
      ui.pairError ? h('span', { class: 'err', role: 'alert', text: ui.pairError }) : null,
    );
  }

  async function doPair(code) {
    const c = String(code || '').trim();
    if (c && !PAIR_CODE_RE.test(c)) {
      ui.pairError = 'The code is 6 digits, like 123-456.';
      ui.v++;
      render();
      return;
    }
    if (!c && !(state.hub.admin || state.hub.local)) {
      ui.pairError = 'Type the pairing code from the board.';
      ui.v++;
      render();
      return;
    }
    ui.pairError = '';
    const r = await act('pair', () => sy.pair(c ? { code: c } : {}));
    if (r && r.ok) {
      ui.pairCode = '';
      ui.useCode = false;
      toast('info', 'Paired with the hub.');
    } else if (r) {
      ui.pairError = r.error || 'Pairing failed.';
      ui.v++;
      render();
    }
  }

  function serviceControls() {
    const hub = state.hub;
    if (!hub.local) return null;
    const running = hub.service === 'running';
    return h('div', { class: 'row-actions' },
      h('span', { class: 'muted', text: `Hub service: ${hub.service || 'unknown'}` }),
      hub.service === 'missing'
        ? h('span', { class: 'muted' }, 'Not installed. Run ', h('code', { text: 'sy setup --hub here' }))
        : h('button', {
          class: 'btn', fk: 'svc-toggle', text: running ? 'Stop' : 'Start', disabled: busy('service'),
          onclick: () => act('service', () => sy.hubService({ action: running ? 'stop' : 'start' })),
        }));
  }

  function cmdRow(text, fk) {
    const input = h('input', { type: 'text', readOnly: true, value: text, fk: `${fk}-in`, 'aria-label': `Command: ${text}`, onfocus: (e) => e.target.select() });
    return h('div', { class: 'cmd' }, input, h('button', {
      class: 'btn', fk: `${fk}-copy`, text: 'Copy', 'aria-label': `Copy ${text}`,
      onclick: async () => {
        let ok = false;
        try { await navigator.clipboard.writeText(text); ok = true; } catch { /* the shell has no clipboard permission */ }
        if (!ok) {
          input.focus();
          input.select();
          try { ok = document.execCommand('copy'); } catch { ok = false; }
        }
        toast('info', ok ? 'Copied.' : 'Selected: press Ctrl+C to copy.');
      },
    }));
  }

  // ---- settings

  const SECTIONS = [
    ['computer', 'This computer'],
    ['notify', 'Notifications'],
    ['tabs', 'Account tabs'],
    ['folders', 'Claude Code folders'],
    ['login', 'Start at login'],
    ['about', 'About'],
  ];

  function settingsPage() {
    if (ui.folders === null && !ui.foldersLoading && !ui.foldersError) setTimeout(loadFolders, 0);
    const nav = h('nav', { class: 'page-nav', 'aria-label': 'App settings sections' }, h('div', { class: 'nav-title', text: 'App settings' }),
      SECTIONS.map(([id, label]) => h('button', {
        fk: `nav-${id}`, text: label, 'aria-current': String(ui.section === id),
        onclick: () => {
          ui.section = id;
          ui.v++;
          render();
          const el = content.querySelector(`#sec-${id}`);
          if (el) el.scrollIntoView({ block: 'start' });
        },
      })));
    const banners = (state.banners || []).map((b) => h('div', { class: `banner ${b.level === 'warn' ? 'warn' : b.level === 'error' ? 'error' : ''}`.trim(), role: 'status', text: b.text }));
    const main = h('div', { class: 'page-main' },
      h('h1', { text: 'App settings' }),
      banners.length ? h('div', { class: 'banners' }, banners) : null,
      sectionComputer(), sectionNotify(), sectionTabs(), sectionFolders(), sectionLogin(), sectionAbout());
    return h('div', { class: 'page' }, nav, main);
  }

  const section = (id, title, intro, ...body) => h('section', { class: 'section', id: `sec-${id}`, 'aria-labelledby': `h-${id}` }, h('h2', { id: `h-${id}`, text: title }), intro ? h('p', { text: intro }) : null, ...body);

  function sw(key, label, checked, onchange, { disabled = false, hint = null } = {}) {
    return h('span', {}, h('button', {
      class: 'switch', role: 'switch', fk: key, 'aria-checked': String(!!checked), disabled: disabled || busy(key), onclick: () => onchange(!checked),
    }, h('span', { class: 'track', 'aria-hidden': 'true' }), h('span', { text: label })), hint ? h('div', { class: 'hint small', text: hint }) : null);
  }

  // -- account tabs

  function sectionTabs() {
    const rows = state.tabs.map((t, i) => tabRow(t, i));
    return section('tabs', 'Account tabs', 'One tab per claude.ai account, each with its own signed-in session.',
      h('div', { class: 'card' }, rows.length ? rows : h('div', { class: 'card-pad' }, h('p', { class: 'hint', text: 'No account tabs yet.' }))),
      h('div', { class: 'row-actions' }, h('button', { class: 'btn btn-lg', fk: 'settings-add-tab', text: 'Add account tab', onclick: () => openAddTab() })));
  }

  function tabRow(t, i) {
    const st = readingStatus(t);
    const owner = !!state.hub.owner;
    const canSeat = owner && !!t.seat;
    const dKey = (f) => `${f}:${t.id}`;
    const draft = (f, dflt) => (Object.hasOwn(ui.drafts, dKey(f)) ? ui.drafts[dKey(f)] : dflt);
    const err = ui.errors[t.id] || '';
    const ids = state.tabs.map((x) => x.id);
    const colour = tabColour(t, i);
    const colourVal = COLOUR_RE.test(draft('colour', t.colour || '')) ? draft('colour', t.colour) : (COLOUR_RE.test(t.colour || '') ? t.colour : '#6a4fd6');

    return h('div', { class: 'tabrow', role: 'group', 'aria-label': `Account tab ${tabTitle(t)}` },
      h('div', { class: 'tabrow-head' },
        h('span', { class: 'dot', style: { background: colour } }),
        h('span', { class: 'name', text: tabTitle(t) }),
        t.label ? h('span', { class: 'muted', text: `tab name: ${t.name}` }) : null,
        h('span', { class: 'spacer' }),
        h('span', { class: 'muted', text: st.text }),
        h('button', { class: 'btn', fk: `move-${t.id}`, text: '↑', 'aria-label': `Move ${tabTitle(t)} earlier`, title: 'Move earlier', disabled: ids.indexOf(t.id) === 0 || busy('reorder'), onclick: () => moveTab(t.id, -1) }),
        h('button', { class: 'btn', fk: `moveb-${t.id}`, text: '↓', 'aria-label': `Move ${tabTitle(t)} later`, title: 'Move later', disabled: ids.indexOf(t.id) === ids.length - 1 || busy('reorder'), onclick: async () => { await moveTab(t.id, 1); } }),
        h('button', { class: 'btn btn-danger', fk: `remove-${t.id}`, text: 'Remove', 'aria-label': `Remove ${tabTitle(t)}`, onclick: () => openRemove(t) }),
      ),
      h('div', { class: 'fields' },
        field('Tab name', `name-${t.id}`, h('input', {
          type: 'text', id: `name-${t.id}`, fk: `name-${t.id}`, maxlength: '24', value: draft('name', t.name),
          oninput: (e) => { ui.drafts[dKey('name')] = e.target.value; },
          onkeydown: (e) => { if (e.key === 'Enter') renameTab(t); },
        })),
        h('button', { class: 'btn', fk: `rename-${t.id}`, text: 'Rename', disabled: busy(`rename:${t.id}`), onclick: () => renameTab(t) }),
        field('Seat label', `label-${t.id}`, h('input', {
          type: 'text', id: `label-${t.id}`, fk: `label-${t.id}`, maxlength: '24', disabled: !canSeat, placeholder: 'e.g. work', value: draft('label', t.label || ''),
          oninput: (e) => { ui.drafts[dKey('label')] = e.target.value; },
          onkeydown: (e) => { if (e.key === 'Enter') saveSeat(t); },
        })),
        field('Colour', `colour-${t.id}`, h('input', {
          type: 'color', id: `colour-${t.id}`, fk: `colour-${t.id}`, disabled: !canSeat, value: colourVal,
          oninput: (e) => { ui.drafts[dKey('colour')] = e.target.value.toLowerCase(); },
        })),
        h('button', { class: 'btn', fk: `seat-save-${t.id}`, text: 'Save label and colour', disabled: !canSeat || busy(`seat:${t.id}`), onclick: () => saveSeat(t) }),
        h('button', { class: 'btn', fk: `seat-clear-${t.id}`, text: 'Clear', title: 'Clear the label and colour set from the app', disabled: !canSeat || busy(`seat:${t.id}`), onclick: () => clearSeat(t) }),
      ),
      !owner ? h('div', { class: 'hint small', text: 'Sign in as owner (Hub section) to edit the seat label and colour.' })
        : !t.seat ? h('div', { class: 'hint small', text: 'This tab has no hub seat yet: it appears after its first usage read reaches the hub.' }) : null,
      err ? h('div', { class: 'err', role: 'alert', text: err }) : null,
      h('div', { class: 'switches' },
        sw(`reading-${t.id}`, 'Read usage', t.reading === 'on' && !t.needs_warning, (on) => toggleReading(t, on)),
        sw(`send-${t.id}`, 'Send to hub', t.send_to_hub, (on) => act(`send-${t.id}`, () => sy.setSend({ id: t.id, on }))),
        h('span', { class: 'muted' }, 'Warning: ',
          t.needs_warning || t.reading === 'never_asked' ? 'not accepted yet. ' : t.reading === 'off' ? 'reading is off. ' : 'accepted. ',
          h('button', { class: 'linkish', fk: `warn-${t.id}`, text: 'Review', onclick: () => openWarning(t, true) })),
        st.action === 'resume' ? h('button', { class: 'btn', fk: `resume-${t.id}`, text: 'Resume reading', onclick: () => act(`resume:${t.id}`, () => sy.resumeReading({ id: t.id })) }) : null,
      ),
    );
  }

  const field = (label, id, input) => h('div', { class: 'field' }, h('label', { for: id, text: label }), input);

  async function renameTab(t) {
    const name = String(Object.hasOwn(ui.drafts, `name:${t.id}`) ? ui.drafts[`name:${t.id}`] : t.name).trim();
    const e = nameError(name);
    ui.errors[t.id] = e;
    if (e) { ui.v++; render(); return; }
    if (name === t.name) { delete ui.drafts[`name:${t.id}`]; ui.v++; render(); return; }
    await act(`rename:${t.id}`, async () => {
      await sy.updateTab({ id: t.id, name });
      delete ui.drafts[`name:${t.id}`];
    });
  }

  async function saveSeat(t) {
    const payload = { seat: t.seat };
    if (Object.hasOwn(ui.drafts, `label:${t.id}`)) {
      const label = String(ui.drafts[`label:${t.id}`]);
      const e = labelError(label);
      if (e) { ui.errors[t.id] = e; ui.v++; render(); return; }
      payload.label = label === '' ? null : label;
    }
    if (Object.hasOwn(ui.drafts, `colour:${t.id}`)) {
      const c = String(ui.drafts[`colour:${t.id}`]);
      payload.colour = COLOUR_RE.test(c) ? c : null;
    }
    if (Object.keys(payload).length === 1) { ui.errors[t.id] = 'Nothing to save.'; ui.v++; render(); return; }
    ui.errors[t.id] = '';
    const r = await act(`seat:${t.id}`, () => sy.updateSeat(payload));
    if (r && r.ok) {
      delete ui.drafts[`label:${t.id}`];
      delete ui.drafts[`colour:${t.id}`];
      toast('info', 'Saved.');
    } else if (r) {
      ui.errors[t.id] = r.error || 'The hub refused the change.';
      ui.v++;
      render();
    }
  }

  async function clearSeat(t) {
    ui.errors[t.id] = '';
    const r = await act(`seat:${t.id}`, () => sy.updateSeat({ seat: t.seat, label: null, colour: null }));
    if (r && r.ok) {
      delete ui.drafts[`label:${t.id}`];
      delete ui.drafts[`colour:${t.id}`];
    } else if (r) {
      ui.errors[t.id] = r.error || 'The hub refused the change.';
      ui.v++;
      render();
    }
  }

  // -- Claude Code folders

  async function loadFolders() {
    if (ui.foldersLoading) return;
    ui.foldersLoading = true;
    try {
      const r = await sy.localAccounts();
      if (r && r.ok === false) {
        ui.folders = [];
        ui.foldersError = r.error || 'sy could not list the folders.';
      } else {
        ui.folders = foldersOf(r);
        ui.foldersError = '';
      }
    } catch (e) {
      ui.folders = [];
      ui.foldersError = errText(e);
    }
    ui.foldersLoading = false;
    ui.v++;
    render();
  }

  function sectionFolders() {
    const folders = ui.folders || [];
    const body = [];
    if (ui.foldersError) body.push(h('div', { class: 'banner warn', role: 'status', text: ui.foldersError }));
    if (ui.folders === null) body.push(h('p', { class: 'hint', text: 'Loading…' }));
    else if (!folders.length && !ui.foldersError) body.push(h('p', { class: 'hint', text: 'No Claude Code folders found. Run "sy setup" to add them.' }));
    if (folders.length) {
      const head = h('tr', {}, h('th', { scope: 'col', text: 'Part' }), folders.map((f) => h('th', { scope: 'col' },
        h('div', { text: `${f.label || ''} ${f.folder}`.trim() }),
        h('div', { class: 'hint small', text: [f.email, f.plan].filter(Boolean).join(' · ') || (f.logged_in === false ? 'not logged in' : '') }))));
      const rows = SHARE_SWITCHES.map((s) => h('tr', {}, h('td', { text: s.label }), folders.map((f) => {
        const st = switchState(f, s);
        if (st === 'source') return h('td', { class: 'muted', text: 'source' });
        if (st === 'elsewhere') return h('td', { class: 'muted', text: 'elsewhere', title: 'Linked to another folder; change it with sy account share' });
        if (st === 'unknown') return h('td', { class: 'muted', text: '–' });
        const key = `share:${f.folder}:${s.id}`;
        return h('td', {}, h('input', {
          type: 'checkbox', fk: key, checked: st === 'on', disabled: busy(key),
          'aria-label': `${f.label || f.folder} shares ${s.label}`,
          onchange: (e) => changeShare(f, s, e.target.checked, key),
        }));
      })));
      rows.push(h('tr', {}, h('td', { class: 'muted', text: 'Login, tokens, live sessions, usage counters' }), folders.map(() => h('td', { class: 'muted', text: 'own, never shared' }))));
      body.push(h('div', { class: 'card table-wrap' }, h('table', {}, h('thead', {}, head), h('tbody', {}, rows))));
      const needLogin = folders.filter((f) => f.logged_in === false && !isPrimary(f));
      for (const f of needLogin) {
        body.push(h('div', {}, h('div', { class: 'hint', text: `${f.label || f.folder} is not logged in. Log in from a terminal:` }), cmdRow(loginCommand(f), `login-${f.folder}`)));
      }
    }
    return section('folders', "This computer's Claude Code folders",
      'Shared parts point at the primary folder (~/.claude). Changes apply at once; open sessions see them after a restart.',
      ...body,
      h('div', { class: 'row-actions' }, h('button', { class: 'btn', fk: 'folders-refresh', text: 'Refresh', disabled: ui.foldersLoading, onclick: loadFolders })),
      h('div', {}, h('div', { class: 'hint', text: 'Add a Claude Code account, then log in. Both need a terminal:' }), cmdRow('sy account add', 'cmd-account-add')));
  }

  async function changeShare(f, s, on, key) {
    const spec = shareSpec(f, s.id, on);
    const r = await act(key, () => sy.shareAccount({ folder: f.folder, share: spec }));
    if (r && r.ok === false) toast('error', r.error || 'sy could not change the sharing.');
    await loadFolders();
  }

  // -- this computer

  function sectionComputer() {
    const hub = state.hub;
    const hb = hubBanner(hub);
    const paired = !!hub.paired;
    const pill = ownerPill(hub);
    const dl = h('dl', { class: 'kv' },
      h('dt', { text: 'Name' }), h('dd', { text: hub.machine || '–' }),
      h('dt', { text: 'Hub' }), h('dd', { text: hub.url ? `${hub.url} · ${hubSummary(hub)}` : 'none' }),
      h('dt', { text: 'Pairing' }), h('dd', { text: paired ? `paired${hub.direct ? '' : ' through sy'}` : hub.removed ? 'removed from the hub' : 'not paired' }),
      h('dt', { text: 'Access' }), h('dd', {},
        h('span', { text: pill ? pill.text : hub.owner ? 'Owner' : 'Read-only' }),
        hub.owner
          ? h('button', { class: 'btn', fk: 'owner-sign-out', text: 'Sign out of owner', disabled: busy('owner-out'), onclick: () => act('owner-out', async () => { await sy.ownerSignOut(); toast('info', 'Signed out of owner. This computer can view, not change.'); }) })
          : [h('button', { class: 'btn', fk: 'owner-sign-in-settings', text: busy('owner') ? 'Signing in…' : 'Sign in as owner', disabled: !hubUsable(hub) || busy('owner'), onclick: ownerSignInClick }), busy('owner') ? progress('owner') : null]),
      hub.local ? [h('dt', { text: 'Hub service' }), h('dd', {}, h('span', { text: hub.service || 'unknown' }), hub.service !== 'missing' ? serviceButtons() : null,
        hub.service === 'missing' ? h('span', { class: 'muted' }, 'Not installed: run ', h('code', { text: 'sy setup --hub here' })) : null)] : null,
      hub.admin ? [h('dt', { text: 'Admin channel' }), h('dd', { text: hub.admin })] : null,
    );
    const warn = state.settings.storage_backend === 'basic_text'
      ? h('div', { class: 'banner warn', role: 'status', text: 'This system has no secret store, so the hub token is kept in a file readable only by you (mode 0600), not encrypted.' }) : null;
    return section('computer', 'This computer', 'Connection, pairing and owner access for this app. The board itself is in the Hub tab; hub-wide settings are in its sidebar, under Hub settings.',
      hb && !hubUsable(hub) ? bannerEl(hb, 'settings') : null, warn, h('div', { class: 'card card-pad' }, dl),
      paired || hub.direct ? h('div', { class: 'row-actions' },
        paired ? h('button', { class: 'btn btn-danger', fk: 'unpair', text: 'Unpair this computer', disabled: busy('unpair'), onclick: openUnpair }) : null) : null);
  }

  async function ownerSignInClick() {
    if (state.hub.admin || state.hub.local) await ownerSignIn();
    else openOwnerLink();
  }

  function serviceButtons() {
    const running = state.hub.service === 'running';
    return h('button', {
      class: 'btn', fk: 'svc-toggle-settings', text: running ? 'Stop' : 'Start', disabled: busy('service'),
      onclick: () => act('service', () => sy.hubService({ action: running ? 'stop' : 'start' })),
    });
  }

  async function ownerSignIn() {
    const r = await act('owner', () => sy.ownerSignIn({}));
    if (r && r.ok === false) toast('error', r.error || 'Owner sign-in did not work.');
    else if (r) await select('hub');
  }

  // -- notifications

  function settingSwitch(key) {
    return (on) => act(`set-${key}`, async () => {
      const r = await sy.setSettings({ [key]: on });
      if (r && typeof r === 'object') state = { ...state, settings: { ...state.settings, ...r } };
    });
  }

  function sectionNotify() {
    const s = state.settings;
    return section('notify', 'Notifications', null,
      h('div', { class: 'card card-pad' },
        h('div', { class: 'switches' },
          sw('set-notifications', 'Notify me when a session needs me', s.notifications, settingSwitch('notifications'), { hint: 'From hub alerts only. The hub decides what and when, including quiet hours.' }),
          sw('set-keep_running', 'Keep running in the tray when the window is closed', s.keep_running, settingSwitch('keep_running')))));
  }

  // -- start at login

  function sectionLogin() {
    const s = state.settings;
    return section('login', 'Start at login', null,
      h('div', { class: 'card card-pad' },
        h('div', { class: 'switches' },
          sw('set-autostart', 'Start Switchyard when I log in', s.autostart, settingSwitch('autostart'), { hint: 'Off by default. A login start goes to the tray; any other start shows the window.' }))));
  }

  // -- about

  function sectionAbout() {
    const a = state.about;
    if (!a) return null;
    return section('about', 'About', null,
      h('div', { class: 'card card-pad' }, h('dl', { class: 'kv', fk: 'about' },
        h('dt', { text: 'Switchyard Desktop' }), h('dd', { text: a.app || '–' }),
        h('dt', { text: 'Hub' }), h('dd', { text: state.hub?.version || '–' }),
        h('dt', { text: 'Electron' }), h('dd', { text: a.electron || '–' }),
        h('dt', { text: 'Chromium' }), h('dd', { text: a.chrome || '–' }),
        h('dt', { text: 'Platform' }), h('dd', { text: a.platform || '–' }))));
  }

  // ---- dialogs

  function openDialog({ title, body, actions, onClose, overlay = true, initialFocus }) {
    if (ui.dialog) return null; // one at a time
    const dlg = h('dialog', { 'aria-labelledby': 'dlg-title' });
    const api = {
      el: dlg,
      close: (reason) => { dlg.dataset.reason = reason || ''; if (dlg.open) dlg.close(); else finish(); },
    };
    let finished = false;
    function finish() {
      if (finished) return;
      finished = true;
      dlg.remove();
      ui.dialog = null;
      const restore = ui.overlayFor && !['view-changed', 'selected'].includes(dlg.dataset.reason)
        ? (dlg.dataset.reason !== 'removed' && (tabById(ui.returnView) || ['hub', 'settings'].includes(ui.returnView)) ? ui.returnView : 'hub')
        : null;
      const wasOverlay = !!ui.overlayFor;
      ui.overlayFor = null;
      if (wasOverlay && restore) select(restore);
      else render();
      if (onClose) onClose(dlg.dataset.reason);
    }
    dlg.addEventListener('close', finish);
    dlg.addEventListener('cancel', () => { dlg.dataset.reason = 'esc'; });
    dlg.append(h('div', { class: 'dlg' }, h('h2', { id: 'dlg-title', text: title }), body, h('div', { class: 'dlg-actions' }, actions(api))));
    ui.dialog = api;
    if (overlay && state.view !== 'settings') {
      ui.overlayFor = state.view;
      ui.returnView = state.view;
      sy.selectView({ view: 'settings' }).catch(() => {});
      state = { ...state, view: 'settings' };
    }
    root.append(dlg);
    render();
    dlg.showModal();
    const f = initialFocus ? dlg.querySelector(initialFocus) : dlg.querySelector('input, button');
    if (f) f.focus();
    return api;
  }

  function openUnpair() {
    openDialog({
      title: 'Unpair this computer?',
      body: h('p', { text: "This computer is removed from the hub's Devices list and the board is hidden here. Account tabs keep working. Pair again with a code to see the board." }),
      actions: (d) => [
        h('button', { class: 'btn btn-lg', text: 'Cancel', onclick: () => d.close('cancel') }),
        h('button', {
          class: 'btn btn-danger btn-lg', fk: 'unpair-confirm', text: 'Unpair this computer',
          onclick: async () => {
            d.close('removed');
            const r = await act('unpair', () => sy.hubUnpair({}));
            if (r && r.warning) toast('warn', r.warning);
            else if (r && r.ok) toast('info', 'This computer is unpaired.');
            await select('hub');
          },
        }),
      ],
      initialFocus: '.btn',
    });
  }

  function openOwnerLink() {
    let input;
    let msg;
    const submit = async (d) => {
      const link = input.value.trim();
      if (!link) { msg.textContent = 'Paste the owner link first.'; input.focus(); return; }
      msg.textContent = '';
      const r = await act('owner', () => sy.ownerSignIn({ link }));
      if (r && r.ok) { d.close('selected'); ui.overlayFor = null; await select('hub'); } else if (r) msg.textContent = r.error || 'That link did not work.';
    };
    openDialog({
      title: 'Sign in as owner',
      body: h('div', { class: 'dlg', style: { padding: '0' } },
        h('label', { for: 'owner-link', text: 'Paste owner link' }),
        (input = h('input', { type: 'text', id: 'owner-link', autocomplete: 'off', spellcheck: 'false', placeholder: `${state.hub.url || 'https://hub.example.com'}/#/owner?s=…`, onkeydown: (e) => { if (e.key === 'Enter') submit(ui.dialog); } })),
        (msg = h('div', { class: 'err', role: 'alert' })),
        h('p', { class: 'fine', text: 'Get one on an owner device: More, Devices, Make another device owner. Or on the hub\u2019s computer: sy hub owner-link.' })),
      actions: (d) => [
        h('button', { class: 'btn btn-lg', text: 'Cancel', onclick: () => d.close('cancel') }),
        h('button', { class: 'btn btn-primary btn-lg', fk: 'owner-link-go', text: 'Sign in as owner', onclick: () => submit(d) }),
      ],
      initialFocus: '#owner-link',
    });
  }

  function openWarning(t, manual = false) {
    if (ui.dialog) return;
    const text = (s) => h('p', { text: s });
    openDialog({
      title: 'Read usage for this account?',
      body: h('div', { class: 'dlg', style: { padding: '0' } },
        text('Every 5 minutes, Switchyard runs a small script inside this tab that reads the same numbers claude.ai shows under Settings → Usage. It reads numbers only: no chats, no cookies. They go to your own hub.'),
        text('This uses an internal claude.ai endpoint, not a public API. It may stop working, and automated access may not be allowed by Anthropic’s consumer terms, so it could get this account flagged. Switchyard is unofficial and not affiliated with Anthropic.'),
        h('p', { class: 'fine', text: 'You can turn reading off for this account at any time in its usage bar or in Settings.' })),
      actions: (d) => [
        h('button', {
          class: 'btn btn-lg', fk: 'warn-not-now', text: 'Not now',
          onclick: async () => {
            d.close('not-now');
            await act(`reading:${t.id}`, () => sy.setReading({ id: t.id, on: false }));
          },
        }),
        h('button', {
          class: 'btn btn-primary btn-lg', fk: 'warn-accept', text: 'Read usage for this account',
          onclick: async () => {
            d.close('accepted');
            await act(`reading:${t.id}`, async () => {
              await sy.acceptWarning({ id: t.id, version: WARNING_VERSION });
              if (tabById(t.id)?.reading !== 'on') await sy.setReading({ id: t.id, on: true });
            });
          },
        }),
      ],
      onClose: (reason) => { if (reason === 'esc' || reason === 'view-changed' || manual === false) ui.dismissed.add(t.id); },
      initialFocus: '[data-fk="warn-accept"]',
    });
  }

  function maybeWarn() {
    if (ui.dialog || document.visibilityState === 'hidden') return;
    const t = tabById(view());
    if (t && t.needs_warning && !ui.dismissed.has(t.id)) openWarning(t, false);
  }

  function openAddTab() {
    let input;
    let msg;
    const submit = async (d) => {
      const name = input.value.trim();
      const e = nameError(name);
      if (e) { msg.textContent = e; input.focus(); return; }
      try {
        const r = await sy.addTab({ name });
        d.close('selected');
        ui.overlayFor = null;
        if (r && r.id) await select(r.id);
      } catch (err) {
        msg.textContent = errText(err);
      }
    };
    openDialog({
      title: 'Add account tab',
      body: h('div', { class: 'dlg', style: { padding: '0' } },
        h('label', { for: 'new-tab-name', text: 'Name for this account (for example work or personal)' }),
        (input = h('input', { type: 'text', id: 'new-tab-name', maxlength: '24', autocomplete: 'off', onkeydown: (e) => { if (e.key === 'Enter') submit(ui.dialog); } })),
        (msg = h('div', { class: 'err', role: 'alert' })),
        h('p', { class: 'fine', text: 'Sign in with email + code. Google sign-in is blocked in embedded browsers.' })),
      actions: (d) => [
        h('button', { class: 'btn btn-lg', text: 'Cancel', onclick: () => d.close('cancel') }),
        h('button', { class: 'btn btn-primary btn-lg', text: 'Add tab', onclick: () => submit(d) }),
      ],
      initialFocus: '#new-tab-name',
    });
  }

  function openRemove(t) {
    openDialog({
      title: `Remove ${tabTitle(t)}?`,
      body: h('p', { text: 'This signs the tab out and deletes its stored session on this computer. Usage already sent to the hub stays there. You can add the tab again and sign in again.' }),
      actions: (d) => [
        h('button', { class: 'btn btn-lg', text: 'Cancel', onclick: () => d.close('cancel') }),
        h('button', {
          class: 'btn btn-danger btn-lg', text: 'Remove tab',
          onclick: async () => {
            d.close('removed');
            await act(`remove:${t.id}`, () => sy.removeTab({ id: t.id }));
          },
        }),
      ],
      initialFocus: '.btn',
    });
  }

  // ---- keyboard

  document.addEventListener('keydown', (e) => {
    if (ui.dialog) return;
    const mod = e.ctrlKey || e.metaKey;
    if (!mod || e.altKey) return;
    if (e.key === ',') { e.preventDefault(); select('settings'); return; }
    if (e.key === 'Tab') {
      e.preventDefault();
      select(cycleView([...viewOrder(state), 'settings'], view(), e.shiftKey ? -1 : 1));
      return;
    }
    if (e.shiftKey || !/^[0-9]$/.test(e.key)) return;
    const id = viewForDigit(state, Number(e.key));
    if (id) { e.preventDefault(); select(id); }
  });
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') maybeWarn(); });

  // ---- go

  sy.on('state', apply);
  sy.on('toast', (t) => { if (t && t.text) toast(t.level || 'info', t.text); });
  setInterval(render, TICK_MS);
  render();
  sy.state().then(apply).catch((e) => toast('error', errText(e)));
  return { get state() { return state; } };
}

// The shell page starts itself (the dev page loads mock-sy.js first, which defines window.sy).
if (typeof document !== 'undefined' && typeof window !== 'undefined' && window.sy) {
  const mount = () => start({ sy: window.sy, root: document.getElementById('app') });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', mount);
  else mount();
}
