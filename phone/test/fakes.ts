// Recording fakes of the native modules (phone.md P1). test/setup.ts installs them with
// jest.mock; tests import them from here to script answers and read the recorded calls.
// No fake touches the network or the real keychain.
import * as React from 'react';

export interface Call {
  fn: string;
  args: unknown[];
}

/** A list of calls, shared by every fake so tests can check order across modules. */
export const calls: Call[] = [];

function rec(fn: string, args: unknown[]): void {
  calls.push({ fn, args });
}

/** Calls whose name starts with prefix ("notifications." for one module). */
export function callsOf(prefix: string): Call[] {
  return calls.filter((c) => c.fn.startsWith(prefix));
}

/**
 * A module of host components: any capitalised export renders as a host element named
 * `${ns}.${Name}` with its props, so a test can walk the tree. Lower-case names are recorded
 * functions returning a description (for modifier helpers).
 */
function hostModule(ns: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  const cache = new Map<string, unknown>();
  return new Proxy(extra, {
    get(target, prop) {
      if (typeof prop !== 'string') return undefined;
      if (prop in target) return target[prop];
      if (prop === '__esModule') return true;
      if (prop === 'then' || prop === 'default') return undefined;
      if (!cache.has(prop)) {
        if (/^[A-Z]/.test(prop)) {
          const type = `${ns}.${prop}`;
          const C = (props: Record<string, unknown>) => React.createElement(type, props);
          C.displayName = type;
          cache.set(prop, C);
        } else {
          cache.set(prop, (...args: unknown[]) => {
            rec(`${ns}.${prop}`, args);
            return { modifier: prop, args };
          });
        }
      }
      return cache.get(prop);
    },
  });
}

// ---------------------------------------------------------------- expo-notifications

type Listener = (x: unknown) => void;

export const notificationsState = {
  permission: 'granted' as 'granted' | 'denied' | 'undetermined',
  token: 'ExponentPushToken[fakefakefake0001]',
  channels: new Map<string, unknown>(),
  categories: new Map<string, unknown>(),
  badge: 0,
  lastResponse: null as unknown,
  responseListeners: [] as Listener[],
  receivedListeners: [] as Listener[],
  handler: null as unknown,
  presented: [] as { request: { identifier: string; content: { data?: unknown } } }[],
};

export function emitNotificationResponse(response: unknown): void {
  notificationsState.responseListeners.forEach((l) => l(response));
}

export function emitNotificationReceived(notification: unknown): void {
  notificationsState.receivedListeners.forEach((l) => l(notification));
}

const perm = () => ({
  granted: notificationsState.permission === 'granted',
  status: notificationsState.permission,
  canAskAgain: true,
  expires: 'never',
});

export const notifications = {
  __esModule: true,
  DEFAULT_ACTION_IDENTIFIER: 'expo.modules.notifications.actions.DEFAULT',
  AndroidImportance: { UNKNOWN: 0, UNSPECIFIED: 1, NONE: 2, MIN: 3, LOW: 4, DEFAULT: 5, HIGH: 6, MAX: 7 },
  setNotificationHandler(h: unknown) {
    rec('notifications.setNotificationHandler', [h]);
    notificationsState.handler = h;
  },
  async setNotificationChannelAsync(id: string, ch: unknown) {
    rec('notifications.setNotificationChannelAsync', [id, ch]);
    notificationsState.channels.set(id, ch);
    return ch;
  },
  async setNotificationCategoryAsync(id: string, actions: unknown, options?: unknown) {
    rec('notifications.setNotificationCategoryAsync', [id, actions, options]);
    notificationsState.categories.set(id, actions);
    return { identifier: id, actions };
  },
  async getPermissionsAsync() {
    rec('notifications.getPermissionsAsync', []);
    return perm();
  },
  async requestPermissionsAsync(req?: unknown) {
    rec('notifications.requestPermissionsAsync', [req]);
    return perm();
  },
  async getExpoPushTokenAsync(opts: unknown) {
    rec('notifications.getExpoPushTokenAsync', [opts]);
    return { type: 'expo', data: notificationsState.token };
  },
  async setBadgeCountAsync(n: number) {
    rec('notifications.setBadgeCountAsync', [n]);
    notificationsState.badge = n;
    return true;
  },
  async getBadgeCountAsync() {
    return notificationsState.badge;
  },
  async dismissAllNotificationsAsync() {
    rec('notifications.dismissAllNotificationsAsync', []);
    notificationsState.presented = [];
  },
  async dismissNotificationAsync(id: string) {
    rec('notifications.dismissNotificationAsync', [id]);
    notificationsState.presented = notificationsState.presented.filter((n) => n.request.identifier !== id);
  },
  async getPresentedNotificationsAsync() {
    return notificationsState.presented;
  },
  async getLastNotificationResponseAsync() {
    rec('notifications.getLastNotificationResponseAsync', []);
    return notificationsState.lastResponse;
  },
  async clearLastNotificationResponseAsync() {
    rec('notifications.clearLastNotificationResponseAsync', []);
    notificationsState.lastResponse = null;
  },
  addNotificationResponseReceivedListener(l: Listener) {
    rec('notifications.addNotificationResponseReceivedListener', []);
    notificationsState.responseListeners.push(l);
    return { remove: () => (notificationsState.responseListeners = notificationsState.responseListeners.filter((x) => x !== l)) };
  },
  addNotificationReceivedListener(l: Listener) {
    rec('notifications.addNotificationReceivedListener', []);
    notificationsState.receivedListeners.push(l);
    return { remove: () => (notificationsState.receivedListeners = notificationsState.receivedListeners.filter((x) => x !== l)) };
  },
  async registerTaskAsync(name: string) {
    rec('notifications.registerTaskAsync', [name]);
  },
  async unregisterTaskAsync(name: string) {
    rec('notifications.unregisterTaskAsync', [name]);
  },
};

