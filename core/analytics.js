// Analytics over the usage store. Pure computation: (store, query) → one JSON document the
// Analytics view renders. Used by the desktop app (IPC) and the self-hosted server (HTTP).
//
// Two sources, kept apart because they measure different things:
//   limits  — % of each account's 5-hour / 7-day / model windows, sampled from claude.ai.
//             Official numbers, but only while a collector was running.
//   tokens  — Claude Code API responses from local transcripts, priced at API rates.
//             Exact counts, but Claude Code only (no chat / cowork), and the $ is an estimate.
const path = require('node:path');
const { costOf, modelLabel, rateFor } = require('./pricing');

const H = 3600e3, D = 864e5;
const DURATION = (key) => (key === 'five_hour' ? 5 * H : key === 'iguana_necktie' ? null : 7 * D);
const isWeekly = (key) => key === 'seven_day' || key.startsWith('seven_day_') || key.startsWith('weekly_');
// Pace tiers by projected % at reset (Claude-Usage-Tracker's bands).
const TIERS = [[50, 'comfortable'], [75, 'on track'], [90, 'warming'], [100, 'pressing'], [120, 'critical'], [Infinity, 'runaway']];
const tierOf = (p) => TIERS.find(([m]) => p < m)[1];
// resets_at jitters by milliseconds between polls; a cycle is identified by its reset time
// rounded to 10 minutes.
const cycleId = (iso) => { const t = Date.parse(iso); return Number.isFinite(t) ? Math.round(t / 6e5) * 6e5 : null; };

