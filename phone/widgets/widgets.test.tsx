import { readFileSync } from 'fs';
import { join } from 'path';
import * as React from 'react';
import { Platform } from 'react-native';
import type { TestInstance } from 'test-renderer';

import { allOf, renderWidget, textOf } from '../test/render-widget';
import { androidWidgetState, callsOf, widgetsState } from '../test/fakes';
import type { Overview, Seat } from '../src/contract/api';
import type { KVStore } from '../src/contract/platform';
import type { LiveCountdownProps } from '../src/contract/live';
import { buildSnapshot, buildTimeline, androidProps, type WidgetTimelineProps } from '../src/lib/widget';
import { chooseCountdown } from '../src/lib/live';
import { widgetFn } from './eval-widget';
import { AccountsMedium } from './ios/AccountsMedium';
import { NeedsYouSmall } from './ios/NeedsYouSmall';
import { ResetCountdown } from './ios/ResetCountdown';
import { clearIos, endCountdown, resetAdapter, startCountdown, syncCountdown, updateTimelines } from './ios/adapter';
import { SyAccounts } from './android/SyAccounts';
import { SyNeedsYou } from './android/SyNeedsYou';
import { clearAndroidWidgets, createTaskHandler, registerAndroidWidgets, updateAndroidWidgets } from './android/task-handler';

const FIX = join(__dirname, '../test/fixtures/hub');
const json = <T,>(f: string): T => JSON.parse(readFileSync(join(FIX, f), 'utf8')) as T;
const NOW = json<{ now_ms: number }>('_meta.json').now_ms;
const overview = json<Overview>('overview.json');
const accounts = json<{ accounts: Seat[] }>('accounts.json').accounts;
const snapshot = buildSnapshot(overview, accounts, NOW, { private: false, origin: 'https://hub.example.ts.net' });
const privateSnapshot = buildSnapshot(overview, accounts, NOW, { private: true, origin: 'https://hub.example.ts.net' });
const props = (s = snapshot, at = NOW): WidgetTimelineProps => buildTimeline(s, at, 'UTC')[0]!.props;

const render = <P extends object>(c: unknown, p: P): TestInstance => renderWidget(React.createElement(widgetFn<P>(c), p));
const texts = (n: TestInstance) => allOf(n, 'SwiftUI.Text').map(textOf);
const modsOf = (n: TestInstance) => ((n.props.modifiers as { modifier: string; args: unknown[] }[]) ?? []).map((m) => m.modifier);

beforeEach(() => {
  resetAdapter();
});

describe('widget components under the babel widgets transform', () => {
  it('are serialised to source strings (the native side evaluates them)', () => {
    expect(typeof NeedsYouSmall).toBe('string');
    expect(typeof AccountsMedium).toBe('string');
    expect(typeof ResetCountdown).toBe('string');
  });

  it('use no import or module state at run time (they evaluate with only the @expo/ui globals)', () => {
    // widgetFn evaluates them with nothing but the mocks in scope: a stray reference would throw here.
    expect(() => render(NeedsYouSmall, props())).not.toThrow();
    expect(() => render(AccountsMedium, props())).not.toThrow();
  });
});

describe('NeedsYouSmall', () => {
  it('renders the count, the top item and a ticking next reset', () => {
    const tree = render(NeedsYouSmall, props());
    expect(tree.type).toBe('SwiftUI.VStack');
    expect(modsOf(tree)).toContain('widgetURL');
    const t = texts(tree);
    expect(t).toContain('Needs you');
    expect(t).toContain('3');
    expect(t).toContain('data-pipeline · failed');
    expect(t).toContain('client resets in');
    const timer = allOf(tree, 'SwiftUI.Text').find((x) => x.props.dateStyle === 'timer')!;
    expect(timer.props.dateStyle).toBe('timer');
    expect((timer.props.date as Date).toISOString()).toBe(snapshot.next_reset!.resets_at);
    expect(callsOf('SwiftUI.modifier.widgetURL')[0]?.args).toEqual(['switchyard://now']);
  });

  it('private mode shows no project', () => {
    const t = texts(render(NeedsYouSmall, props(privateSnapshot))).join('|');
    expect(t).not.toContain('data-pipeline');
    expect(t).toContain('3');
  });

  it('after the reset the timer points at the next one and the entry reads stale', () => {
    const tl = buildTimeline(snapshot, NOW, 'UTC');
    const last = tl[tl.length - 1]!;
    const tree = render(NeedsYouSmall, last.props);
    expect(texts(tree).some((x) => x.startsWith('as of '))).toBe(true);
    const timer = allOf(tree, 'SwiftUI.Text').find((x) => x.props.dateStyle === 'timer');
    expect(timer && (timer.props.date as Date).getTime()).toBeGreaterThan(NOW);
  });

  it('says to open the app when there is no snapshot', () => {
    const tree = render(NeedsYouSmall, { snapshot: null, reset: [], stale: null, next: null, rows: [] });
    expect(texts(tree)).toContain('Open Switchyard to set up');
  });
});

