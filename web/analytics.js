// Analytics view — shared by the desktop shell and the self-hosted web UI.
//   Analytics.mount(rootEl, { load(query) → Promise<data>, accounts?, onJump(accountId, url)?, standalone? })
// load() returns core/analytics.compute() output. Rendering is plain HTML + SVG (charts.js);
// dynamic sizes/colours go through the CSSOM so it runs under a strict CSP.
(function () {
  'use strict';
  const { esc, tipRow } = window.Charts;
  const C = window.Charts;
  const D = 864e5, H = 3600e3;

  // Validated categorical palette (dark surface #171A21; dataviz validator: all checks pass).
  const SLOTS = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767'];
  const OTHER = '#6B7280';
  // Models keep one colour each regardless of which are in view.
  const MODEL_ORDER = ['Opus 5.5', 'Sonnet 5.5', 'Fable 5.1', 'Haiku 5.5', 'Opus 5', 'Sonnet 5', 'Opus 4.8', 'Haiku 4.5'];
  const modelColor = (m) => { const i = MODEL_ORDER.indexOf(m); return i >= 0 ? SLOTS[i] : OTHER; };
  const BLUE = ['#104281', '#184f95', '#1c5cab', '#2a78d6', '#5598e7', '#86b6ef', '#b7d3f6']; // sequential, dark→light
  const STATUS = {
    good: { color: '#0ca30c', icon: '<path d="M5 12.5l4.5 4.5L19 7.5"/>' },
    warning: { color: '#fab219', icon: '<path d="M12 4l9 16H3z"/><path d="M12 10v4M12 17v.5"/>' },
    serious: { color: '#ec835a', icon: '<path d="M12 3v11M12 18v1"/>' },
    critical: { color: '#d03b3b', icon: '<path d="M6 6l12 12M18 6L6 18"/>' },
  };
  const TIER = { comfortable: 'good', 'on track': 'good', warming: 'warning', pressing: 'warning', critical: 'serious', runaway: 'critical', limited: 'critical' };
  const RANGES = [['1', '24h'], ['7', '7 days'], ['30', '30 days'], ['90', '90 days'], ['all', 'All time']];

  // ---------- formatting ----------
  const usd = (v) => (v == null ? '—' : v >= 1000 ? '$' + Math.round(v).toLocaleString('en-US') : v >= 100 ? '$' + Math.round(v) : '$' + v.toFixed(2));
  const tok = (v) => (v >= 1e9 ? (v / 1e9).toFixed(1) + 'B' : v >= 1e6 ? (v / 1e6).toFixed(1) + 'M' : v >= 1e3 ? Math.round(v / 1e3) + 'K' : String(Math.round(v || 0)));
  const pct = (v, d = 0) => (v == null ? '—' : (Math.round(v * 10 ** d) / 10 ** d) + '%');
  const pts = (v) => (Math.round(v * 10) / 10) + ' pts';
  const dayShort = (s) => new Date(s + 'T12:00:00Z').toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
  const dayLong = (s) => new Date(s + 'T12:00:00Z').toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric', timeZone: 'UTC' });
  const when = (t) => new Date(t).toLocaleString(undefined, { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
  const clock = (t) => new Date(t).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit', hour12: false });
  function dur(ms) {
    if (ms == null) return '—';
    const m = Math.max(0, Math.round(ms / 60e3));
    if (m < 60) return m + 'm';
    if (m < 48 * 60) return Math.floor(m / 60) + 'h ' + String(m % 60).padStart(2, '0') + 'm';
    return Math.floor(m / 1440) + 'd ' + Math.floor((m % 1440) / 60) + 'h';
  }
  const ago = (t) => (t ? dur(Date.now() - t) + ' ago' : '—');
  const delta = (cur, prev) => {
    if (!prev) return '';
    const d = ((cur - prev) / prev) * 100;
    return `<span class="an-delta">${d >= 0 ? '▲' : '▼'} ${Math.abs(Math.round(d))}% vs previous ${'period'}</span>`;
  };
  const icon = (k) => `<svg class="an-ic" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="${STATUS[k].color}" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${STATUS[k].icon}</svg>`;
  const statusChip = (k, label) => `<span class="an-status" data-c="${STATUS[k].color}">${icon(k)}${esc(label)}</span>`;

  function mount(root, opts) {
    const st = { range: '30', accounts: null, tables: false, spendBy: 'account', data: null, loading: false, err: null };
    try { Object.assign(st, JSON.parse(localStorage.getItem('sy.analytics') || '{}'), { data: null, loading: false, err: null }); } catch {}
    const save = () => { try { localStorage.setItem('sy.analytics', JSON.stringify({ range: st.range, accounts: st.accounts, tables: st.tables, spendBy: st.spendBy })); } catch {} };
    let charts = {};

    async function load() {
      st.loading = true; paint();
      const now = Date.now();
      const q = { tz: -new Date().getTimezoneOffset(), to: now, from: st.range === 'all' ? 1 : now - Number(st.range) * D, accounts: st.accounts || undefined };
      try { st.data = await opts.load(q); st.err = null; } catch (e) { st.err = String(e && e.message || e); }
      st.loading = false; paint();
    }

    function acctMap() {
      const m = new Map();
      for (const a of (st.data ? st.data.accounts : [])) m.set(a.id, { ...a, color: a.id === 'unattributed' ? OTHER : a.color || OTHER, label: a.label || a.id });
      return m;
    }

    // ---------- page ----------
    function paint() {
      C.hideTip();
      const d = st.data;
      if (!d) {
        root.innerHTML = `<div class="an"><header class="an-head"><div><h1>Analytics</h1><p class="muted">${st.err ? 'Couldn’t load analytics: ' + esc(st.err) : 'Reading usage history and Claude Code transcripts…'}</p></div></header></div>`;
        return;
      }
      const A = acctMap();
      const ids = [...A.keys()];
      const T = d.tokens.totals, P = d.tokens.previous;
      const L = d.limits;
      const hits5 = L.hits.filter((h) => h.type === 'five_hour').length, hits7 = L.hits.length - hits5;
      const weeklyPts = L.dailyPct.reduce((s, x) => s + Object.values(x.byAccount).reduce((a, b) => a + b, 0), 0);
      const projects = new Set(d.tokens.projects.map((p) => p.path)).size;
      const tokAll = T.input + T.output + T.cache5m + T.cache1h + T.cacheRead;
      const limitsSince = d.since.limits;
      const rangeFrom = d.range.from;

      const filterRow = `<div class="an-filters" role="toolbar" aria-label="Filters">
        <div class="seg" role="group" aria-label="Date range">${RANGES.map(([v, l]) => `<button data-range="${v}" aria-pressed="${st.range === v}">${l}</button>`).join('')}</div>
        <div class="an-accts" role="group" aria-label="Accounts">${[...A.values()].filter((a) => a.id !== 'unattributed' || !st.accounts).map((a) => `<button class="an-acct" data-acct="${esc(a.id)}" aria-pressed="${!st.accounts || st.accounts.includes(a.id)}"><span class="dot" data-bg="${esc(a.color)}"></span>${esc(a.label)}</button>`).join('')}${st.accounts ? '<button class="link" data-acct="*">All accounts</button>' : ''}</div>
        <span class="spacer"></span>
        <label class="an-toggle"><input type="checkbox" data-tables ${st.tables ? 'checked' : ''}> Show data tables</label>
        ${st.loading ? '<span class="muted">Updating…</span>' : ''}
      </div>`;

      const kpis = `<section class="an-kpis" aria-label="Summary">
        ${kpi('API-equivalent value', usd(T.cost), `${d.since.tokens && d.since.tokens <= rangeFrom - (d.range.to - rangeFrom) ? delta(T.cost, P.cost) : ''}<span>Claude Code, priced at API rates · estimate</span>`)}
        ${kpi('Claude Code tokens', tok(tokAll), `<span>${pct((T.cacheHit || 0) * 100)} served from cache · ${tok(T.output)} output</span>`)}
        ${kpi('Limit hits', String(L.hits.length), `<span>5-hour × ${hits5} · weekly × ${hits7}</span>`)}
        ${kpi('Weekly allowance used', pts(weeklyPts), `<span>${limitsSince && limitsSince > rangeFrom ? 'since sampling began ' + esc(dayShort(new Date(limitsSince + d.range.tz * 6e4).toISOString().slice(0, 10))) : 'sum of 7-day % added, all accounts'}</span>`)}
        ${kpi('Sessions', String(T.sessions), `<span>${T.activeDays} active days · ${projects} projects</span>`)}
      </section>`;

      root.innerHTML = `<div class="an">
        <header class="an-head">
          <div><h1>Analytics</h1>
          <p class="muted">Limits: sampled from claude.ai every 5 minutes while Switchyard runs${limitsSince ? ' (since ' + esc(when(limitsSince)) + ')' : ''}. Tokens: Claude Code transcripts on this machine${d.since.tokens ? ' (since ' + esc(when(d.since.tokens)) + ')' : ''}. $ figures are API-equivalent estimates — subscriptions bill in %, not dollars.</p></div>
        </header>
        ${filterRow}
        ${kpis}
        <div class="an-row">
          <section class="an-panel grow2" aria-labelledby="h-pace"><div class="an-ph"><h2 id="h-pace">Pace — current windows</h2><p class="muted">Bar = used; tick = where even use would be by now. Projection assumes the recent rate holds.</p></div>${paceTable(A)}</section>
          <section class="an-panel" aria-labelledby="h-pick"><div class="an-ph"><h2 id="h-pick">Use next</h2><p class="muted">Available now first, then the weekly window that resets soonest with allowance left — use it before it expires.</p></div>${picker(A)}</section>
        </div>
        <section class="an-panel" aria-labelledby="h-burn"><div class="an-ph"><h2 id="h-burn">Weekly burndown</h2><p class="muted">Current weekly window per account. Dashed grey: even use to the reset. Dashed colour: projection at the current pace. Gaps are times Switchyard wasn’t running.</p></div>
          <div class="an-multi">${burnCards(A)}</div></section>
        <section class="an-panel" aria-labelledby="h-daily"><div class="an-ph"><h2 id="h-daily">Weekly allowance used per day</h2><p class="muted">Percentage points of each account’s 7-day window consumed that day (official % from claude.ai, all surfaces).</p></div>
          ${legend([...A.values()].filter((a) => L.dailyPct.some((x) => x.byAccount[a.id])))}<div class="cx" data-chart="dailyPct"></div>${table('dailyPct')}</section>
        <section class="an-panel" aria-labelledby="h-spend"><div class="an-ph"><h2 id="h-spend">Claude Code spend per day</h2><p class="muted">API-equivalent $ from local transcripts, deduplicated per response.</p>
          <div class="seg sm" role="group" aria-label="Split by"><button data-spend="account" aria-pressed="${st.spendBy === 'account'}">By account</button><button data-spend="model" aria-pressed="${st.spendBy === 'model'}">By model</button></div></div>
          ${legend(spendSeries(A))}<div class="cx" data-chart="spend"></div>${table('spend')}</section>
        <div class="an-row">
          <section class="an-panel" aria-labelledby="h-models"><div class="an-ph"><h2 id="h-models">Models</h2><p class="muted">Share of API-equivalent spend.</p></div>${modelsList()}</section>
          <section class="an-panel" aria-labelledby="h-mix"><div class="an-ph"><h2 id="h-mix">Token mix &amp; caching</h2><p class="muted">Cache reads are most of the volume but a small part of the cost.</p></div>${mix()}<h3 class="an-h3">Cache hit rate per day</h3><div class="cx" data-chart="cache"></div></section>
        </div>
        <section class="an-panel" aria-labelledby="h-heat"><div class="an-ph"><h2 id="h-heat">When the work happens</h2><p class="muted">API-equivalent $ by weekday and hour (local time).</p></div>
          <div class="cx" data-chart="heat"></div><div class="an-ramp" aria-hidden="true"><span class="muted">less</span>${BLUE.map((c) => `<i data-bg="${c}"></i>`).join('')}<span class="muted">more</span></div></section>
        <section class="an-panel" aria-labelledby="h-cyc"><div class="an-ph"><h2 id="h-cyc">Limit windows</h2><p class="muted">Every weekly window seen. “Left at reset” is allowance that expired unused. “$ per 1%” joins both sources: Claude Code spend in the window ÷ the % it used.</p></div>${cycleTable(A)}</section>
        <section class="an-panel" aria-labelledby="h-five"><div class="an-ph"><h2 id="h-five">5-hour windows</h2><p class="muted">Windows sampled in this range, how many ran out (from Claude Code’s limit replies), and the peak of each window.</p></div>${fiveSummary(A)}</section>
        <section class="an-panel" aria-labelledby="h-hits"><div class="an-ph"><h2 id="h-hits">Limit hits</h2><p class="muted">From Claude Code’s own “You’ve hit your limit” replies in transcripts. Circle = 5-hour, diamond = weekly.</p></div>
          <div class="cx" data-chart="hits"></div>${hitsList(A)}</section>
        <div class="an-row">
          <section class="an-panel grow2" aria-labelledby="h-proj"><div class="an-ph"><h2 id="h-proj">Projects</h2></div>${projectsTable(A)}</section>
          <section class="an-panel grow2" aria-labelledby="h-sess"><div class="an-ph"><h2 id="h-sess">Biggest sessions</h2></div>${sessionsTable(A)}</section>
        </div>
        <div class="an-row">
          <section class="an-panel" aria-labelledby="h-cred"><div class="an-ph"><h2 id="h-cred">Cloud session credits</h2><p class="muted">Dollars used of each account’s monthly credit (iguana_necktie).</p></div>${legend([...A.values()].filter((a) => L.credits.some((c) => c.account === a.id)))}<div class="cx" data-chart="credits"></div></section>
          <section class="an-panel" aria-labelledby="h-surf"><div class="an-ph"><h2 id="h-surf">Where the weekly limit went</h2><p class="muted">Latest split of this week’s usage by surface, from claude.ai.</p></div>${surfaces(A)}</section>
        </div>
        <section class="an-panel" aria-labelledby="h-act"><div class="an-ph"><h2 id="h-act">Claude Code attention</h2><p class="muted">Hook events per day: times a session stopped to ask you, and finished turns.</p></div>${legend(actSeries())}<div class="cx" data-chart="activity"></div></section>
        <section class="an-panel" aria-labelledby="h-q"><div class="an-ph"><h2 id="h-q">Data coverage</h2></div>${coverage(A)}</section>
        <p class="muted an-foot">Switchyard is unofficial and not affiliated with Anthropic. Limit data comes from the same endpoints claude.ai’s Settings → Usage page reads; it may change without notice.</p>
      </div>`;
      apply(root);
      draw();
    }

    const kpi = (label, value, sub) => `<div class="an-kpi"><span class="an-kl">${esc(label)}</span><span class="an-kv">${esc(value)}</span><span class="an-ks">${sub}</span></div>`;
    const legend = (items) => (items.length > 1 ? `<div class="an-legend">${items.map((a) => `<span><i data-bg="${esc(a.color)}"></i>${esc(a.label)}</span>`).join('')}</div>` : '');
    const table = (id) => (st.tables ? `<div class="an-tablewrap" data-table="${id}"></div>` : '');

    // ---------- pace ----------
    function paceTable(A) {
      const rows = st.data.limits.pace.filter((p) => p.key !== 'iguana_necktie' && !(p.actual === 0 && p.key.startsWith('weekly_')));
      if (!rows.length) return '<p class="state-msg">No current samples. Open the Dashboard or keep Switchyard running in the background to collect them.</p>';
      return `<div class="an-pace">${rows.map((p) => {
        const a = A.get(p.account) || { label: p.account, color: OTHER };
        const k = TIER[p.tier] || 'good';
        const proj = p.actual >= 100 ? `limited until ${clock(p.resetsAt)}`
          : p.early ? 'too early in the window to project'
            : p.lastsToReset ? `lasts to reset · ~${pct(Math.min(100, p.projected))} at reset`
              : `hits 100% in ~${dur(p.etaAt - Date.now())} · before reset`;
        return `<div class="an-prow">
          <span class="an-pa"><span class="dot" data-bg="${esc(a.color)}"></span>${esc(a.label)}</span>
          <span class="an-pw">${esc(p.label || p.key)}</span>
          <span class="an-pbar"><span class="track"><span class="fill" data-w="${Math.min(100, p.actual)}" data-c="${esc(a.color)}"></span></span><span class="an-tick" data-l="${Math.min(100, p.expected)}" title="Even use by now: ${pct(p.expected)}"></span></span>
          <span class="an-pv">${pct(p.actual)}<small> / ${pct(p.expected)} even</small></span>
          <span class="an-pr">${statusChip(k, p.tier)}<small>${esc(proj)} · resets ${esc(clock(p.resetsAt))}</small></span>
        </div>`;
      }).join('')}</div>`;
    }

    function picker(A) {
      const list = st.data.limits.picker;
      if (!list.length) return '<p class="state-msg">Needs current usage samples.</p>';
      return `<ol class="an-pick">${list.map((x, i) => {
        const a = A.get(x.account) || { label: x.account, color: OTHER };
        const why = x.blocked ? 'limited right now'
          : x.headroom != null && x.headroom <= 5 ? `weekly nearly used (${pct(x.weekly)})`
            : x.weeklyResetsAt ? `${pct(x.headroom)} weekly left · resets ${clock(x.weeklyResetsAt)}`
              : 'no overall weekly cap reported';
        const k = x.blocked ? 'critical' : x.headroom != null && x.headroom <= 5 ? 'warning' : 'good';
        return `<li><span class="an-rank">${i + 1}</span><span class="dot" data-bg="${esc(a.color)}"></span><span class="an-pn">${esc(a.label)}</span>${statusChip(k, x.blocked ? 'blocked' : k === 'warning' ? 'low' : 'available')}<small class="muted">${esc(why)}${x.fiveHour != null ? ` · 5-hour ${pct(x.fiveHour)}` : ''}</small></li>`;
      }).join('')}</ol>`;
    }

    // ---------- burndown small multiples ----------
    function currentCycles() {
      const now = Date.now();
      const out = [];
      const L = st.data.limits;
      const accts = [...new Set(L.cycles.map((c) => c.account))];
      for (const id of accts) {
        const mine = L.cycles.filter((c) => c.account === id && c.end > now);
        const c = mine.find((x) => x.key === 'seven_day') || mine.find((x) => x.key.startsWith('weekly_') && x.peak > 0) || mine.find((x) => x.key === 'five_hour');
        if (c) out.push(c);
      }
      return out;
    }
    function burnCards(A) {
      const cs = currentCycles();
      if (!cs.length) return '<p class="state-msg">No current window samples yet.</p>';
      return cs.map((c, i) => {
        const a = A.get(c.account) || { label: c.account, color: OTHER };
        const p = st.data.limits.pace.find((x) => x.account === c.account && x.key === c.key);
        return `<div class="an-burn"><div class="an-bh"><span class="dot" data-bg="${esc(a.color)}"></span><b>${esc(a.label)}</b><span class="muted">${esc(st.data.labels[c.key] || c.key)}</span><span class="spacer"></span><span class="an-bv">${pct(c.last)}</span></div>
          <div class="cx" data-chart="burn:${i}"></div>
          <small class="muted">${p ? (p.actual >= 100 ? 'limited · resets ' + esc(clock(c.end)) : p.lastsToReset ? 'on pace to last · resets ' + esc(clock(c.end)) : 'at this pace: 100% in ~' + esc(dur(p.etaAt - Date.now()))) : 'resets ' + esc(clock(c.end))}</small></div>`;
      }).join('');
    }

    // ---------- series helpers ----------
    function spendSeries(A) {
      const T = st.data.tokens;
      if (st.spendBy === 'model') return T.models.map((m) => ({ id: m.model, label: m.model, color: modelColor(m.model) }));
      return [...A.values()].filter((a) => T.byAccount[a.id]).map((a) => ({ id: a.id, label: a.label, color: a.color }));
    }
    const ACT = [['permission', 'Permission prompts', SLOTS[1]], ['elicitation', 'Questions', SLOTS[3]], ['notification', 'Other notifications', SLOTS[6]], ['finished', 'Finished turns', SLOTS[0]]];
    const actKind = (k) => (k === 'permission' ? 'permission' : k === 'finished' ? 'finished' : k === 'elicitation' ? 'elicitation' : k === 'idle' ? null : 'notification');
    const actSeries = () => ACT.filter(([k]) => st.data.activity.some((r) => actKind(r.kind) === k)).map(([id, label, color]) => ({ id, label, color }));

    function daysInRange() {
      const r = st.data.range;
      const from = st.range === 'all' ? Math.min(...[r.to, ...st.data.tokens.daily.map((x) => Date.parse(x.day))]) : r.from;
      const out = [];
      const tz = r.tz * 6e4;
      for (let t = Math.floor((from + tz) / D) * D; t <= r.to + tz; t += D) out.push(new Date(t).toISOString().slice(0, 10));
      return out;
    }

    // ---------- lists & tables ----------
    function modelsList() {
      const m = st.data.tokens.models; const tot = m.reduce((s, x) => s + x.cost, 0) || 1;
      if (!m.length) return '<p class="state-msg">No Claude Code usage in this range.</p>';
      return `<div class="an-hbars">${m.map((x) => `<div class="an-hb"><span class="an-hl"><i data-bg="${modelColor(x.model)}"></i>${esc(x.model)}</span><span class="track"><span class="fill" data-w="${(x.cost / tot) * 100}" data-c="${modelColor(x.model)}"></span></span><span class="an-hv">${usd(x.cost)} <small>${pct((x.cost / tot) * 100)} · ${tok(x.output)} out</small></span></div>`).join('')}</div>`;
    }
    function mix() {
      const T = st.data.tokens.totals;
      const cw = T.cache5m + T.cache1h;
      // cost per type, using the same per-model pricing the server applied (approximate split)
      const parts = [['Input', T.input, BLUE[6]], ['Output', T.output, BLUE[4]], ['Cache write', cw, BLUE[2]], ['Cache read', T.cacheRead, BLUE[0]]];
      const sum = parts.reduce((s, p) => s + p[1], 0) || 1;
      const seg = (vals) => `<div class="an-stack">${vals.map(([l, v, c]) => (v > 0 ? `<span data-w="${Math.max(0.6, v * 100)}" data-c="${c}" title="${esc(l)}: ${pct(v * 100, 1)}"></span>` : '')).join('')}</div>`;
      const cm = st.data.tokens.costMix;
      const costParts = cm ? [['Input', cm.input], ['Output', cm.output], ['Cache write', cm.cacheWrite], ['Cache read', cm.cacheRead]] : null;
      const cs = costParts ? costParts.reduce((s, p) => s + p[1], 0) || 1 : 1;
      return `<div class="an-mix">
        <div class="an-mrow"><span class="an-ml">Tokens</span>${seg(parts.map(([l, v, c]) => [l, v / sum, c]))}</div>
        ${costParts ? `<div class="an-mrow"><span class="an-ml">Cost</span>${seg(costParts.map(([l, v], i) => [l, v / cs, parts[i][2]]))}</div>` : ''}
        <div class="an-legend">${parts.map(([l, v, c], i) => `<span><i data-bg="${c}"></i>${esc(l)} <b>${tok(v)}</b>${costParts ? ` · ${usd(costParts[i][1])}` : ''}</span>`).join('')}</div>
        <p class="muted an-note">Cache hit rate: <b class="an-ink">${pct((T.cacheHit || 0) * 100, 1)}</b> of input tokens were cache reads. Thinking: ${tok(T.thinking)} of the output.</p>
      </div>`;
    }
    function cycleTable(A) {
      const cs = st.data.limits.cycles.filter((c) => c.key === 'seven_day' || (c.key.startsWith('weekly_') && c.peak > 0) || c.key.startsWith('seven_day_')).slice().reverse();
      if (!cs.length) return '<p class="state-msg">No weekly windows sampled in this range yet.</p>';
      return `<div class="an-tablewrap"><table class="an-table"><thead><tr><th>Account</th><th>Window</th><th>Period</th><th class="num">Peak</th><th>Hit 100%</th><th class="num">Left at reset</th><th class="num">Code $</th><th class="num">$ per 1%</th></tr></thead><tbody>
        ${cs.map((c) => { const a = A.get(c.account) || { label: c.account, color: OTHER }; return `<tr><td><span class="dot" data-bg="${esc(a.color)}"></span> ${esc(a.label)}</td><td>${esc(st.data.labels[c.key] || c.key)}</td><td>${esc(when(c.start))} → ${esc(when(c.end))}${c.done ? '' : ' <span class="an-pill">running</span>'}</td><td class="num">${pct(c.peak)}</td><td>${c.hitAt ? esc(when(c.hitAt)) : '—'}</td><td class="num">${c.done ? pct(Math.max(0, 100 - c.peak)) : '—'}</td><td class="num">${c.codeUsd != null ? usd(c.codeUsd) : '—'}</td><td class="num">${c.usdPerPct != null ? usd(c.usdPerPct) : '—'}</td></tr>`; }).join('')}
      </tbody></table></div><p class="muted an-note">Sampling only sees windows while Switchyard is running; a window’s peak is a lower bound if samples are missing near its end.</p>`;
    }
    function fiveSummary(A) {
      const L = st.data.limits;
      const by = new Map();
      for (const c of L.cycles.filter((x) => x.key === 'five_hour')) { if (!by.has(c.account)) by.set(c.account, []); by.get(c.account).push(c); }
      const hits = new Map();
      for (const h of L.hits.filter((x) => x.type === 'five_hour')) hits.set(h.account, (hits.get(h.account) || 0) + 1);
      const ids = [...new Set([...by.keys(), ...hits.keys()])];
      if (!ids.length) return '<p class="state-msg">No 5-hour windows in this range.</p>';
      return `<div class="an-five">${ids.map((id) => {
        const a = A.get(id) || { label: id, color: OTHER };
        const cs = by.get(id) || [];
        const peaks = cs.map((c) => c.peak);
        const avg = peaks.length ? peaks.reduce((s, v) => s + v, 0) / peaks.length : null;
        return `<div class="an-frow"><span class="an-pa"><span class="dot" data-bg="${esc(a.color)}"></span>${esc(a.label)}</span>
          <span><b>${cs.length}</b> <small class="muted">window${cs.length === 1 ? '' : 's'} sampled</small></span>
          <span><b>${hits.get(id) || 0}</b> <small class="muted">hit the limit</small></span>
          <span><b>${pct(avg)}</b> <small class="muted">avg peak</small></span>
          <span class="an-peaks" aria-label="Peak per window">${cs.slice(-24).map((c) => `<i data-h="${Math.max(4, Math.min(100, c.peak))}" data-bg="${esc(a.color)}" title="${esc(when(c.start))}: peak ${pct(c.peak)}"></i>`).join('')}</span></div>`;
      }).join('')}</div>`;
    }
    function hitsList(A) {
      const hs = st.data.limits.hits.slice().reverse().slice(0, 8);
      if (!hs.length) return '';
      return `<div class="an-tablewrap"><table class="an-table"><thead><tr><th>When</th><th>Account</th><th>Limit</th><th class="num">Waited</th></tr></thead><tbody>${hs.map((h) => { const a = A.get(h.account) || { label: h.account, color: OTHER }; return `<tr><td>${esc(when(h.ts))}</td><td><span class="dot" data-bg="${esc(a.color)}"></span> ${esc(a.label)}</td><td>${h.type === 'five_hour' ? '5-hour' : 'Weekly'}</td><td class="num">${h.waitH != null ? dur(h.waitH * H) : '—'}</td></tr>`; }).join('')}</tbody></table></div>`;
    }
    function projectsTable(A) {
      const ps = st.data.tokens.projects.slice(0, 12);
      if (!ps.length) return '<p class="state-msg">No Claude Code usage in this range.</p>';
      const max = ps[0].cost || 1;
      return `<div class="an-tablewrap"><table class="an-table"><thead><tr><th>Project</th><th>Accounts</th><th class="num">Sessions</th><th class="num">Tokens</th><th class="num">API-eq $</th><th>Last active</th></tr></thead><tbody>
        ${ps.map((p) => `<tr><td title="${esc(p.path)}"><span class="an-cellbar"><span data-w="${(p.cost / max) * 100}" data-c="#2A3446"></span><b>${esc(p.name)}</b></span></td><td>${p.accounts.map((id) => { const a = A.get(id); return `<span class="dot" data-bg="${esc(a ? a.color : OTHER)}" title="${esc(a ? a.label : id)}"></span>`; }).join(' ')}</td><td class="num">${p.sessions}</td><td class="num">${tok(p.input + p.output + p.cache5m + p.cache1h + p.cacheRead)}</td><td class="num">${usd(p.cost)}</td><td>${esc(ago(p.last))}</td></tr>`).join('')}
      </tbody></table></div>`;
    }
    function sessionsTable(A) {
      const ss = st.data.tokens.sessions.slice(0, 12);
      if (!ss.length) return '<p class="state-msg">No sessions in this range.</p>';
      return `<div class="an-tablewrap"><table class="an-table"><thead><tr><th>Started</th><th>Project</th><th>Account</th><th>Models</th><th class="num">Length</th><th class="num">API-eq $</th><th></th></tr></thead><tbody>
        ${ss.map((s) => { const a = A.get(s.account) || { label: s.account, color: OTHER }; return `<tr><td>${esc(when(s.start))}</td><td>${esc(s.project || '—')}</td><td><span class="dot" data-bg="${esc(a.color)}"></span> ${esc(a.label)}</td><td>${esc(s.models.slice(0, 2).join(', ') + (s.models.length > 2 ? ` +${s.models.length - 2}` : ''))}</td><td class="num">${dur(s.end - s.start)}</td><td class="num">${usd(s.cost)}</td><td>${s.url && (opts.onJump || opts.standalone) && s.account !== 'unattributed' ? `<button class="link" data-jump="${esc(s.account)}" data-url="${esc(s.url)}">Jump in</button>` : ''}</td></tr>`; }).join('')}
      </tbody></table></div>`;
    }
    function surfaces(A) {
      const S = st.data.limits.surfaces;
      const latest = new Map();
      for (const [k, v] of Object.entries(S)) { const [id, day] = k.split('|'); if (!latest.has(id) || latest.get(id).day < day) latest.set(id, { day, v }); }
      if (!latest.size) return '<p class="state-msg">No split reported yet.</p>';
      const names = { claude_code: 'Claude Code', chat: 'Chat', cowork: 'Cowork', other: 'Other' };
      const cols = { claude_code: BLUE[5], chat: BLUE[3], cowork: BLUE[1], other: '#4B5563' };
      return `<div class="an-mix">${[...latest].map(([id, { v }]) => {
        const a = A.get(id) || { label: id, color: OTHER };
        const sum = Object.values(v).reduce((s, x) => s + x, 0);
        const body = sum > 0 ? `<div class="an-stack">${Object.entries(v).filter(([, x]) => x > 0).map(([k, x]) => `<span data-w="${(x / sum) * 100}" data-c="${cols[k] || '#4B5563'}" title="${esc(names[k] || k)} ${pct(x)}">${x / sum > 0.12 ? esc((names[k] || k) + ' ' + pct(x)) : ''}</span>`).join('')}</div>` : '<span class="muted">nothing used this week</span>';
        return `<div class="an-mrow"><span class="an-ml"><span class="dot" data-bg="${esc(a.color)}"></span> ${esc(a.label)}</span>${body}</div>`;
      }).join('')}<div class="an-legend">${Object.entries(names).map(([k, l]) => `<span><i data-bg="${cols[k]}"></i>${l}</span>`).join('')}</div></div>`;
    }
    function coverage(A) {
      const L = st.data.limits;
      const T = st.data.tokens;
      const un = T.byAccount.unattributed;
      const rows = L.quality.map((q) => { const a = A.get(q.account) || { label: q.account, color: OTHER }; return `<tr><td><span class="dot" data-bg="${esc(a.color)}"></span> ${esc(a.label)}</td><td class="num">${q.samples}</td><td class="num">${pct((q.coverage || 0) * 100)}</td><td class="num">${q.gaps}</td><td>${esc(ago(q.last))}</td></tr>`; }).join('');
      return `<div class="an-tablewrap"><table class="an-table"><thead><tr><th>Account</th><th class="num">Samples</th><th class="num">Time covered</th><th class="num">Gaps &gt; 20 min</th><th>Last sample</th></tr></thead><tbody>${rows || '<tr><td colspan="5" class="muted">No samples in this range.</td></tr>'}</tbody></table></div>
        <p class="muted an-note">${un ? `<b class="an-ink">${usd(un.cost)}</b> of Claude Code usage (${pct((un.cost / (T.totals.cost || 1)) * 100)}) is unattributed: those sessions ran before the hooks were connected and without Remote Control, so their account can’t be told apart (every login shares one projects folder). New sessions are attributed by the SessionStart hook. ` : ''}Run Switchyard in the background (see README) so limits are sampled even when the window is closed.</p>`;
    }

    // ---------- charts (sized to their container) ----------
    function draw() {
      if (!st.data) return;
      const A = acctMap();
      const d = st.data;
      root.querySelectorAll('[data-chart]').forEach((el) => {
        const w = Math.max(240, Math.floor(el.clientWidth));
        const id = el.dataset.chart;
        try { drawOne(el, id, w, A, d); } catch (e) { el.innerHTML = `<p class="state-msg">Chart failed: ${esc(e.message)}</p>`; }
      });
      if (st.tables) drawTables(A, d);
    }

    function drawOne(el, id, w, A, d) {
      const days = daysInRange();
      if (id === 'dailyPct') {
        const map = new Map(d.limits.dailyPct.map((x) => [x.day, x.byAccount]));
        const series = [...A.values()].filter((a) => d.limits.dailyPct.some((x) => x.byAccount[a.id])).map((a) => ({ id: a.id, label: a.label, color: a.color }));
        if (!series.length) { el.innerHTML = '<p class="state-msg">Builds up as Switchyard samples usage over the days.</p>'; return; }
        C.bars(el, { width: w, height: 200, cats: days, series, value: (i, s) => (map.get(days[i]) || {})[s] || 0, fmt: pts, fmtAxis: (v) => (Number.isInteger(v) ? String(v) : v.toFixed(1)), fmtCat: dayShort, fmtCatLong: dayLong, title: 'Weekly allowance used per day' });
      } else if (id === 'spend') {
        const map = new Map(d.tokens.daily.map((x) => [x.day, st.spendBy === 'model' ? x.byModel : x.byAccount]));
        C.bars(el, { width: w, height: 240, cats: days, series: spendSeries(A), value: (i, s) => (map.get(days[i]) || {})[s] || 0, fmt: usd, fmtAxis: (v) => '$' + (v >= 1000 ? (v / 1000) + 'k' : v), fmtCat: dayShort, fmtCatLong: dayLong, title: 'Claude Code API-equivalent spend per day' });
      } else if (id === 'cache') {
        const pts2 = d.tokens.daily.filter((x) => x.tokens.cacheHit != null).map((x) => [Date.parse(x.day + 'T12:00:00Z'), x.tokens.cacheHit * 100]);
        if (!pts2.length) { el.innerHTML = '<p class="state-msg">No data.</p>'; return; }
        const x0 = Date.parse(days[0] + 'T00:00:00Z'), x1 = Date.parse(days[days.length - 1] + 'T23:59:59Z');
        C.lines(el, { width: w, height: 150, x0, x1, yMax: 100, yStep: 25, series: [{ label: 'Cache hit rate', color: '#C9CED6', points: pts2 }], fmtY: (v, tip) => (tip ? pct(v, 1) : v + '%'), fmtT: (t) => dayLong(new Date(t).toISOString().slice(0, 10)), ticks: dayTicks(days, w), title: 'Cache hit rate per day' });
      } else if (id === 'heat') {
        const wd = [1, 2, 3, 4, 5, 6, 0];
        const names = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
        C.heat(el, { width: Math.min(w, 980), rows: names, cols: Array.from({ length: 24 }, (_, h) => String(h).padStart(2, '0')), values: wd.map((x) => d.tokens.heat[x]), ramp: BLUE, empty: '#1D2129', title: 'Spend by weekday and hour',
          tipFor: (r, c) => `<div class="cx-h">${names[r]} ${String(c).padStart(2, '0')}:00–${String(c + 1).padStart(2, '0')}:00</div>${tipRow(null, 'API-equivalent', usd(d.tokens.heat[wd[r]][c]))}${tipRow(null, 'Responses', String(d.tokens.heatN[wd[r]][c]))}` });
      } else if (id.startsWith('burn:')) {
        const c = currentCycles()[Number(id.slice(5))];
        if (!c) return;
        const a = A.get(c.account) || { label: c.account, color: OTHER };
        const p = d.limits.pace.find((x) => x.account === c.account && x.key === c.key);
        const now = Date.now();
        const series = [{ label: 'Even use', color: '#5B6170', dash: true, points: [[c.start, 0], [c.end, 100]], noTip: true, width: 1.5 },
          { label: a.label, color: a.color, points: c.points, maxGap: 30 * 60e3 }];
        if (p && !p.early && p.actual < 100 && p.ratePerHour > 0) {
          const tEnd = Math.min(c.end, p.etaAt || c.end);
          series.push({ label: 'Projection', color: a.color, dash: true, width: 1.5, noTip: true, points: [[c.lastSample, c.last], [tEnd, Math.min(100, c.last + p.ratePerHour * (tEnd - c.lastSample) / H)]] });
        }
        const hits = d.limits.hits.filter((h) => h.account === c.account && h.ts >= c.start && h.ts <= c.end).map((h) => ({ t: h.ts, v: 100, color: '#d03b3b' }));
        const span = c.end - c.start;
        const ticks = [];
        for (let t = c.start; t <= c.end + 1; t += span / 4) ticks.push({ t, label: span > 2 * D ? new Date(t).toLocaleDateString(undefined, { weekday: 'short' }) : new Date(t).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false }) });
        C.lines(el, { width: w, height: 150, padL: 36, x0: c.start, x1: c.end, yMax: 100, yStep: 50, series, marks: now < c.end ? [{ t: now, label: 'now' }] : [], dots: hits, fmtY: (v, tip) => (tip ? pct(v) : v + '%'), fmtT: when, ticks, title: `${a.label} weekly burndown` });
      } else if (id === 'hits') {
        hitsChart(el, w, A, d);
      } else if (id === 'credits') {
        const cr = d.limits.credits.filter((c) => c.points.length);
        if (!cr.length) { el.innerHTML = '<p class="state-msg">No credit samples in this range.</p>'; return; }
        const all = cr.flatMap((c) => c.points);
        const x0 = Math.min(...all.map((p) => p[0])), x1 = Math.max(Date.now(), ...all.map((p) => p[0]));
        const nm = C.niceMax(Math.max(...all.map((p) => p[2] || 0), ...all.map((p) => p[1] || 0), 1));
        C.lines(el, { width: w, height: 170, x0: x0 - 60e3, x1, yMax: nm.max, yStep: nm.step, series: cr.map((c) => { const a = A.get(c.account) || { label: c.account, color: OTHER }; return { label: a.label, color: a.color, points: c.points.map((p) => [p[0], p[1]]), maxGap: 30 * 60e3 }; }),
          fmtY: (v, tip) => (tip ? usd(v) : '$' + Math.round(v)), fmtT: when, ticks: [{ t: x0, label: clock(x0), anchor: 'start' }, { t: x1, label: clock(x1), anchor: 'end' }], title: 'Cloud session credits used' });
      } else if (id === 'activity') {
        const by = new Map();
        for (const r of d.activity) { const k = actKind(r.kind); if (!k) continue; const o = by.get(r.day) || {}; o[k] = (o[k] || 0) + r.n; by.set(r.day, o); }
        if (!by.size) { el.innerHTML = '<p class="state-msg">No hook events in this range. Connect Claude Code (bell icon on the Dashboard).</p>'; return; }
        C.bars(el, { width: w, height: 160, cats: days, series: actSeries(), value: (i, s) => (by.get(days[i]) || {})[s] || 0, fmt: (v) => String(Math.round(v)), fmtCat: dayShort, fmtCatLong: dayLong, title: 'Claude Code hook events per day' });
      }
    }
    function dayTicks(days, w) {
      const n = Math.max(2, Math.floor(w / 90));
      const every = Math.max(1, Math.ceil(days.length / n));
      return days.filter((_, i) => i % every === 0).map((d) => ({ t: Date.parse(d + 'T12:00:00Z'), label: dayShort(d) }));
    }

    // Limit hits: one swimlane per account across the range.
    function hitsChart(el, w, A, d) {
      const hs = d.limits.hits;
      if (!hs.length) { el.innerHTML = '<p class="state-msg">No limit hits in this range.</p>'; return; }
      const lanes = [...new Set(hs.map((h) => h.account))];
      const padL = 120, padR = 14, lh = 30, Hh = lanes.length * lh + 26;
      const x0 = d.range.from === 1 ? Math.min(...hs.map((h) => h.ts)) : d.range.from, x1 = d.range.to;
      const x = (t) => padL + ((t - x0) / (x1 - x0)) * (w - padL - padR);
      let s = '';
      lanes.forEach((id, i) => {
        const a = A.get(id) || { label: id, color: OTHER };
        const y = i * lh + lh / 2;
        s += `<text x="${padL - 10}" y="${y + 4}" text-anchor="end" class="cx-lbl">${esc(a.label)}</text><line x1="${padL}" x2="${w - padR}" y1="${y}" y2="${y}" stroke="${C.INK.grid}"/>`;
        for (const h of hs.filter((z) => z.account === id)) {
          s += h.type === 'five_hour' ? `<circle cx="${x(h.ts)}" cy="${y}" r="5" fill="${esc(a.color)}" stroke="${C.INK.surface}" stroke-width="2"/>`
            : `<rect x="${x(h.ts) - 5}" y="${y - 5}" width="10" height="10" transform="rotate(45 ${x(h.ts)} ${y})" fill="${esc(a.color)}" stroke="${C.INK.surface}" stroke-width="2"/>`;
        }
      });
      const days = daysInRange();
      dayTicks(days, w - padL).forEach((t) => { s += `<text x="${x(t.t)}" y="${Hh - 6}" text-anchor="middle" class="cx-ax">${esc(t.label)}</text>`; });
      el.innerHTML = `<svg width="${w}" height="${Hh}" role="img" aria-label="Limit hits over time">${s}</svg>`;
      const svg = el.querySelector('svg');
      svg.addEventListener('mousemove', (e) => {
        const r = svg.getBoundingClientRect(); const lx = e.clientX - r.left, ly = e.clientY - r.top;
        const lane = lanes[Math.floor(ly / lh)];
        const near = hs.filter((h) => h.account === lane).map((h) => ({ h, dx: Math.abs(x(h.ts) - lx) })).sort((p, q) => p.dx - q.dx)[0];
        if (!near || near.dx > 12) { C.hideTip(); return; }
        const a = A.get(lane) || { label: lane, color: OTHER };
        const h = near.h;
        C.showTip(`<div class="cx-h">${esc(when(h.ts))}</div>${tipRow(a.color, a.label, h.type === 'five_hour' ? '5-hour limit' : 'Weekly limit')}${h.waitH != null ? tipRow(null, 'Reset after', dur(h.waitH * H)) : ''}`, e.clientX, e.clientY);
      });
      svg.addEventListener('mouseleave', () => C.hideTip());
    }
    // Accessible table alternative under each bar chart.
    function drawTables(A, d) {
      const days = daysInRange();
      const mk = (cols, rows) => `<table class="an-table"><thead><tr>${cols.map((c, i) => `<th${i ? ' class="num"' : ''}>${esc(c)}</th>`).join('')}</tr></thead><tbody>${rows.map((r) => `<tr>${r.map((c, i) => `<td${i ? ' class="num"' : ''}>${esc(c)}</td>`).join('')}</tr>`).join('')}</tbody></table>`;
      root.querySelectorAll('[data-table]').forEach((el) => {
        if (el.dataset.table === 'dailyPct') {
          const ss = [...A.values()].filter((a) => d.limits.dailyPct.some((x) => x.byAccount[a.id]));
          const map = new Map(d.limits.dailyPct.map((x) => [x.day, x.byAccount]));
          el.innerHTML = mk(['Day', ...ss.map((a) => a.label)], days.filter((x) => map.has(x)).map((x) => [dayLong(x), ...ss.map((a) => pts(map.get(x)[a.id] || 0))]));
        } else if (el.dataset.table === 'spend') {
          const ss = spendSeries(A);
          const map = new Map(d.tokens.daily.map((x) => [x.day, st.spendBy === 'model' ? x.byModel : x.byAccount]));
          el.innerHTML = mk(['Day', ...ss.map((s) => s.label), 'Total'], days.filter((x) => map.has(x)).map((x) => { const m = map.get(x); return [dayLong(x), ...ss.map((s) => usd(m[s.id] || 0)), usd(Object.values(m).reduce((a, b) => a + b, 0))]; }));
        }
      });
    }

    // CSSOM application of data-* sizes/colours (CSP blocks style attributes).
    function apply(el) {
      el.querySelectorAll('[data-w]').forEach((x) => { x.style.width = x.dataset.w + '%'; if (x.dataset.c) x.style.background = x.dataset.c; });
      el.querySelectorAll('[data-bg]').forEach((x) => { x.style.background = x.dataset.bg; });
      el.querySelectorAll('[data-l]').forEach((x) => { x.style.left = x.dataset.l + '%'; });
      el.querySelectorAll('[data-h]').forEach((x) => { x.style.height = x.dataset.h + '%'; });
      el.querySelectorAll('.an-status[data-c]').forEach((x) => { x.style.borderColor = x.dataset.c; });
    }

    // ---------- events ----------
    root.addEventListener('click', (e) => {
      const r = e.target.closest('[data-range]');
      if (r) { st.range = r.dataset.range; save(); load(); return; }
      const ac = e.target.closest('[data-acct]');
      if (ac) {
        const id = ac.dataset.acct;
        const all = (st.data ? st.data.accounts : []).map((a) => a.id).filter((x) => x !== 'unattributed');
        if (id === '*') st.accounts = null;
        else {
          let cur = st.accounts ? st.accounts.slice() : all.slice();
          cur = cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id];
          st.accounts = !cur.length || cur.length === all.length ? null : cur;
        }
        save(); load(); return;
      }
      const sp = e.target.closest('[data-spend]');
      if (sp) { st.spendBy = sp.dataset.spend; save(); paint(); return; }
      const j = e.target.closest('[data-jump]');
      if (j) { if (opts.onJump) opts.onJump(j.dataset.jump, j.dataset.url); else window.open(j.dataset.url, '_blank', 'noopener'); }
    });
    root.addEventListener('change', (e) => {
      if (e.target.matches('[data-tables]')) { st.tables = e.target.checked; save(); paint(); }
    });
    let rt;
    const ro = new ResizeObserver(() => { clearTimeout(rt); rt = setTimeout(draw, 120); });
    ro.observe(root);

    return { refresh: load, isLoaded: () => !!st.data };
  }

  window.Analytics = { mount };
})();
