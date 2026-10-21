// The wave 2 glue (phases/8.md WP-I): the state hooks after a refresh, on unpair and on pairing,
// the start-up registration, the Lock Screen countdown and the
// auto countdown, and the unified provider rendering a screen. Offline: the hub is a fake
// fetch and every native module is a recording fake (test/fakes.ts).
import AsyncStorage from '@react-native-async-storage/async-storage';
import { act, render, screen } from '@testing-library/react-native';
import * as Notifications from 'expo-notifications';
import { Platform } from 'react-native';
import type { Me, Overview, Seat } from '../src/contract/api';
import { KEYCHAIN_ACCESSIBLE, LOCAL_PREFS_KEY, WIDGET_KEY } from '../src/contract/constants';
import type { AppInfo } from '../src/contract/platform';
import NowScreen from '../src/components/screens/NowScreen';
import { useHubApi, type HubApi } from '../src/components/hub-api';
import type { HubData } from '../src/lib/data';
import type { HubClient } from '../src/lib/hub';
import {
  createGlue,
  isLimitPush,
  loadLocalPrefs,
  wantsAutoCountdown,
  type GluePlatform,
} from '../src/lib/integrate';
import { DEFAULT_ACTION, type NotificationResponseLike, type NotificationsLike } from '../src/lib/notify';
import { createMemorySecureStore, createSecureHub, secureOptions } from '../src/lib/secure';
import { HubGlue, HubProvider, installStateHooks, stateHooks, systemGluePlatform } from '../src/state';
import { resetAdapter } from '../widgets/ios/adapter';
import { calls, callsOf, notificationsState, widgetsState } from './fakes';
import accountsFx from './fixtures/hub/accounts.json';
import alertsFx from './fixtures/hub/alerts.json';
import meFx from './fixtures/hub/me.json';
import meta from './fixtures/hub/_meta.json';
import overviewFx from './fixtures/hub/overview.json';
import rulesFx from './fixtures/hub/rules.json';
import sessionsFx from './fixtures/hub/sessions.json';
import { fakeAppState, fakeFetch, flush, manualClock, memoryKV, response, TOKEN } from './hubfake';
import type { FetchCall } from './hubfake';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);

const NOW = meta.now_ms;
const ORIGIN = 'https://hub.example.ts.net';
const overview = overviewFx as unknown as Overview;
const accounts = accountsFx.accounts as unknown as Seat[];
const data: HubData = { overview, sessions: [], accounts, alerts: [], fetchedAt: NOW, offline: false };
const NEEDS = overview.counts!.needs_you;

function meWith(prefs: Record<string, unknown>): Me {
  return { ...(structuredClone(meFx) as unknown as Me), device: { ...(meFx.device as unknown as Me['device']), prefs: { ...meFx.device.prefs, ...prefs } as Me['device']['prefs'] } };
}

function rulesWith(snoozeMin: number) {
  return { ...rulesFx, settings: { ...rulesFx.settings, snooze_min: snoozeMin } };
}

function hubReplies(opts: { snoozeMin?: number; widgets?: boolean } = {}) {
  return (url: string, init: { method?: string }) => {
    if (url.endsWith('/v1/overview')) return response(200, overviewFx);
    if (url.includes('/v1/sessions')) return response(200, sessionsFx);
    if (url.endsWith('/v1/accounts')) return response(200, accountsFx);
    if (url.includes('/v1/alerts')) return response(200, alertsFx);
    if (url.endsWith('/v1/rules')) return response(200, rulesWith(opts.snoozeMin ?? 15));
    const me = meWith('widgets' in opts ? { widgets: opts.widgets } : { widgets: true });
    if (url.endsWith('/v1/devices/self')) return response(init.method === 'DELETE' ? 204 : 200, me.device);
    if (url.endsWith('/v1/me')) return response(200, me);
    return response(404, {});
  };
}