describe('AccountsMedium', () => {
  it('renders four gauges with pct and reset times', () => {
    const tree = render(AccountsMedium, props());
    expect(tree.type).toBe('SwiftUI.VStack');
    const gauges = allOf(tree, 'SwiftUI.Gauge');
    expect(gauges.map((g) => g.props.value)).toEqual([0.62, 0.41, 1, 0.08]);
    const t = texts(tree);
    expect(t).toEqual(expect.arrayContaining(['personal', 'work', 'client', 'research', '62%', '100%', '7d 12%']));
    const times = allOf(tree, 'SwiftUI.Text').filter((x) => x.props.dateStyle === 'time');
    expect(times).toHaveLength(4);
    expect((times[0]!.props.date as Date).toISOString()).toBe('2026-10-13T08:07:11.742Z');
  });

  it('shows a window that has reset as 0% "reset"', () => {
    const tl = buildTimeline(snapshot, NOW, 'UTC');
    const afterClient = tl.find((e) => e.props.reset.includes('seat_client:five_hour'))!;
    const tree = render(AccountsMedium, afterClient.props);
    const g = allOf(tree, 'SwiftUI.Gauge').map((x) => x.props.value);
    expect(g[2]).toBe(0);
    expect(texts(tree)).toContain('reset');
  });

  it('says to open the app when there is no snapshot', () => {
    expect(texts(render(AccountsMedium, { snapshot: null, reset: [], stale: null, next: null, rows: [] }))).toContain('Open Switchyard to set up');
  });
});

describe('ResetCountdown (Live Activity)', () => {
  const live = chooseCountdown(snapshot, NOW)!;

  it('chooses the client 5-hour window from the fixtures', () => {
    expect(live).toMatchObject({ seat: 'client', window: 'five_hour', pct: 100 });
  });

  it('renders every region with a counting-down timerInterval', () => {
    const layout = (widgetFn<LiveCountdownProps>(ResetCountdown) as unknown as (p: LiveCountdownProps) => Record<string, React.ReactElement>)(live);
    expect(Object.keys(layout).sort()).toEqual(['banner', 'compactLeading', 'compactTrailing', 'expandedBottom', 'expandedLeading', 'expandedTrailing', 'minimal']);
    const banner = renderWidget(layout.banner!);
    const timer = allOf(banner, 'SwiftUI.Text').find((x) => x.props.timerInterval)!;
    expect(timer.props.countsDown).toBe(true);
    const range = timer.props.timerInterval as { lower: Date; upper: Date };
    expect(range.lower.toISOString()).toBe(live.started_at);
    expect(range.upper.toISOString()).toBe(live.resets_at);
    expect(texts(banner)).toEqual(expect.arrayContaining(['client', '5-hour limit at 100%', 'until it resets']));
    for (const region of ['compactTrailing', 'expandedTrailing']) {
      expect(renderWidget(layout[region]!).props.timerInterval).toBeDefined();
    }
    expect(textOf(renderWidget(layout.minimal!))).toBe('100');
    expect(textOf(renderWidget(layout.compactLeading!))).toBe('client');
  });
});

