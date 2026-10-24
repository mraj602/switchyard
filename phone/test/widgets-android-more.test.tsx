// The Needs-you list, Account ring, Next reset and Machines Android widgets (phone.md P11): the
// snapshot additions, the pure helpers and each component's tree (empty, normal, many, idle).
import { readFileSync } from 'fs';
import { join } from 'path';

import type { Overview, Seat } from '../src/contract/api';
import { SNAPSHOT_MAX_MACHINES, SNAPSHOT_NEEDS_LIST, type WidgetSnapshot } from '../src/contract/snapshot';
import { validateWidgetSnapshot } from '../src/contract/validate';
import { androidProps, buildSnapshot, limitRows } from '../src/lib/widget';
import { windowChoice, dropRingSeat, machineGrid, machineRows, needsRows, nextRefreshAt, nextResetModel, resetClock, ringModel, ringSeat, ringSvg, setRingSeat, shortAge } from '../src/lib/widget/extra';
import { createTaskHandler } from '../widgets/android/task-handler';
import { WyLimits } from '../widgets/android/WyLimits';
import { WyMachines } from '../widgets/android/WyMachines';
import { WyNeeds } from '../widgets/android/WyNeeds';
import { WyNextReset } from '../widgets/android/WyNextReset';
import { WyRing } from '../widgets/android/WyRing';
import { allOf, renderWidget } from './render-widget';

const FIX = join(__dirname, 'fixtures/hub');
const json = <T,>(f: string): T => JSON.parse(readFileSync(join(FIX, f), 'utf8')) as T;
const NOW = json<{ now_ms: number }>('_meta.json').now_ms;
const overview = json<Overview>('overview.json');
const accounts = json<{ accounts: Seat[] }>('accounts.json').accounts;
const PREFS = { private: false, origin: 'https://hub.example.ts.net' };
const MIN = 60_000;
const H = 3_600_000;
const iso = (ms: number) => new Date(ms).toISOString();

const snap = (over: Partial<WidgetSnapshot> = {}): WidgetSnapshot => ({ ...buildSnapshot(overview, accounts, NOW, PREFS), ...over });
const texts = (el: Parameters<typeof renderWidget>[0]): string[] => allOf(renderWidget(el), 'Android.TextWidget').map((n) => String(n.props.text));
const uris = (el: Parameters<typeof renderWidget>[0]): string[] =>
  [renderWidget(el)].flatMap((t) => [t, ...t.queryAll(() => true)]).map((n) => (n.props.clickActionData as { uri?: string } | undefined)?.uri).filter((u): u is string => !!u);

function memoryKV() {
  const m = new Map<string, string>();
  return { getItem: async (k: string) => m.get(k) ?? null, setItem: async (k: string, v: string) => void m.set(k, v), removeItem: async (k: string) => void m.delete(k) };
}