const app = (platform: 'ios' | 'android', over: Partial<AppInfo> = {}): AppInfo => ({
  version: '0.8.0',
  build: '12',
  model: 'Pixel 9',
  platform,
  dev: false,
  ...over,
});

function clientOf(handler = hubReplies()): { client: HubClient; calls: FetchCall[] } {
  const f = fakeFetch(handler);
  const c = {
    origin: ORIGIN,
    health: async () => true,
    me: async () => meWith({ widgets: true }),
    rules: async () => (await (await f(`${ORIGIN}/v1/rules`, {})).json()) as never,
    patchSelf: async (p: unknown) => (await (await f(`${ORIGIN}/v1/devices/self`, { method: 'PATCH', body: JSON.stringify({ prefs: p }) })).json()) as never,
    deviceSelf: async () => (await (await f(`${ORIGIN}/v1/devices/self`, {})).json()) as never,
  } as unknown as HubClient;
  return { client: c, calls: f.calls };
}

function ctx(me: Me | null, over: Partial<Parameters<ReturnType<typeof createGlue>['setContext']>[0]> = {}) {
  return { me, origin: ORIGIN, client: null, kick: jest.fn(), reloadMe: jest.fn(), ...over };
}

const idx = (fn: string) => calls.findIndex((c) => c.fn === fn);

let restoreOS: (() => void) | null = null;
function onPlatform(os: 'ios' | 'android') {
  restoreOS = jest.replaceProperty(Platform, 'OS', os).restore;
}

beforeEach(async () => {
  resetAdapter();
  await AsyncStorage.clear();
  stateHooks.afterRefresh = null;
  stateHooks.onCleared = null;
  stateHooks.onPaired = null;
});

afterEach(() => {
  restoreOS?.();
  restoreOS = null;
});

// ------------------------------------------------------------------ stateHooks

describe('afterRefresh', () => {
  it('Android, widgets on: the snapshot goes to sy.widget.v1, both widgets redraw, the badge is set', async () => {
    onPlatform('android');
    const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
    installStateHooks(glue);
    glue.setContext(ctx(meWith({ widgets: true })));
    await stateHooks.afterRefresh!(data);
    const snap = JSON.parse((await AsyncStorage.getItem(WIDGET_KEY))!);
    expect(snap).toMatchObject({ v: 1, hub: 'hub.example.ts.net', needs_you: NEEDS, private: false });
    expect(snap.top).not.toBeNull();
    expect(callsOf('androidWidget.requestWidgetUpdate')).toHaveLength(2);
    expect(notificationsState.badge).toBe(NEEDS);
    expect(callsOf('widgets.')).toHaveLength(0); // no iOS calls on Android
  });

  it('private comes from the device pref, else the hub default', async () => {
    onPlatform('android');
    const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
    glue.setContext(ctx(meWith({ widgets: true, private: true })));
    await glue.afterRefresh(data);
    expect(JSON.parse((await AsyncStorage.getItem(WIDGET_KEY))!)).toMatchObject({ private: true, top: null });
  });

  it('iOS, widgets on: both timelines are written, the badge is set', async () => {
    onPlatform('ios');
    const glue = createGlue(systemGluePlatform(app('ios'), manualClock(NOW)));
    glue.setContext(ctx(meWith({ widgets: true })));
    await glue.afterRefresh(data);
    expect(callsOf('widgets.updateTimeline').map((c) => c.args[0])).toEqual(['NeedsYouSmall', 'AccountsMedium']);
    expect(widgetsState.widgets.get('NeedsYouSmall')!.timeline.length).toBeGreaterThan(0);
    expect(notificationsState.badge).toBe(NEEDS);
    expect(await AsyncStorage.getItem(WIDGET_KEY)).toBeNull();
  });

  it('widgets pref off or never set: the Android snapshot is still written (the pref only gates hub refresh frames and iOS)', async () => {
    onPlatform('android');
    for (const prefs of [{ widgets: false }, {}]) {
      await AsyncStorage.removeItem(WIDGET_KEY);
      const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
      glue.setContext(ctx(meWith(prefs)));
      await glue.afterRefresh(data);
      expect(JSON.parse((await AsyncStorage.getItem(WIDGET_KEY))!)).toMatchObject({ v: 1, needs_you: NEEDS });
      expect(callsOf('androidWidget.requestWidgetUpdate').length).toBeGreaterThanOrEqual(2);
      expect(notificationsState.badge).toBe(NEEDS);
    }
  });

  it('iOS with the widgets pref off: no timeline is written', async () => {
    onPlatform('ios');
    const glue = createGlue(systemGluePlatform(app('ios'), manualClock(NOW)));
    glue.setContext(ctx(meWith({ widgets: false })));
    await glue.afterRefresh(data);
    expect(callsOf('widgets.updateTimeline')).toHaveLength(0);
  });

  it('before GET /v1/me: the Android snapshot is written at once, private, and rewritten once me arrives', async () => {
    onPlatform('android');
    const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
    glue.setContext(ctx(null));
    await glue.afterRefresh(data);
    expect(JSON.parse((await AsyncStorage.getItem(WIDGET_KEY))!)).toMatchObject({ private: true, top: null });
    expect(notificationsState.badge).toBe(NEEDS);
    glue.setContext(ctx(meWith({ widgets: true })));
    await flush(40);
    expect(JSON.parse((await AsyncStorage.getItem(WIDGET_KEY))!)).toMatchObject({ private: false });
  });

  it('errors never escape', async () => {
    const p = fakePlatform({ updateAndroidWidgets: jest.fn(async () => Promise.reject(new Error('disk full'))) });
    (p.notifications.setBadgeCountAsync as jest.Mock).mockRejectedValue(new Error('no badge'));
    const glue = createGlue(p);
    glue.setContext(ctx(meWith({ widgets: true })));
    await expect(glue.afterRefresh(data)).resolves.toBeUndefined();
    expect(p.syncCountdown).toHaveBeenCalled();
  });
});