describe('iOS adapter', () => {
  const live = chooseCountdown(snapshot, NOW)!;

  it('writes both timelines', () => {
    updateTimelines(snapshot, NOW, 'UTC');
    const calls = callsOf('widgets.updateTimeline');
    expect(calls.map((c) => c.args[0])).toEqual(['NeedsYouSmall', 'AccountsMedium']);
    const entries = calls[0]!.args[1] as { date: Date }[];
    expect(entries.length).toBeGreaterThan(1);
    expect(entries.length).toBeLessThanOrEqual(8);
    expect(callsOf('widgets.createWidget').map((c) => c.args[0])).toEqual(['NeedsYouSmall', 'AccountsMedium']);
  });

  it('writes a "set up" entry without a snapshot', () => {
    updateTimelines(null, NOW);
    const entries = callsOf('widgets.updateTimeline')[0]!.args[1] as { props: WidgetTimelineProps }[];
    expect(entries).toHaveLength(1);
    expect(entries[0]!.props.snapshot).toBeNull();
  });

  it('starts at most one activity: a second start updates it, extras are ended', async () => {
    expect(await startCountdown(live, 'seat_client')).toBe('started');
    expect(await startCountdown({ ...live, pct: 99 }, 'seat_client')).toBe('updated');
    expect(callsOf('widgets.activity.start')).toHaveLength(1);
    expect(callsOf('widgets.activity.start')[0]!.args[2]).toBe('switchyard://accounts?seat=seat_client');
    expect(widgetsState.activities).toHaveLength(1);
    expect(callsOf('widgets.activity.update')).toHaveLength(1);
    // A stray second activity (say, from an older run) is ended on the next start.
    (jest.requireMock('expo-widgets') as typeof import('../test/fakes').widgets).createLiveActivity('ResetCountdown', {}).start(live);
    expect(widgetsState.activities).toHaveLength(2);
    await startCountdown(live);
    expect(widgetsState.activities).toHaveLength(1);
  });

  it('syncCountdown starts only when asked, updates a running one, and ends it after the reset', async () => {
    expect(await syncCountdown(snapshot, NOW, false)).toBe('none');
    expect(widgetsState.activities).toHaveLength(0);
    expect(await syncCountdown(snapshot, NOW, true)).toBe('started');
    expect(callsOf('widgets.activity.start')[0]!.args[2]).toBe('switchyard://accounts?seat=seat_client');
    expect(await syncCountdown(snapshot, NOW + 1000, false)).toBe('updated');
    expect(widgetsState.activities).toHaveLength(1);
    const resetMs = Date.parse(live.resets_at);
    expect(await syncCountdown(snapshot, resetMs + 1000, false)).toBe('ended');
    const end = callsOf('widgets.activity.end')[0]!;
    expect(end.args[1]).toEqual({ after: new Date(resetMs + 15 * 60_000) });
    expect(widgetsState.activities).toHaveLength(0);
  });

  it('endCountdown(immediate) and clearIos remove everything', async () => {
    await startCountdown(live);
    await clearIos(NOW);
    expect(callsOf('widgets.activity.end')[0]!.args[1]).toBe('immediate');
    expect(widgetsState.activities).toHaveLength(0);
    const entries = callsOf('widgets.updateTimeline')[0]!.args[1] as { props: WidgetTimelineProps }[];
    expect(entries[0]!.props.snapshot).toBeNull();
    await endCountdown(true); // nothing running: fine
  });

  it('does nothing off iOS', async () => {
    const spy = jest.replaceProperty(Platform, 'OS', 'android');
    updateTimelines(snapshot, NOW);
    expect(await startCountdown(live)).toBe('none');
    expect(callsOf('widgets.')).toHaveLength(0);
    spy.restore();
  });
});

