// The app state (phone.md P3, P5): start-up, 401, pairing, unpairing, `me`, and the
// notification bridge (decisions through WP-N's performDecision). All offline. Seen and Snooze
// are the screens' (src/components/alert-actions.tsx, test/screens.test.tsx).
import { act, render } from '@testing-library/react-native';
import * as Notifications from 'expo-notifications';
import { CACHE_KEY, KEYCHAIN_ACCESSIBLE, SECURE_HUB_KEY, SECURE_REVOKE_KEY, WIDGET_KEY } from '../src/contract/constants';
import type { Overview } from '../src/contract/api';
import { createMemorySecureStore, createSecureHub, secureOptions } from '../src/lib/secure';
import type { StoredHub } from '../src/lib/secure';
import { bridge, HubProvider, stateHooks, UNPAIRED_ON_HUB, useHub } from '../src/state';
import type { HubState, ProviderDeps } from '../src/state';
import type { NotificationResponseLike, NotificationsLike } from '../src/lib/notify';
import { callsOf } from './fakes';
import overviewFx from './fixtures/hub/overview.json';
import sessionsFx from './fixtures/hub/sessions.json';
import accountsFx from './fixtures/hub/accounts.json';
import alertsFx from './fixtures/hub/alerts.json';
import { fakeAppState, fakeFetch, flush, manualClock, memoryKV, ORIGIN, response, TOKEN } from './hubfake';
import type { FakeAppState, FakeFetch } from './hubfake';

const stored: StoredHub = { origin: ORIGIN, token: TOKEN, device: 'dev_phone', name: 'Pixel · app', paired_at: '2026-10-13T00:00:00.000Z' };
const overview = overviewFx as unknown as Overview;
const needsAlert = overview.needs_you.find((s) => s.alert)!.alert!.id;
const ACTION = 'a1.abcDEF_-123.sig_-456';

function hubReplies(url: string, init: { method?: string }) {
  if (url.endsWith('/v1/me')) return response(200, { device: { id: 'dev_phone', prefs: { widgets: false } }, owner: false, hub: {} });
  if (url.endsWith('/v1/devices/self') && init.method === 'PATCH') return response(200, { id: 'dev_phone', prefs: { widgets: true } });
  if (url.endsWith('/v1/overview')) return response(200, overviewFx);
  if (url.includes('/v1/sessions')) return response(200, sessionsFx);
  if (url.endsWith('/v1/accounts')) return response(200, accountsFx);
  if (url.endsWith('/v1/alerts') || url.includes('/v1/alerts?')) return response(200, alertsFx);
  if (url.includes('/seen') || url.includes('/snooze')) return response(200, { id: needsAlert, status: 'seen' });
  if (url.endsWith('/v1/devices/self') && init.method === 'DELETE') return response(204);
  if (url.includes('/v1/actions/')) return response(200, { applied: true, alert: { id: needsAlert, status: 'seen' } });
  return response(404, {});
}

interface Harness {
  api: () => HubState;
  fetch: FakeFetch;
  secureMem: ReturnType<typeof createMemorySecureStore>;
  cache: ReturnType<typeof memoryKV>;
  clock: ReturnType<typeof manualClock>;
  navigate: jest.Mock;
  openUrl: jest.Mock;
  appState: FakeAppState;
}

async function mount(
  opts: {
    paired?: boolean;
    handler?: FakeFetch['handler'];
    cached?: boolean;
    secureMem?: ReturnType<typeof createMemorySecureStore>;
    appState?: FakeAppState;
    hub?: StoredHub;
  } = {},
): Promise<Harness> {
  const secureMem = opts.secureMem ?? createMemorySecureStore();
  const secure = createSecureHub(secureMem, secureOptions(KEYCHAIN_ACCESSIBLE));
  if (opts.paired) await secure.save(opts.hub ?? stored);
  const appState = opts.appState ?? fakeAppState('active');
  const cache = memoryKV();
  const clock = manualClock();
  const f = fakeFetch(opts.handler ?? hubReplies);
  const deps: ProviderDeps = {
    secure,
    cache,
    fetch: f,
    clock,
    appState,
    app: { version: '0.8.0', build: '3', model: 'Pixel', platform: 'android', dev: false },
    notifications: Notifications as unknown as NotificationsLike,
  };
  let latest: HubState | null = null;
  const Probe = () => {
    latest = useHub();
    return null;
  };
  const navigate = jest.fn();
  const openUrl = jest.fn(async () => {});
  await render(
    <HubProvider deps={deps} navigate={navigate} openUrl={openUrl}>
      <Probe />
    </HubProvider>,
  );
  await act(async () => {
    await flush(60);
  });
  return { api: () => latest!, fetch: f, secureMem, cache, clock, navigate, openUrl, appState };
}

