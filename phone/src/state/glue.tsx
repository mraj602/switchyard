// The glue between the app core and the screens, notifications, widgets and the Live Activity
// (phone.md P6, P11, P12). The logic is pure in src/lib/integrate; this file binds the real
// modules and renders the screens' `HubApiProvider` from `useHub()`: the provider is the one
// source of truth, and the screens' `HubApi` is derived from it here.
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as Notifications from '../../modules/wy-notify';
import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from 'react';
import type { ReactNode } from 'react';
import { HubApiProvider, type HubApi } from '../components/hub-api';
import type { AppInfo, Clock } from '../contract/platform';
import type { Glue, GluePlatform } from '../lib/integrate';
import { hostOf } from '../lib/pair';
import { createUpdater } from '../lib/update';
import { clearAndroidWidgets, updateAndroidWidgets } from '../../widgets/android/task-handler';
import { clearIos, startCountdown, syncCountdown, updateTimelines } from '../../widgets/ios/adapter';
import { useHub } from './context';
import { stateHooks } from './hooks';
import { systemClock, systemLink, systemSecureHub } from './platform';

/** The ticking `now` of the screens. */
export const NOW_TICK_MS = 30_000;

function deviceTz(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/** The real platform pieces (in tests the native modules behind them are the recording fakes). */
export function systemGluePlatform(app: AppInfo, clock: Clock = systemClock): GluePlatform {
  return {
    notifications: Notifications,
    app,
    clock,
    local: AsyncStorage,
    tz: deviceTz,
    updateTimelines: (snap, now) => updateTimelines(snap, now),
    updateAndroidWidgets: (snap) => updateAndroidWidgets(snap),
    syncCountdown: (snap, now, wantStart) => syncCountdown(snap, now, wantStart),
    startCountdown: (props, seatId) => startCountdown(props, seatId),
    clearIos: (now) => clearIos(now),
    clearAndroidWidgets: () => clearAndroidWidgets(),
    link: systemLink(app),
  };
}

/** Fills stateHooks (src/state/hooks.ts) from the integration. */
export function installStateHooks(glue: Glue): void {
  stateHooks.afterRefresh = (d) => glue.afterRefresh(d);
  stateHooks.onCleared = () => glue.onCleared();
  stateHooks.onPaired = (client) => glue.onPaired(client);
}

/** The updater's `loadHub`: the keychain entry narrowed to the origin and token. */
async function loadUpdaterHub(): Promise<{ origin: string; token: string } | null> {
  const s = await systemSecureHub().load();
  return s ? { origin: s.origin, token: s.token } : null;
}

/** Derives the screens' HubApi from the app core and the integration, and provides it. */
export function HubGlue({ glue, clock = systemClock, children }: { glue: Glue; clock?: Clock; children: ReactNode }) {
  const hub = useHub();
  const view = useSyncExternalStore(glue.subscribe, glue.view, glue.view);
  const [now, setNow] = useState(() => clock.now());
  const { client, me, status, refresh, reloadMe, data } = hub;
  const origin = hub.hub?.origin ?? null;

  useEffect(() => {
    let h: unknown = null;
    const tick = () => {
      setNow(clock.now());
      h = clock.setTimeout(tick, NOW_TICK_MS);
    };
    h = clock.setTimeout(tick, NOW_TICK_MS);
    return () => clock.clearTimeout(h);
  }, [clock]);

  useEffect(() => {
    void glue.loadLocal();
  }, [glue]);

  // The link's refresh and revoked events refresh now (push.md 14.8).
  useEffect(() => glue.attachLink(), [glue]);

  useEffect(() => {
    glue.setContext({ me, origin, client, kick: () => void refresh(), reloadMe: () => void reloadMe() });
  }, [glue, me, origin, client, refresh, reloadMe]);

  // The updater (phone.md P16.3): only where the wy-link module is (Android).
  const appBuild = Number(hub.claimDeps.app.build) || 0;
  const updater = useMemo(
    () =>
      glue.link
        ? createUpdater({
            link: glue.link,
            loadHub: loadUpdaterHub,
            kv: AsyncStorage,
            clock,
            installedBuild: appBuild,
          })
        : undefined,
    [glue, clock, appBuild],
  );
  // Once a day while the app is used: on start when paired.
  useEffect(() => {
    if (updater && status === 'paired') void updater.load().then(() => updater.checkIfDue());
  }, [updater, status]);

  // On start when paired (once per client; a fresh pairing runs onPaired instead).
  useEffect(() => {
    if (status === 'paired' && client) void glue.startUp(client);
  }, [glue, status, client]);

  const enablePush = useCallback(async () => {
    if (client) await glue.enablePush(client);
  }, [glue, client]);

  const ios = hub.claimDeps.app.platform === 'ios';
  const countdown = useMemo<HubApi['countdown']>(
    () =>
      ios
        ? {
            can: (seatId) => glue.canCountdown(data, seatId),
            start: (seatId) => glue.startCountdown(data, seatId),
          }
        : undefined,
    // `now` re-evaluates `can` as resets come closer
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ios, glue, data, now],
  );
  const autoCountdown = useMemo<HubApi['autoCountdown']>(
    () => (ios ? { on: view.autoCountdown, set: (on) => glue.setAutoCountdown(on) } : undefined),
    [ios, glue, view.autoCountdown],
  );

  const value: HubApi = {
    data,
    dispatch: hub.dispatch,
    kick: refresh,
    now,
    host: origin ? hostOf(origin) : '',
    client,
    me,
    app: hub.claimDeps.app,
    pushState: view.pushState,
    enablePush,
    snoozeMin: view.snoozeMin,
    ...(glue.link ? { link: glue.link } : {}),
    ...(updater ? { updater } : {}),
    ...(countdown ? { countdown } : {}),
    ...(autoCountdown ? { autoCountdown } : {}),
    moved: hub.moved,
    switchToMoved: hub.switchToMoved,
    unpair: hub.unpair,
  };
  return <HubApiProvider value={value}>{children}</HubApiProvider>;
}