describe('Android widgets', () => {
  const memory = (init?: string): KVStore & { m: Map<string, string> } => {
    const m = new Map<string, string>(init === undefined ? [] : [['sy.widget.v1', init]]);
    return { m, getItem: async (k) => m.get(k) ?? null, setItem: async (k, v) => void m.set(k, v), removeItem: async (k) => void m.delete(k) };
  };
  const aTexts = (n: TestInstance) => allOf(n, 'Android.TextWidget').map((t) => String(t.props.text));
  const info = (widgetName: string) => ({ widgetName, widgetId: 1, height: 100, width: 100, screenInfo: { screenHeightDp: 1, screenWidthDp: 1, density: 1, densityDpi: 1 } });

  it('SyNeedsYou renders the count, the top item and the reset as a clock time', () => {
    const tree = renderWidget(<SyNeedsYou {...androidProps(snapshot, NOW, 'UTC')} />);
    expect(tree.type).toBe('Android.FlexWidget');
    expect(tree.props.clickActionData).toEqual({ uri: 'switchyard://now' });
    expect(aTexts(tree)).toEqual(['Needs you', '3', 'data-pipeline · failed', 'client resets 06:36']);
  });

  it('SyNeedsYou in private mode has no project', () => {
    expect(aTexts(renderWidget(<SyNeedsYou {...androidProps(privateSnapshot, NOW, 'UTC')} />)).join('|')).not.toContain('data-pipeline');
  });

  it('SyAccounts renders up to four columns with bars, pct and clock times', () => {
    const tree = renderWidget(<SyAccounts {...androidProps(snapshot, NOW, 'UTC')} />);
    const t = aTexts(tree);
    expect(t).toEqual(expect.arrayContaining(['personal', '62%', 'resets 08:07', 'work', 'client', '100%', 'resets 06:36', 'research', '8%', '7d 48%']));
    expect(allOf(tree, 'Android.TextWidget').filter((x) => x.props.text === '62%')).toHaveLength(1);
  });

  it('SyAccounts shows a window past its reset as 0% "reset"', () => {
    const t = aTexts(renderWidget(<SyAccounts {...androidProps(snapshot, Date.parse('2026-10-13T06:40:00.000Z'), 'UTC')} />));
    expect(t).toContain('reset');
    expect(t).toContain('0%');
  });

  it('both widgets say "Open Switchyard to set up" without a snapshot', () => {
    expect(aTexts(renderWidget(<SyNeedsYou {...androidProps(null, NOW)} />))).toContain('Open Switchyard to set up');
    expect(aTexts(renderWidget(<SyAccounts {...androidProps(null, NOW)} />))).toContain('Open Switchyard to set up');
  });

  it('the task handler renders the stored snapshot for WIDGET_UPDATE', async () => {
    const store = memory(JSON.stringify(snapshot));
    const handler = createTaskHandler({ store, now: () => NOW, tz: 'UTC' });
    const rendered: React.ReactElement[] = [];
    await handler({ widgetInfo: info('SyNeedsYou'), widgetAction: 'WIDGET_UPDATE', renderWidget: (w: unknown) => void rendered.push(w as React.ReactElement) } as never);
    expect(rendered).toHaveLength(1);
    expect(aTexts(renderWidget(rendered[0]!))).toContain('3');
    await handler({ widgetInfo: info('SyAccounts'), widgetAction: 'WIDGET_ADDED', renderWidget: (w: unknown) => void rendered.push(w as React.ReactElement) } as never);
    expect(aTexts(renderWidget(rendered[1]!))).toContain('work');
  });

  it('the task handler says "Open Switchyard to set up" with no snapshot (and with a broken one)', async () => {
    for (const store of [memory(), memory('{"v":1}')]) {
      const handler = createTaskHandler({ store, now: () => NOW });
      const rendered: React.ReactElement[] = [];
      await handler({ widgetInfo: info('SyNeedsYou'), widgetAction: 'WIDGET_UPDATE', renderWidget: (w: unknown) => void rendered.push(w as React.ReactElement) } as never);
      expect(aTexts(renderWidget(rendered[0]!))).toContain('Open Switchyard to set up');
    }
  });

  it('the task handler ignores clicks, deletes and unknown widgets', async () => {
    const handler = createTaskHandler({ store: memory(JSON.stringify(snapshot)), now: () => NOW });
    const render = jest.fn();
    for (const [name, action] of [['SyNeedsYou', 'WIDGET_CLICK'], ['SyNeedsYou', 'WIDGET_DELETED'], ['Other', 'WIDGET_UPDATE']] as const) {
      await handler({ widgetInfo: info(name), widgetAction: action, renderWidget: render } as never);
    }
    expect(render).not.toHaveBeenCalled();
  });

  it('registers the handler on Android only', () => {
    registerAndroidWidgets();
    expect(androidWidgetState.taskHandler).toBeNull(); // jest-expo's default platform is iOS
    const spy = jest.replaceProperty(Platform, 'OS', 'android');
    registerAndroidWidgets();
    expect(androidWidgetState.taskHandler).not.toBeNull();
    spy.restore();
  });

  it('updateAndroidWidgets stores the snapshot and redraws both widgets; clear drops it', async () => {
    const spy = jest.replaceProperty(Platform, 'OS', 'android');
    const store = memory();
    await updateAndroidWidgets(snapshot, { store, now: () => NOW, tz: 'UTC' });
    expect(JSON.parse(store.m.get('sy.widget.v1')!)).toEqual(snapshot);
    expect(callsOf('androidWidget.requestWidgetUpdate').map((c) => (c.args[0] as { widgetName: string }).widgetName)).toEqual(['SyNeedsYou', 'SyAccounts']);
    await clearAndroidWidgets({ store, now: () => NOW });
    expect(store.m.size).toBe(0);
    expect(callsOf('androidWidget.requestWidgetUpdate')).toHaveLength(4);
    spy.restore();
  });
});
