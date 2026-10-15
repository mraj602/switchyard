// The sy-tabs module's JS API (docs/spec/phone.md P17.1): one claude.ai WebView per seat, each in
// its own WebView profile. Android only; elsewhere (and where the native module is missing, e.g.
// Expo Go) `available` is false, `isSupported` resolves false and the rest reject.
import { requireNativeView, requireOptionalNativeModule } from 'expo';
import type { ComponentType } from 'react';
import { Platform, type ViewProps } from 'react-native';

/** What the tab reports (P17.1). */
export type TabEvent = {
  state: 'loading' | 'loaded' | 'http' | 'error' | 'crashed' | 'download' | 'external';
  url?: string;
  status?: number;
  height?: number;
  code?: number;
  reason?: string;
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
