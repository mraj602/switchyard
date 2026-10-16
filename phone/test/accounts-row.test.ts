// The Accounts list's rules (phone.md P17.8): "most room" (effective use of both windows), the
// bar's tighter window, and the one status line of a row.
import type { Seat, Window } from '../src/contract/api';
import { mostRoom, tighterWindow } from '../src/lib/format';
import { attention, type PhoneTab } from '../src/lib/tabs';

const NOW = Date.parse('2026-10-17T12:00:00Z');
const H = 36e5;
const iso = (ms: number) => new Date(ms).toISOString();
const win = (key: string, used: number | null, resetsIn: number, more: Partial<Window> = {}): Window => ({
  key, used_pct: used, resets_at: iso(NOW + resetsIn * H), updated_at: iso(NOW - H), ...more,
});
const seat = (id: string, windows: Window[], more: Partial<Seat> = {}): Seat => ({ id, label: id, account: id, org: 'o', windows, ...more });
const name = (r: ReturnType<typeof mostRoom>) => r?.seat.id ?? null;

describe('mostRoom', () => {
  it('an idle account (no recent reading) beats one used a moment ago', () => {
    const idle = seat('idle', [win('five_hour', 0, 3), win('seven_day', 3, 100)]);
    idle.windows![0]!.updated_at = iso(NOW - 20 * H); // older than 6 h: still counts
    const busy = seat('busy', [win('five_hour', 40, 3), win('seven_day', 30, 100)]);
    const r = mostRoom([busy, idle], NOW);
    expect(name(r)).toBe('idle');
    expect([r!.five, r!.seven]).toEqual([0, 3]);
  });

  it('a window whose reset has passed counts as 0', () => {
    const stale = seat('stale', [win('five_hour', 95, -1), win('seven_day', 10, 50)]);
    const mid = seat('mid', [win('five_hour', 20, 3), win('seven_day', 20, 50)]);
    const r = mostRoom([mid, stale], NOW);
    expect(name(r)).toBe('stale');
    expect([r!.five, r!.seven]).toEqual([0, 10]);
  });

  it('a missing window counts 0 when the other one has a reading; no reading at all is skipped', () => {
    const onlyWeek = seat('week', [win('seven_day', 5, 50)]);
    const nothing = seat('none', []);
    const nulls = seat('nulls', [win('five_hour', null, 3), win('seven_day', null, 50)]);
    expect(mostRoom([nothing, nulls], NOW)).toBeNull();
    const r = mostRoom([nothing, nulls, onlyWeek], NOW);
    expect(name(r)).toBe('week');
    expect([r!.five, r!.seven]).toEqual([0, 5]);
  });

  it('hidden accounts are skipped, and nothing is named at 80% or more', () => {
    const hidden = seat('hid', [win('five_hour', 0, 3), win('seven_day', 0, 50)], { hidden: true });
    const full = seat('full', [win('five_hour', 10, 3), win('seven_day', 80, 50)]);
    expect(mostRoom([hidden, full], NOW)).toBeNull();
  });

  it('ties go to the lower 7 d, then the lower 5 h, then list order', () => {
    const a = seat('a', [win('five_hour', 10, 3), win('seven_day', 30, 50)]);
    const b = seat('b', [win('five_hour', 30, 3), win('seven_day', 20, 50)]);
    const c = seat('c', [win('five_hour', 30, 3), win('seven_day', 30, 50)]);
    expect(name(mostRoom([a, c, b], NOW))).toBe('b'); // scores 30, 30, 30: b has the lowest 7 d
    const d = seat('d', [win('five_hour', 10, 3), win('seven_day', 30, 50)]);
    const e = seat('e', [win('five_hour', 5, 3), win('seven_day', 30, 50)]);
    expect(name(mostRoom([d, e], NOW))).toBe('e'); // same 7 d, lower 5 h
    const f = seat('f', [win('five_hour', 5, 3), win('seven_day', 30, 50)]);
    expect(name(mostRoom([e, f], NOW))).toBe('e'); // identical: list order
    expect(name(mostRoom([f, e], NOW))).toBe('f');
  });
});

describe('tighterWindow', () => {
  it('shows the tighter window, 7 d on a tie, nothing without a reading', () => {
    expect(tighterWindow(seat('x', [win('five_hour', 91, 1), win('seven_day', 40, 50)]), NOW)).toMatchObject({ short: '5 h', used: 91 });
    expect(tighterWindow(seat('x', [win('five_hour', 10, 1), win('seven_day', 40, 50)]), NOW)).toMatchObject({ short: '7 d', used: 40 });
    expect(tighterWindow(seat('x', [win('five_hour', 0, 1), win('seven_day', 0, 50)]), NOW)).toMatchObject({ short: '7 d', used: 0 });
    expect(tighterWindow(seat('x', [win('five_hour', 95, -1), win('seven_day', 10, 50)]), NOW)).toMatchObject({ short: '7 d', used: 10 });
    expect(tighterWindow(seat('x', []), NOW)).toBeNull();
  });
});

describe('attention', () => {
  const none: PhoneTab = { kind: 'none' };
  const out = { machine: 'm1', machine_name: 'mraj602', slot: 's', since: iso(NOW) };
  const text = (r: ReturnType<typeof attention>) => r?.line.parts.map((p) => p.t).join('');

  it('is null when nothing needs attention', () => {
    expect(attention(seat('x', [win('five_hour', 10, 1), win('seven_day', 40, 50)]), none, NOW)).toBeNull();
  });

  it('puts the tab signed in as another account first, then the limit, then signed out, with +N', () => {
    const other = seat('claude-6', []);
    const s = seat('x', [win('five_hour', 50, 1), win('seven_day', 92, 50)], { signed_out: [out] });
    const r = attention(s, { kind: 'other', at: NOW, seat: other }, NOW);
    expect(text(r)).toBe('Phone tab is signed in as claude-6');
    expect(r!.more).toBe(2);
    expect(text(attention(s, none, NOW))).toMatch(/^7-day limit nearly used, resets /);
    expect(attention(s, none, NOW)!.more).toBe(1);
    expect(text(attention(seat('y', [win('five_hour', 1, 1)], { signed_out: [out] }), none, NOW))).toBe('Claude Code signed out on mraj602');
  });

  it('flags a runaway 5 h pace with when it is full', () => {
    const full = iso(NOW + H);
    const s = seat('x', [win('five_hour', 60, 2, { pace: { band: 'runaway', projected_pct: 140, full_at: full } }), win('seven_day', 10, 50)]);
    expect(text(attention(s, none, NOW))).toMatch(/^Near the 5-hour limit: full by /);
  });
});