describe('snapshot additions', () => {
  it('needs_list: up to 3, oldest first, ids and colours, validates', () => {
    const s = snap();
    expect(s.needs_list!.length).toBeGreaterThan(0);
    expect(s.needs_list!.length).toBeLessThanOrEqual(SNAPSHOT_NEEDS_LIST);
    const since = s.needs_list!.map((n) => Date.parse(n.since));
    expect(since).toEqual([...since].sort((a, b) => a - b));
    expect(s.needs_list![0]).toEqual(expect.objectContaining({ id: expect.any(String), colour: expect.stringMatching(/^#/), project: expect.any(String) }));
    expect(validateWidgetSnapshot(s)).toEqual([]);
  });

  it('needs_list never holds message text and is empty in private mode', () => {
    const pub = JSON.stringify(buildSnapshot(overview, accounts, NOW, PREFS).needs_list);
    expect(pub).not.toContain('migration');
    const priv = buildSnapshot(overview, accounts, NOW, { ...PREFS, private: true });
    expect(priv.needs_list).toEqual([]);
    expect(validateWidgetSnapshot(priv)).toEqual([]);
    expect(validateWidgetSnapshot({ ...priv, needs_list: snap().needs_list })).not.toEqual([]);
  });

  it('machines: at most 6, online is not silent, validates', () => {
    const many: Overview = {
      ...overview,
      machines: Array.from({ length: 9 }, (_, i) => ({ id: `m${i}`, name: `machine-number-${i}`, silent: i === 1, live_sessions: i, last_seen: iso(NOW - i * H) })),
    };
    const s = buildSnapshot(many, accounts, NOW, PREFS);
    expect(s.machines).toHaveLength(SNAPSHOT_MAX_MACHINES);
    expect(s.machines![1]).toMatchObject({ online: false, live: 1 });
    expect(s.machines![0]!.name.length).toBeLessThanOrEqual(12);
    expect(validateWidgetSnapshot(s)).toEqual([]);
    expect(buildSnapshot(overview, accounts, NOW, PREFS).machines!.length).toBe(Math.min(overview.machines.length, 6));
  });

  it('an idle window is kept as 0 % with no reset time', () => {
    const idle: Seat = { id: 'z', account: 'z', org: 'o', label: 'idle', windows: [{ key: 'five_hour', used_pct: 0, resets_at: null, state: 'idle' }] };
    const s = buildSnapshot(overview, [idle], NOW, PREFS);
    expect(s.seats[0]!.five_hour).toEqual({ pct: 0, resets_at: null });
    expect(validateWidgetSnapshot(s)).toEqual([]);
    expect(limitRows(s, NOW)[0]).toMatchObject({ pct: 0, idle: true, window: '5 h', words: 'not started' });
    expect(s.next_reset).toBeNull();
  });
});

describe('helpers', () => {
  it('shortAge, resetClock, grid', () => {
    expect(shortAge(iso(NOW - 4 * MIN), NOW)).toBe('4 min');
    expect(shortAge(iso(NOW - 3 * H), NOW)).toBe('3 h');
    expect(shortAge(iso(NOW), NOW)).toBe('now');
    expect(resetClock(NOW + H, NOW, 'UTC')).toMatch(/^\d\d:\d\d$/);
    expect(resetClock(NOW + 30 * H, NOW, 'UTC')).toMatch(/^\w{3} \d\d:\d\d$/);
    expect(machineGrid(320, 70, 6)).toEqual({ perLine: 2, shown: 2 });
    expect(machineGrid(undefined, undefined, 2)).toEqual({ perLine: 3, shown: 2 });
  });

  it('next refresh is just after the soonest reset still ahead', () => {
    const s = snap();
    const n = nextResetModel(s, NOW, 'UTC')!;
    expect(nextRefreshAt(s, NOW)).toBe(Date.parse(n.resetsAt) + 5000);
    expect(nextRefreshAt(snap({ seats: [] }), NOW)).toBeNull();
  });

  it('next reset ignores idle windows', () => {
    const s = snap({ seats: [{ id: 'a', label: 'a', colour: '#111111', five_hour: { pct: 0, resets_at: null }, seven_day: { pct: 5, resets_at: iso(NOW + 2 * H) } }] });
    expect(nextResetModel(s, NOW, 'UTC')).toMatchObject({ window: '7 d' });
    expect(nextResetModel(snap({ seats: [{ id: 'a', label: 'a', colour: '#111111', five_hour: { pct: 0, resets_at: null } }] }), NOW)).toBeNull();
  });

  it('the ring picks the seat, the tighter window, and says not started when idle', () => {
    const s = snap();
    const id = s.seats[0]!.id;
    const m = ringModel(s, id, NOW, 'UTC')!;
    expect(m.label).toBe(s.seats[0]!.label);
    expect(m.caption).toMatch(/^resets /);
    expect(ringModel(s, 'gone', NOW)).toBeNull();
    const idle = snap({ seats: [{ id: 'i', label: 'idle', colour: '#222222', five_hour: { pct: 0, resets_at: null } }] });
    expect(ringModel(idle, 'i', NOW)).toMatchObject({ pct: 0, caption: 'not started' });
  });

  it('ring svg: empty, partial, full', () => {
    expect(ringSvg(0, '#fff')).not.toContain('<path');
    expect(ringSvg(30, '#fff')).toContain('<path');
    expect(ringSvg(100, '#fff')).toContain('stroke="#fff"');
  });

  it('per-widget account storage', async () => {
    const kv = memoryKV();
    await setRingSeat(kv, 7, 'seat_a');
    await setRingSeat(kv, 8, 'seat_b');
    expect(await ringSeat(kv, 7)).toBe('seat_a');
    await dropRingSeat(kv, 7);
    expect(await ringSeat(kv, 7)).toBeUndefined();
    expect(await ringSeat(kv, 8)).toBe('seat_b');
  });
});

describe('WyNeeds', () => {
  it('no snapshot: open the app', () => {
    expect(texts(<WyNeeds {...androidProps(null, NOW)} />)).toContain('Open Watchyard once to load');
  });
  it('normal: header with the count, rows, one tap target each', () => {
    const s = snap();
    const t = texts(<WyNeeds {...androidProps(s, NOW + MIN)} />);
    expect(t[0]).toBe('Watchyard · Needs you');
    expect(t[1]).toBe(`${s.counts!.needs_you} waiting`);
    expect(t.some((x) => /· (asked|needs permission|failed)$/.test(x))).toBe(true);
    expect(uris(<WyNeeds {...androidProps(s, NOW)} />).filter((u) => u.startsWith('watchyard://sessions/'))).toHaveLength(needsRows(s, NOW).length);
  });
  it('many: at most 3 rows; a short widget shows fewer', () => {
    const base = snap().needs_list![0]!;
    const four = snap({ needs_you: 9, counts: { working: 0, needs_you: 9, done: 0 }, needs_list: [0, 1, 2].map((i) => ({ ...base, id: `s${i}`, since: iso(NOW - i * MIN) })) });
    expect(needsRows(four, NOW).map((r) => r.id)).toEqual(['s2', 's1', 's0']);
    expect(texts(<WyNeeds {...androidProps(four, NOW, undefined, 200)} />).filter((x) => x.includes('·')).length).toBe(1 + 3);
    expect(texts(<WyNeeds {...androidProps(four, NOW, undefined, 80)} />).filter((x) => x.includes('·')).length).toBe(1 + 1);
  });
  it('empty: Nothing needs you', () => {
    const t = texts(<WyNeeds {...androidProps(snap({ needs_you: 0, counts: { working: 1, needs_you: 0, done: 0 }, needs_list: [], top: null }), NOW)} />);
    expect(t).toContain('Nothing needs you');
    expect(t[0]).toBe('Watchyard · Needs you');
    expect(t[1]).toBe('all clear');
  });
  it('private: the count without a list', () => {
    const t = texts(<WyNeeds {...androidProps(buildSnapshot(overview, accounts, NOW, { ...PREFS, private: true }), NOW)} />);
    expect(t.join('|')).toContain('waiting');
    expect(t).toContain('Open Watchyard to see which');
  });
});

describe('WyRing', () => {
  it('no snapshot / unknown account', () => {
    expect(texts(<WyRing {...androidProps(null, NOW)} />)).toContain('Open Watchyard once to load');
    expect(texts(<WyRing {...androidProps(snap(), NOW, undefined, undefined, undefined, 'gone')} />).join(' ')).toContain('Reconfigure');
  });
  it('normal: label, %, window, reset clock, tap opens that account', () => {
    const s = snap();
    const id = s.seats[0]!.id;
    const el = <WyRing {...androidProps(s, NOW, 'UTC', undefined, undefined, id)} />;
    const t = texts(el);
    expect(t).toContain(s.seats[0]!.label);
    expect(t.some((x) => /^\d+ %$/.test(x))).toBe(true);
    expect(t).toEqual(expect.arrayContaining(['5 h', '7 d']));
    expect(t.some((x) => /^resets /.test(x))).toBe(true);
    expect(uris(el)).toContain(`watchyard://accounts?seat=${id}`);
    expect(allOf(renderWidget(el), 'Android.SvgWidget')).toHaveLength(2); // the mark and the ring
  });
  it('idle: 0 % and not started', () => {
    const idle = snap({ seats: [{ id: 'i', label: 'idle', colour: '#222222', five_hour: { pct: 0, resets_at: null } }] });
    const t = texts(<WyRing {...androidProps(idle, NOW, undefined, undefined, undefined, 'i')} />);
    expect(t).toContain('0 %');
    expect(t).toContain('not started');
  });
  it('the handler draws the configured account and forgets it on delete', async () => {
    const store = memoryKV();
    const s = snap();
    await store.setItem('wy.widget.v1', JSON.stringify(s));
    await setRingSeat(store, 42, s.seats[1]!.id);
    const out: unknown[] = [];
    const alarm = jest.fn(async () => {});
    const h = createTaskHandler({ store, now: () => NOW, alarm });
    const info = { widgetName: 'WyRing', widgetId: 42, width: 150, height: 150 };
    await h({ widgetInfo: info, widgetAction: 'WIDGET_UPDATE', renderWidget: (t: unknown) => out.push(t) } as never);
    expect((out[0] as { props: { seat: string } }).props.seat).toBe(s.seats[1]!.id);
    expect(alarm).toHaveBeenCalledWith(nextRefreshAt(s, NOW));
    await h({ widgetInfo: info, widgetAction: 'WIDGET_DELETED', renderWidget: () => {} } as never);
    expect(await ringSeat(store, 42)).toBeUndefined();
  });
});

describe('WyNextReset', () => {
  it('no snapshot', () => {
    expect(texts(<WyNextReset {...androidProps(null, NOW)} />)).toContain('Open Watchyard once to load');
  });
  it('normal: seat, window and the time; taps Accounts', () => {
    const s = snap();
    const n = nextResetModel(s, NOW, 'UTC')!;
    const el = <WyNextReset {...androidProps(s, NOW, 'UTC')} />;
    expect(texts(el)).toEqual(['Watchyard · Next reset', n.at, `  ${n.seat} · ${n.window}`]);
    expect(texts(<WyNextReset {...androidProps(s, NOW, 'UTC', 40, 110)} />)[0]).toBe('Watchyard');
    expect(uris(el)).toContain('watchyard://accounts');
  });
  it('nothing ahead (all idle or passed)', () => {
    const t = texts(<WyNextReset {...androidProps(snap({ seats: [{ id: 'i', label: 'i', colour: '#222222', five_hour: { pct: 0, resets_at: null } }] }), NOW)} />);
    expect(t).toEqual(['Watchyard · Next reset', 'None ahead']);
  });
});

describe('WyMachines', () => {
  const ms = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `m${i}`, name: `box-${i}`, online: i !== 1, live: i === 0 ? 2 : 0, last_seen: iso(NOW - H) }));
  it('no snapshot', () => {
    expect(texts(<WyMachines {...androidProps(null, NOW)} />)).toContain('Open Watchyard once to load');
  });
  it('normal: header, live and silent notes, taps Machines', () => {
    const el = <WyMachines {...androidProps(snap({ machines: ms(3) }), NOW, undefined, 70, 320)} />;
    const t = texts(el);
    expect(t).toContain('Machines · 2 of 3 online');
    expect(t[0]).toBe('Watchyard');
    expect(t).toContain(' 2 live');
    expect(machineRows(snap({ machines: ms(3) }), NOW)[1]!.note).toBe('silent 1 h');
    expect(uris(el)).toContain('watchyard://machines');
  });
  it('many: a narrow 4x1 shows what fits; a tall one shows all 6', () => {
    const s = snap({ machines: ms(6) });
    expect(allOf(renderWidget(<WyMachines {...androidProps(s, NOW, undefined, 70, 320)} />), 'Android.TextWidget').filter((n) => String(n.props.text).startsWith('box-'))).toHaveLength(2);
    expect(allOf(renderWidget(<WyMachines {...androidProps(s, NOW, undefined, 200, 320)} />), 'Android.TextWidget').filter((n) => String(n.props.text).startsWith('box-'))).toHaveLength(6);
  });
  it('empty', () => {
    expect(texts(<WyMachines {...androidProps(snap({ machines: [] }), NOW)} />)).toContain('No machines yet');
  });
});

describe('brand header and the 5 h | 7 d toggle', () => {
  const all = [
    ['WyLimits', (p: ReturnType<typeof androidProps>) => <WyLimits {...p} />],
    ['WyRing', (p: ReturnType<typeof androidProps>) => <WyRing {...p} />],
    ['WyNeeds', (p: ReturnType<typeof androidProps>) => <WyNeeds {...p} />],
    ['WyNextReset', (p: ReturnType<typeof androidProps>) => <WyNextReset {...p} />],
    ['WyMachines', (p: ReturnType<typeof androidProps>) => <WyMachines {...p} />],
  ] as const;
  it.each(all)('%s draws the mark and the word Watchyard, with and without a snapshot', (_n, make) => {
    for (const s of [null, snap()]) {
      const tree = renderWidget(make(androidProps(s, NOW, 'UTC', undefined, undefined, snap().seats[0]!.id)));
      expect(allOf(tree, 'Android.SvgWidget')[0]!.props.svg).toContain('#E8B04B');
      expect(texts(make(androidProps(s, NOW, 'UTC', undefined, undefined, snap().seats[0]!.id))).some((x) => x.startsWith('Watchyard'))).toBe(true);
    }
  });

  it('the toggle has a 5 h and a 7 d segment, the selected one highlighted', () => {
    const tree = renderWidget(<WyLimits {...androidProps(snap(), NOW, 'UTC', 300, undefined, undefined, '7 d')} />);
    const segs = [tree, ...tree.queryAll(() => true)].filter((n) => n.props.clickAction === 'WY_TOGGLE_WINDOW');
    expect(segs.map((n) => (n.props.clickActionData as { window: string }).window)).toEqual(['5 h', '7 d']);
    expect(segs.map((n) => n.props.style as { backgroundColor: string }).map((s) => s.backgroundColor)).toEqual(['#2A2E32', '#E8B04B']);
  });

  it('the ring shows the selected window of its account', () => {
    const s = snap();
    const id = s.seats[0]!.id;
    const five = ringModel(s, id, NOW, 'UTC', '5 h')!;
    const seven = ringModel(s, id, NOW, 'UTC', '7 d')!;
    expect(five.window).toBe('5 h');
    expect(seven.window).toBe('7 d');
    expect([five.pct, five.caption]).not.toEqual([seven.pct, seven.caption]);
  });

  it('a toggle tap stores the choice for that widget and redraws, without the app; update keeps it; delete drops it', async () => {
    const store = memoryKV();
    const s = snap();
    await store.setItem('wy.widget.v1', JSON.stringify(s));
    const out: { props: { win?: string } }[] = [];
    const h = createTaskHandler({ store, now: () => NOW });
    const info = { widgetName: 'WyLimits', widgetId: 9, width: 300, height: 300 };
    const call = (widgetAction: string, extra: object = {}) => h({ widgetInfo: info, widgetAction, renderWidget: (t: unknown) => out.push(t as never), ...extra } as never);
    await call('WIDGET_UPDATE');
    expect(out.pop()!.props.win).toBe('5 h');
    await call('WIDGET_CLICK', { clickAction: 'WY_TOGGLE_WINDOW', clickActionData: { window: '7 d' } });
    expect(out.pop()!.props.win).toBe('7 d');
    expect(await windowChoice(store, 9)).toBe('7 d');
    await call('WIDGET_UPDATE');
    expect(out.pop()!.props.win).toBe('7 d');
    // another widget keeps its own choice; junk or other clicks do nothing
    expect(await windowChoice(store, 10)).toBe('5 h');
    await call('WIDGET_CLICK', { clickAction: 'WY_TOGGLE_WINDOW', clickActionData: { window: 'x' } });
    await call('WIDGET_CLICK', { clickAction: 'OPEN_URI', clickActionData: { uri: 'watchyard://now' } });
    expect(out).toHaveLength(0);
    await call('WIDGET_DELETED');
    expect(await windowChoice(store, 9)).toBe('5 h');
  });
});

describe('WyLimits idle', () => {
  it('shows an idle seat as 0% not started', () => {
    const idle = snap({ seats: [{ id: 'i', label: 'idle', colour: '#222222', five_hour: { pct: 0, resets_at: null } }] });
    const t = texts(<WyLimits {...androidProps(idle, NOW)} />);
    expect(t).toContain('0%');
    expect(t).toContain('not started');
  });
});
