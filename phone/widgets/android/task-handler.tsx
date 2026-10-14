// Android widget task handler and app-side adapter (phone.md P11). The handler reads the last
// WidgetSnapshot from AsyncStorage key sy.widget.v1 (never a secret) and renders it; it never
// reads the keychain or calls the hub. Register once at module scope in the app entry
// (`registerAndroidWidgets()`); the app calls `updateAndroidWidgets` after every refresh.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { Platform } from 'react-native';
import { registerWidgetTaskHandler, requestWidgetUpdate, type WidgetTaskHandlerProps } from 'react-native-android-widget';

import type { KVStore } from '../../src/contract/platform';
import type { WidgetSnapshot } from '../../src/contract/snapshot';
import { androidProps, clearSnapshot, readSnapshot, writeSnapshot } from '../../src/lib/widget';
import { SyAccounts } from './SyAccounts';
import { SyNeedsYou } from './SyNeedsYou';

export const ANDROID_WIDGET_NAMES = ['SyNeedsYou', 'SyAccounts'] as const;
type Name = (typeof ANDROID_WIDGET_NAMES)[number];

export interface HandlerDeps {
  store: KVStore;
  now: () => number;
  tz?: string;
}

const defaults = (): HandlerDeps => ({ store: AsyncStorage, now: Date.now });

function tree(name: string, snapshot: WidgetSnapshot | null, d: HandlerDeps) {
  const props = androidProps(snapshot, d.now(), d.tz);
  return name === 'SyAccounts' ? <SyAccounts {...props} /> : <SyNeedsYou {...props} />;
}

export function createTaskHandler(deps: Partial<HandlerDeps> = {}) {
  const d = { ...defaults(), ...deps };
  return async function widgetTaskHandler(props: WidgetTaskHandlerProps): Promise<void> {
    const name = props.widgetInfo.widgetName;
    if (!(ANDROID_WIDGET_NAMES as readonly string[]).includes(name)) return;
    switch (props.widgetAction) {
      case 'WIDGET_ADDED':
      case 'WIDGET_UPDATE':
      case 'WIDGET_RESIZED':
        props.renderWidget(tree(name, await readSnapshot(d.store), d));
        return;
      default:
        // WIDGET_CLICK opens switchyard:// links natively (OPEN_URI); WIDGET_DELETED needs nothing.
        return;
    }
  };
}

export const widgetTaskHandler = createTaskHandler();

/** Call at module scope of the app entry (Android only). */
export function registerAndroidWidgets(): void {
  if (Platform.OS !== 'android') return;
  registerWidgetTaskHandler(widgetTaskHandler);
}

async function refresh(snapshot: WidgetSnapshot | null, d: HandlerDeps): Promise<void> {
  await Promise.all(
    ANDROID_WIDGET_NAMES.map((widgetName: Name) => requestWidgetUpdate({ widgetName, renderWidget: () => tree(widgetName, snapshot, d) })),
  );
}

/** After a refresh: store the snapshot and redraw both widgets. */
export async function updateAndroidWidgets(snapshot: WidgetSnapshot, deps: Partial<HandlerDeps> = {}): Promise<void> {
  if (Platform.OS !== 'android') return;
  const d = { ...defaults(), ...deps };
  await writeSnapshot(d.store, snapshot);
  await refresh(snapshot, d);
}

/** Unpair: drop the snapshot and show "Open Switchyard to set up". */
export async function clearAndroidWidgets(deps: Partial<HandlerDeps> = {}): Promise<void> {
  if (Platform.OS !== 'android') return;
  const d = { ...defaults(), ...deps };
  await clearSnapshot(d.store);
  await refresh(null, d);
}