// ---------------------------------------------------------------- expo-secure-store

export const secureState = { items: new Map<string, string>(), locked: false };

export const secureStore = {
  __esModule: true,
  AFTER_FIRST_UNLOCK: 0,
  AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY: 1,
  ALWAYS: 2,
  WHEN_PASSCODE_SET_THIS_DEVICE_ONLY: 3,
  ALWAYS_THIS_DEVICE_ONLY: 4,
  WHEN_UNLOCKED: 5,
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 6,
  async getItemAsync(key: string, options?: unknown) {
    rec('secure.getItemAsync', [key, options]);
    if (secureState.locked) throw new Error('User interaction is not allowed.');
    return secureState.items.get(key) ?? null;
  },
  async setItemAsync(key: string, value: string, options?: unknown) {
    rec('secure.setItemAsync', [key, '<value>', options]); // never record the value
    if (secureState.locked) throw new Error('User interaction is not allowed.');
    secureState.items.set(key, value);
  },
  async deleteItemAsync(key: string, options?: unknown) {
    rec('secure.deleteItemAsync', [key, options]);
    secureState.items.delete(key);
  },
  async isAvailableAsync() {
    return true;
  },
};

// ---------------------------------------------------------------- expo-camera

export const cameraState = { granted: true };

export const camera = {
  __esModule: true,
  CameraView: (props: Record<string, unknown>) => React.createElement('Camera.CameraView', props),
  useCameraPermissions: () => [
    { granted: cameraState.granted, status: cameraState.granted ? 'granted' : 'denied', canAskAgain: true },
    async () => {
      rec('camera.requestPermission', []);
      return { granted: cameraState.granted };
    },
  ],
  Camera: {
    async requestCameraPermissionsAsync() {
      rec('camera.requestCameraPermissionsAsync', []);
      return { granted: cameraState.granted, status: cameraState.granted ? 'granted' : 'denied' };
    },
  },
};

// ---------------------------------------------------------------- expo-widgets

export interface FakeLiveActivity {
  id: string;
  props: unknown;
  url?: string;
  ended: unknown;
}

export const widgetsState = {
  widgets: new Map<string, { component: unknown; timeline: unknown[]; snapshot: unknown }>(),
  liveComponents: new Map<string, unknown>(),
  activities: [] as FakeLiveActivity[],
};

function fakeActivity(a: FakeLiveActivity) {
  return {
    getId: () => a.id,
    async update(props: unknown) {
      rec('widgets.activity.update', [a.id, props]);
      a.props = props;
    },
    async end(policy?: unknown, props?: unknown) {
      rec('widgets.activity.end', [a.id, policy, props]);
      a.ended = policy ?? 'default';
      widgetsState.activities = widgetsState.activities.filter((x) => x !== a);
    },
    async getPushToken() {
      return null;
    },
    addPushTokenListener() {
      return { remove() {} };
    },
  };
}

