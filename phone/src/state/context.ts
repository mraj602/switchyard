// The app core's state (phone.md P3 to P5). Owner: WP-P. The single source of truth: the
// screens read it through `HubApi` (src/components/hub-api.tsx), which src/state/glue.tsx
// derives from `useHub()`; app/_layout.tsx and the Pair screen use `useHub()` directly.
// `HubContext` is exported so tests can provide a fake value (see `HubTestProvider`).
import { createContext, createElement, useContext } from 'react';
import type { ReactNode } from 'react';
import type { Me } from '../contract/api';
import type { ClaimDeps } from '../lib/pair';
import type { DataAction, HubData } from '../lib/data';
import { initialData } from '../lib/data';
import type { HubClient } from '../lib/hub';
import type { StoredHub } from '../lib/secure';

/** 'locked': the keychain could not be read (before the first unlock); read again on foreground. */
export type PairStatus = 'loading' | 'locked' | 'unpaired' | 'paired';

export interface HubState {
  status: PairStatus;
  /** The paired hub (origin, device name); null unless paired. Never exposes the token to screens: see `hubInfo`. */
  hub: { origin: string; device: string; name: string; paired_at: string } | null;
  /** Why the app is unpaired ("This phone was unpaired on the hub"), or null. */
  notice: string | null;
  client: HubClient | null;
  data: HubData;
  /** The reducer (optimistic Seen and Snooze of the screens). */
  dispatch(a: DataAction): void;
  /** GET /v1/me of the paired hub: at start, after pairing, kept fresh by PATCH /v1/devices/self. */
  me: Me | null;
  /** Re-read GET /v1/me. */
  reloadMe(): Promise<void>;
  /** Refresh now. */
  refresh(): Promise<void>;
  /** Keep a claimed hub in the keychain and start polling (replaces any previous hub). */
  pairWith(hub: StoredHub): Promise<void>;
  /** Record the unpairing, clear keychain, cache and widget data, then DELETE /v1/devices/self (retried until the hub confirms it). */
  unpair(): Promise<void>;
  /** What the Pair screen needs to claim. */
  claimDeps: ClaimDeps;
  dev: boolean;
}

export const HubContext = createContext<HubState | null>(null);

export function useHub(): HubState {
  const v = useContext(HubContext);
  if (!v) throw new Error('useHub: no HubProvider above this component');
  return v;
}

/** Just the data: `const { overview, sessions, accounts, alerts, offline, fetchedAt } = useHubData()`. */
export function useHubData(): HubData {
  return useHub().data;
}

const noopAsync = async () => {};

/** A fixed value for tests: `<HubTestProvider value={{ status: 'unpaired' }}>`. */
export function HubTestProvider(props: { value?: Partial<HubState>; children: ReactNode }) {
  const base: HubState = {
    status: 'paired',
    hub: { origin: 'https://hub.test.ts.net', device: 'dev_phone', name: 'Pixel · app', paired_at: '2026-10-13T00:00:00.000Z' },
    notice: null,
    client: null,
    data: initialData,
    dispatch: () => {},
    me: null,
    reloadMe: noopAsync,
    refresh: noopAsync,
    pairWith: noopAsync,
    unpair: noopAsync,
    claimDeps: {
      fetch: () => Promise.reject(new Error('no network in tests')),
      clock: { now: () => 0, setTimeout: () => null, clearTimeout: () => {} },
      app: { version: '0.8.0', build: '1', model: 'Pixel', platform: 'android', projectId: null, dev: true },
    },
    dev: true,
  };
  return createElement(HubContext.Provider, { value: { ...base, ...props.value } }, props.children);
}