describe('onCleared', () => {
  it('ends the countdown, empties both platforms\' widgets and resets the badge', async () => {
    onPlatform('ios');
    const glue = createGlue(systemGluePlatform(app('ios'), manualClock(NOW)));
    installStateHooks(glue);
    glue.setContext(ctx(meWith({ widgets: true })));
    await glue.startCountdown(data, 'seat_client');
    expect(widgetsState.activities).toHaveLength(1);
    notificationsState.badge = 3;
    await stateHooks.onCleared!();
    expect(widgetsState.activities).toHaveLength(0);
    expect(callsOf('widgets.activity.end')[0]!.args[1]).toBe('immediate');
    expect(callsOf('widgets.updateTimeline')).toHaveLength(2); // the "Open Switchyard to set up" timeline
    expect(notificationsState.badge).toBe(0);
  });

  it('Android: the snapshot is removed and the widgets redraw empty', async () => {
    onPlatform('android');
    await AsyncStorage.setItem(WIDGET_KEY, '{"v":1}');
    const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
    await glue.onCleared();
    expect(await AsyncStorage.getItem(WIDGET_KEY)).toBeNull();
    expect(callsOf('androidWidget.requestWidgetUpdate')).toHaveLength(2);
    expect(notificationsState.badge).toBe(0);
  });

  it("removes the old hub's notifications, so their Snooze and Seen can't be pressed later (finding 2)", async () => {
    notificationsState.presented = [{ request: { identifier: 'old', content: { data: {} } } }];
    const glue = createGlue(systemGluePlatform(app('ios'), manualClock(NOW)));
    await glue.onCleared();
    expect(callsOf('notifications.dismissAllNotificationsAsync')).toHaveLength(1);
    expect(notificationsState.presented).toEqual([]);
  });

  it('a refresh of the old pairing still running writes nothing after the clearing (finding 4)', async () => {
    let releaseTimeline: () => void = () => {};
    const timelineGate = new Promise<void>((r) => (releaseTimeline = r));
    const p = fakePlatform({ updateTimelines: jest.fn(() => timelineGate as unknown as void) });
    const glue = createGlue(p);
    const rules = jest.fn(async () => rulesWith(15));
    glue.setContext(ctx(meWith({ widgets: true }), { client: { rules } as unknown as HubClient }));
    const running = glue.afterRefresh(data);
    await flush();
    expect(p.updateTimelines).toHaveBeenCalledTimes(1);
    await glue.onCleared();
    releaseTimeline();
    await running;
    expect(p.updateAndroidWidgets).not.toHaveBeenCalled();
    expect(p.syncCountdown).not.toHaveBeenCalled();
    expect(rules).not.toHaveBeenCalled();
  });
});

