// Android widget task handler and app-side adapter (phone.md P11). The handler reads the last
// WidgetSnapshot from AsyncStorage key sy.widget.v1 (never a secret) and renders it; it never
// reads the keychain or calls the hub. The handler is registered in phone/index.js (the app's
// entry), not in a screen: Android starts the widget's headless task with no UI mounted, and a
// handler registered inside app/_layout.tsx would not exist then. The app calls
// `updateAndroidWidgets` after every refresh.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { Platform } from 'react-native';
import {
  registerWidgetConfigurationScreen,
  registerWidgetTaskHandler,
  requestWidgetUpdate,
  type WidgetInfo,
  type WidgetTaskHandlerProps,
} from 'react-native-android-widget';

import type { KVStore } from '../../src/contract/platform';
import type { WidgetSnapshot } from '../../src/contract/snapshot';
import type { LinkRefresh } from '../../src/contract/link';
import { androidProps, applyLinkRefresh, clearSnapshot, readSnapshot, writeSnapshot } from '../../src/lib/widget';
import { dropRingSeat, dropWindowChoice, nextRefreshAt, ringSeat, setWindowChoice, windowChoice } from '../../src/lib/widget/extra';
import { getAppInfo, systemLink, systemWidgetAlarm } from '../../src/state/platform';
import { TOGGLE_ACTION } from './Brand';
import { RingConfig } from './RingConfig';
import { SyLimits } from './SyLimits';
import { SyMachines } from './SyMachines';
import { SyNeeds } from './SyNeeds';
import { SyNextReset } from './SyNextReset';
import { SyRing } from './SyRing';
import { SySessions } from './SySessions';

export const ANDROID_WIDGET_NAMES = ['SyLimits', 'SySessions', 'SyNeeds', 'SyRing', 'SyNextReset', 'SyMachines'] as const;
type Name = (typeof ANDROID_WIDGET_NAMES)[number];

export interface HandlerDeps {
  store: KVStore;
  now: () => number;
  tz?: string;
  /** The link's newest `refresh` frame (push.md 13.5), when the sy-link module is there. */
  linkRefresh?: () => Promise<LinkRefresh | null>;
  /** Asks the native side to redraw the widgets at this time (epoch ms; 0 cancels): the reset alarm. */
  alarm?: (atMs: number) => Promise<void>;
}

const defaults = (): HandlerDeps => {
  let link: ReturnType<typeof systemLink> = null;
  let alarm: ReturnType<typeof systemWidgetAlarm> = null;
  try {
    const app = getAppInfo();
    link = systemLink(app);
    alarm = systemWidgetAlarm(app);
  } catch {
    // no native module yet (a headless start): the stored snapshot alone
  }
  return {
    store: AsyncStorage,
    now: Date.now,
    ...(link ? { linkRefresh: () => link.getRefresh() } : {}),
    ...(alarm ? { alarm } : {}),
  };
};

/** The stored snapshot with the link's newest refresh on top (the redraw the service asks for). */
async function current(d: HandlerDeps): Promise<WidgetSnapshot | null> {
  const snap = await readSnapshot(d.store);
  if (!snap || !d.linkRefresh) return snap;
  try {
    return applyLinkRefresh(snap, await d.linkRefresh());
  } catch {
    return snap;
  }
}

async function tree(name: string, snapshot: WidgetSnapshot | null, d: HandlerDeps, info?: Pick<WidgetInfo, 'height' | 'width' | 'widgetId'>) {
  const seat = name === 'SyRing' && info ? await ringSeat(d.store, info.widgetId) : undefined;
  const win = (name === 'SyRing' || name === 'SyLimits') && info ? await windowChoice(d.store, info.widgetId) : undefined;
  const props = androidProps(snapshot, d.now(), d.tz, info?.height, info?.width, seat, win);
  switch (name) {
    case 'SyLimits':
      return <SyLimits {...props} />;
    case 'SyNeeds':
      return <SyNeeds {...props} />;
    case 'SyRing':
      return <SyRing {...props} />;
    case 'SyNextReset':
      return <SyNextReset {...props} />;
    case 'SyMachines':
      return <SyMachines {...props} />;
    default:
      return <SySessions {...props} />;
  }
}