beforeEach(() => {
  bridge.reset();
  stateHooks.afterRefresh = null;
  stateHooks.onCleared = null;
  stateHooks.onPaired = null;
});

describe('HubProvider start-up', () => {
  it('is unpaired without a keychain entry and makes no request', async () => {
    const h = await mount();
    expect(h.api().status).toBe('unpaired');
    expect(h.api().client).toBeNull();
    expect(h.fetch.calls).toHaveLength(0);
  });

  it('pairs from the keychain, checks GET /v1/me with the bearer and polls', async () => {
    const h = await mount({ paired: true });
    expect(h.api().status).toBe('paired');
    expect(h.api().hub).toEqual({ origin: ORIGIN, device: 'dev_phone', name: 'Pixel · app', paired_at: stored.paired_at });
    expect(JSON.stringify(h.api().hub)).not.toContain(TOKEN); // screens never see the token
    const me = h.fetch.calls.find((c) => c.url.endsWith('/v1/me'))!;
    expect(me.init.headers?.Authorization).toBe(`Bearer ${TOKEN}`);
    expect(h.api().data.overview).toMatchObject({ time: overview.time });
    expect(h.api().data.offline).toBe(false);
    expect(h.api().me?.device.id).toBe('dev_phone');
  });

  it('a PATCH /v1/devices/self through the client keeps me fresh', async () => {
    const h = await mount({ paired: true });
    expect(h.api().me?.device.prefs?.widgets).toBe(false);
    await act(async () => {
      await h.api().client!.patchSelf({ widgets: true });
    });
    expect(h.api().me?.device.prefs?.widgets).toBe(true);
  });

  it('dispatch reaches the reducer (the screens\' optimistic Seen)', async () => {
    const h = await mount({ paired: true });
    await act(async () => {
      h.api().dispatch({ type: 'seen', alert: needsAlert });
    });
    expect(h.api().data.overview!.needs_you.find((s) => s.alert?.id === needsAlert)!.alert!.status).toBe('seen');
  });

  it('shows the cache first and goes offline when the hub is down', async () => {
    const cacheHub = await mount({ paired: true });
    const saved = cacheHub.cache.items.get(CACHE_KEY)!;
    expect(saved).toBeTruthy();

    const secureMem = createMemorySecureStore();
    const secure = createSecureHub(secureMem, secureOptions(KEYCHAIN_ACCESSIBLE));
    await secure.save(stored);
    const cache = memoryKV();
    cache.items.set(CACHE_KEY, saved);
    const f = fakeFetch(() => Promise.reject(new Error('down')));
    let latest: HubState | null = null;
    const Probe = () => {
      latest = useHub();
      return null;
    };
    await render(
      <HubProvider
        deps={{ secure, cache, fetch: f, clock: manualClock(), appState: fakeAppState(), app: { version: '1', build: '1', model: 'P', platform: 'ios', dev: false }, notifications: Notifications as unknown as NotificationsLike }}
        navigate={jest.fn()}
        openUrl={jest.fn()}
      >
        <Probe />
      </HubProvider>,
    );
    await act(async () => {
      await flush(60);
    });
    expect(latest!.status).toBe('paired');
    expect(latest!.data.overview).not.toBeNull();
    expect(latest!.data.offline).toBe(true);
  });

  it('a 401 clears keychain, cache and widget data, runs onCleared and shows Pair', async () => {
    const onCleared = jest.fn();
    stateHooks.onCleared = onCleared;
    const h = await mount({ paired: true, handler: (url, init) => (url.endsWith('/v1/me') ? response(401, { code: 'unauthorized' }) : hubReplies(url, init)) });
    h.cache.items.set(WIDGET_KEY, '{"v":1}');
    expect(h.api().status).toBe('unpaired');
    expect(h.api().notice).toBe(UNPAIRED_ON_HUB);
    expect(h.api().client).toBeNull();
    expect(h.secureMem.dump()).toEqual({});
    expect(h.cache.items.has(CACHE_KEY)).toBe(false);
    expect(onCleared).toHaveBeenCalledTimes(1);
    expect(h.api().data.overview).toBeNull();
  });

  it('clears the widget key too', async () => {
    const h = await mount({ paired: true, handler: () => response(401, {}) });
    expect(h.cache.items.has(WIDGET_KEY)).toBe(false);
    expect(h.api().status).toBe('unpaired');
  });
});

