// The app state (phone.md P3, P5): keychain entry, hub client, poller, cache and the data
// reducer, behind `useHub()`. Every platform service is a prop, so tests run it
// with the in-memory fakes and no native module.
import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { CACHE_KEY, WIDGET_KEY } from '../contract/constants';
import type { AppInfo, AppStateSource, Clock, FetchLike, KVStore } from '../contract/platform';
import type { Device, Me } from '../contract/api';
import type { AppRoute } from '../contract/routes';
import { createPoller, initialData, loadCache, reduce } from '../lib/data';
import type { Poller } from '../lib/data';
import { addPendingRevoke, createHubClient, flushPendingRevokes, normalizeOrigin, revokeDevice } from '../lib/hub';
import { hostOf } from '../lib/pair';
import type { HubClient } from '../lib/hub';
import { performDecision } from '../lib/notify';
import type { NotificationsLike } from '../lib/notify';
import type { ClaimLink } from '../contract/link';
import type { PendingRevoke, SecureHub, StoredHub } from '../lib/secure';
import { bridge } from './bridge';
import { HubContext } from './context';
import type { HubState, PairStatus } from './context';
import { stateHooks } from './hooks';

export interface ProviderDeps {
  secure: SecureHub;
  cache: KVStore;
  fetch: FetchLike;
  clock: Clock;
  appState: AppStateSource;
  app: AppInfo;
  /** modules/sy-notify (dismissing a notification after Snooze or Seen, P6.4). */
  notifications: NotificationsLike;
  /** The sy-link module (Android): the claim carries keys (P2). */
  link?: ClaimLink | null;
}

export interface HubProviderProps {
  deps: ProviderDeps;
  /** Open an app route (a notification tap). */
  navigate(route: AppRoute): void;
  /** Open an external URL (a notification tap that opens Claude). */
  openUrl(url: string): Promise<unknown>;
  children: ReactNode;
}

export const UNPAIRED_ON_HUB = 'This phone was removed from the hub';

