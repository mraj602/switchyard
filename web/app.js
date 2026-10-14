'use strict';
// Self-hosted web UI: sign-in, an overview of every account's limit windows and Claude Code
// activity, and the same Analytics view as the desktop app (web/analytics.js).
(() => {
  const $ = (s) => document.querySelector(s);
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const QUIET = new Set(['idle', 'finished']);

  // status colours always ship with an icon + label, never colour alone
  const STATUS = [
    [100, 'critical', 'var(--critical)', '⨯', 'At limit'],
    [90, 'serious', 'var(--serious)', '▲', 'Nearly full'],
    [75, 'warning', 'var(--warning)', '!', 'Filling up'],
    [0, 'good', 'var(--good)', '✓', 'OK'],
  ];
  const status = (p) => STATUS.find(([t]) => p >= t);

  function until(iso) {
    if (!iso) return '—';
    const ms = new Date(iso).getTime() - Date.now();
    if (!(ms > 0)) return 'now';
    const m = Math.round(ms / 60000);
    if (m < 60) return `${m}m`;
    if (m < 1440) return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`;
    return new Date(iso).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit', hour12: false });
  }
  function ago(ts) {
    if (!ts) return 'never';
    const s = Math.max(0, Math.round((Date.now() - ts) / 1000));
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.round(s / 60)}m ago`;
    if (s < 86400) return `${Math.round(s / 3600)}h ago`;
    return `${Math.round(s / 86400)}d ago`;
  }
  function paintStyles(root) {
    root.querySelectorAll('[data-w]').forEach((el) => { el.style.width = el.dataset.w + '%'; });
    root.querySelectorAll('[data-bg]').forEach((el) => { el.style.background = el.dataset.bg; });
    root.querySelectorAll('[data-fg]').forEach((el) => { el.style.color = el.dataset.fg; });
  }

  async function api(path, opts = {}) {
    const r = await fetch(path, { credentials: 'same-origin', ...opts });
    if (r.status === 401) { showLogin(); throw new Error('signed out'); }
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body.error || `HTTP ${r.status}`);
    return body;
  }

  // ---------- sign in ----------
  function showLogin() { $('#app').hidden = true; $('#login').hidden = false; $('#token').focus(); }
  $('#login-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    $('#login-err').textContent = '';
    const r = await fetch('/login', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ token: $('#token').value }) });
    if (!r.ok) { $('#login-err').textContent = (await r.json().catch(() => ({}))).error || 'Sign-in failed'; return; }
    $('#token').value = '';
    start();
  });
  $('#logout').addEventListener('click', async () => { await fetch('/logout', { method: 'POST' }); showLogin(); });

  // ---------- views ----------
  let view = 'overview';
  try { view = sessionStorage.getItem('sy.view') || 'overview'; } catch {}
  function setView(v) {
    view = v;
    try { sessionStorage.setItem('sy.view', v); } catch {}
    document.querySelectorAll('[data-view]').forEach((b) => b.setAttribute('aria-selected', String(b.dataset.view === v)));
    $('#overview').hidden = v !== 'overview';
    $('#analytics').hidden = v !== 'analytics';
    if (v === 'analytics') loadAnalytics(); else loadOverview();
  }
  document.querySelectorAll('[data-view]').forEach((b) => b.addEventListener('click', () => setView(b.dataset.view)));

  // ---------- overview ----------
  let OV = null, feedAll = false;
  try { feedAll = localStorage.getItem('sy.feedAll') === '1'; } catch {}
  $('#overview').addEventListener('click', (e) => {
    const b = e.target.closest('[data-feed]'); if (!b) return;
    feedAll = b.dataset.feed === 'all';
    try { localStorage.setItem('sy.feedAll', feedAll ? '1' : '0'); } catch {}
    renderOverview();
  });
  async function loadOverview() {
    try { OV = await api('/api/overview'); renderOverview(); } catch (e) { if (e.message !== 'signed out') $('#overview').innerHTML = `<p class="state-msg">Couldn't load: ${esc(e.message)}</p>`; }
  }
  function syncPill() {
    if (!OV) return;
    const last = OV.sync.lastIngest;
    const stale = !last || Date.now() - last > 15 * 60e3;
    const pill = $('#sync-pill');
    pill.innerHTML = `<span class="dot" data-bg="${stale ? 'var(--warning)' : 'var(--good)'}"></span>${stale ? (last ? 'Desktop sync ' + ago(last) : 'Desktop never synced') : 'Synced ' + ago(last)}`;
    pill.title = 'Usage % comes from the Switchyard desktop app; it pushes here every minute while running.';
    paintStyles(pill);
  }
  function win(w) {
    const p = Math.max(0, Number(w.pct) || 0);
    const [, , color, icon, label] = status(p);
    const money = w.limitDollars != null ? ` · $${(w.usedDollars || 0).toFixed(2)} of $${w.limitDollars}` : '';
    return `<div class="win"><div class="lbl"><span title="${esc(w.key)}">${esc(w.label || w.key)} <span class="state" data-fg="${color}" aria-label="${label}">${icon} ${label}</span></span><span>${Math.round(p)}%${money} · resets ${until(w.resetsAt)}</span></div>
      <div class="track"><div class="fill" data-w="${Math.min(100, p)}" data-bg="${color}"></div></div></div>`;
  }
  function card(a) {
    const stale = a.sampledAt && Date.now() - a.sampledAt > 30 * 60e3;
    const body = a.windows.length ? a.windows.map(win).join('') : `<p class="state-msg">${a.sampledAt ? 'No limit windows reported.' : 'No usage synced yet for this account.'}</p>`;
    return `<article class="card">
      <div class="card-top"><div class="card-id">
        <div class="row"><span class="dot" data-bg="${esc(a.color || '#9AA1AC')}"></span><h2>${esc(a.label)}</h2>${a.plan ? `<span class="plan">${esc(a.plan)}</span>` : ''}</div>
        <span class="email">${esc(a.email || '—')}${a.org ? ' · ' + esc(a.org) : ''}</span></div></div>
      ${body}
      <div class="card-foot"><span class="${stale ? 'stale' : ''}">${a.sampledAt ? (stale ? '! ' : '') + 'sampled ' + ago(a.sampledAt) : 'not sampled'}</span></div>
    </article>`;
  }
  function renderOverview() {
    const accts = OV.accounts;
    const worst = (a) => Math.max(0, ...a.windows.filter((w) => w.key === 'five_hour' || w.key === 'seven_day').map((w) => w.pct || 0));
    const live = accts.filter((a) => a.sampledAt);
    const near = live.filter((a) => worst(a) >= 80).length;
    const best = live.slice().sort((x, y) => worst(x) - worst(y))[0];
    const needs = OV.events.filter((e) => !QUIET.has(e.kind) && e.ts > Date.now() - 864e5).length;
    const byId = new Map(accts.map((a) => [a.id, a]));
    const shown = feedAll ? OV.events : OV.events.filter((e) => !QUIET.has(e.kind));
    const feed = shown.length ? shown.slice(0, 25).map((e) => {
      const a = byId.get(e.accountId);
      return `<div class="ev"><span class="dot" data-bg="${esc(a ? a.color : '#9AA1AC')}"></span><span class="acct">${esc(e.account)}</span><span class="kind">${esc(e.kind)}</span><span class="txt" title="${esc(e.text)}">${esc(e.text)}${e.cwd ? ' · ' + esc(e.cwd) : ''}</span><span class="ago">${ago(e.ts)}</span>${e.url ? `<a href="${esc(e.url)}" target="_blank" rel="noopener noreferrer">Jump in</a>` : '<span></span>'}</div>`;
    }).join('') : `<p class="state-msg">${OV.events.length ? 'Nothing waiting on you. Switch to “All” to see finished turns.' : 'No Claude Code events yet. They arrive from the desktop app (hooks) or from <code>POST /hook</code>.'}</p>`;
    const lastSync = OV.sync.lastIngest;
    const notice = !lastSync
      ? `<div class="notice"><span aria-hidden="true">!</span><span><strong>Nothing synced yet.</strong> Open the Switchyard desktop app → Background &amp; sync, enter this server's URL and token, then Sync now. Usage % is read by the desktop app (it holds your claude.ai sign-ins); this server never talks to claude.ai.</span></div>`
      : Date.now() - lastSync > 15 * 60e3
        ? `<div class="notice"><span aria-hidden="true">!</span><span><strong>Usage may be out of date:</strong> the desktop app last synced ${ago(lastSync)}. Limit windows only update while it runs (it can run hidden at login).</span></div>`
        : '';
    $('#overview').innerHTML = `
      <header class="ov-head">
        <div><h1>All accounts</h1><p class="muted">${accts.length} account${accts.length === 1 ? '' : 's'} · self-hosted · unofficial, not affiliated with Anthropic</p></div>
        <div class="chips">
          <div class="chip"><span>Near a limit</span><span>${near}</span></div>
          <div class="chip"><span>Most headroom</span><span>${best ? esc(best.label) : '—'}</span></div>
          <div class="chip"><span>Needs you (24h)</span><span>${needs}</span></div>
          <div class="chip"><span>Phone pushes</span><span>${OV.ntfy ? 'On (ntfy)' : 'Off'}</span></div>
        </div>
      </header>
      ${notice}
      <section class="grid" aria-label="Accounts">${accts.map(card).join('') || '<p class="state-msg">No accounts yet.</p>'}</section>
      <section class="panel" aria-label="Claude Code activity"><div class="panel-head"><h2>Claude Code activity</h2><span class="seg-sm" role="group" aria-label="Filter activity"><button data-feed="needs" aria-pressed="${!feedAll}">Needs you</button><button data-feed="all" aria-pressed="${feedAll}">All</button></span></div>${feed}</section>
      <p class="foot">${OV.scan ? `Transcript scan: ${OV.scan.running ? 'running…' : OV.scan.at ? 'last ' + ago(OV.scan.at) : 'starting'}. ` : 'Token analytics come from desktop sync (transcript scanning is off on this server). '}Server v${esc(OV.version)}.</p>`;
    paintStyles($('#overview'));
    syncPill();
  }

  // ---------- analytics ----------
  let AN = null, anAt = 0;
  function loadAnalytics() {
    if (!AN) {
      AN = window.Analytics.mount($('#analytics'), {
        standalone: true,
        load: (q) => {
          const p = new URLSearchParams();
          for (const k of ['from', 'to', 'tz']) if (q[k] != null) p.set(k, q[k]);
          if (q.accounts && q.accounts.length) p.set('accounts', q.accounts.join(','));
          return api('/api/analytics?' + p);
        },
      });
      AN.refresh(); anAt = Date.now();
    } else if (Date.now() - anAt > 60e3) { AN.refresh(); anAt = Date.now(); }
  }

  // ---------- start ----------
  async function start() {
    try { OV = await api('/api/overview'); } catch { return; }
    $('#login').hidden = true; $('#app').hidden = false;
    setView(view);
  }
  setInterval(() => { if (!$('#app').hidden && !document.hidden) { if (view === 'overview') loadOverview(); else if (AN && Date.now() - anAt > 300e3) { AN.refresh(); anAt = Date.now(); } } }, 30e3);
  document.addEventListener('visibilitychange', () => { if (!document.hidden && !$('#app').hidden) (view === 'overview' ? loadOverview() : loadAnalytics()); });
  start();
})();