describe('pairing and unpairing', () => {
  it('pairWith keeps the claim in the keychain with the right option and starts polling', async () => {
    const onPaired = jest.fn();
    stateHooks.onPaired = onPaired;
    const h = await mount();
    await act(async () => {
      await h.api().pairWith(stored);
      await flush(60);
    });
    expect(h.api().status).toBe('paired');
    expect(h.api().notice).toBeNull();
    expect(JSON.parse(h.secureMem.dump()[SECURE_HUB_KEY]!)).toEqual(stored);
    expect(h.fetch.calls.some((c) => c.url.endsWith('/v1/overview'))).toBe(true);
    expect(onPaired).toHaveBeenCalledTimes(1);
    expect(onPaired.mock.calls[0]![1]).toEqual(stored);
  });

  it('a failing onPaired does not undo the pairing', async () => {
    stateHooks.onPaired = () => Promise.reject(new Error('push blocked'));
    const h = await mount();
    await act(async () => {
      await h.api().pairWith(stored);
    });
    expect(h.api().status).toBe('paired');
  });

  it('unpair calls DELETE /v1/devices/self then clears everything', async () => {
    const h = await mount({ paired: true });
    await act(async () => {
      await h.api().unpair();
      await flush();
    });
    const del = h.fetch.calls.find((c) => c.init.method === 'DELETE')!;
    expect(del.url).toBe(`${ORIGIN}/v1/devices/self`);
    expect(h.api().status).toBe('unpaired');
    expect(h.api().notice).toBeNull();
    expect(h.secureMem.dump()).toEqual({});
    expect(h.cache.items.has(CACHE_KEY)).toBe(false);
  });

  const deletes = (f: FakeFetch) => f.calls.filter((c) => c.init.method === 'DELETE');
  const down: FakeFetch['handler'] = (url, init) => (init.method === 'DELETE' ? Promise.reject(new Error('down')) : hubReplies(url, init));

  it('unpair while the hub cannot be reached clears locally and keeps a pending revoke in the keychain (finding 2)', async () => {
    const h = await mount({ paired: true, handler: down });
    await act(async () => {
      await h.api().unpair();
      await flush(60);
    });
    expect(h.api().status).toBe('unpaired');
    expect(deletes(h.fetch)).toHaveLength(1);
    const dump = h.secureMem.dump();
    expect(Object.keys(dump)).toEqual([SECURE_REVOKE_KEY]); // the hub entry is gone
    expect(JSON.parse(dump[SECURE_REVOKE_KEY]!)).toEqual([{ origin: ORIGIN, token: TOKEN }]);
    expect(h.cache.items.has(CACHE_KEY)).toBe(false);
    expect([...h.cache.items.values()].join()).not.toContain(TOKEN);
  });

  it('the next start retries DELETE /v1/devices/self until the hub confirms it (finding 2)', async () => {
    const first = await mount({ paired: true, handler: down });
    await act(async () => {
      await first.api().unpair();
      await flush(60);
    });
    const secureMem = first.secureMem;
    // a 503 is not a confirmation: kept
    const again = await mount({ secureMem, handler: (url, init) => (init.method === 'DELETE' ? response(503, {}) : hubReplies(url, init)) });
    expect(again.api().status).toBe('unpaired');
    expect(deletes(again.fetch)).toHaveLength(1);
    expect(deletes(again.fetch)[0]!.init.headers?.Authorization).toBe(`Bearer ${TOKEN}`);
    expect(secureMem.dump()[SECURE_REVOKE_KEY]).toBeTruthy();
    // 204 ends it
    const done = await mount({ secureMem });
    expect(deletes(done.fetch)).toHaveLength(1);
    expect(secureMem.dump()).toEqual({});
    // nothing left: the start after makes no request
    const quiet = await mount({ secureMem });
    expect(quiet.fetch.calls).toHaveLength(0);
  });

  it.each([401, 404])('a %i to the retried DELETE also ends it (already revoked or gone)', async (status) => {
    const secureMem = createMemorySecureStore();
    await createSecureHub(secureMem, secureOptions(KEYCHAIN_ACCESSIBLE)).saveRevokes([{ origin: ORIGIN, token: TOKEN }]);
    await mount({ secureMem, handler: (url, init) => (init.method === 'DELETE' ? response(status, {}) : hubReplies(url, init)) });
    expect(secureMem.dump()).toEqual({});
  });

  it('a foreground retries it too', async () => {
    const h = await mount({ paired: true, handler: down });
    await act(async () => {
      await h.api().unpair();
      await flush(60);
    });
    h.fetch.handler = hubReplies;
    await act(async () => {
      h.appState.set('background');
      h.appState.set('active');
      await flush(60);
    });
    expect(deletes(h.fetch)).toHaveLength(2);
    expect(h.secureMem.dump()).toEqual({});
  });

  it('re-pairing with the same hub confirms its old unpairing before registering for pushes (no 409)', async () => {
    const h = await mount({ paired: true, handler: down });
    await act(async () => {
      await h.api().unpair();
      await flush(60);
    });
    h.fetch.handler = hubReplies;
    let deletesAtOnPaired = -1;
    stateHooks.onPaired = () => {
      deletesAtOnPaired = deletes(h.fetch).length;
    };
    const fresh: StoredHub = { ...stored, token: 'sy_p_NEWTOKEN', paired_at: '2026-10-14T00:00:00.000Z' };
    await act(async () => {
      await h.api().pairWith(fresh);
      await flush(60);
    });
    expect(deletesAtOnPaired).toBe(2);
    expect(deletes(h.fetch)[1]!.init.headers?.Authorization).toBe(`Bearer ${TOKEN}`); // the old token, never the new one
    expect(Object.keys(h.secureMem.dump())).toEqual([SECURE_HUB_KEY]);
  });

  it('unpair stops polling first: a refresh in flight writes no cache and runs no widget step after it (finding 4)', async () => {
    const after = jest.fn(async () => {});
    stateHooks.afterRefresh = after;
    const h = await mount({ paired: true });
    const ranBefore = after.mock.calls.length;
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    let releaseDelete: () => void = () => {};
    const deleteGate = new Promise<void>((r) => (releaseDelete = r));
    h.fetch.handler = async (url, init) => {
      if (url.endsWith('/v1/overview')) await gate;
      if (init.method === 'DELETE') await deleteGate; // a slow hub: the unpair is still going on
      return hubReplies(url, init);
    };
    let pending: Promise<void> = Promise.resolve();
    await act(async () => {
      pending = h.api().refresh();
      await flush();
    });
    await act(async () => {
      const unpairing = h.api().unpair();
      await flush();
      release(); // the refresh answers while the unpairing is under way
      await pending;
      await flush(60);
      releaseDelete();
      await unpairing;
      await flush(60);
    });
    expect(after.mock.calls.length).toBe(ranBefore);
    expect(h.cache.items.has(CACHE_KEY)).toBe(false);
    expect(h.api().data.overview).toBeNull();
    expect(h.api().status).toBe('unpaired');
  });
});

