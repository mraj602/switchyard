'use strict';
// Validated categorical palette (dark surface), in slot order — new accounts take the next slot.
const COLORS = [['Blue', '#3987e5'], ['Orange', '#d95926'], ['Aqua', '#199e70'], ['Yellow', '#c98500'], ['Magenta', '#d55181'], ['Green', '#008300'], ['Violet', '#9085e9'], ['Red', '#e66767']];
const $ = (s) => document.querySelector(s);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

let S = { accounts: [], events: [], history: [], active: 'dashboard', hook: {} };
const seen = {}; // accountId -> ts of last visit (unread badge)

// ---------- formatting ----------
const tone = (p) => (p >= 90 ? 'var(--bad)' : p >= 70 ? 'var(--warn)' : 'var(--ok)');
function until(iso) {
  if (!iso) return '—';
  const ms = new Date(iso).getTime() - Date.now();
  if (ms <= 0) return 'now';
  const m = Math.round(ms / 60000);
  if (m < 60) return `${m}m`;
  if (m < 24 * 60) return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`;
  return new Date(iso).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit', hour12: false });
}
function ago(ts) {
  const s = Math.round((Date.now() - ts) / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  if (s < 86400) return `${Math.round(s / 3600)}h`;
  return `${Math.round(s / 86400)}d`;
}
const weekOf = (u) => (u.windows || []).find((w) => w.key === 'seven_day') || (u.windows || []).find((w) => /^7-day/.test(w.label));
const fiveOf = (u) => (u.windows || []).find((w) => w.key === 'five_hour');
const worst = (u) => Math.max(0, ...(u.windows || []).filter((w) => !w.inactive).map((w) => w.pct));

// The CSP (style-src 'self') blocks style="" attributes, so dynamic widths/colours go
// through data-* attributes and are applied via the CSSOM after each render.
function applyBars(root) {
  root.querySelectorAll('[data-w]').forEach((el) => { el.style.width = el.dataset.w + '%'; el.style.background = el.dataset.c; });
  root.querySelectorAll('[data-bg]').forEach((el) => { el.style.background = el.dataset.bg; });
}
const barH = (w) => `<div class="track"><div class="fill" data-w="${Math.min(100, w.pct)}" data-c="${tone(w.pct)}"></div></div>`;

function spark(id, color) {
  const pts = S.history.filter((h) => h.id === id && h.ts > Date.now() - 7 * 864e5);
  if (pts.length < 2) return '<span></span>';
  const t0 = Date.now() - 7 * 864e5;
  const xy = pts.map((h) => {
    const w = h.windows.find((x) => x.key === 'seven_day') || h.windows[0] || { pct: 0 };
    return `${(((h.ts - t0) / (7 * 864e5)) * 120).toFixed(1)},${(26 - w.pct * 0.24).toFixed(1)}`;
  }).join(' ');
  return `<svg width="120" height="28" viewBox="0 0 120 28" role="img" aria-label="7-day usage over the last week"><polyline points="${xy}" fill="none" stroke="${esc(color)}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/></svg>`;
}

// ---------- render ----------
function unread(a) {
  return S.events.filter((e) => e.accountId === a.id && e.ts > (seen[a.id] || 0) && e.kind !== 'idle' && e.kind !== 'finished').length;
}

function renderTabs() {
  const tabs = S.accounts.map((a, i) => {
    const n = unread(a);
    const hot = a.usage.status === 'ok' && worst(a.usage) >= 90;
    return `<button class="tab" role="tab" data-tab="${a.id}" aria-selected="${S.active === a.id}" title="${esc(a.email || a.label)} — Ctrl+${i + 1}">
      <span class="dot" data-bg="${esc(a.color)}"></span>${esc(a.label)}${hot ? '<span class="warn" aria-label="near limit">!</span>' : ''}${n ? `<span class="badge">${n}</span>` : ''}</button>`;
  }).join('');
  $('#tabs').innerHTML = `
    <button class="tab dash" role="tab" data-tab="dashboard" aria-selected="${S.active === 'dashboard'}" title="Dashboard — Ctrl+0">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="3" y="3" width="7" height="9" rx="1"/><rect x="14" y="3" width="7" height="5" rx="1"/><rect x="14" y="12" width="7" height="9" rx="1"/><rect x="3" y="16" width="7" height="5" rx="1"/></svg>Dashboard</button>
    <button class="tab dash" role="tab" data-tab="analytics" aria-selected="${S.active === 'analytics'}" title="Analytics — Ctrl+Shift+A">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/></svg>Analytics</button>
    ${tabs}
    <button class="icon-btn" data-act="add" aria-label="Add account" title="Add account">+</button>
    <span class="spacer"></span>
    <button class="icon-btn" data-act="settings" aria-label="Background and sync" title="Background &amp; sync">
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></svg></button>
    <button class="icon-btn" data-act="connect" aria-label="Connect Claude Code" title="Connect Claude Code hooks">
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/></svg></button>`;
  applyBars($('#tabs'));
}

