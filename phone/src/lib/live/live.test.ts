import type { SnapshotSeat, WidgetSnapshot } from '../../contract/snapshot';
import { chooseCountdown, countdownFor, endAt, planLive } from './index';

const NOW = Date.parse('2026-10-13T14:00:00.000Z');
const H = 3_600_000;
const iso = (ms: number) => new Date(ms).toISOString();

const seat = (id: string, five?: [number, number], seven?: [number, number]): SnapshotSeat => ({
  id,
  label: id.replace('seat_', ''),
  colour: '#123456',
  five_hour: five ? { pct: five[0], resets_at: iso(NOW + five[1]) } : null,
  seven_day: seven ? { pct: seven[0], resets_at: iso(NOW + seven[1]) } : null,
});
const snap = (...seats: SnapshotSeat[]): WidgetSnapshot => ({ v: 1, as_of: iso(NOW), hub: 'h', needs_you: 0, top: null, seats, next_reset: null, private: false });

describe('chooseCountdown', () => {
  it('is null under 80%', () => {
    expect(chooseCountdown(snap(seat('seat_a', [79, H]), seat('seat_b', [10, H], [79, 2 * H])), NOW)).toBeNull();
  });

  it('qualifies at exactly 80% and returns the P12 props', () => {
    expect(chooseCountdown(snap(seat('seat_a', [80, 2 * H])), NOW)).toEqual({
      seat: 'a',
      colour: '#123456',
      window: 'five_hour',
      pct: 80,
      resets_at: iso(NOW + 2 * H),
      started_at: iso(NOW),
    });
  });

  it('takes the 7-day window only within its last 8 hours', () => {
    expect(chooseCountdown(snap(seat('seat_a', undefined, [95, 8 * H + 1])), NOW)).toBeNull();
    expect(chooseCountdown(snap(seat('seat_a', undefined, [95, 8 * H])), NOW)?.window).toBe('seven_day');
    expect(chooseCountdown(snap(seat('seat_a', undefined, [95, 3 * 24 * H])), NOW)).toBeNull();
  });

  it('ignores windows that already reset', () => {
    expect(chooseCountdown(snap(seat('seat_a', [99, 0]), seat('seat_b', [99, -H])), NOW)).toBeNull();
  });

  it('picks the highest pct, then the earlier reset, then Accounts order', () => {
    const a = seat('seat_a', [90, 3 * H]);
    const b = seat('seat_b', [95, 4 * H]);
    expect(chooseCountdown(snap(a, b), NOW)?.seat).toBe('b');
    expect(chooseCountdown(snap(seat('seat_a', [95, 5 * H]), seat('seat_b', [95, 4 * H])), NOW)?.seat).toBe('b');
    expect(chooseCountdown(snap(seat('seat_a', [95, 4 * H]), seat('seat_b', [95, 4 * H])), NOW)?.seat).toBe('a');
    // Across windows of one seat the higher pct wins.
    expect(chooseCountdown(snap(seat('seat_a', [85, 2 * H], [97, 6 * H])), NOW)?.window).toBe('seven_day');
  });
});

describe('end policy', () => {
  const props = chooseCountdown(snap(seat('seat_a', [90, 2 * H])), NOW)!;

  it('dismisses 15 minutes after the reset', () => {
    expect(endAt(props).toISOString()).toBe(iso(NOW + 2 * H + 15 * 60_000));
  });

  it('countdownFor finds a seat by id or label, only when it qualifies', () => {
    const s = snap(seat('seat_a', [90, 2 * H]), seat('seat_b', [20, H]));
    expect(countdownFor(s, 'seat_a', NOW)?.seat).toBe('a');
    expect(countdownFor(s, 'a', NOW)?.seat).toBe('a');
    expect(countdownFor(s, 'seat_b', NOW)).toBeNull();
    expect(countdownFor(s, 'nope', NOW)).toBeNull();
  });

  it('planLive: starts only when asked; updates while it qualifies; ends at the reset or when it stops qualifying', () => {
    const s = snap(seat('seat_a', [90, 2 * H]));
    expect(planLive(s, NOW, null, false)).toEqual({ kind: 'none' });
    expect(planLive(s, NOW, null, true)).toMatchObject({ kind: 'start' });
    expect(planLive(snap(seat('seat_a', [10, 2 * H])), NOW, null, true)).toEqual({ kind: 'none' });
    expect(planLive(s, NOW, props, false)).toMatchObject({ kind: 'update' });
    expect(planLive(snap(seat('seat_a', [10, 2 * H])), NOW, props, false)).toEqual({ kind: 'end', at: endAt(props) });
    expect(planLive(null, NOW, props, false)).toEqual({ kind: 'end', at: endAt(props) });
    expect(planLive(s, NOW + 2 * H, props, false)).toEqual({ kind: 'end', at: endAt(props) });
  });
});
