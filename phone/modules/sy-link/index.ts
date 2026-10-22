// The sy-link module's JS API (docs/spec/push.md 14.8). Android only: every function rejects
// "sy-link: Android only" elsewhere (and where the native module is missing, e.g. Expo Go).
// JavaScript only configures the link and reads its state; the service, the notifications and
// their actions run natively without React Native.
import { requireOptionalNativeModule } from 'expo';
import { Platform } from 'react-native';
import type { LinkConfig, LinkRefresh, LinkState, PublicKeys, Subscription, UpdateInfo } from './src/SyLink.types';

export * from './src/SyLink.types';

type Native = {
  prepareKeys(): Promise<PublicKeys>;
  sealKeys(k: string, secret: string): Promise<string>;
  configure(cfg: LinkConfig): Promise<void>;
  start(): Promise<void>;
  stop(): Promise<void>;
  reconnect(): Promise<void>;
  getState(): Promise<LinkState>;
  setPingInterval(seconds: number): Promise<void>;
  setSnoozeMinutes(minutes: number): Promise<void>;
  requestBatteryExemption(): Promise<boolean>;
  openBatterySettings(): Promise<void>;
  sendLocalTest(): Promise<void>;
  getRefresh(): Promise<string | null>;
  scheduleWidgetRefresh(atMs: number): Promise<void>;
  reset(): Promise<void>;
  checkUpdate(origin: string, token: string): Promise<UpdateInfo | null>;
  installUpdate(info: UpdateInfo): Promise<'started' | 'refused'>;
  addListener(event: string, cb: (payload: unknown) => void): Subscription;
};

const native: Native | null = Platform.OS === 'android' ? requireOptionalNativeModule<Native>('SyLink') : null;

/** True where the module exists (an Android build that includes it). */
export const available = native !== null;

function mod(): Native {
  if (!native) throw new Error('sy-link: Android only');
  return native;
}

const call = <T>(f: (n: Native) => Promise<T>): Promise<T> => {
  try {
    return f(mod());
  } catch (e) {
    return Promise.reject(e);
  }
};

/** Make keys for a claim (reused until configure). */
export const prepareKeys = (): Promise<PublicKeys> => call((n) => n.prepareKeys());
/** keys.md 7.1: the prepared keys sealed with the QR's `k` for this code's `secret` (b64u). */
export const sealKeys = (k: string, secret: string): Promise<string> => call((n) => n.sealKeys(k, secret));
/** After a claim: checks the reply (keys.md 7.3), then keeps pins and urls. */
export const configure = (cfg: LinkConfig): Promise<void> => call((n) => n.configure(cfg));
/** Instant delivery on: the service started. */
export const start = (): Promise<void> => call((n) => n.start());
/** Off: the watchdog does 15-minute checks (push.md 14.9). */
export const stop = (): Promise<void> => call((n) => n.stop());
/** Settings "Reconnect now": drop the connection and connect at once with the backoff reset. */
export const reconnect = (): Promise<void> => call((n) => n.reconnect());
export const getState = (): Promise<LinkState> => call((n) => n.getState());
/** 60–900 s. */
export const setPingInterval = (seconds: number): Promise<void> => call((n) => n.setPingInterval(seconds));
/** The hub's snooze_min, for the "Snooze <n> min" button of link notifications. */
export const setSnoozeMinutes = (minutes: number): Promise<void> => call((n) => n.setSnoozeMinutes(minutes));
/** System dialog; resolves with the new state. */
export const requestBatteryExemption = (): Promise<boolean> => call((n) => n.requestBatteryExemption());
/** The maker's screen when known, else Android's. */
export const openBatterySettings = (): Promise<void> => call((n) => n.openBatterySettings());
/** A local notification on sy_test, no hub. */
export const sendLocalTest = (): Promise<void> => call((n) => n.sendLocalTest());
/** The newest `refresh` frame body, for the widgets (null before the first one). */
export const getRefresh = (): Promise<LinkRefresh | null> =>
  call(async (n) => {
    const s = await n.getRefresh();
    if (!s) return null;
    try {
      return JSON.parse(s) as LinkRefresh;
    } catch {
      return null;
    }
  });
/** Redraw the Android widgets at `atMs` (epoch ms; 0 cancels): a rough alarm for the next limit reset (P11). */
export const scheduleWidgetRefresh = (atMs: number): Promise<void> => call((n) => n.scheduleWidgetRefresh(atMs));
/** Unpair: keys, pins and storage deleted. */
export const reset = (): Promise<void> => call((n) => n.reset());

export function addListener(event: 'state', cb: (s: LinkState) => void): Subscription;
export function addListener(event: 'refresh' | 'revoked' | 'upgrade', cb: () => void): Subscription;
export function addListener(event: string, cb: (s: LinkState) => void): Subscription {
  if (!native) return { remove() {} };
  return native.addListener(event, (p) => cb(p as LinkState));
}

// The updater (phone.md P16), same module.
export const checkUpdate = (origin: string, token: string): Promise<UpdateInfo | null> => call((n) => n.checkUpdate(origin, token));
export const installUpdate = (info: UpdateInfo): Promise<'started' | 'refused'> => call((n) => n.installUpdate(info));
