// Notifications (phone.md P6). Owner: WP-N.
// Pure: takes a Notifications-shaped object (expo-notifications' API surface it uses), fetch
// and a clock. No token, title, body or response body ever reaches an error or a log (hard
// rule 3): failures are reported as states and status codes only.
import type { ActionReply, DevicePrefs } from '../../contract/api';
import {
  ACTED_KEY,
  ACTION_OPEN,
  ACTION_SEEN,
  ACTION_SNOOZE,
  ACTION_TOKEN,
  ALERT_CATEGORY,
  ANDROID_CHANNELS,
  REQUEST_TIMEOUT_MS,
} from '../../contract/constants';
import type { Clock, FetchLike, AppInfo, KVStore } from '../../contract/platform';
import type { AppRoute } from '../../contract/routes';
import { normalizeOrigin } from '../hub';
import { hashToRoute } from '../links';

/**
 * Flip to false if the manual check shows iOS does not run a background action's JS handler
 * while the app is killed (P6.2, risk 3): Snooze and Seen then open the app briefly.
 */
export const BACKGROUND_ACTIONS_IOS = true;

/** What expo-notifications' DEFAULT_ACTION_IDENTIFIER is (a plain tap). */
export const DEFAULT_ACTION = 'expo.modules.notifications.actions.DEFAULT';

/** expo-notifications' AndroidImportance values, used when the adapter passes none. */
const IMPORTANCE_FALLBACK: Record<string, number> = { LOW: 2, DEFAULT: 3, HIGH: 4 };

/** The expo-notifications surface the app uses (a recording fake in test/setup.ts). */
export interface NotificationsLike {
  /** The module's AndroidImportance enum; pass the module itself and it is there. */
  AndroidImportance?: Record<string, number>;
  setNotificationChannelAsync(id: string, channel: { name: string; importance: number }): Promise<unknown>;
  setNotificationCategoryAsync(
    id: string,
    actions: { identifier: string; buttonTitle: string; options?: { opensAppToForeground?: boolean } }[],
  ): Promise<unknown>;
  getPermissionsAsync(): Promise<{ granted: boolean; status: string }>;
  requestPermissionsAsync(req?: unknown): Promise<{ granted: boolean; status: string }>;
  setBadgeCountAsync(n: number): Promise<boolean>;
  dismissNotificationAsync(id: string): Promise<void>;
  /** Unpairing removes the old hub's notifications, so their buttons can't be pressed later. */
  dismissAllNotificationsAsync?(): Promise<void>;
  getLastNotificationResponseAsync(): Promise<NotificationResponseLike | null>;
  clearLastNotificationResponseAsync?(): Promise<void>;
  setNotificationHandler?(handler: unknown): void;
  addNotificationResponseReceivedListener?(l: (r: NotificationResponseLike) => void): { remove(): void };
  addNotificationReceivedListener?(l: (n: unknown) => void): { remove(): void };
}

/** The parts of a NotificationResponse decideResponse reads. */
export interface NotificationResponseLike {
  actionIdentifier: string; // DEFAULT_ACTION for a plain tap
  /** `date`: when the OS delivered it (ms, the phone's clock), as expo-notifications reports it. */
  notification: { date?: number; request: { identifier: string; content: { data?: unknown } } };
}

export type Decision =
  /**
   * `at`: when the notification arrived (ms), and `origin`: the hub that sent it, when the
   * response says; the act is dropped when either shows another pairing (P6.4).
   */
  | { kind: 'act'; token: string; action: 'snooze' | 'seen'; at?: number; origin?: string }
  | { kind: 'open-link'; url: string }
  | { kind: 'route'; route: AppRoute }
  /** Ask the hub's /health first (1.5 s), then take one of the two decisions. */
  | { kind: 'probe'; then: { reachable: Decision; unreachable: Decision } }
  /** A button press without a usable token: do nothing. */
  | { kind: 'none' };

export type RegisterState =
  | 'registered'
  | 'blocked' // "Blocked in system settings"
  | 'error';

export interface RegisterDeps {
  notifications: NotificationsLike;
  app: AppInfo;
  patchSelf(prefs: Partial<DevicePrefs>): Promise<unknown>;
  tz: string;
  /**
   * The device's current prefs (GET /v1/me or /v1/devices/self), so a registration turns the
   * widgets on only the first time. Without it, or when it fails, `widgets` is not sent.
   */
  currentPrefs?(): Promise<DevicePrefs | null | undefined>;
}

// ------------------------------------------------------------------ channels, categories

/** Creates the P6.1 channels (Android only). Must run before any permission call. */
export async function setupChannels(n: NotificationsLike, platform: 'ios' | 'android'): Promise<void> {
  if (platform !== 'android') return;
  const table = n.AndroidImportance ?? IMPORTANCE_FALLBACK;
  for (const ch of ANDROID_CHANNELS) {
    const importance = table[ch.importance] ?? IMPORTANCE_FALLBACK[ch.importance]!;
    await n.setNotificationChannelAsync(ch.id, { name: ch.name, importance });
  }
}