describe('onPaired and start-up registration', () => {
  it('Android: channels before the permission, then the prefs, then categories with the hub snooze_min', async () => {
    const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
    installStateHooks(glue);
    const { client, calls: hubCalls } = clientOf(hubReplies({ snoozeMin: 30, widgets: undefined }));
    await stateHooks.onPaired!(client, { origin: ORIGIN, token: TOKEN, device: 'd', name: 'n', paired_at: 't' });
    const firstChannel = idx('notifications.setNotificationChannelAsync');
    expect(firstChannel).toBeGreaterThanOrEqual(0);
    expect(firstChannel).toBeLessThan(idx('notifications.getPermissionsAsync'));
    expect(idx('notifications.getPermissionsAsync')).toBeLessThan(idx('notifications.setNotificationCategoryAsync'));
    const actions = notificationsState.categories.get('sy_alert') as { buttonTitle: string }[];
    expect(actions.map((a) => a.buttonTitle)).toEqual(['Open', 'Snooze 30 min', 'Seen']);
    // GET /v1/devices/self: the device's prefs, so widgets: true goes only on the first registration
    expect(hubCalls.map((c) => c.url.replace(ORIGIN, ''))).toEqual(['/v1/rules', '/v1/devices/self', '/v1/devices/self']);
    expect(JSON.parse(hubCalls[2]!.init.body!).prefs).toMatchObject({ widgets: true });
    expect(glue.view()).toMatchObject({ pushState: 'registered', snoozeMin: 30 });
  });

  it('"Turn on notifications" again keeps widget refresh off when the person turned it off (finding 8)', async () => {
    const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
    const { client, calls: hubCalls } = clientOf(hubReplies({ widgets: false }));
    await glue.enablePush(client);
    const patch = hubCalls.find((c) => c.init.method === 'PATCH')!;
    expect(JSON.parse(patch.init.body!).prefs).toEqual({ tz: expect.any(String) });
  });

  it('start-up registers nothing: the link target is the hub\'s, only the rules are read', async () => {
    const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
    const { client, calls: hubCalls } = clientOf(hubReplies({ widgets: false }));
    await glue.startUp(client);
    expect(hubCalls.map((c) => c.url.replace(ORIGIN, ''))).toEqual(['/v1/rules']);
  });

  it('a refused permission: blocked, no categories', async () => {
    notificationsState.permission = 'denied';
    const glue = createGlue(systemGluePlatform(app('ios'), manualClock(NOW)));
    await glue.onPaired(clientOf().client);
    expect(glue.view().pushState).toBe('blocked');
    expect(callsOf('notifications.setNotificationCategoryAsync')).toHaveLength(0);
  });

  it('on start with the permission granted: "registered" without a write; categories once per client', async () => {
    const glue = createGlue(systemGluePlatform(app('android'), manualClock(NOW)));
    const { client, calls: hubCalls } = clientOf();
    await glue.startUp(client);
    await glue.startUp(client);
    expect(glue.view().pushState).toBe('registered');
    expect(hubCalls.some((c) => c.init.method === 'POST' || c.init.method === 'PATCH')).toBe(false);
    expect(callsOf('notifications.setNotificationCategoryAsync')).toHaveLength(1);
    expect(callsOf('notifications.requestPermissionsAsync')).toHaveLength(0);
  });

  it('on start, never asked: "off" (Settings offers Turn on), nothing registered', async () => {
    notificationsState.permission = 'undetermined';
    const glue = createGlue(systemGluePlatform(app('ios'), manualClock(NOW)));
    await glue.startUp(clientOf().client);
    expect(glue.view().pushState).toBeNull();
  });

  it('the categories follow a new snooze_min (re-read at most every 5 minutes)', async () => {
    let min = 15;
    const clock = manualClock(NOW);
    const glue = createGlue(systemGluePlatform(app('ios'), clock));
    const { client } = clientOf((url, init) => hubReplies({ snoozeMin: min })(url, init));
    await glue.startUp(client);
    glue.setContext(ctx(meWith({}), { client }));
    min = 45;
    await glue.afterRefresh(data);
    expect(glue.view().snoozeMin).toBe(15); // too soon
    await clock.advance(5 * 60_000);
    await glue.afterRefresh(data);
    expect(glue.view().snoozeMin).toBe(45);
    const cats = callsOf('notifications.setNotificationCategoryAsync');
    expect(cats).toHaveLength(2);
    expect((cats[1]!.args[1] as { buttonTitle: string }[])[1]!.buttonTitle).toBe('Snooze 45 min');
  });
});