export const widgets = {
  __esModule: true,
  createWidget(name: string, component: unknown) {
    rec('widgets.createWidget', [name]);
    const w = { component, timeline: [] as unknown[], snapshot: null as unknown };
    widgetsState.widgets.set(name, w);
    return {
      reload: () => rec('widgets.reload', [name]),
      updateTimeline(entries: unknown[]) {
        rec('widgets.updateTimeline', [name, entries]);
        w.timeline = entries;
      },
      updateSnapshot(props: unknown) {
        rec('widgets.updateSnapshot', [name, props]);
        w.snapshot = props;
      },
      getTimeline: async () => w.timeline,
    };
  },
  createLiveActivity(name: string, component: unknown) {
    rec('widgets.createLiveActivity', [name]);
    widgetsState.liveComponents.set(name, component);
    return {
      start(props: unknown, url?: string) {
        rec('widgets.activity.start', [name, props, url]);
        const a: FakeLiveActivity = { id: `la_${widgetsState.activities.length + 1}`, props, url, ended: null };
        widgetsState.activities.push(a);
        return fakeActivity(a);
      },
      getInstances() {
        return widgetsState.activities.map(fakeActivity);
      },
    };
  },
  after(date: Date) {
    return { after: date };
  },
  addUserInteractionListener() {
    return { remove() {} };
  },
  addPushToStartTokenListener() {
    return { remove() {} };
  },
  widgetsDirectory: 'file:///fake/app-group/widgets/',
};

// ---------------------------------------------------------------- @expo/ui/swift-ui

export const swiftUI = hostModule('SwiftUI');
export const swiftUIModifiers = hostModule('SwiftUI.modifier');

// ---------------------------------------------------------------- react-native-android-widget

export const androidWidgetState = {
  taskHandler: null as null | ((props: unknown) => Promise<void>),
};

export const androidWidget = hostModule('Android', {
  __esModule: true,
  registerWidgetTaskHandler(fn: (props: unknown) => Promise<void>) {
    rec('androidWidget.registerWidgetTaskHandler', []);
    androidWidgetState.taskHandler = fn;
  },
  async requestWidgetUpdate(opts: unknown) {
    rec('androidWidget.requestWidgetUpdate', [opts]);
  },
  async getWidgetInfo(name: string) {
    rec('androidWidget.getWidgetInfo', [name]);
    return [];
  },
});

// ---------------------------------------------------------------- expo-task-manager

export const taskState = { tasks: new Map<string, (body: unknown) => Promise<unknown>>() };

export const taskManager = {
  __esModule: true,
  defineTask(name: string, fn: (body: unknown) => Promise<unknown>) {
    rec('tasks.defineTask', [name]);
    taskState.tasks.set(name, fn);
  },
  isTaskDefined: (name: string) => taskState.tasks.has(name),
  isTaskRegisteredAsync: async (name: string) => taskState.tasks.has(name),
  async unregisterTaskAsync(name: string) {
    rec('tasks.unregisterTaskAsync', [name]);
  },
  isAvailableAsync: async () => true,
};

/** Runs a defined task as the OS would (the background notification task, P6.5). */
export async function runTask(name: string, body: unknown): Promise<unknown> {
  const fn = taskState.tasks.get(name);
  if (!fn) throw new Error(`task ${name} not defined`);
  return fn(body);
}

// ---------------------------------------------------------------- sy-link (push.md 14.8)

type LinkStateT = import('../modules/sy-link').LinkState;
type LinkConfigT = import('../modules/sy-link').LinkConfig;

const linkInitial = (): LinkStateT => ({
  enabled: false,
  status: 'off',
  transport: null,
  connectedSince: null,
  nextRetryAt: null,
  lastError: null,
  lastSeq: 0,
  received: 0,
  rejected: 0,
  batteryExempt: false,
  notificationsAllowed: true,
  verified: 'qr',
  pingS: 180,
  checkCode: null,
});

/** Scriptable state of the sy-link fake; `emitLink` plays the native side's events. */
export const linkState = {
  available: true,
  state: linkInitial(),
  keys: {
    sign: { alg: 'p256' as const, pub: 'BFAKESIGNPUBLICKEY' },
    box: { alg: 'p256' as const, pub: 'BFAKEBOXPUBLICKEY' },
    auth: 'FAKEAUTHSECRET0000000A',
  },
  sealed: 'SEALEDKEYSFAKE',
  config: null as LinkConfigT | null,
  /** A code configure() rejects with (ERR_SY_HUB_KEY, ERR_SY_TAMPERED, ...), or null. */
  configureError: null as string | null,
  prepareError: false,
  /** What requestBatteryExemption() turns batteryExempt into. */
  exemptAfterRequest: true,
  refresh: null as import('../modules/sy-link').LinkRefresh | null,
  /** What checkUpdate() finds (null: up to date), what it rejects with, and installUpdate()'s answer or rejection. */
  update: null as import('../modules/sy-link').UpdateInfo | null,
  updateError: null as string | null,
  installResult: 'started' as 'started' | 'refused',
  installError: null as string | null,
  listeners: new Map<string, Set<(p: unknown) => void>>(),
};

export function emitLink(event: 'state' | 'refresh' | 'revoked' | 'upgrade', payload?: unknown): void {
  if (event === 'state' && payload) linkState.state = { ...linkState.state, ...(payload as Partial<LinkStateT>) };
  linkState.listeners.get(event)?.forEach((l) => l(event === 'state' ? linkState.state : payload));
}

