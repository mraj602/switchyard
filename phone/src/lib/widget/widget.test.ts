import { readFileSync, writeFileSync } from 'fs';
import { join } from 'path';

import type { Overview, Seat } from '../../contract/api';
import type { KVStore } from '../../contract/platform';
import { SNAPSHOT_LABEL_MAX, TIMELINE_MAX, type WidgetSnapshot } from '../../contract/snapshot';
import { validateWidgetSnapshot } from '../../contract/validate';
import {
  buildSnapshot,
  buildTimeline,
  clearSnapshot,
  clock,
  emptyTimeline,
  nextResetAt,
  readSnapshot,
  resetKey,
  seatRows,
  writeSnapshot,
} from './index';

const FIX = join(__dirname, '../../../test/fixtures/hub');
const json = <T>(f: string): T => JSON.parse(readFileSync(join(FIX, f), 'utf8')) as T;
const NOW = json<{ now_ms: number }>('_meta.json').now_ms;
const overview = json<Overview>('overview.json');
const accounts = json<{ accounts: Seat[] }>('accounts.json').accounts;
const GOLDEN = join(__dirname, 'testdata/snapshot.golden.json');
const H = 3_600_000;
const iso = (ms: number) => new Date(ms).toISOString();

describe('buildSnapshot', () => {
  const prefs = { private: false, origin: 'https://hub.example.ts.net:8443' };

  it('matches the golden built from the fixtures', () => {
    const snap = buildSnapshot(overview, accounts, NOW, prefs);
    if (process.env.UPDATE_GOLDEN) writeFileSync(GOLDEN, JSON.stringify(snap, null, 2) + '\n');
    expect(snap).toEqual(JSON.parse(readFileSync(GOLDEN, 'utf8')));
    expect(validateWidgetSnapshot(snap)).toEqual([]);
  });

  it('keeps the host name only, at most 4 seats in Accounts order, labels cut to 12', () => {
    const many: Seat[] = [5, 3, 1, 4, 2, 0].map((order) => ({
      id: `s${order}`,
      account: `a${order}`,
      org: 'o',
      label: order === 3 ? 'a-very-long-account-label' : `l${order}`,
      order,
      windows: [{ key: 'five_hour', used_pct: 10, resets_at: iso(NOW + H) }],
    }));
    many.push({ id: 'hid', account: 'x', org: 'o', label: 'hidden', order: -1, hidden: true });
    const snap = buildSnapshot(overview, many, NOW, prefs);
    expect(snap.hub).toBe('hub.example.ts.net');
    expect(snap.seats.map((s) => s.id)).toEqual(['s0', 's1', 's2', 's3']);
    expect(snap.seats[3]?.label).toBe('a-very-long-'.slice(0, SNAPSHOT_LABEL_MAX));
    expect(snap.seats[3]?.label).toHaveLength(SNAPSHOT_LABEL_MAX);
  });

  it('private mode drops top and nothing else', () => {
    const pub = buildSnapshot(overview, accounts, NOW, prefs);
    const priv = buildSnapshot(overview, accounts, NOW, { ...prefs, private: true });
    expect(pub.top).not.toBeNull();
    expect(priv.top).toBeNull();
    expect(priv.private).toBe(true);
    expect({ ...priv, top: pub.top, private: false }).toEqual(pub);
    expect(validateWidgetSnapshot(priv)).toEqual([]);
    expect(JSON.stringify(priv)).not.toContain('data-pipeline');
  });

  it('has no top when nothing needs you; falls back to the overview seats; skips unknown usage', () => {
    const quiet: Overview = { ...overview, needs_you: [], counts: { needs_you: 0 }, seats: [{ id: 'x', account: 'a', org: 'o', label: 'x', windows: [{ key: 'five_hour', used_pct: null, resets_at: iso(NOW + H) }] }] };
    const snap = buildSnapshot(quiet, [], NOW, prefs);
    expect(snap.top).toBeNull();
    expect(snap.needs_you).toBe(0);
    expect(snap.seats[0]?.five_hour).toBeNull();
    expect(snap.next_reset).toBeNull();
  });

  it('never carries a token-shaped field', () => {
    const text = JSON.stringify(buildSnapshot(overview, accounts, NOW, prefs));
    expect(text).not.toMatch(/token|secret|bearer|remote_url|resume/i);
  });
});

