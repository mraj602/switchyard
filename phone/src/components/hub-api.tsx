// What the screens need from the app core (phases/8.md). WP-S codes against this interface only;
// WP-I's glue (src/state/glue.tsx, `HubGlue`) derives it from WP-P's `useHub()` state plus the
// integration (push state, snooze_min, countdown) and renders <HubApiProvider> in
// app/_layout.tsx. Tests pass a fake (test/screen-harness.tsx).
import { createContext, useContext, type ReactNode } from 'react';
import type { Me } from '../contract/api';
import type { LinkLike } from '../contract/link';
import type { AppInfo } from '../contract/platform';
import type { DataAction, HubData } from '../lib/data';
import type { HubClient } from '../lib/hub';
import type { RegisterState } from '../lib/notify';
import type { Updater } from '../lib/update';

export interface HubApi {
  /** The reducer state: last good replies, `offline`, `fetchedAt`. */
  data: HubData;
  dispatch(a: DataAction): void;
  /** Refresh now (pull to refresh, after Seen or Snooze). */
  kick(): Promise<void>;
  /** A clock that ticks (every 30 s or so), so ages and countdowns stay fresh. */
  now: number;
  /** The paired hub's host name ("hub.example.ts.net"). */
  host: string;
  /** null before pairing (the app shows Pair then). */
  client: HubClient | null;
  me: Me | null;
  app: AppInfo;
  /** Result of registerForPush; null until it ran. */
  pushState: RegisterState | null;
  /** Ask for permission and register this phone again (Settings). */
  enablePush(): Promise<void>;
  /** Settings "snooze_min" of the hub (GET /v1/rules); labels the Snooze button. Default 15. */
  snoozeMin?: number;
  /**
   * "Count down on Lock Screen" (iOS, P12). WP-W's live module decides which card qualifies and
   * starts the Live Activity; WP-I wires it here. Absent (Android, or not wired): no button.
   */
  countdown?: { can(seatId: string): boolean; start(seatId: string): Promise<void> };
  /**
   * Settings "Auto countdown" (iOS, P12): a local app setting (AsyncStorage `sy.local.v1`), not
   * a hub pref. Absent (Android): no switch.
   */
  autoCountdown?: { on: boolean; set(on: boolean): Promise<void> };
  /**
   * The sy-link module (Android, push.md 14.8): Settings → Notifications (P6.7). Absent: iOS or a
   * build without the module.
   */
  link?: LinkLike;
  /**
   * The in-app updater (phone.md P16.3), Android only: Settings → App version shows it. Absent
   * elsewhere (iOS, no module): no Updates row.
   */
  updater?: Updater;
  /** The hub said it moved (/health `frozen.moved_to`): where to, else absent. */
  moved?: { to: string } | null;
  /** Re-point this phone at `moved.to` with the same token; see HubState.switchToMoved. */
  switchToMoved?(): Promise<'switched' | 'rescan' | 'failed'>;
  /** DELETE /v1/devices/self (best effort), then clear the keychain, cache and widget data. */
  unpair(): Promise<void>;
}

const Ctx = createContext<HubApi | null>(null);

export function HubApiProvider({ value, children }: { value: HubApi; children: ReactNode }) {
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useHubApi(): HubApi {
  const v = useContext(Ctx);
  if (!v) throw new Error('useHubApi: no HubApiProvider above this screen');
  return v;
}
