// Adapters from the native modules to the platform interfaces of src/contract/platform.ts.
// The only place (with app/) where src code imports expo-* for state.
import AsyncStorage from '@react-native-async-storage/async-storage';
import Constants from 'expo-constants';
import * as Device from 'expo-device';
import * as SecureStore from 'expo-secure-store';
import { AppState, Platform } from 'react-native';
import * as WyLink from '../../modules/wy-link';
import type { LinkLike } from '../contract/link';
import type { AppInfo, AppStateSource, Clock, FetchLike, KVStore } from '../contract/platform';
import { createSecureHub, secureOptions } from '../lib/secure';
import type { SecureHub } from '../lib/secure';

export function getAppInfo(): AppInfo {
  const cfg = Constants.expoConfig;
  const extra = cfg?.extra as { cleartext?: boolean } | undefined;
  // The app config embedded at build time: the same version and versionCode the APK's manifest
  // has (app.config.ts sets both). expo-application, which read them from the package manager,
  // is not used: it links Google's Play Install Referrer library.
  const code = cfg?.android?.versionCode;
  return {
    version: cfg?.version ?? '0.0.0',
    build: typeof code === 'number' ? String(code) : '0',
    model: Device.modelName ?? 'Phone',
    platform: Platform.OS === 'ios' ? 'ios' : 'android',
    // __DEV__, or a build made with WY_APP_VARIANT=development (extra.cleartext): the builds that
    // may talk to an http:// loopback / LAN hub (phone.md P2).
    dev: __DEV__ || extra?.cleartext === true,
    ...(Device.manufacturer ? { maker: Device.manufacturer } : {}),
  };
}

export const systemClock: Clock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

export const systemAppState: AppStateSource = {
  current: () => AppState.currentState,
  subscribe(fn) {
    const sub = AppState.addEventListener('change', fn);
    return () => sub.remove();
  },
};

/** The keychain entry, readable while locked after the first unlock (P3). */
export function systemSecureHub(): SecureHub {
  // expo-secure-store defines the keychain constants on iOS only; on Android they are undefined
  // (and the native side ignores keychainAccessible: its key is in the Android Keystore, usable
  // after the first unlock). createSecureHub refuses an undefined value, so name it explicitly.
  const accessible = SecureStore.AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY ?? 'AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY';
  return createSecureHub(SecureStore, secureOptions(accessible));
}

export const systemCache: KVStore = AsyncStorage;

/** The wy-link module (push.md 14.8) on Android; null elsewhere (iOS, Expo Go, no native module). */
export function systemLink(app: AppInfo): LinkLike | null {
  return app.platform === 'android' && WyLink.available ? WyLink : null;
}

/** The widgets' reset alarm (wy-link `scheduleWidgetRefresh`, phone.md P11) on Android; null elsewhere or without the module. */
export function systemWidgetAlarm(app: AppInfo): ((atMs: number) => Promise<void>) | null {
  return app.platform === 'android' && WyLink.available ? (atMs) => WyLink.scheduleWidgetRefresh(atMs) : null;
}

export const systemFetch: FetchLike = (url, init) => fetch(url, init as RequestInit) as unknown as ReturnType<FetchLike>;