describe('buildTimeline', () => {
  const seat = (id: string, five: number | null, seven: number | null, pct = 50): WidgetSnapshot['seats'][number] => ({
    id,
    label: id,
    colour: '#112233',
    five_hour: five === null ? null : { pct, resets_at: iso(five) },
    seven_day: seven === null ? null : { pct: pct + 1, resets_at: iso(seven) },
  });
  const snap = (asOf: number, seats: WidgetSnapshot['seats']): WidgetSnapshot => ({
    v: 1,
    as_of: iso(asOf),
    hub: 'h',
    needs_you: 1,
    top: null,
    seats,
    next_reset: null,
    private: false,
  });
  const dates = (s: WidgetSnapshot, now: number, tz?: string) => buildTimeline(s, now, tz).map((e) => e.date.getTime() - now);

  it('has now, each reset within 24 h and the stale entry, in order', () => {
    const s = snap(NOW, [seat('a', NOW + 2 * H, NOW + 30 * H), seat('b', NOW + 5 * H, NOW + 24 * H)]);
    // The 24 h edge is in; the 30 h reset is out.
    expect(dates(s, NOW)).toEqual([0, H, 2 * H, 5 * H, 24 * H]);
  });

  it('puts a reset in the window and shows it as 0% "reset" from then on', () => {
    const s = snap(NOW, [seat('a', NOW + 2 * H, NOW + 30 * H, 88)]);
    const tl = buildTimeline(s, NOW, 'UTC');
    const [first, , after] = tl;
    expect(first?.props.reset).toEqual([]);
    expect(first?.props.rows[0]).toMatchObject({ pct: 88, reset: false, resets_at: iso(NOW + 2 * H) });
    expect(first?.props.next?.resets_at).toBe(iso(NOW + 2 * H));
    expect(after?.props.reset).toEqual([resetKey('a', 'five_hour')]);
    expect(after?.props.rows[0]).toMatchObject({ pct: 0, reset: true, resets_at: null });
    // The 7-day window is still ahead and becomes the next reset.
    expect(after?.props.next).toMatchObject({ window: 'seven_day', resets_at: iso(NOW + 30 * H) });
  });

  it('adds the stale entry at as_of + 60 min with "as of HH:MM"', () => {
    const asOf = Date.parse('2026-10-13T14:02:00.000Z');
    const s = snap(asOf, [seat('a', asOf + 3 * H, null)]);
    const tl = buildTimeline(s, asOf, 'UTC');
    expect(tl.map((e) => e.date.toISOString())).toEqual(['2026-10-13T14:02:00.000Z', '2026-10-13T15:02:00.000Z', '2026-10-13T17:02:00.000Z']);
    expect(tl.map((e) => e.props.stale)).toEqual([null, 'as of 14:02', 'as of 14:02']);
  });

  it('an hour-old snapshot is stale at once and gets no extra stale entry', () => {
    const s = snap(NOW - 2 * H, [seat('a', NOW + H, null)]);
    const tl = buildTimeline(s, NOW, 'UTC');
    expect(tl.map((e) => e.date.getTime() - NOW)).toEqual([0, H]);
    expect(tl.every((e) => e.props.stale !== null)).toBe(true);
  });

  it('merges a reset that lands on the stale instant', () => {
    const s = snap(NOW, [seat('a', NOW + H, null)]);
    const tl = buildTimeline(s, NOW, 'UTC');
    expect(tl).toHaveLength(2);
    expect(tl[1]?.props.reset).toEqual([resetKey('a', 'five_hour')]);
    expect(tl[1]?.props.stale).not.toBeNull();
  });

  it('is at most 8 entries and keeps now, the stale entry and the earliest resets', () => {
    const seats = ['a', 'b', 'c', 'd'].map((id, i) => seat(id, NOW + (2 + i) * H + 7 * 60_000, NOW + (10 + i) * H + 7 * 60_000));
    const tl = buildTimeline(snap(NOW, seats), NOW, 'UTC');
    expect(tl).toHaveLength(TIMELINE_MAX);
    const offs = tl.map((e) => e.date.getTime() - NOW);
    expect(offs[0]).toBe(0);
    expect(offs).toContain(H);
    expect(offs.slice().sort((x, y) => x - y)).toEqual(offs);
    // 6 resets fit: the four 5-hour ones and the two earliest 7-day ones.
    expect(offs.filter((o) => o !== 0 && o !== H)).toEqual([2, 3, 4, 5, 10, 11].map((h) => h * H + 7 * 60_000));
  });

  it('keeps absolute instants across the Europe/London DST change (clocks back 2026-10-25 01:00Z)', () => {
    const asOf = Date.parse('2026-10-25T00:10:00.000Z'); // 01:10 BST
    const s = snap(asOf, [seat('a', Date.parse('2026-10-25T01:30:00.000Z'), Date.parse('2026-10-26T00:10:00.000Z'))]);
    const tl = buildTimeline(s, asOf, 'Europe/London');
    expect(tl.map((e) => e.date.toISOString())).toEqual([
      '2026-10-25T00:10:00.000Z',
      '2026-10-25T01:10:00.000Z', // stale: exactly 60 minutes later, even though the wall clock repeats 01:10
      '2026-10-25T01:30:00.000Z',
      '2026-10-26T00:10:00.000Z', // 24 h later is in (a 25 h wall-clock day)
    ]);
    expect(tl[1]?.props.stale).toBe('as of 01:10'); // as_of written in BST
    expect(clock('2026-10-25T00:30:00.000Z', 'Europe/London')).toBe('01:30'); // BST
    expect(clock('2026-10-25T01:30:00.000Z', 'Europe/London')).toBe('01:30'); // GMT
  });

  it('has a single "set up" entry when there is no snapshot', () => {
    const tl = emptyTimeline(NOW);
    expect(tl).toHaveLength(1);
    expect(tl[0]?.props.snapshot).toBeNull();
  });
});