function renderStrip() {
  const a = S.accounts.find((x) => x.id === S.active);
  const strip = $('#strip');
  strip.hidden = !a;
  if (!a) return;
  const u = a.usage;
  const minis = u.status === 'ok'
    ? [fiveOf(u), weekOf(u)].filter(Boolean).map((w) => `<span class="mini">${esc(w.label)} ${barH(w)}<span class="val">${w.pct}% · ${until(w.resetsAt)}</span></span>`).join('')
    : `<span class="muted">${u.status === 'signed-out' ? 'Not signed in — sign in below with email + code' : u.status === 'off' ? 'Usage tracking off' : u.status === 'error' ? 'Usage unavailable: ' + esc(u.error) : 'Reading usage…'}</span>`;
  strip.innerHTML = `
    <span class="who"><span class="dot" data-bg="${esc(a.color)}"></span><strong>${esc(a.label)}</strong><span class="muted">${esc(a.email || '')}${u.plan ? ' · ' + esc(u.plan) : ''}</span></span>
    ${minis}
    <span class="spacer"></span>
    <button class="sbtn" data-act="refresh" data-id="${a.id}">Refresh usage</button>
    <button class="sbtn" data-act="home" data-id="${a.id}">Home</button>
    <button class="sbtn" data-act="reload" data-id="${a.id}">Reload</button>
    <button class="sbtn" data-act="external" data-id="${a.id}">Open in browser</button>
    <button class="sbtn" data-act="edit" data-id="${a.id}">Settings</button>`;
  applyBars(strip);
}

function card(a) {
  const u = a.usage;
  let body;
  if (u.status === 'ok') {
    const wins = u.windows.length ? u.windows : [];
    const money = (w) => (w.limitDollars != null ? ` · $${(w.usedDollars || 0).toFixed(2)} of $${w.limitDollars}` : '');
    body = wins.map((w) => `<div class="win${w.inactive ? ' inactive' : ''}"><div class="lbl"><span title="${esc(w.key)}">${esc(w.label)}${w.inactive ? ' (not counting yet)' : ''}${w.locked ? ' · locked' : ''}</span><span>${w.pct}%${money(w)} · resets ${until(w.resetsAt)}</span></div>${barH(w)}</div>`).join('')
      || '<p class="state-msg">No limit windows reported for this account.</p>';
    if (u.breakdown && u.breakdown.rows.some((r) => r.percent > 0)) {
      body += `<p class="split">This week: ${u.breakdown.rows.filter((r) => r.percent > 0).map((r) => `${esc(r.label)} ${Math.round(r.percent)}%`).join(' · ')}</p>`;
    }
  } else {
    const msg = { 'signed-out': 'Not signed in. Open the tab and sign in with email + code.', off: 'Usage tracking is off for this account.', error: 'Couldn\'t read usage: ' + esc(u.error), loading: 'Reading usage…' }[u.status] || 'Reading usage…';
    body = `<p class="state-msg">${msg}</p>`;
  }
  const note = u.status === 'ok' ? `updated ${ago(u.fetchedAt)} ago` : '';
  return `<article class="card">
    <div class="card-top">
      <div class="card-id">
        <div class="row"><span class="dot" data-bg="${esc(a.color)}"></span><h2>${esc(a.label)}</h2>${u.plan ? `<span class="plan">${esc(u.plan)}</span>` : ''}</div>
        <span class="email">${esc(a.email || '—')}</span>
      </div>
      <button class="btn" data-tab="${a.id}">Open</button>
    </div>
    ${body}
    <div class="card-foot">${spark(a.id, a.color)}<span>${note}</span></div>
  </article>`;
}