describe('a stale 401 (finding 3)', () => {
  it('a 401 from the old client after "Unpair and pair" does not clear the new pairing', async () => {
    const h = await mount({ paired: true });
    const oldClient = h.api().client!;
    const fresh: StoredHub = { ...stored, origin: 'https://new.ts.net', token: 'sy_p_NEWTOKEN' };
    await act(async () => {
      await h.api().unpair();
      await h.api().pairWith(fresh);
      await flush(60);
    });
    // the old hub now answers 401 to its old token (it was revoked)
    h.fetch.handler = (url, init) => (init.headers?.Authorization === `Bearer ${TOKEN}` ? response(401, {}) : hubReplies(url, init));
    await act(async () => {
      await oldClient.overview().catch(() => {});
      await flush(60);
    });
    expect(h.api().status).toBe('paired');
    expect(h.api().notice).toBeNull();
    expect(h.api().hub?.origin).toBe('https://new.ts.net');
    expect(JSON.parse(h.secureMem.dump()[SECURE_HUB_KEY]!)).toMatchObject({ origin: 'https://new.ts.net' });
  });

  it('a 401 from the current client still clears', async () => {
    const h = await mount({ paired: true });
    h.fetch.handler = () => response(401, {});
    await act(async () => {
      await h.api().client!.overview().catch(() => {});
      await flush(60);
    });
    expect(h.api().status).toBe('unpaired');
    expect(h.api().notice).toBe(UNPAIRED_ON_HUB);
  });
});