describe('rows and next reset', () => {
  it('orders rings 5-hour first and falls back to 7-day only', () => {
    const snapshot = buildSnapshot(overview, accounts, NOW, { private: false, origin: 'https://h' });
    const rows = seatRows(snapshot, []);
    expect(rows.map((r) => r.id)).toEqual(['seat_personal', 'seat_work', 'seat_client', 'seat_research']);
    expect(rows[0]).toMatchObject({ window: 'five_hour', pct: 62, other: { window: 'seven_day', pct: 48 } });
    expect(nextResetAt(snapshot, NOW)?.seat).toBe('client');
    expect(nextResetAt(snapshot, NOW + 24 * 24 * H)).toBeNull();
  });
});

describe('snapshot storage', () => {
  function memory(): KVStore & { m: Map<string, string> } {
    const m = new Map<string, string>();
    return { m, getItem: async (k) => m.get(k) ?? null, setItem: async (k, v) => void m.set(k, v), removeItem: async (k) => void m.delete(k) };
  }

  it('round-trips through sy.widget.v1 and clears', async () => {
    const store = memory();
    const snap = buildSnapshot(overview, accounts, NOW, { private: false, origin: 'https://h' });
    expect(await readSnapshot(store)).toBeNull();
    await writeSnapshot(store, snap);
    expect([...store.m.keys()]).toEqual(['sy.widget.v1']);
    expect(await readSnapshot(store)).toEqual(snap);
    await clearSnapshot(store);
    expect(await readSnapshot(store)).toBeNull();
  });

  it('treats garbage as no snapshot', async () => {
    const store = memory();
    store.m.set('sy.widget.v1', '{"v":2}');
    expect(await readSnapshot(store)).toBeNull();
    store.m.set('sy.widget.v1', 'not json');
    expect(await readSnapshot(store)).toBeNull();
  });
});
