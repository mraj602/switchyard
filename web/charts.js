// Tiny SVG chart kit for the Analytics view (no dependencies; works under a strict CSP —
// only SVG presentation attributes, never inline style). Every chart returns markup plus a
// hover handler; tooltips are one shared element. Rules followed (dataviz skill): one y-axis,
// thin marks, 2px gaps between stacked segments, rounded data-ends, recessive grid, text in
// ink colours, hit targets wider than the marks.
(function () {
  'use strict';
  const INK = { text: '#E8EAED', text2: '#C9CED6', muted: '#9AA1AC', grid: '#262A33', axis: '#383C46', surface: '#171A21' };
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const f1 = (n) => (Math.round(n * 10) / 10).toString();

  // "nice" axis max and ticks
  function niceMax(v, n = 4) {
    if (!(v > 0)) return { max: 1, step: 0.25 };
    const raw = v / n, mag = 10 ** Math.floor(Math.log10(raw));
    const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw);
    return { max: Math.ceil(v / step) * step, step };
  }

  // ---------- tooltip ----------
  let tip;
  function showTip(html, x, y) {
    if (!tip) { tip = document.createElement('div'); tip.className = 'cx-tip'; tip.setAttribute('role', 'tooltip'); document.body.appendChild(tip); }
    tip.innerHTML = html;
    tip.hidden = false;
    const r = tip.getBoundingClientRect();
    let left = x + 14, top = y + 14;
    if (left + r.width > window.innerWidth - 8) left = x - r.width - 14;
    if (top + r.height > window.innerHeight - 8) top = y - r.height - 14;
    tip.style.left = Math.max(8, left) + 'px';
    tip.style.top = Math.max(8, top) + 'px';
  }
  function hideTip() { if (tip) tip.hidden = true; }
  const tipRow = (color, label, value) => `<div class="cx-row">${color ? `<svg width="10" height="10" aria-hidden="true"><rect width="10" height="10" rx="2" fill="${esc(color)}"/></svg>` : '<span class="cx-sp"></span>'}<span>${esc(label)}</span><b>${esc(value)}</b></div>`;

  // Attach hover to a rendered chart container. getTip(evt, localX, localY) → html|null
  function hover(el, getTip, onMove) {
    const svg = el.querySelector('svg');
    if (!svg) return;
    svg.addEventListener('mousemove', (e) => {
      const r = svg.getBoundingClientRect();
      const html = getTip(e, e.clientX - r.left, e.clientY - r.top);
      if (onMove) onMove(e.clientX - r.left, html != null);
      if (html) showTip(html, e.clientX, e.clientY); else hideTip();
    });
    svg.addEventListener('mouseleave', () => { hideTip(); if (onMove) onMove(null, false); });
  }

  // ---------- stacked bars over categories (days) ----------
  // o: {width, height, cats:[label], series:[{id,label,color}], value(catIdx, seriesId), fmt, fmtAxis, title}
  function bars(el, o) {
    const W = o.width, Hh = o.height || 220, padL = 52, padR = 12, padT = 10, padB = 28;
    const iw = W - padL - padR, ih = Hh - padT - padB;
    const n = o.cats.length;
    const totals = o.cats.map((_, i) => o.series.reduce((s, se) => s + Math.max(0, o.value(i, se.id) || 0), 0));
    const { max, step } = niceMax(Math.max(...totals, 0));
    const y = (v) => padT + ih - (v / max) * ih;
    const band = iw / Math.max(1, n);
    const bw = Math.max(2, Math.min(28, band * 0.68));
    let g = '';
    for (let t = 0; t <= max + 1e-9; t += step) g += `<line x1="${padL}" x2="${W - padR}" y1="${y(t)}" y2="${y(t)}" stroke="${INK.grid}" stroke-width="1"/><text x="${padL - 8}" y="${y(t) + 4}" text-anchor="end" class="cx-ax">${esc((o.fmtAxis || o.fmt)(t))}</text>`;
    let marks = '';
    o.cats.forEach((c, i) => {
      const x = padL + band * i + (band - bw) / 2;
      let acc = 0;
      const segs = o.series.map((se) => ({ se, v: Math.max(0, o.value(i, se.id) || 0) })).filter((s) => s.v > 0);
      segs.forEach((s, k) => {
        const y0 = y(acc), y1 = y(acc + s.v);
        const top = k === segs.length - 1;
        const h = Math.max(0, y0 - y1 - (k ? 2 : 0)); // 2px surface gap between segments
        const yy = y1;
        if (h <= 0) { acc += s.v; return; }
        const r = top ? Math.min(4, bw / 2, h) : 0;
        marks += r ? `<path d="M${x},${yy + h} V${yy + r} Q${x},${yy} ${x + r},${yy} H${x + bw - r} Q${x + bw},${yy} ${x + bw},${yy + r} V${yy + h} Z" fill="${esc(s.se.color)}"/>`
          : `<rect x="${x}" y="${yy}" width="${bw}" height="${h}" fill="${esc(s.se.color)}"/>`;
        acc += s.v;
      });
    });
    // x labels: about 8 across
    const every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(iw / 72))));
    let xl = '';
    o.cats.forEach((c, i) => { if (i % every === 0 || i === n - 1) xl += `<text x="${padL + band * i + band / 2}" y="${Hh - 8}" text-anchor="middle" class="cx-ax">${esc(o.fmtCat ? o.fmtCat(c) : c)}</text>`; });
    const base = `<line x1="${padL}" x2="${W - padR}" y1="${y(0)}" y2="${y(0)}" stroke="${INK.axis}" stroke-width="1"/>`;
    el.innerHTML = `<svg width="${W}" height="${Hh}" role="img" aria-label="${esc(o.title || '')}">${g}<g class="cx-hl"></g>${marks}${base}${xl}</svg>`;
    const hl = el.querySelector('.cx-hl');
    hover(el, (e, lx) => {
      const i = Math.floor((lx - padL) / band);
      if (i < 0 || i >= n) { hl.innerHTML = ''; return null; }
      hl.innerHTML = `<rect x="${padL + band * i}" y="${padT}" width="${band}" height="${ih}" fill="#FFFFFF" fill-opacity="0.05"/>`;
      const rows = o.series.map((se) => ({ se, v: o.value(i, se.id) || 0 })).filter((r) => r.v > 0).reverse();
      return `<div class="cx-h">${esc(o.fmtCatLong ? o.fmtCatLong(o.cats[i]) : o.cats[i])}</div>${rows.map((r) => tipRow(r.se.color, r.se.label, o.fmt(r.v))).join('')}${rows.length > 1 ? tipRow(null, 'Total', o.fmt(totals[i])) : ''}${rows.length ? '' : '<div class="cx-row muted">nothing recorded</div>'}`;
    });
  }

  // ---------- lines over time ----------
  // o: {width,height,x0,x1,yMax?,series:[{label,color,points:[[t,v]],dash?,step?}], marks:[{t,label,color}],
  //     bands:[{t0,t1}], now, fmtY, fmtT, title, guide:{label}}
  function lines(el, o) {
    const W = o.width, Hh = o.height || 200, padL = o.padL ?? 44, padR = 14, padT = 10, padB = 26;
    const iw = W - padL - padR, ih = Hh - padT - padB;
    const allV = o.series.flatMap((s) => s.points.map((p) => p[1])).filter((v) => v != null);
    const { max, step } = o.yMax ? { max: o.yMax, step: o.yStep || o.yMax / 4 } : niceMax(Math.max(...allV, 0));
    const x = (t) => padL + ((t - o.x0) / (o.x1 - o.x0)) * iw;
    const y = (v) => padT + ih - (Math.min(v, max) / max) * ih;
    let g = '';
    for (let t = 0; t <= max + 1e-9; t += step) g += `<line x1="${padL}" x2="${W - padR}" y1="${y(t)}" y2="${y(t)}" stroke="${INK.grid}"/><text x="${padL - 8}" y="${y(t) + 4}" text-anchor="end" class="cx-ax">${esc(o.fmtY(t))}</text>`;
    // x ticks
    const ticks = o.ticks || [];
    for (const t of ticks) g += `<text x="${x(t.t)}" y="${Hh - 8}" text-anchor="${t.anchor || 'middle'}" class="cx-ax">${esc(t.label)}</text>`;
    let paths = '';
    for (const b of o.bands || []) paths += `<rect x="${x(b.t0)}" y="${padT}" width="${Math.max(1, x(b.t1) - x(b.t0))}" height="${ih}" fill="#FFFFFF" fill-opacity="0.025"/>`;
    for (const s of o.series) {
      // break the line where samples are far apart (collector not running)
      let d = '', prev = null;
      for (const [t, v] of s.points) {
        if (v == null) { prev = null; continue; }
        const gap = s.maxGap && prev && t - prev[0] > s.maxGap;
        d += (!prev || gap ? 'M' : 'L') + x(t).toFixed(1) + ',' + y(v).toFixed(1);
        prev = [t, v];
      }
      if (d) paths += `<path d="${d}" fill="none" stroke="${esc(s.color)}" stroke-width="${s.width || 2}" stroke-linejoin="round" stroke-linecap="round"${s.dash ? ' stroke-dasharray="4 4"' : ''}/>`;
      if (s.points.length === 1 && s.points[0][1] != null) paths += `<circle cx="${x(s.points[0][0])}" cy="${y(s.points[0][1])}" r="4" fill="${esc(s.color)}" stroke="${INK.surface}" stroke-width="2"/>`;
    }
    for (const m of o.marks || []) paths += `<g><line x1="${x(m.t)}" x2="${x(m.t)}" y1="${padT}" y2="${padT + ih}" stroke="${esc(m.color || INK.muted)}" stroke-dasharray="2 3"/>${m.label ? `<text x="${x(m.t) + 4}" y="${padT + 10}" class="cx-ax">${esc(m.label)}</text>` : ''}</g>`;
    for (const p of o.dots || []) paths += `<circle cx="${x(p.t)}" cy="${y(p.v)}" r="4.5" fill="${esc(p.color)}" stroke="${INK.surface}" stroke-width="2"/>`;
    const base = `<line x1="${padL}" x2="${W - padR}" y1="${y(0)}" y2="${y(0)}" stroke="${INK.axis}"/>`;
    el.innerHTML = `<svg width="${W}" height="${Hh}" role="img" aria-label="${esc(o.title || '')}">${g}${paths}${base}<g class="cx-hl"></g></svg>`;
    const hl = el.querySelector('.cx-hl');
    hover(el, (e, lx) => {
      if (lx < padL || lx > W - padR) { hl.innerHTML = ''; return null; }
      const t = o.x0 + ((lx - padL) / iw) * (o.x1 - o.x0);
      const rows = [];
      let ht = null;
      for (const s of o.series) {
        if (s.noTip) continue;
        let best = null;
        for (const p of s.points) if (p[1] != null && (!best || Math.abs(p[0] - t) < Math.abs(best[0] - t))) best = p;
        if (!best || Math.abs(x(best[0]) - lx) > (o.snap || 24)) continue;
        rows.push({ s, p: best }); if (ht == null) ht = best[0];
      }
      if (!rows.length) { hl.innerHTML = ''; return null; }
      hl.innerHTML = `<line x1="${x(ht)}" x2="${x(ht)}" y1="${padT}" y2="${padT + ih}" stroke="${INK.muted}" stroke-width="1"/>` +
        rows.map((r) => `<circle cx="${x(r.p[0])}" cy="${y(r.p[1])}" r="4" fill="${esc(r.s.color)}" stroke="${INK.surface}" stroke-width="2"/>`).join('');
      return `<div class="cx-h">${esc(o.fmtT(ht))}</div>${rows.map((r) => tipRow(r.s.color, r.s.label, o.fmtY(r.p[1], true))).join('')}`;
    });
  }

  // ---------- heatmap (weekday × hour) ----------
  // o: {width, rows:[label], cols:[label], values[r][c], ramp:[hex…], fmt, title, tipFor(r,c)}
  function heat(el, o) {
    const padL = 40, padT = 4, padB = 22, gap = 2;
    const cw = Math.max(8, Math.floor((o.width - padL - 4) / o.cols.length));
    const ch = Math.min(26, Math.max(14, cw * 0.8));
    const W = padL + cw * o.cols.length + 4, Hh = padT + ch * o.rows.length + padB;
    const flat = o.values.flat().filter((v) => v > 0).sort((a, b) => a - b);
    // quantile bins so a few huge hours don't flatten the rest
    const q = (p) => flat.length ? flat[Math.min(flat.length - 1, Math.floor(p * flat.length))] : 0;
    const cuts = o.ramp.slice(1).map((_, i) => q((i + 1) / o.ramp.length)); // ramp[0] = smallest non-zero
    const colorOf = (v) => (v > 0 ? o.ramp[Math.min(o.ramp.length - 1, cuts.filter((c) => v > c).length)] : o.empty);
    let cells = '';
    o.rows.forEach((rl, r) => {
      cells += `<text x="${padL - 8}" y="${padT + ch * r + ch / 2 + 4}" text-anchor="end" class="cx-ax">${esc(rl)}</text>`;
      o.cols.forEach((cl, c) => {
        cells += `<rect x="${padL + cw * c}" y="${padT + ch * r}" width="${cw - gap}" height="${ch - gap}" rx="3" fill="${colorOf(o.values[r][c])}"/>`;
      });
    });
    o.cols.forEach((cl, c) => { if (c % 3 === 0) cells += `<text x="${padL + cw * c + (cw - gap) / 2}" y="${Hh - 6}" text-anchor="middle" class="cx-ax">${esc(cl)}</text>`; });
    el.innerHTML = `<svg width="${W}" height="${Hh}" role="img" aria-label="${esc(o.title || '')}">${cells}<g class="cx-hl"></g></svg>`;
    const hl = el.querySelector('.cx-hl');
    hover(el, (e, lx, ly) => {
      const c = Math.floor((lx - padL) / cw), r = Math.floor((ly - padT) / ch);
      if (c < 0 || r < 0 || c >= o.cols.length || r >= o.rows.length) { hl.innerHTML = ''; return null; }
      hl.innerHTML = `<rect x="${padL + cw * c - 1}" y="${padT + ch * r - 1}" width="${cw - gap + 2}" height="${ch - gap + 2}" rx="4" fill="none" stroke="${INK.text}" stroke-width="1.5"/>`;
      return o.tipFor(r, c);
    });
  }

  window.Charts = { bars, lines, heat, esc, niceMax, tipRow, showTip, hideTip, f1, INK };
})();