const codedError = (code: string, message: string) => Object.assign(new Error(message), { code });

export const syLink = {
  __esModule: true,
  get available() {
    return linkState.available;
  },
  CONFIGURE_ERRORS: { hubKey: 'ERR_SY_HUB_KEY', tampered: 'ERR_SY_TAMPERED' },
  async prepareKeys() {
    rec('link.prepareKeys', []);
    if (linkState.prepareError) throw codedError('ERR_SY_KEYS', 'sy-link: keystore');
    return linkState.keys;
  },
  async sealKeys(k: string, secret: string) {
    rec('link.sealKeys', [k, secret.length]); // never the secret itself
    return linkState.sealed;
  },
  async configure(cfg: LinkConfigT) {
    rec('link.configure', [cfg]);
    if (linkState.configureError) throw codedError(linkState.configureError, 'sy-link: refused');
    linkState.config = cfg;
    linkState.state = { ...linkState.state, verified: cfg.reg, pingS: cfg.pingS, checkCode: 'k7q2-m4xa' };
  },
  async start() {
    rec('link.start', []);
    if (!linkState.config) throw codedError('ERR_SY_NOT_CONFIGURED', 'sy-link: configure first');
    linkState.state = { ...linkState.state, enabled: true, status: 'connecting' };
  },
  async stop() {
    rec('link.stop', []);
    linkState.state = { ...linkState.state, enabled: false, status: 'off', transport: null, connectedSince: null };
  },
  async reconnect() {
    rec('link.reconnect', []);
  },
  async getState() {
    rec('link.getState', []);
    return { ...linkState.state };
  },
  async setPingInterval(seconds: number) {
    rec('link.setPingInterval', [seconds]);
    if (seconds < 60 || seconds > 900) throw codedError('ERR_SY_RANGE', 'sy-link: range');
    linkState.state = { ...linkState.state, pingS: seconds };
  },
  async setSnoozeMinutes(minutes: number) {
    rec('link.setSnoozeMinutes', [minutes]);
  },
  async requestBatteryExemption() {
    rec('link.requestBatteryExemption', []);
    linkState.state = { ...linkState.state, batteryExempt: linkState.exemptAfterRequest };
    return linkState.exemptAfterRequest;
  },
  async openBatterySettings() {
    rec('link.openBatterySettings', []);
  },
  async sendLocalTest() {
    rec('link.sendLocalTest', []);
  },
  async getRefresh() {
    rec('link.getRefresh', []);
    return linkState.refresh;
  },
  async reset() {
    rec('link.reset', []);
    linkState.config = null;
    linkState.state = linkInitial();
  },
  addListener(event: string, cb: (p: unknown) => void) {
    rec('link.addListener', [event]);
    const set = linkState.listeners.get(event) ?? new Set();
    set.add(cb);
    linkState.listeners.set(event, set);
    return { remove: () => set.delete(cb) };
  },
  async checkUpdate(origin: string, token: string) {
    // The token is a secret: only its presence is recorded.
    rec('link.checkUpdate', [origin, token ? '<token>' : '']);
    if (linkState.updateError) throw codedError('ERR_SY_UPDATE', `sy-link: ${linkState.updateError}`);
    return linkState.update;
  },
  async installUpdate(info: { version: string }) {
    rec('link.installUpdate', [info.version]);
    if (linkState.installError) throw codedError('ERR_SY_UPDATE', `sy-link: ${linkState.installError}`);
    return linkState.installResult;
  },
};

// ---------------------------------------------------------------- reset

/** Clears every fake (test/setup.ts runs it before each test). */
export function resetFakes(): void {
  calls.length = 0;
  notificationsState.permission = 'granted';
  notificationsState.token = 'ExponentPushToken[fakefakefake0001]';
  notificationsState.channels.clear();
  notificationsState.categories.clear();
  notificationsState.badge = 0;
  notificationsState.lastResponse = null;
  notificationsState.responseListeners = [];
  notificationsState.receivedListeners = [];
  notificationsState.handler = null;
  notificationsState.presented = [];
  secureState.items.clear();
  secureState.locked = false;
  cameraState.granted = true;
  widgetsState.widgets.clear();
  widgetsState.liveComponents.clear();
  widgetsState.activities = [];
  androidWidgetState.taskHandler = null;
  taskState.tasks.clear();
  linkState.available = true;
  linkState.state = linkInitial();
  linkState.config = null;
  linkState.configureError = null;
  linkState.prepareError = false;
  linkState.exemptAfterRequest = true;
  linkState.refresh = null;
  linkState.update = null;
  linkState.updateError = null;
  linkState.installResult = 'started';
  linkState.installError = null;
  linkState.listeners.clear();
}