function compute(store, q = {}) {
  const db = store.raw;
  const now = q.now || Date.now();
  const tz = Number.isFinite(q.tz) ? q.tz : 0;          // minutes east of UTC (−getTimezoneOffset())
  const tzms = tz * 60e3;
  const to = q.to || now;
  const from = q.from || to - 30 * D;
  const span = to - from;
  const dayOf = (ts) => Math.floor((ts + tzms) / D);
  const dayStr = (d) => new Date(d * D).toISOString().slice(0, 10);
  const sel = Array.isArray(q.accounts) && q.accounts.length ? new Set(q.accounts) : null;
  const keep = (a) => !sel || sel.has(a);

  // ---------- accounts + session attribution ----------
  const accounts = db.prepare('SELECT id, label, color, email, plan FROM accounts ORDER BY ord').all().map((r) => ({ ...r }));
  const byEmail = new Map(accounts.filter((a) => a.email).map((a) => [a.email.toLowerCase(), a.id]));
  const sessAcct = new Map();
  const dirEmail = new Map(db.prepare('SELECT dir, email FROM config_dirs').all().map((r) => [r.dir, r.email]));
  const dirsOf = new Map();
  for (const r of db.prepare('SELECT session_id, config_dir FROM session_dirs').all()) {
    if (!dirsOf.has(r.session_id)) dirsOf.set(r.session_id, new Set());
    dirsOf.get(r.session_id).add(r.config_dir);
  }
  for (const [sid, dirs] of dirsOf) {
    const ids = new Set([...dirs].map((d) => byEmail.get((dirEmail.get(d) || '').toLowerCase())).filter(Boolean));
    if (ids.size === 1) sessAcct.set(sid, [...ids][0]);
  }
  // Remote Control bridge lines name the owning account/org: map through the config dirs'
  // account uuids (→ email → tab) or the orgs seen in usage snapshots.
  const dirByAcctUuid = new Map(db.prepare('SELECT account_uuid u, email FROM config_dirs WHERE account_uuid IS NOT NULL').all().map((r) => [r.u, r.email]));
  const acctByOrg = new Map(db.prepare('SELECT org_uuid o, account_id a, max(ts) FROM snapshots WHERE org_uuid IS NOT NULL GROUP BY org_uuid').all().map((r) => [r.o, r.a]));
  for (const r of db.prepare('SELECT session_id, account_uuid, org_uuid FROM session_owner').all()) {
    const id = byEmail.get((dirByAcctUuid.get(r.account_uuid) || '').toLowerCase()) || acctByOrg.get(r.org_uuid);
    if (id) sessAcct.set(r.session_id, id);
  }
  const sessionRows = db.prepare('SELECT session_id, account_id, cwd, remote_url FROM sessions').all();
  for (const r of sessionRows) if (r.account_id) sessAcct.set(r.session_id, r.account_id); // hooks win
  // Each account's weekly window resets at a fixed time of the week, so a 7-day limit hit's
  // reset time fingerprints the account; that attributes the session the hit happened in.
  const W = 7 * D, slot = (t) => Math.round((((t % W) + W) % W) / 6e5);
  const weekSlot = new Map(); // slot → Set(account)
  const note = (t, a) => { if (!t || !a) return; const k = slot(t); if (!weekSlot.has(k)) weekSlot.set(k, new Set()); weekSlot.get(k).add(a); };
  for (const r of db.prepare("SELECT DISTINCT account_id a, resets_at r FROM windows WHERE key = 'seven_day'").all()) note(Date.parse(r.r), r.a);
  const weeklyHits = db.prepare("SELECT session_id sid, resets_at r FROM limit_hits WHERE type = 'seven_day' AND resets_at IS NOT NULL").all();
  for (const h of weeklyHits) note(h.r, sessAcct.get(h.sid));
  for (const h of weeklyHits) {
    if (sessAcct.has(h.sid)) continue;
    const c = weekSlot.get(slot(h.r));
    if (c && c.size === 1) sessAcct.set(h.sid, [...c][0]);
  }
  const acctOf = (sid) => sessAcct.get(sid) || 'unattributed';
  const remoteOf = new Map(sessionRows.filter((r) => r.remote_url).map((r) => [r.session_id, r.remote_url]));

  // ---------- tokens ----------
  const sumCols = 'sum(input) i, sum(output) o, sum(cache_5m) c5, sum(cache_1h) c1, sum(cache_read) cr, sum(thinking) th, count(*) n';
  const toT = (r) => ({ input: r.i || 0, output: r.o || 0, cache5m: r.c5 || 0, cache1h: r.c1 || 0, cacheRead: r.cr || 0, thinking: r.th || 0, responses: r.n || 0 });
  const add = (a, b) => { for (const k of Object.keys(b)) a[k] = (a[k] || 0) + (b[k] || 0); return a; };
  const zero = () => ({ input: 0, output: 0, cache5m: 0, cache1h: 0, cacheRead: 0, thinking: 0, responses: 0, cost: 0 });

  const rows = db.prepare(`SELECT CAST((ts + ?) / ${D} AS INT) day, session_id sid, model, project, ${sumCols}, min(ts) t0, max(ts) t1
    FROM tokens WHERE ts >= ? AND ts < ? GROUP BY day, sid, model, project`).all(tzms, from, to);

  const totals = zero();
  const daily = new Map();       // day → {byAccount:{}, byModel:{}, tokens}
  const models = new Map();      // label → totals
  const projects = new Map();    // path → {…}
  const sessions = new Map();    // sid → {…}
  const acctTok = new Map();     // account → totals
  let unpriced = 0;
  const costMix = { input: 0, output: 0, cacheWrite: 0, cacheRead: 0 };
  for (const r of rows) {
    if (!keep(acctOf(r.sid))) continue;
    const t = toT(r);
    const cost = costOf(r.model, t);
    if (cost == null) unpriced += t.responses;
    const rt = rateFor(r.model);
    if (rt) {
      costMix.input += t.input * rt.input / 1e6; costMix.output += t.output * rt.output / 1e6;
      costMix.cacheWrite += (t.cache5m * rt.write5m + t.cache1h * rt.write1h) / 1e6; costMix.cacheRead += t.cacheRead * rt.read / 1e6;
    }
    t.cost = cost || 0;
    const acct = acctOf(r.sid);
    const ml = modelLabel(r.model);
    add(totals, t);
    if (!daily.has(r.day)) daily.set(r.day, { byAccount: {}, byModel: {}, tokens: zero() });
    const dd = daily.get(r.day);
    dd.byAccount[acct] = (dd.byAccount[acct] || 0) + t.cost;
    dd.byModel[ml] = (dd.byModel[ml] || 0) + t.cost;
    add(dd.tokens, t);
    add(models.get(ml) || models.set(ml, zero()).get(ml), t);
    add(acctTok.get(acct) || acctTok.set(acct, zero()).get(acct), t);
    const pk = r.project || '(unknown)';
    if (!projects.has(pk)) projects.set(pk, { path: pk, name: path.basename(pk) || pk, ...zero(), sessions: new Set(), accounts: new Set(), last: 0 });
    const p = projects.get(pk);
    add(p, t); p.sessions.add(r.sid); p.accounts.add(acct); p.last = Math.max(p.last, r.t1);
    if (r.sid) {
      if (!sessions.has(r.sid)) sessions.set(r.sid, { id: r.sid, account: acct, project: path.basename(r.project || '') , start: r.t0, end: r.t1, models: new Set(), ...zero(), url: remoteOf.get(r.sid) || null });
      const s = sessions.get(r.sid);
      add(s, t); s.models.add(ml); s.start = Math.min(s.start, r.t0); s.end = Math.max(s.end, r.t1);
    }
  }
  const cacheHit = (t) => { const all = t.input + t.cache5m + t.cache1h + t.cacheRead; return all ? t.cacheRead / all : null; };

  // Previous period of the same length, for deltas on the KPI tiles.
  const prevRows = db.prepare(`SELECT session_id sid, model, ${sumCols} FROM tokens WHERE ts >= ? AND ts < ? GROUP BY sid, model`).all(from - span, from);
  const prev = zero();
  for (const r of prevRows) { if (!keep(acctOf(r.sid))) continue; const t = toT(r); t.cost = costOf(r.model, t) || 0; add(prev, t); }

  // Weekday × hour heatmap (local time) of API-equivalent $.
  const heatRows = db.prepare(`SELECT CAST(((ts + ?) / ${H}) % 24 AS INT) h, CAST(((ts + ?) / ${D} + 4) % 7 AS INT) wd, session_id sid, model, ${sumCols}
    FROM tokens WHERE ts >= ? AND ts < ? GROUP BY h, wd, sid, model`).all(tzms, tzms, from, to);
  const heat = Array.from({ length: 7 }, () => Array(24).fill(0)); // [weekday 0=Sun][hour]
  const heatN = Array.from({ length: 7 }, () => Array(24).fill(0));
  for (const r of heatRows) { if (!keep(acctOf(r.sid))) continue; const t = toT(r); heat[r.wd][r.h] += costOf(r.model, t) || 0; heatN[r.wd][r.h] += t.responses; }

  // ---------- limits ----------
  const wrows = db.prepare(`SELECT ts, account_id a, key, label, pct, resets_at r, used_dollars ud, limit_dollars ld
    FROM windows WHERE ts >= ? AND ts < ? ORDER BY ts`).all(from - 8 * D, Math.max(to, now) + 1);
  const series = new Map(); // a|key → [{ts,pct,r,ud,ld}]
  const labels = {};
  for (const w of wrows) {
    const k = `${w.a}|${w.key}`;
    labels[w.key] = w.label || w.key;
    if (!series.has(k)) series.set(k, []);
    series.get(k).push({ ts: w.ts, pct: w.pct, cyc: cycleId(w.r), r: w.r, ud: w.ud, ld: w.ld });
  }

  const cycles = [];       // completed + current windows
  const pace = [];         // current state per account × window
  const dailyPct = new Map(); // day → {account: pct points of the 7-day window used}
  const credits = [];      // cloud-session credit series
  const quality = new Map();
  for (const [k, pts] of series) {
    const [acct, key] = k.split('|');
    if (!keep(acct)) continue;
    const dur = DURATION(key);
    if (key === 'iguana_necktie') {
      credits.push({ account: acct, points: pts.filter((p) => p.ts >= from).map((p) => [p.ts, p.ud ?? null, p.ld ?? null]) });
      continue;
    }
    // group by cycle
    const byCyc = new Map();
    for (const p of pts) { if (p.cyc == null) continue; if (!byCyc.has(p.cyc)) byCyc.set(p.cyc, []); byCyc.get(p.cyc).push(p); }
    for (const [cyc, cp] of byCyc) {
      const start = cyc - dur;
      if (cyc < from || start > to) continue;
      const peak = Math.max(...cp.map((p) => p.pct ?? 0));
      const hit = cp.find((p) => (p.pct ?? 0) >= 100);
      cycles.push({ account: acct, key, start, end: cyc, done: cyc <= now, peak, last: cp[cp.length - 1].pct, hitAt: hit ? hit.ts : null,
        samples: cp.length, firstSample: cp[0].ts, lastSample: cp[cp.length - 1].ts,
        points: cp.map((p) => [p.ts, p.pct]) });
      // positive deltas inside a cycle → per-day consumption (7-day window only)
      if (key === 'seven_day') {
        for (let i = 1; i < cp.length; i++) {
          const d = (cp[i].pct ?? 0) - (cp[i - 1].pct ?? 0);
          if (d <= 0 || cp[i].ts < from || cp[i].ts >= to) continue;
          const day = dayOf(cp[i].ts);
          if (!dailyPct.has(day)) dailyPct.set(day, {});
          const o = dailyPct.get(day); o[acct] = (o[acct] || 0) + d;
        }
      }
    }
    // current pace
    const lastP = pts[pts.length - 1];
    if (lastP && lastP.cyc && lastP.cyc > now && now - lastP.ts < 2 * H) {
      const cur = byCyc.get(lastP.cyc) || [lastP];
      const start = lastP.cyc - dur;
      const elapsed = Math.max(0, Math.min(1, (now - start) / dur));
      const actual = lastP.pct ?? 0;
      // Rate: since window start for weekly windows; recent slope for the 5-hour window.
      let rate = elapsed > 0 ? actual / ((now - start) / H) : 0; // %/hour
      if (key === 'five_hour') {
        const recent = cur.filter((p) => p.ts >= now - 1.5 * H);
        if (recent.length >= 2 && recent[recent.length - 1].ts > recent[0].ts) {
          rate = Math.max(0, ((recent[recent.length - 1].pct ?? 0) - (recent[0].pct ?? 0)) / ((recent[recent.length - 1].ts - recent[0].ts) / H));
        }
      }
      const hoursLeft = (lastP.cyc - now) / H;
      const projected = actual + rate * hoursLeft;
      const etaH = actual >= 100 ? 0 : rate > 0 ? (100 - actual) / rate : null;
      pace.push({ account: acct, key, label: labels[key], actual, expected: elapsed * 100, delta: actual - elapsed * 100, ratePerHour: rate,
        resetsAt: lastP.cyc, hoursLeft, projected, etaAt: etaH == null ? null : now + etaH * H,
        lastsToReset: etaH == null || etaH >= hoursLeft, tier: actual >= 100 ? 'limited' : tierOf(projected),
        early: elapsed < (key === 'five_hour' ? 0.2 : 0.1), sampledAt: lastP.ts });
    }
    // data quality (sampling coverage inside the range)
    const inRange = pts.filter((p) => p.ts >= from && p.ts < to);
    const qd = quality.get(acct) || { account: acct, samples: 0, first: null, last: null, gaps: 0, covered: 0 };
    if (key === 'five_hour' || (key === 'seven_day' && !quality.has(acct))) {
      qd.samples = inRange.length; qd.first = inRange[0]?.ts ?? null; qd.last = inRange[inRange.length - 1]?.ts ?? null;
      qd.gaps = 0; qd.covered = 0;
      for (let i = 1; i < inRange.length; i++) { const g = inRange[i].ts - inRange[i - 1].ts; if (g > 20 * 60e3) qd.gaps++; else qd.covered += g; }
      // share of the time since the first sample in range that was sampled without gaps
      const base = qd.first != null ? Math.min(to, now) - qd.first : 0;
      qd.coverage = base > 0 ? Math.min(1, qd.covered / base) : 0;
      quality.set(acct, qd);
    }
  }
  cycles.sort((a, b) => a.end - b.end);

  // API-equivalent $ per 1% of a weekly window, per completed/current 7-day cycle: joins the
  // two sources (Claude Code spend during the cycle ÷ % it consumed). Chat/cowork also consume
  // the %, so the latest Claude Code share of the window is applied when known.
  const shareOf = new Map();
  for (const r of db.prepare(`SELECT b.account_id a, b.surface s, b.percent p FROM breakdown b
      JOIN (SELECT account_id, max(ts) m FROM breakdown GROUP BY account_id) l ON l.account_id = b.account_id AND l.m = b.ts`).all()) {
    if (!shareOf.has(r.a)) shareOf.set(r.a, {});
    shareOf.get(r.a)[r.s] = r.p;
  }
  const costIn = db.prepare('SELECT session_id sid, model, sum(input) i, sum(output) o, sum(cache_5m) c5, sum(cache_1h) c1, sum(cache_read) cr, sum(thinking) th, count(*) n FROM tokens WHERE ts >= ? AND ts < ? GROUP BY sid, model');
  for (const c of cycles) {
    if (c.key !== 'seven_day') continue;
    let usd = 0;
    for (const r of costIn.all(c.start, Math.min(c.end, now))) if (acctOf(r.sid) === c.account) usd += costOf(r.model, toT(r)) || 0;
    c.codeUsd = usd;
    const share = shareOf.get(c.account)?.claude_code;
    const codePct = c.peak * (share != null ? share / 100 : 1);
    c.usdPerPct = codePct >= 1 ? usd / codePct : null;
  }

  // Which account to use next: available now, then the one whose weekly window resets
  // soonest with allowance left (use it before it expires), then most headroom.
  const picker = accounts.filter((a) => keep(a.id)).map((a) => {
    const p7 = pace.find((p) => p.account === a.id && p.key === 'seven_day');
    const p5 = pace.find((p) => p.account === a.id && p.key === 'five_hour');
    const weekly = pace.filter((p) => p.account === a.id && isWeekly(p.key));
    const blocked = [p5, ...weekly].some((p) => p && p.actual >= 100);
    return { account: a.id, fiveHour: p5 ? p5.actual : null, fiveResetsAt: p5 ? p5.resetsAt : null,
      weekly: p7 ? p7.actual : null, weeklyResetsAt: p7 ? p7.resetsAt : null,
      headroom: p7 ? 100 - p7.actual : null, blocked, known: !!(p5 || p7) };
  }).filter((x) => x.known).sort((x, y) => (x.blocked - y.blocked)
    || ((x.headroom ?? 100) > 5 ? 0 : 1) - ((y.headroom ?? 100) > 5 ? 0 : 1)
    || (x.weeklyResetsAt ?? Infinity) - (y.weeklyResetsAt ?? Infinity)
    || (y.headroom ?? 0) - (x.headroom ?? 0));

  // Surface split history: last sample per account per day.
  const brk = db.prepare(`SELECT CAST((ts + ?) / ${D} AS INT) day, account_id a, surface s, percent p, max(ts) t
    FROM breakdown WHERE ts >= ? AND ts < ? GROUP BY day, a, s`).all(tzms, from, to);
  const surfaces = {};
  for (const r of brk) { const k = `${r.a}|${dayStr(r.day)}`; (surfaces[k] = surfaces[k] || {})[r.s] = r.p; }

  // Hook activity per day by kind.
  const ev = db.prepare(`SELECT CAST((ts + ?) / ${D} AS INT) day, coalesce(account_id,'unattributed') a, kind, count(*) n
    FROM events WHERE ts >= ? AND ts < ? GROUP BY day, a, kind`).all(tzms, from, to);
  const activity = ev.filter((r) => keep(r.a)).map((r) => ({ day: dayStr(r.day), account: r.a, kind: r.kind, n: r.n }));

  // Limit hits recorded by Claude Code itself ("You've hit your … limit"), one per window.
  const hitRows = db.prepare('SELECT session_id sid, ts, type, status, resets_at r FROM limit_hits WHERE ts >= ? AND ts < ? ORDER BY ts').all(from, to);
  const hitSeen = new Set();
  const hits = [];
  for (const h of hitRows) {
    const acct = acctOf(h.sid);
    if (!keep(acct)) continue;
    const k = `${acct}|${h.type}|${h.r || dayOf(h.ts)}`;
    if (hitSeen.has(k)) continue;
    hitSeen.add(k);
    hits.push({ ts: h.ts, account: acct, type: h.type, status: h.status, resetsAt: h.r, waitH: h.r ? Math.max(0, (h.r - h.ts) / H) : null });
  }

  const ser = (m) => [...m.values()].map((v) => ({ ...v, sessions: v.sessions ? v.sessions.size : undefined, accounts: v.accounts ? [...v.accounts] : undefined, models: v.models ? [...v.models] : undefined }));
  const firstToken = db.prepare('SELECT min(ts) t FROM tokens').get().t;
  const firstSample = db.prepare('SELECT min(ts) t FROM windows').get().t;

  return {
    generatedAt: now,
    range: { from, to, tz, days: Math.round(span / D) },
    since: { tokens: firstToken, limits: firstSample },
    accounts: [...accounts, ...(acctTok.has('unattributed') ? [{ id: 'unattributed', label: 'Unattributed', color: null }] : [])],
    labels,
    tokens: {
      totals: { ...totals, cacheHit: cacheHit(totals), sessions: sessions.size, activeDays: daily.size, unpriced },
      previous: { ...prev, cacheHit: cacheHit(prev) },
      costMix,
      byAccount: Object.fromEntries([...acctTok].map(([k, v]) => [k, { ...v, cacheHit: cacheHit(v) }])),
      daily: [...daily].sort((a, b) => a[0] - b[0]).map(([d, v]) => ({ day: dayStr(d), ...v, tokens: { ...v.tokens, cacheHit: cacheHit(v.tokens) } })),
      models: ser(models).map((m, i) => ({ model: [...models.keys()][i], ...m })).sort((a, b) => b.cost - a.cost),
      projects: ser(projects).sort((a, b) => b.cost - a.cost).slice(0, 20),
      sessions: ser(sessions).sort((a, b) => b.cost - a.cost).slice(0, 25),
      heat, heatN,
    },
    limits: {
      pace, picker, cycles, hits,
      dailyPct: [...dailyPct].sort((a, b) => a[0] - b[0]).map(([d, v]) => ({ day: dayStr(d), byAccount: v })),
      credits, surfaces,
      quality: [...quality.values()],
    },
    activity,
  };
}

module.exports = { compute };