function renderDash() {
  const ok = S.accounts.filter((a) => a.usage.status === 'ok');
  const near = ok.filter((a) => worst(a.usage) >= 80).length;
  const best = ok.slice().sort((x, y) => worst(x.usage) - worst(y.usage))[0];
  const recent = S.events.filter((e) => e.ts > Date.now() - 864e5 && e.kind !== 'finished' && e.kind !== 'idle').length;
  const feed = S.events.length
    ? S.events.slice(0, 12).map((e) => {
        const a = S.accounts.find((x) => x.id === e.accountId);
        return `<div class="ev"><span class="dot" data-bg="${esc(a ? a.color : '#9AA1AC')}"></span><span class="acct">${esc(e.account)}</span><span class="kind">${esc(e.kind)}</span><span class="txt">${esc(e.text)}${e.cwd ? ' · ' + esc(e.cwd) : ''}</span><span class="ago">${ago(e.ts)}</span>${a ? `<button class="link" data-act="code" data-id="${a.id}" data-url="${esc(e.url || '')}" title="${e.url ? 'Open this session on claude.ai' : 'Local session (no Remote Control) — opens Claude Code on claude.ai'}">${e.url ? 'Jump in' : 'Open Code'}</button>` : ''}</div>`;
      }).join('')
    : '<p class="state-msg">No Claude Code events yet. Connect your accounts\' hooks (bell icon, top right) and sessions that need you will appear here.</p>';

  $('#dash').innerHTML = `<div class="wrap">
    <header class="head">
      <div><h1>All accounts</h1><p class="muted">${S.accounts.length} account${S.accounts.length === 1 ? '' : 's'} · unofficial, not affiliated with Anthropic</p></div>
      <div class="chips">
        <div class="chip"><span>Near a limit</span><span data-c2="${near ? 'var(--warn)' : ''}">${near}</span></div>
        <div class="chip"><span>Most headroom</span><span>${best ? esc(best.label) : '—'}</span></div>
        <div class="chip"><span>Needs you (24h)</span><span>${recent}</span></div>
        <button class="btn" data-act="refresh-all">Refresh all</button>
      </div>
    </header>
    <section class="grid" aria-label="Accounts">${S.accounts.map(card).join('')}<button class="add-card" data-act="add">+ Add account</button></section>
    <div class="lower">
      <section class="panel feed" aria-label="Needs you"><h2>Claude Code activity</h2>${feed}</section>
    </div>
  </div>`;
  applyBars($('#dash'));
  $('#dash').querySelectorAll('[data-c2]').forEach((el) => { if (el.dataset.c2) el.style.color = el.dataset.c2; });
}

// Analytics: mounted once, reloaded when the tab is opened and every 5 minutes while visible.
let AN = null, anLoaded = 0;
function renderAnalytics() {
  if (!AN) AN = window.Analytics.mount($('#analytics'), { load: (q) => window.sy.analytics(q), onJump: (id, url) => window.sy.code(id, url) });
  if (Date.now() - anLoaded > 5 * 60e3) { anLoaded = Date.now(); AN.refresh(); }
}

let lastActive = null;
function render() {
  if (S.active !== 'dashboard' && S.active !== 'analytics') seen[S.active] = Date.now();
  renderTabs();
  renderStrip();
  $('#dash').hidden = S.active !== 'dashboard';
  $('#analytics').hidden = S.active !== 'analytics';
  if (S.active === 'dashboard') renderDash();
  if (S.active === 'analytics') { if (lastActive !== 'analytics') anLoaded = 0; renderAnalytics(); }
  lastActive = S.active;
}

// ---------- dialogs ----------
function swatches(el, current) {
  el.innerHTML = COLORS.map(([n, h]) => `<button type="button" aria-label="${n}" aria-pressed="${h === current}" data-color="${h}" data-bg="${h}"></button>`).join('');
  applyBars(el);
  el.onclick = (e) => {
    const b = e.target.closest('[data-color]'); if (!b) return;
    el.querySelectorAll('button').forEach((x) => x.setAttribute('aria-pressed', x === b));
  };
}
const picked = (el) => (el.querySelector('[aria-pressed="true"]') || {}).dataset?.color;

async function openAdd() {
  await window.sy.select('dashboard');
  const f = $('#form-add'); f.reset();
  const free = COLORS.find(([, h]) => !S.accounts.some((x) => x.color === h));
  swatches($('#add-colors'), (free || COLORS[S.accounts.length % COLORS.length])[1]);
  $('#dlg-add').showModal();
  $('#add-label').focus();
}
$('#form-add').addEventListener('submit', async (e) => {
  e.preventDefault();
  $('#dlg-add').close();
  await window.sy.add({ label: $('#add-label').value, color: picked($('#add-colors')), track: $('#add-track').checked });
});