describe('a locked keychain at start (finding 7)', () => {
  function lockable() {
    const mem = createMemorySecureStore();
    const state = { locked: true };
    const guard = () => {
      if (state.locked) throw new Error('User interaction is not allowed.');
    };
    const store: ReturnType<typeof createMemorySecureStore> = {
      async getItemAsync(k, o) {
        guard();
        return mem.getItemAsync(k, o);
      },
      async setItemAsync(k, v, o) {
        guard();
        return mem.setItemAsync(k, v, o);
      },
      async deleteItemAsync(k, o) {
        guard();
        return mem.deleteItemAsync(k, o);
      },
      dump: () => mem.dump(),
    };
    return { mem, store, state };
  }

  it('is locked, not unpaired: nothing is cleared, queued decisions wait, and it pairs on the next foreground', async () => {
    const l = lockable();
    await createSecureHub(l.mem, secureOptions(KEYCHAIN_ACCESSIBLE)).save(stored);
    await bridge.run({ kind: 'act', token: ACTION, action: 'seen' }, { actionIdentifier: 'seen', notification: { request: { identifier: 'lockq', content: { data: {} } } } });
    const h = await mount({ secureMem: l.store, appState: fakeAppState('background') });
    expect(h.api().status).toBe('locked');
    expect(h.api().notice).toBeNull();
    expect(h.fetch.calls).toHaveLength(0); // the queued act did not run (and was not dropped)
    expect(l.mem.dump()[SECURE_HUB_KEY]).toBeTruthy();

    l.state.locked = false;
    await act(async () => {
      h.appState.set('active');
      await flush(80);
    });
    expect(h.api().status).toBe('paired');
    expect(h.fetch.calls.filter((c) => c.url === `${ORIGIN}/v1/actions/${ACTION}`)).toHaveLength(1);
    expect(callsOf('notifications.dismissNotificationAsync').map((c) => c.args)).toEqual([['lockq']]);
  });

  it('stays locked while the keychain is still locked on a foreground', async () => {
    const l = lockable();
    const h = await mount({ secureMem: l.store, appState: fakeAppState('background') });
    await act(async () => {
      h.appState.set('active');
      await flush(60);
    });
    expect(h.api().status).toBe('locked');
  });
});

describe('notification bridge', () => {
  const respOf = (action: string, id = 'n42'): NotificationResponseLike => ({ actionIdentifier: action, notification: { request: { identifier: id, content: { data: {} } } } });

  it('runs an act decision as POST /v1/actions/<token> with no bearer, dismisses it, and refreshes', async () => {
    const h = await mount({ paired: true });
    const before = h.fetch.calls.length;
    await act(async () => {
      await bridge.run({ kind: 'act', token: ACTION, action: 'seen' }, respOf('seen'));
      await flush(60);
    });
    expect(callsOf('notifications.dismissNotificationAsync').map((c) => c.args)).toEqual([['n42']]);
    const post = h.fetch.calls.slice(before).find((c) => c.url.includes('/v1/actions/'))!;
    expect(post.url).toBe(`${ORIGIN}/v1/actions/${ACTION}`);
    expect(post.init.headers?.Authorization).toBeUndefined();
    expect(post.init.body).toBe('{}');
    expect(h.fetch.calls.slice(before).some((c) => c.url.endsWith('/v1/overview'))).toBe(true);
  });

  it('snooze dismisses that notification too; without a response nothing is dismissed', async () => {
    const h = await mount({ paired: true });
    await act(async () => {
      await bridge.run({ kind: 'act', token: ACTION, action: 'snooze' }, respOf('snooze', 'n7'));
      await bridge.run({ kind: 'act', token: ACTION, action: 'snooze' });
      await flush(60);
    });
    expect(h.fetch.calls.filter((c) => c.url.includes('/v1/actions/'))).toHaveLength(2);
    expect(callsOf('notifications.dismissNotificationAsync').map((c) => c.args)).toEqual([['n7']]);
  });

  it('a queued act keeps its response: it is dismissed once the provider is ready', async () => {
    await bridge.run({ kind: 'act', token: ACTION, action: 'seen' }, respOf('seen', 'cold1'));
    const h = await mount({ paired: true });
    expect(h.fetch.calls.some((c) => c.url === `${ORIGIN}/v1/actions/${ACTION}`)).toBe(true);
    expect(callsOf('notifications.dismissNotificationAsync').map((c) => c.args)).toEqual([['cold1']]);
  });

  it('holds a cold-start decision until the provider is ready, then runs it once', async () => {
    const queued = bridge.run({ kind: 'route', route: { name: 'now' } });
    await queued;
    const h = await mount({ paired: true });
    expect(h.navigate).toHaveBeenCalledTimes(1);
    expect(h.navigate).toHaveBeenCalledWith({ name: 'now' });
  });

  it('a received push triggers a refresh', async () => {
    const h = await mount({ paired: true });
    const before = h.fetch.calls.length;
    await act(async () => {
      bridge.onReceived();
      await flush(60);
    });
    expect(h.fetch.calls.length).toBeGreaterThan(before);
  });

  it('opens a link decision with openUrl', async () => {
    const h = await mount({ paired: true });
    await act(async () => {
      await bridge.run({ kind: 'open-link', url: 'https://claude.ai/code/session_x' });
    });
    expect(h.openUrl).toHaveBeenCalledWith('https://claude.ai/code/session_x');
  });

  it('does nothing for an action when unpaired', async () => {
    const h = await mount();
    await act(async () => {
      await bridge.run({ kind: 'act', token: ACTION, action: 'seen' });
    });
    expect(h.fetch.calls).toHaveLength(0);
  });
});