/**
 * One alarm for the soonest reset still ahead, so the reset ring and Next reset widgets move on
 * when it happens (the library cannot tick). Each redraw sets the next one. Best effort.
 */
export async function scheduleResetRedraw(snapshot: WidgetSnapshot | null, d: HandlerDeps): Promise<void> {
  if (!d.alarm) return;
  try {
    await d.alarm(snapshot ? (nextRefreshAt(snapshot, d.now()) ?? 0) : 0);
  } catch {
    // no alarm: the 30-minute period still redraws
  }
}

export function createTaskHandler(deps: Partial<HandlerDeps> = {}) {
  const d = { ...defaults(), ...deps };
  return async function widgetTaskHandler(props: WidgetTaskHandlerProps): Promise<void> {
    const name = props.widgetInfo.widgetName;
    if (!(ANDROID_WIDGET_NAMES as readonly string[]).includes(name)) return;
    switch (props.widgetAction) {
      case 'WIDGET_ADDED':
      case 'WIDGET_UPDATE':
      case 'WIDGET_RESIZED': {
        const snap = await current(d);
        props.renderWidget(await tree(name, snap, d, props.widgetInfo));
        await scheduleResetRedraw(snap, d);
        return;
      }
      case 'WIDGET_DELETED':
        if (name === 'SyRing') await dropRingSeat(d.store, props.widgetInfo.widgetId);
        if (name === 'SyRing' || name === 'SyLimits') await dropWindowChoice(d.store, props.widgetInfo.widgetId);
        return;
      case 'WIDGET_CLICK': {
        // The 5 h | 7 d toggle: keep the choice for this widget and redraw; the app stays closed.
        // Every other tap is an OPEN_URI, which Android opens natively.
        const w = props.clickActionData?.window;
        if (props.clickAction !== TOGGLE_ACTION || (w !== '5 h' && w !== '7 d')) return;
        await setWindowChoice(d.store, props.widgetInfo.widgetId, w);
        props.renderWidget(await tree(name, await current(d), d, props.widgetInfo));
        return;
      }
      default:
        return;
    }
  };
}

export const widgetTaskHandler = createTaskHandler();

/** Call at module scope of the app entry, phone/index.js (Android only). */
export function registerAndroidWidgets(): void {
  if (Platform.OS !== 'android') return;
  registerWidgetTaskHandler(widgetTaskHandler);
  registerWidgetConfigurationScreen(RingConfig);
}

async function refresh(snapshot: WidgetSnapshot | null, d: HandlerDeps): Promise<void> {
  await Promise.all(
    ANDROID_WIDGET_NAMES.map((widgetName: Name) => requestWidgetUpdate({ widgetName, renderWidget: (info) => tree(widgetName, snapshot, d, info) })),
  );
  await scheduleResetRedraw(snapshot, d);
}

/** After a refresh: store the snapshot and redraw every widget. */
export async function updateAndroidWidgets(snapshot: WidgetSnapshot, deps: Partial<HandlerDeps> = {}): Promise<void> {
  if (Platform.OS !== 'android') return;
  const d = { ...defaults(), ...deps };
  await writeSnapshot(d.store, snapshot);
  await refresh(snapshot, d);
}

/** Unpair: drop the snapshot and show "Open Switchyard once to load". */
export async function clearAndroidWidgets(deps: Partial<HandlerDeps> = {}): Promise<void> {
  if (Platform.OS !== 'android') return;
  const d = { ...defaults(), ...deps };
  await clearSnapshot(d.store);
  await refresh(null, d);
}
