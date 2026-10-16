// The sy-tabs module's JS API (docs/spec/phone.md P17.1): one claude.ai WebView per seat, each in
// its own WebView profile. Android only; elsewhere (and where the native module is missing, e.g.
// Expo Go) `available` is false, `isSupported` resolves false and the rest reject.
import { requireNativeView, requireOptionalNativeModule } from 'expo';
import type { ComponentType } from 'react';
import { Platform, type ViewProps } from 'react-native';

/**
 * What the tab reports (P17.1). `url`: an in-page navigation (claude.ai is a single-page app).
 * `account` (P17.7): claude.ai's answer to "which account is this?" in the tab's own session,
 * the account uuid only, or `status` (401 signed out, 0 no answer).
 */
export type TabEvent = {
  state: 'loading' | 'loaded' | 'http' | 'error' | 'crashed' | 'download' | 'external' | 'url' | 'account';
  url?: string;
  status?: number;
  height?: number;
  code?: number;
  reason?: string;
  account?: string;
};

export type TabViewProps = ViewProps & {
  /** The hub seat id; its profile is `sy-tab-<seat>`. */
  seat: string;
  /** An https claude.ai URL; anything else loads https://claude.ai/. */
  url: string;
  onTabState?: (e: { nativeEvent: TabEvent }) => void;
};

type Native = {
  isSupported(): Promise<boolean>;
  listProfiles(): Promise<string[]>;
  deleteProfile(seat: string): Promise<boolean>;
  moveProfile(from: string, to: string): Promise<boolean>;
};

const native: Native | null = Platform.OS === 'android' ? requireOptionalNativeModule<Native>('SyTabs') : null;

/** True where the module exists (an Android build that includes it). */
export const available = native !== null;

let view: ComponentType<TabViewProps> | null = null;
/** The native tab view; only render it after `isSupported()` said yes. */
export function TabView(props: TabViewProps) {
  if (!native) return null;
  view ??= requireNativeView<TabViewProps>('SyTabs');
  const V = view;
  return <V {...props} />;
}

/** WebView 110+ with MULTI_PROFILE; false where the module is missing. */
export const isSupported = (): Promise<boolean> => (native ? native.isSupported().catch(() => false) : Promise.resolve(false));
/** Seat ids that have a tab profile. */
export const listProfiles = (): Promise<string[]> => (native ? native.listProfiles() : Promise.reject(new Error('sy-tabs: Android only')));
/** Sign out: deletes the seat's profile (cookies, storage, cache). Rejects while its tab is open. */
export const deleteProfile = (seat: string): Promise<boolean> =>
  native ? native.deleteProfile(seat) : Promise.reject(new Error('sy-tabs: Android only'));
/**
 * P17.7: the tab profile of `from` (and its sign-in, untouched) becomes `to`'s. Rejects
 * `ERR_SY_TABS_TAKEN` when `to` has a tab, `ERR_SY_TABS_NONE` when `from` has none,
 * `ERR_SY_TABS_IN_USE` while the tab is open, `ERR_SY_TABS_SEAT` on a bad seat id.
 */
export const moveProfile = (from: string, to: string): Promise<boolean> =>
  native ? native.moveProfile(from, to) : Promise.reject(new Error('sy-tabs: Android only'));
