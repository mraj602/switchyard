// The phone's Now header: one ring per account showing the 5-hour window (PhoneNow).

import { h } from './dom.js';
import { svg } from './ui.js';
import { seatName, seatIndex } from './seat.js';
import { clock, pct } from '../format.js';
import { shownUse, windowPhase } from './windows.js';

const R = 18, C = 2 * Math.PI * R;

function ring(seat, colour, now) {
  const w = (seat.windows || []).find((x) => x.key === 'five_hour');
  const used = shownUse(w, now);
  const phase = used == null ? null : windowPhase(w, now);
  const hot = used != null && used >= 90;
  const name = seatName(seat);
  const filled = used == null ? 0 : (Math.min(100, Math.max(0, used)) / 100) * C;
  return h('a', { class: 'ring', href: `#/accounts?seat=${encodeURIComponent(seat.id)}`, 'aria-label': `${name}, ${used == null ? 'no data' : pct(used) + ' of the 5-hour window'}` },
    svg('svg', { width: 48, height: 48, viewBox: '0 0 48 48', 'aria-hidden': 'true' },
      svg('circle', { cx: 24, cy: 24, r: R, fill: 'none', class: 'ring-track', 'stroke-width': 6 }),
      svg('circle', { cx: 24, cy: 24, r: R, fill: 'none', stroke: colour, 'stroke-width': 6, 'stroke-linecap': 'round', 'stroke-dasharray': `${filled.toFixed(1)} ${C.toFixed(1)}`, transform: 'rotate(-90 24 24)' }),
      (() => { const t = svg('text', { x: 24, y: 28, 'text-anchor': 'middle', 'font-size': used != null && used >= 100 ? 9.5 : 10.5, class: `ring-num${hot ? ' hot' : ''}` }); t.textContent = used == null ? '–' : `${Math.round(used)}%`; return t; })()),
    h('span', { class: 'ring-text' },
      h('span', { class: 'ring-name', title: name }, name),
      h('span', { class: `ring-reset${hot ? ' hot' : ''}` }, phase === 'idle' ? 'not started' : w && w.resets_at ? `${phase === 'reset' ? 'reset' : 'resets'} ${clock(w.resets_at, now)}` : used == null ? 'no data' : '')));
}

export function ringRow(seats, now) {
  const idx = seatIndex(seats);
  const shown = seats.filter((s) => !s.hidden);
  if (!shown.length) return null;
  return h('div', { class: 'rings phone-only', role: 'group', 'aria-label': '5-hour limit per account' }, shown.map((s) => ring(s, idx.get(s.id).colour, now)));
}