// ------------------------------------------------------------------ countdown

describe('Count down on Lock Screen (iOS)', () => {
  it('can() only for a card at >= 80% resetting within 8 h; start() starts one activity', async () => {
    onPlatform('ios');
    const glue = createGlue(systemGluePlatform(app('ios'), manualClock(NOW)));
    glue.setContext(ctx(meWith({})));
    expect(glue.canCountdown(data, 'seat_client')).toBe(true); // 100%, resets in 9 min
    expect(glue.canCountdown(data, 'seat_work')).toBe(false); // 41%
    expect(glue.canCountdown({ overview: null, accounts: [] }, 'seat_client')).toBe(false);
    await glue.startCountdown(data, 'seat_client');
    const start = callsOf('widgets.activity.start')[0]!;
    expect(start.args[1]).toMatchObject({ seat: 'client', window: 'five_hour', pct: 100 });
    expect(start.args[2]).toBe('switchyard://accounts?seat=seat_client');
    await expect(glue.startCountdown(data, 'seat_work')).rejects.toThrow();
    expect(widgetsState.activities).toHaveLength(1);
  });
});

describe('auto countdown', () => {
  const resp = (action: string, data: unknown): NotificationResponseLike => ({
    actionIdentifier: action,
    notification: { request: { identifier: 'n1', content: { data } } },
  });
  const alert = (over: Record<string, unknown>) => ({ v: 1, type: 'alert', title: 't', body: 'b', private: false, ...over });

  it.each([
    ['limit_95', alert({ trigger: 'limit_95' }), true],
    ['limit_95, private', alert({ trigger: 'limit_95', private: true, title: 'Switchyard' }), true],
    ['limit_95 as a JSON string', JSON.stringify(alert({ trigger: 'limit_95' })), true],
    ['rate-limit failed (title)', alert({ trigger: 'failed', title: 'client · data-pipeline · rate limited' }), true],
    ['rate-limit failed (body)', alert({ trigger: 'failed', title: 'x', body: 'Resets 17:00 · work has 59% left' }), true],
    ['other failed', alert({ trigger: 'failed', title: 'client · api · login needed', body: 'laptop' }), false],
    ['private failed', alert({ trigger: 'failed', private: true, title: 'Switchyard', body: 'A session stopped' }), false],
    ['limit_80', alert({ trigger: 'limit_80' }), false],
    ['reset', alert({ trigger: 'reset' }), false],
    ['asked', alert({ trigger: 'asked' }), false],
    ['summary', { v: 1, type: 'summary', trigger: 'limit_95' }, false],
    ['refresh', { v: 1, type: 'refresh', needs_you: 1 }, false],
    ['version 2', alert({ v: 2, trigger: 'limit_95' }), false],
    ['no data', undefined, false],
    ['garbage', 'not json', false],
  ])('isLimitPush: %s', (_name, data, want) => {
    expect(isLimitPush(data)).toBe(want);
  });

  it.each([
    [DEFAULT_ACTION, true, true],
    ['open', true, true],
    ['snooze', true, false],
    ['seen', true, false],
    [DEFAULT_ACTION, false, false],
  ])('wantsAutoCountdown: action %s, setting %s', (action, on, want) => {
    expect(wantsAutoCountdown(resp(action, alert({ trigger: 'limit_95' })), on)).toBe(want);
  });

  it('a limit push sets the one-shot wantStart and kicks a refresh; only the next refresh starts', async () => {
    const p = fakePlatform();
    const glue = createGlue(p);
    const c = ctx(meWith({}));
    glue.setContext(c);
    glue.noteResponse(resp(DEFAULT_ACTION, alert({ trigger: 'limit_95' })));
    expect(glue.internals().wantStart).toBe(true);
    expect(c.kick).toHaveBeenCalledTimes(1);
    await glue.afterRefresh(data);
    await glue.afterRefresh(data);
    expect((p.syncCountdown as jest.Mock).mock.calls.map((x) => x[2])).toEqual([true, false]);
  });

  it('snooze or seen on a limit push, or the setting off, start nothing', async () => {
    const p = fakePlatform();
    const glue = createGlue(p);
    glue.setContext(ctx(meWith({})));
    glue.noteResponse(resp('snooze', alert({ trigger: 'limit_95' })));
    await glue.setAutoCountdown(false);
    glue.noteResponse(resp(DEFAULT_ACTION, alert({ trigger: 'limit_95' })));
    await glue.afterRefresh(data);
    expect((p.syncCountdown as jest.Mock).mock.calls.map((x) => x[2])).toEqual([false]);
  });

  it('a cold-start response before me keeps wantStart for the refresh that can use it', async () => {
    const p = fakePlatform();
    const glue = createGlue(p);
    glue.noteResponse(resp(DEFAULT_ACTION, alert({ trigger: 'limit_95' }))); // nothing is ready yet
    await glue.afterRefresh(data); // no me: badge only
    expect(p.syncCountdown).not.toHaveBeenCalled();
    glue.setContext(ctx(meWith({})));
    await flush(20);
    expect((p.syncCountdown as jest.Mock).mock.calls.map((x) => x[2])).toEqual([true]);
  });

  it('the setting is local (sy.local.v1), default on, and never a hub pref', async () => {
    const kv = memoryKV();
    expect(await loadLocalPrefs(kv)).toEqual({ auto_countdown: true });
    const glue = createGlue(fakePlatform({ local: kv }));
    await glue.setAutoCountdown(false);
    expect(JSON.parse(kv.items.get(LOCAL_PREFS_KEY)!)).toEqual({ auto_countdown: false });
    const again = createGlue(fakePlatform({ local: kv }));
    await again.loadLocal();
    expect(again.view().autoCountdown).toBe(false);
    kv.items.set(LOCAL_PREFS_KEY, '{oops');
    expect(await loadLocalPrefs(kv)).toEqual({ auto_countdown: true });
  });
});