let editing = null;
async function openEdit(id) {
  const a = S.accounts.find((x) => x.id === id); if (!a) return;
  editing = id;
  await window.sy.select('dashboard');
  $('#edit-label').value = a.label;
  $('#edit-track').checked = a.track !== false;
  swatches($('#edit-colors'), a.color);
  const rm = $('#edit-remove'); rm.classList.remove('armed'); rm.textContent = 'Remove account…';
  $('#dlg-edit').showModal();
}
$('#form-edit').addEventListener('submit', async (e) => {
  e.preventDefault();
  $('#dlg-edit').close();
  await window.sy.update(editing, { label: $('#edit-label').value, color: picked($('#edit-colors')), track: $('#edit-track').checked });
  window.sy.select(editing);
});
$('#edit-remove').addEventListener('click', async (e) => {
  const b = e.currentTarget;
  if (!b.classList.contains('armed')) { b.classList.add('armed'); b.textContent = 'Click again: sign out & delete this tab\'s data'; return; }
  $('#dlg-edit').close();
  await window.sy.remove(editing);
});

async function openSettings() {
  const st = await window.sy.settings();
  $('#set-autostart').checked = st.autostart;
  $('#set-keep').checked = st.keepRunning;
  $('#set-url').value = st.server.url;
  $('#set-token').value = '';
  $('#set-token').placeholder = st.server.hasToken ? 'saved — leave empty to keep it' : 'token from the server\'s .env (SWITCHYARD_TOKEN)';
  const s = st.status, bits = [];
  if (s.progress) bits.push(`Reading Claude Code transcripts… ${s.progress.done}/${s.progress.files} files`);
  else if (s.scan) bits.push(`Transcripts scanned ${ago(s.scan.at)} ago`);
  if (s.sync) bits.push(s.sync.ok ? `last sync ${ago(s.sync.at)} ago (${s.sync.sent} rows)` : `sync failed: ${s.sync.error}`);
  bits.push(`data in ${st.dataDir}`);
  $('#set-status').textContent = bits.join(' · ');
  $('#dlg-settings').showModal();
}
$('#form-settings').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    await window.sy.setSettings({ autostart: $('#set-autostart').checked, keepRunning: $('#set-keep').checked, server: { url: $('#set-url').value, token: $('#set-token').value } });
    $('#dlg-settings').close();
  } catch (err) { $('#set-status').textContent = String(err.message || err).replace(/^Error invoking remote method '[^']+': (Error: )?/, ''); }
});
$('#set-sync').addEventListener('click', async () => { await window.sy.syncNow(); $('#set-status').textContent = 'Sync started — reopen this dialog in a moment to see the result.'; });

function openConnect() {
  window.sy.select('dashboard');
  const h = S.hook;
  $('#connect-cmds').innerHTML = `<div class="cmd"><pre>bash ${esc(h.script)} --all</pre></div>
    <p class="muted">Connects every <code>~/.claude*</code> config directory (or pass <code>--config-dir ~/.claude-3</code> for one). It installs one shared hook script, <code>~/.claude/switchyard-hook.sh</code>, which reports the login email of whichever account ran it, so each event lands on the tab signed in with that email. The token and port are read from Switchyard's config at run time, so nothing account-specific goes into <code>settings.json</code> and symlinked settings stay symlinked. Add <code>--uninstall</code> to remove the hooks again.</p>`;
  $('#dlg-connect').showModal();
}

document.addEventListener('click', (e) => {
  if (e.target.closest('[data-close]')) { e.target.closest('dialog').close(); return; }
  const t = e.target.closest('[data-tab]');
  if (t) { window.sy.select(t.dataset.tab); return; }
  const b = e.target.closest('[data-act]'); if (!b) return;
  const id = b.dataset.id;
  ({
    add: openAdd, connect: openConnect, settings: openSettings, edit: () => openEdit(id),
    refresh: () => window.sy.refresh(id), 'refresh-all': () => window.sy.refresh(),
    code: () => window.sy.code(id, b.dataset.url || null),
    reload: () => window.sy.reload(id), home: () => window.sy.home(id), external: () => window.sy.external(id),
  })[b.dataset.act]?.();
});

window.sy.onState((s) => { S = s; render(); });
window.sy.state().then((s) => { S = s; render(); if (!S.accounts.length) openAdd(); });
setInterval(() => { if (!document.querySelector('dialog[open]')) render(); }, 30000); // keep countdowns fresh
