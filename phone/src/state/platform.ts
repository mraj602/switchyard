// Adapters from the native modules to the platform interfaces of src/contract/platform.ts.
// Owner: WP-P. The only place (with app/) where src code imports expo-* for state.
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as Application from 'expo-application';
import Constants from 'expo-constants';
import * as Device from 'expo-device';
import * as SecureStore from 'expo-secure-store';
import { AppState, Platform } from 'react-native';
import * as SyLink from '../../modules/sy-link';
import type { LinkLike } from '../contract/link';
import type { AppInfo, AppStateSource, Clock, FetchLike, KVStore } from '../contract/platform';
import { createSecureHub, secureOptions } from '../lib/secure';
import type { SecureHub } from '../lib/secure';

export function getAppInfo(): AppInfo {
  const extra = Constants.expoConfig?.extra as { eas?: { projectId?: string }; push?: string; cleartext?: boolean } | undefined;
  return {
    version: Application.nativeApplicationVersion ?? Constants.expoConfig?.version ?? '0.0.0',
    build: Application.nativeBuildVersion ?? '0',
    model: Device.modelName ?? 'Phone',
    platform: Platform.OS === 'ios' ? 'ios' : 'android',
    projectId: extra?.eas?.projectId ?? null,
    // __DEV__, or a build made with SY_APP_VARIANT=development (extra.cleartext): the builds that
    // may talk to an http:// loopback / LAN hub (phone.md P2).
    dev: __DEV__ || extra?.cleartext === true,
    // app.config.ts sets extra.push; a build without it is the self-hosted default (P13).
    push: extra?.push === 'expo' ? 'expo' : 'link',
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
  return createSecureHub(SecureStore, secureOptions(SecureStore.AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY));
}

export const systemCache: KVStore = AsyncStorage;

/**
 * The sy-link module (push.md 14.8) in the self-hosted profile on Android; null otherwise (iOS,
 * Expo Go, an `SY_PUSH=expo` build), and the app then works as in phase 8.
 */
export function systemLink(app: AppInfo): LinkLike | null {
  return app.push === 'link' && app.platform === 'android' && SyLink.available ? SyLink : null;
}

export const systemFetch: FetchLike = (url, init) => fetch(url, init as RequestInit) as unknown as ReturnType<FetchLike>;