// ------------------------------------------------------------------ the unified provider

describe('HubProvider + HubGlue', () => {
  // The first mount pays the cold start of the whole screen tree (about 2.5 s locally, no real
  // timers or polling: every wait here is a microtask flush), so a slow CI runner needs headroom.
  jest.setTimeout(30_000);

  async function mountApp(platform: 'ios' | 'android') {
    const secureMem = createMemorySecureStore();
    const secure = createSecureHub(secureMem, secureOptions(KEYCHAIN_ACCESSIBLE));
    await secure.save({ origin: ORIGIN, token: TOKEN, device: 'dev_phone', name: 'Pixel · app', paired_at: 't' });
    const clock = manualClock(NOW);
    const f = fakeFetch(hubReplies({ snoozeMin: 20 }));
    const a = app(platform);
    const glue = createGlue(systemGluePlatform(a, clock));
    installStateHooks(glue);
    let api: HubApi | null = null;
    const Probe = () => {
      api = useHubApi();
      return null;
    };
    await render(
      <HubProvider
        deps={{ secure, cache: memoryKV(), fetch: f, clock, appState: fakeAppState(), app: a, notifications: Notifications as unknown as NotificationsLike }}
        navigate={jest.fn()}
        openUrl={jest.fn()}
      >
        <HubGlue glue={glue} clock={clock}>
          <Probe />
          <NowScreen />
        </HubGlue>
      </HubProvider>,
    );
    await act(async () => {
      await flush(120);
    });
    return { api: () => api!, f, clock };
  }

  it('renders Now from the hub fixtures through HubApiProvider, with the integration fields', async () => {
    onPlatform('android');
    const h = await mountApp('android');
    const ids = screen.getAllByTestId(/^needs-/).map((n) => String(n.props.testID).slice('needs-'.length));
    expect(ids).toEqual(overview.needs_you.map((s) => s.id));
    const api = h.api();
    expect(api.host).toBe('hub.example.ts.net');
    expect(api.me?.device.id).toBe('dev_phone');
    expect(api.snoozeMin).toBe(20);
    expect(api.pushState).toBe('registered'); // start-up sync re-registered the missing target
    expect(api.countdown).toBeUndefined(); // Android
    expect(api.autoCountdown).toBeUndefined();
    expect(api.now).toBe(NOW);
    await act(async () => {
      await h.clock.advance(30_000);
    });
    expect(h.api().now).toBe(NOW + 30_000);
    expect(await AsyncStorage.getItem(WIDGET_KEY)).toBeTruthy(); // afterRefresh with me loaded
  });

  it('iOS: countdown and the Auto countdown setting are offered', async () => {
    onPlatform('ios');
    const h = await mountApp('ios');
    const api = h.api();
    expect(api.countdown?.can('seat_client')).toBe(true);
    expect(api.countdown?.can('seat_work')).toBe(false);
    expect(api.autoCountdown?.on).toBe(true);
    await act(async () => {
      await api.autoCountdown!.set(false);
    });
    expect(h.api().autoCountdown?.on).toBe(false);
    expect(JSON.parse((await AsyncStorage.getItem(LOCAL_PREFS_KEY))!)).toEqual({ auto_countdown: false });
  });
});

// ------------------------------------------------------------------ helpers

function fakePlatform(over: Partial<GluePlatform> = {}): GluePlatform {
  return {
    notifications: {
      setBadgeCountAsync: jest.fn(async () => true),
      getPermissionsAsync: jest.fn(async () => ({ granted: true, status: 'granted' })),
      setNotificationCategoryAsync: jest.fn(async () => null),
      setNotificationChannelAsync: jest.fn(async () => null),
    } as unknown as NotificationsLike,
    app: app('ios'),
    clock: manualClock(NOW),
    local: memoryKV(),
    tz: () => 'Europe/London',
    updateTimelines: jest.fn(),
    updateAndroidWidgets: jest.fn(async () => {}),
    syncCountdown: jest.fn(async () => 'none'),
    startCountdown: jest.fn(async () => 'started'),
    clearIos: jest.fn(async () => {}),
    clearAndroidWidgets: jest.fn(async () => {}),
    ...over,
  };
}
