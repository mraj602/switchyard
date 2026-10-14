// Live Activity props (docs/spec/phone.md P12). Started only by the app, locally; no push
// updates (decision 9).

import type { WindowKey } from './snapshot';

/** Windows resetting within this many hours qualify (P12; unverified iOS limit, risk 7). */
export const LIVE_MAX_H = 8;

/** Only windows at or above this used % qualify. */
export const LIVE_MIN_PCT = 80;

/** The activity is dismissed this long after the reset. */
export const LIVE_END_AFTER_MS = 15 * 60 * 1000;

export interface LiveCountdownProps {
  seat: string;
  colour: string;
  window: WindowKey;
  pct: number;
  resets_at: string;
  started_at: string;
}

/** The expo-widgets Live Activity name (createLiveActivity). */
export const LIVE_ACTIVITY_NAME = 'ResetCountdown';