describe('a hub that moved (machines.md 8.1)', () => {
  const NEW = 'https://new.example.ts.net';
  const movedHandler = (meStatus = 200): FakeFetch['handler'] => (url, init) => {
    if (url === `${ORIGIN}/health`) return response(200, { ok: true, frozen: { since: 't', moved_to: NEW } });
    if (url === `${NEW}/v1/me`) return meStatus === 200 ? response(200, { device: { id: 'dev_phone', prefs: {} }, owner: false, hub: {} }) : response(meStatus, { code: 'unauthorized' });
    if (url.startsWith(NEW)) return response(200, url.endsWith('/v1/overview') ? overviewFx : url.includes('/v1/sessions') ? sessionsFx : url.endsWith('/v1/accounts') ? accountsFx : alertsFx);
    return hubReplies(url, init);
  };

  it('offers the new address and re-points with the same token when it knows this phone', async () => {
    const h = await mount({ paired: true, handler: movedHandler() });
    expect(h.api().moved).toEqual({ to: NEW });
    let r: string | undefined;
    await act(async () => {
      r = await h.api().switchToMoved();
      await flush(60);
    });
    expect(r).toBe('switched');
    expect(h.api().status).toBe('paired');
    expect(h.api().hub?.origin).toBe(NEW);
    expect(h.api().moved).toBeNull();
    const probe = h.fetch.calls.find((c) => c.url === `${NEW}/v1/me`)!;
    expect(probe.init.headers?.Authorization).toBe(`Bearer ${TOKEN}`);
    expect(JSON.parse(h.secureMem.dump()[SECURE_HUB_KEY] ?? '{}')).toMatchObject({ origin: NEW, token: TOKEN });
  });

  it('falls back to Scan again when the new address rejects the token', async () => {
    const h = await mount({ paired: true, handler: movedHandler(401) });
    let r: string | undefined;
    await act(async () => {
      r = await h.api().switchToMoved();
      await flush(60);
    });
    expect(r).toBe('rescan');
    expect(h.api().status).toBe('unpaired');
    expect(h.api().notice).toContain('Your hub moved to new.example.ts.net');
  });

  it('changes nothing when the new address cannot be reached yet', async () => {
    const h = await mount({
      paired: true,
      handler: (url, init) => (url.startsWith(NEW) ? Promise.reject(new Error('down')) : movedHandler()(url, init)),
    });
    let r: string | undefined;
    await act(async () => {
      r = await h.api().switchToMoved();
    });
    expect(r).toBe('failed');
    expect(h.api().status).toBe('paired');
    expect(h.api().hub?.origin).toBe(ORIGIN);
  });
});