/** Creates the P6.1 channels (Android) and the sy_alert category with snoozeMin's title. */
export async function setupChannelsAndCategories(
  n: NotificationsLike,
  platform: 'ios' | 'android',
  snoozeMin: number,
): Promise<void> {
  await setupChannels(n, platform);
  // Only iOS has background actions to flip (P6.2); on Android they never open the app.
  const background = platform === 'ios' ? !BACKGROUND_ACTIONS_IOS : false;
  await n.setNotificationCategoryAsync(ALERT_CATEGORY, [
    { identifier: ACTION_OPEN, buttonTitle: 'Open', options: { opensAppToForeground: true } },
    { identifier: ACTION_SNOOZE, buttonTitle: `Snooze ${snoozeMin} min`, options: { opensAppToForeground: background } },
    { identifier: ACTION_SEEN, buttonTitle: 'Seen', options: { opensAppToForeground: background } },
  ]);
}

// ------------------------------------------------------------------ registration

/**
 * The PATCH after a registration (P6.3): the time zone always; `widgets: true` only on the first
 * registration, when the device has never had `prefs.widgets` set. A person who turned widget
 * refresh off keeps it off through re-registrations; unknown prefs never override it.
 */
export function registrationPrefs(tz: string, prefs: DevicePrefs | null | undefined): Partial<DevicePrefs> {
  return prefs && prefs.widgets === undefined ? { tz, widgets: true } : { tz };
}

async function knownPrefs(deps: RegisterDeps, known?: DevicePrefs | null): Promise<DevicePrefs | null> {
  if (known !== undefined) return known ?? {};
  if (!deps.currentPrefs) return null;
  try {
    return (await deps.currentPrefs()) ?? {};
  } catch {
    return null;
  }
}

/**
 * Android channels before any permission call, then the permission, then the prefs PATCH (P6.3).
 * The link target itself comes from pairing (push.md 13.7): nothing is registered here.
 */
export async function registerForPush(deps: RegisterDeps): Promise<RegisterState> {
  const n = deps.notifications;
  try {
    await setupChannels(n, deps.app.platform);
    let perm = await n.getPermissionsAsync();
    if (!perm.granted) {
      perm = await n.requestPermissionsAsync({ ios: { allowAlert: true, allowBadge: true, allowSound: true } });
    }
    if (!perm.granted) return 'blocked';
    await deps.patchSelf(registrationPrefs(deps.tz, await knownPrefs(deps)));
    return 'registered';
  } catch {
    return 'error';
  }
}

/**
 * On every start (P6.3): the link target is the hub's (push.md 13.7), so there is nothing to
 * register again; this only reports whether the permission is still granted. Never asks.
 */
export async function syncRegistration(deps: RegisterDeps): Promise<RegisterState | 'unchanged'> {
  try {
    return (await deps.notifications.getPermissionsAsync()).granted ? 'unchanged' : 'blocked';
  } catch {
    return 'error';
  }
}

// ------------------------------------------------------------------ responses

interface ParsedData {
  tap: 'auto' | 'session' | 'claude';
  url: unknown;
  remote: string | null;
  actions: { snooze?: unknown; seen?: unknown } | null;
  /** The sending hub's origin, if the payload names one (not in push.md 6.5 yet; read if present). */
  origin: string | null;
}