export function HubProvider({ deps: initialDeps, navigate, openUrl, children }: HubProviderProps) {
  const [deps] = useState(initialDeps); // platform services are fixed for the app's lifetime
  const [data, dispatch] = useReducer(reduce, initialData);
  const [status, setStatusState] = useState<PairStatus>('loading');
  const [stored, setStored] = useState<StoredHub | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [client, setClient] = useState<HubClient | null>(null);
  const [me, setMe] = useState<Me | null>(null);
  const [moved, setMoved] = useState<{ to: string } | null>(null);
  const pollerRef = useRef<Poller | null>(null);
  const clientRef = useRef<HubClient | null>(null);
  const storedRef = useRef<StoredHub | null>(null);
  const statusRef = useRef<PairStatus>('loading');
  /** The pairing generation: bumped by every pairing and clearing, so a step that started
   *  for an older one (a stale 401, a refresh in flight) changes nothing (P3). */
  const genRef = useRef(0);
  /** Unpairing retries run one after another (each reads the keychain list afresh). */
  const revokeChain = useRef<Promise<void>>(Promise.resolve());
  const mounted = useRef(true);

  const setStatus = useCallback((s: PairStatus) => {
    statusRef.current = s;
    setStatusState(s);
  }, []);

  /** Retries the unconfirmed unpairings (SECURE_REVOKE_KEY); `only` limits it to some. */
  const flushRevokes = useCallback(
    (only?: (r: PendingRevoke) => boolean): Promise<void> => {
      const next = revokeChain.current.then(() => flushPendingRevokes(deps.secure, deps, only));
      revokeChain.current = next.catch(() => {});
      return revokeChain.current;
    },
    [deps],
  );

  /**
   * Clears keychain entry, cache and widget data and shows Pair. With `expected`, only while
   * that pairing is still the current one: a 401 from a client of a previous pairing (after
   * "Unpair and pair") must not wipe the new one.
   */
  const clearAll = useCallback(
    async (reason: string | null, expected?: StoredHub) => {
      if (expected && storedRef.current !== expected) return;
      const d = deps;
      const mine = ++genRef.current;
      pollerRef.current?.stop();
      pollerRef.current = null;
      storedRef.current = null;
      try {
        await d.secure.clear();
      } catch {
        // nothing more to do: the entry is unreadable anyway
      }
      if (mine !== genRef.current) return; // paired again meanwhile: that pairing stands
      await Promise.allSettled([d.cache.removeItem(CACHE_KEY), d.cache.removeItem(WIDGET_KEY)]);
      if (mine !== genRef.current) return;
      try {
        await stateHooks.onCleared?.();
      } catch {
        // best effort
      }
      if (mine !== genRef.current || !mounted.current) return;
      dispatch({ type: 'reset' });
      setStored(null);
      setClient(null);
      setMe(null);
      setMoved(null);
      setNotice(reason);
      setStatus('unpaired');
    },
    [deps, setStatus],
  );

  /** Keep a hub in state and build its client; a 401 on that client clears everything (while
   *  it is still the current pairing). A PATCH /v1/devices/self through it also updates `me`
   *  (the widgets and private prefs). */
  const adopt = useCallback(
    (s: StoredHub): HubClient => {
      const raw = createHubClient({
        origin: s.origin,
        token: s.token,
        fetch: deps.fetch,
        clock: deps.clock,
        onUnauthorized: () => {
          void clearAll(UNPAIRED_ON_HUB, s);
        },
      });
      const c: HubClient = {
        ...raw,
        async patchSelf(prefs) {
          const d: Device = await raw.patchSelf(prefs);
          if (storedRef.current === s) setMe((m) => (m ? { ...m, device: { ...m.device, ...d } } : m));
          return d;
        },
      };
      storedRef.current = s;
      setStored(s);
      setClient(c);
      return c;
    },
    [deps, clearAll],
  );

  /**
   * Start-up: the keychain entry and the last-known cache. A keychain that cannot be read
   * (locked before the first unlock) is 'locked', not unpaired: nothing is cleared, queued
   * notification decisions wait, and it is read again when the app becomes active.
   */
  const boot = useCallback(async () => {
    const mine = genRef.current;
    let s: StoredHub | null;
    try {
      s = await deps.secure.load();
    } catch {
      if (mounted.current && mine === genRef.current && (statusRef.current === 'loading' || statusRef.current === 'locked')) setStatus('locked');
      return;
    }
    if (!mounted.current || mine !== genRef.current) return;
    if (statusRef.current !== 'loading' && statusRef.current !== 'locked') return;
    if (!s) {
      setStatus('unpaired');
      void flushRevokes();
      return;
    }
    const cached = await loadCache(deps.cache);
    if (!mounted.current || mine !== genRef.current) return;
    if (cached) {
      dispatch({
        type: 'loaded',
        at: cached.fetchedAt ?? 0,
        ...(cached.overview ? { overview: cached.overview } : {}),
        sessions: cached.sessions,
        accounts: cached.accounts,
        alerts: cached.alerts,
      });
    }
    adopt(s);
    setStatus('paired');
    void flushRevokes();
  }, [deps, adopt, flushRevokes, setStatus]);

  /** Ask the paired hub whether it was frozen for a move; keeps the answer for the card. */
  const checkMoved = useCallback(async () => {
    const c = clientRef.current;
    const mine = genRef.current;
    if (!c) return;
    const to = await c.movedTo();
    if (mine === genRef.current && mounted.current) setMoved((m) => (m?.to === to || (!m && !to) ? m : to ? { to } : null));
  }, []);

  useEffect(() => {
    mounted.current = true;
    void (async () => {
      await boot();
    })();
    // Foreground: read a locked keychain again, and retry unconfirmed unpairings.
    const unsub = deps.appState.subscribe((st) => {
      if (st !== 'active') return;
      if (statusRef.current === 'locked') void boot();
      void flushRevokes();
      void checkMoved();
    });
    return () => {
      mounted.current = false;
      unsub();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // The poller and the start-up GET /v1/me (a 401 clears everything through onUnauthorized).
  useEffect(() => {
    if (!client) return;
    const mine = genRef.current;
    const poller = createPoller({
      client,
      clock: deps.clock,
      appState: deps.appState,
      cache: deps.cache,
      dispatch,
      // Not after the pairing changed: the old hub's data never reach the widgets (P5).
      afterRefresh: (d) => (mine === genRef.current ? (stateHooks.afterRefresh?.(d) ?? Promise.resolve()) : Promise.resolve()),
    });
    pollerRef.current = poller;
    clientRef.current = client;
    void checkMoved();
    let live = true;
    client.me().then(
      (m) => live && mine === genRef.current && setMe(m),
      () => {},
    );
    poller.start();
    return () => {
      live = false;
      poller.stop();
      if (pollerRef.current === poller) pollerRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client]);

  // Notification decisions (performDecision in src/lib/notify) need the origin and the client. While the
  // keychain is loading or locked the bridge keeps them queued.
  useEffect(() => {
    if (status === 'loading' || status === 'locked') return;
    return bridge.install({
      run: (d, response) =>
        performDecision(
          {
            notifications: deps.notifications,
            fetch: deps.fetch,
            clock: deps.clock,
            origin: async () => storedRef.current?.origin ?? null, // not paired: nothing to act on
            pairedAt: async () => {
              const t = Date.parse(storedRef.current?.paired_at ?? '');
              return Number.isFinite(t) ? t : null;
            },
            acted: deps.cache,
            openUrl: async (u) => {
              await openUrl(u);
            },
            navigate,
            onActed: () => void pollerRef.current?.kick(),
          },
          d,
          response?.notification?.request?.identifier,
        ),
      onReceived: () => void pollerRef.current?.kick(),
      probe: async () => (client ? client.health() : false),
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, client, navigate, openUrl]);

  const refresh = useCallback(async () => {
    await pollerRef.current?.kick();
    void checkMoved();
  }, [checkMoved]);

  const reloadMe = useCallback(async () => {
    const c = client;
    if (!c) return;
    const mine = genRef.current;
    try {
      const m = await c.me();
      if (pollerRef.current && mine === genRef.current) setMe(m);
    } catch {
      // keep the last one; a 401 clears everything through onUnauthorized
    }
  }, [client]);

  const pairWith = useCallback(
    async (hub: StoredHub) => {
      const d = deps;
      pollerRef.current?.stop();
      pollerRef.current = null;
      genRef.current++;
      await d.secure.save(hub);
      await d.cache.removeItem(CACHE_KEY).catch(() => {});
      dispatch({ type: 'reset' });
      setNotice(null);
      setMoved(null);
      setMe(null);
      const c = adopt(hub);
      setStatus('paired');
      // An unconfirmed unpairing from this same hub first, so it is settled before the new
      // pairing is used. Others in the background (that hub may be unreachable).
      const same = normalizeOrigin(hub.origin);
      await flushRevokes((r) => normalizeOrigin(r.origin) === same);
      void flushRevokes();
      try {
        await stateHooks.onPaired?.(c, hub);
      } catch {
        // registration problems are shown in Settings (P6.3), not here
      }
    },
    [deps, adopt, flushRevokes, setStatus],
  );

  /**
   * Unpair (P3): stop polling first (no refresh of this hub writes anything after), record the
   * unpairing in the keychain, clear locally, then DELETE /v1/devices/self, retried on later
   * starts and foregrounds until the hub confirms it (2xx, 401 or 404).
   */
  const unpair = useCallback(async () => {
    const s = storedRef.current;
    pollerRef.current?.stop();
    pollerRef.current = null;
    genRef.current++;
    let recorded = false;
    if (s) {
      try {
        await addPendingRevoke(deps.secure, { origin: s.origin, token: s.token });
        recorded = true;
      } catch {
        // the keychain refused it: one try now, at least
      }
    }
    await clearAll(null);
    if (recorded) void flushRevokes();
    else if (s) void revokeDevice(deps, s);
  }, [deps, clearAll, flushRevokes]);

  /**
   * The hub moved (machines.md 8): /health said so, but /health has no signature, so anyone who
   * can answer for the old address (a captive portal, a hostile network, a repointed name) can
   * write any `moved_to`. The bearer token is therefore never sent there. The phone unpairs and
   * asks for a new pairing with the new address's own QR code, which is the one safe way to learn
   * that address.
   */
  const switchToMoved = useCallback(async (): Promise<'switched' | 'rescan' | 'failed'> => {
    const s = storedRef.current;
    const to = moved?.to;
    if (!s || !to) return 'failed';
    await clearAll(`Your hub moved to ${hostOf(to)}. Scan its QR code there to pair this phone again.`, s);
    return 'rescan';
  }, [moved, clearAll]);

  const hub = stored ? { origin: stored.origin, device: stored.device, name: stored.name, paired_at: stored.paired_at } : null;

  const value: HubState = {
    status,
    hub,
    notice,
    moved,
    switchToMoved,
    client,
    data,
    dispatch,
    me,
    reloadMe,
    refresh,
    pairWith,
    unpair,
    claimDeps: { fetch: deps.fetch, clock: deps.clock, app: deps.app, ...(deps.link ? { link: deps.link } : {}) },
    dev: deps.app.dev,
  };
  return <HubContext.Provider value={value}>{children}</HubContext.Provider>;
}