const ORIGIN_RE = /^https?:\/\/[^\s/\\@?#]+\/?$/;

const HTTPS_URL = /^https:\/\/[^\s/\\@?#]+(?:[/?#][^\s\\]*)?$/;

/** The payload's `data`, or null when missing, not an object or not version 1. */
function parseData(data: unknown): ParsedData | null {
  let d = data;
  if (typeof d === 'string') {
    try {
      d = JSON.parse(d);
    } catch {
      return null;
    }
  }
  if (!d || typeof d !== 'object' || Array.isArray(d)) return null;
  const o = d as Record<string, unknown>;
  if (o.v !== 1) return null;
  const tap = o.tap === 'session' || o.tap === 'claude' ? o.tap : 'auto';
  const remote = typeof o.remote_url === 'string' && HTTPS_URL.test(o.remote_url) ? o.remote_url : null;
  const actions = o.actions && typeof o.actions === 'object' && !Array.isArray(o.actions) ? (o.actions as ParsedData['actions']) : null;
  const origin = typeof o.origin === 'string' && ORIGIN_RE.test(o.origin) ? o.origin : null;
  return { tap, url: o.url, remote, actions, origin };
}

const NOW_ROUTE: Decision = { kind: 'route', route: { name: 'now' } };

/**
 * P6.4: what a tap or a button does. `reachable` answers the /health probe when it was
 * asked (null = not asked yet: a 'probe' decision comes back when the rule needs one).
 */
export function decideResponse(response: NotificationResponseLike, reachable: boolean | null): Decision {
  const id = response?.actionIdentifier;
  const p = parseData(response?.notification?.request?.content?.data);

  if (id === ACTION_SNOOZE || id === ACTION_SEEN) {
    const token = p?.actions?.[id];
    if (typeof token !== 'string' || !ACTION_TOKEN.test(token)) return { kind: 'none' };
    const d: Extract<Decision, { kind: 'act' }> = { kind: 'act', token, action: id };
    const at = response?.notification?.date;
    if (typeof at === 'number' && Number.isFinite(at) && at > 0) d.at = at;
    if (p?.origin) d.origin = p.origin;
    return d;
  }
  if (id !== DEFAULT_ACTION && id !== ACTION_OPEN) return NOW_ROUTE;
  if (!p) return NOW_ROUTE;

  // The tap rule of sw-lib.js chooseTarget.
  const link: Decision | null = p.remote ? { kind: 'open-link', url: p.remote } : null;
  const route: Decision = {
    kind: 'route',
    route: (typeof p.url === 'string' ? hashToRoute(p.url) : null) ?? { name: 'now' },
  };
  if (link && p.tap === 'claude') return link;
  if (link && p.tap === 'auto') {
    if (reachable === null) return { kind: 'probe', then: { reachable: route, unreachable: link } };
    return reachable ? route : link;
  }
  return route;
}

// ------------------------------------------------------------------ the act call

export interface ActResult {
  ok: boolean;
  /** HTTP status, or null when the hub could not be reached. */
  status: number | null;
  applied?: boolean;
}

/**
 * POST <origin>/v1/actions/<token> with body {} and no bearer (P6.4). Never retried, never
 * throws, and nothing it returns contains the token.
 */
export async function actOnToken(
  deps: { fetch: FetchLike; clock: Clock },
  origin: string,
  token: string,
): Promise<ActResult> {
  if (!ACTION_TOKEN.test(token)) return { ok: false, status: null };
  const ctl = typeof AbortController === 'function' ? new AbortController() : null;
  const timer = deps.clock.setTimeout(() => ctl?.abort(), REQUEST_TIMEOUT_MS);
  try {
    const res = await deps.fetch(`${origin.replace(/\/+$/, '')}/v1/actions/${token}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
      redirect: 'error',
      signal: ctl?.signal,
    });
    let applied: boolean | undefined;
    if (res.ok) {
      try {
        const j = (await res.json()) as Partial<ActionReply> | null;
        if (j && typeof j.applied === 'boolean') applied = j.applied;
      } catch {
        // an unreadable body does not change the outcome
      }
    }
    return { ok: res.ok, status: res.status, applied };
  } catch {
    return { ok: false, status: null };
  } finally {
    deps.clock.clearTimeout(timer);
  }
}

export interface PerformDeps {
  notifications: NotificationsLike;
  fetch: FetchLike;
  clock: Clock;
  /** The paired hub's origin, or null when not paired / the keychain is locked. */
  origin(): Promise<string | null>;
  openUrl(url: string): Promise<void>;
  navigate(route: AppRoute): void;
  /** After a Snooze or Seen was applied: refresh the screens. */
  onActed?(action: 'snooze' | 'seen'): void;
  /**
   * When the current pairing was made (ms), or null when unknown. An act on a notification
   * that arrived before it belongs to a previous pairing and is dropped (its token is another
   * hub's, or this hub's for a device that is gone).
   */
  pairedAt?(): Promise<number | null>;
  /** Shared storage (AsyncStorage) of the acts already done (ACTED_KEY), see claimAct. */
  acted?: KVStore;
}

const ACTED_KEEP = 50;
const actingNow = new Set<string>();

/**
 * True the first time a "<notification id>|<action>" is seen, false after: within this JS
 * runtime at once (two handlers side by side), and across runtimes through `kv`. A failing store never blocks an act.
 */
export async function claimAct(kv: KVStore | undefined, key: string): Promise<boolean> {
  if (actingNow.has(key)) return false;
  actingNow.add(key);
  if (actingNow.size > ACTED_KEEP) actingNow.delete(actingNow.values().next().value as string);
  if (!kv) return true;
  try {
    const text = await kv.getItem(ACTED_KEY);
    const parsed = text ? (JSON.parse(text) as unknown) : [];
    const list = Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string') : [];
    if (list.includes(key)) return false;
    list.push(key);
    await kv.setItem(ACTED_KEY, JSON.stringify(list.slice(-ACTED_KEEP)));
  } catch {
    // unreadable or full: act anyway (the in-memory set still dedupes this runtime)
  }
  return true;
}

/** Tests: forget the in-memory acts. */
export function resetActed(): void {
  actingNow.clear();
}

/** The act belongs to another pairing than the current one (P6.4). */
async function fromOtherPairing(deps: PerformDeps, d: Extract<Decision, { kind: 'act' }>, origin: string): Promise<boolean> {
  if (d.origin && normalizeOrigin(d.origin) !== normalizeOrigin(origin)) return true;
  if (d.at === undefined || !deps.pairedAt) return false;
  let since: number | null;
  try {
    since = await deps.pairedAt();
  } catch {
    since = null;
  }
  return since !== null && Number.isFinite(since) && d.at < since;
}

async function dismiss(deps: PerformDeps, notificationId?: string): Promise<void> {
  if (!notificationId) return;
  try {
    await deps.notifications.dismissNotificationAsync(notificationId);
  } catch {
    // already gone
  }
}

/**
 * Carries out a final decision (never 'probe'): the act call and then dismissing that
 * notification, a link, or a route. The result of an act is not retried (pwa.md 5). An act
 * goes only to the hub that sent the notification: one from a previous pairing is dismissed
 * and dropped. Each notification's Snooze or Seen runs once (claimAct).
 */
export async function performDecision(deps: PerformDeps, d: Decision, notificationId?: string): Promise<void> {
  switch (d.kind) {
    case 'act': {
      const origin = await deps.origin();
      if (!origin) return;
      if (await fromOtherPairing(deps, d, origin)) {
        await dismiss(deps, notificationId);
        return;
      }
      if (notificationId && !(await claimAct(deps.acted, `${notificationId}|${d.action}`))) return;
      const r = await actOnToken(deps, origin, d.token);
      await dismiss(deps, notificationId);
      if (r.ok) deps.onActed?.(d.action);
      return;
    }
    case 'open-link':
      try {
        await deps.openUrl(d.url);
      } catch {
        deps.navigate({ name: 'now' });
      }
      return;
    case 'route':
      deps.navigate(d.route);
      return;
    case 'probe':
    case 'none':
      return;
  }
}

// ------------------------------------------------------------------ listeners

export interface InitDeps {
  notifications: NotificationsLike;
  app: AppInfo;
  /** Runs a final decision (never 'probe'): wire it to performDecision. */
  run(d: Decision, response: NotificationResponseLike): Promise<void>;
  /** The /health probe of the paired hub (1.5 s); false when unpaired or unreachable. */
  probe(): Promise<boolean>;
  /** A push arrived in the foreground: refresh now. */
  onReceived(): void;
}

const SEEN_KEEP = 50;

function dataType(n: unknown): string | null {
  const content = (n as { request?: { content?: { data?: unknown } } } | null)?.request?.content;
  const d = content?.data;
  return d && typeof d === 'object' ? String((d as { type?: unknown }).type ?? '') : null;
}

/** Called once from app/_layout.tsx (module scope): listeners and the cold-start response. */
export function initNotifications(deps: InitDeps): () => void {
  const n = deps.notifications;
  const handled: string[] = [];
  let disposed = false;

  n.setNotificationHandler?.({
    // Foreground pushes are shown (banner, list, badge); silent refreshes are not (P6.4).
    handleNotification: async (notification: unknown) => {
      const refresh = dataType(notification) === 'refresh';
      return { shouldShowBanner: !refresh, shouldShowList: !refresh, shouldSetBadge: true, shouldPlaySound: false };
    },
  });

  const handle = async (r: NotificationResponseLike): Promise<void> => {
    if (disposed) return;
    const key = `${r.notification?.request?.identifier}|${r.actionIdentifier}`;
    if (handled.includes(key)) return; // cold start can deliver the same response twice
    handled.push(key);
    if (handled.length > SEEN_KEEP) handled.shift();
    try {
      let d = decideResponse(r, null);
      if (d.kind === 'probe') {
        const ok = await deps.probe().catch(() => false);
        d = ok ? d.then.reachable : d.then.unreachable;
      }
      await deps.run(d, r);
    } catch {
      // a failed action is not retried and carries nothing worth logging
    }
  };

  const subs: { remove(): void }[] = [];
  const onResp = n.addNotificationResponseReceivedListener?.((r) => void handle(r));
  if (onResp) subs.push(onResp);
  const onRecv = n.addNotificationReceivedListener?.(() => deps.onReceived());
  if (onRecv) subs.push(onRecv);

  void (async () => {
    try {
      const last = await n.getLastNotificationResponseAsync();
      if (last) {
        await handle(last);
        await n.clearLastNotificationResponseAsync?.();
      }
    } catch {
      // nothing to do
    }
  })();

  return () => {
    disposed = true;
    subs.forEach((s) => s.remove());
  };
}

export * from './makers';
export * from './link';
