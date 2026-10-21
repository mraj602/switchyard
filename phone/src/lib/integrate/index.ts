// The integration of the app core with notifications, widgets and the Live Activity (phases/8.md
// wave 2, WP-I): what runs after a refresh, on pairing, on start, on unpair, and the auto
// countdown. Pure: every platform piece (the widget adapters, storage, fetch, the clock) is an
// argument, so tests drive it through the recording fakes and the app binds the real modules
// (src/state/glue.tsx).
import type { Me, Overview, PushSettings } from '../../contract/api';
import { ACTION_OPEN, LOCAL_PREFS_KEY } from '../../contract/constants';
import type { LinkLike } from '../../contract/link';
import type { LiveCountdownProps } from '../../contract/live';
import type { AppInfo, Clock, KVStore } from '../../contract/platform';
import type { WidgetSnapshot } from '../../contract/snapshot';
import type { HubData } from '../data';
import type { HubClient } from '../hub';
import { countdownFor } from '../live';
import {
  DEFAULT_ACTION,
  registerForPush,
  setupChannelsAndCategories,
  syncRegistration,
  type NotificationResponseLike,
  type NotificationsLike,
  type RegisterState,
} from '../notify';
import { buildSnapshot } from '../widget';

export const DEFAULT_SNOOZE_MIN = 15;

/** How often, at most, the foreground re-reads GET /v1/rules and GET /v1/me. */
export const SETTINGS_EVERY_MS = 5 * 60_000;

/** The platform pieces the integration drives (the real ones are bound in src/state/glue.tsx). */
export interface GluePlatform {
  notifications: NotificationsLike;
  app: AppInfo;
  clock: Clock;
  /** AsyncStorage: the local settings (LOCAL_PREFS_KEY). */
  local: KVStore;
  /** The device's IANA time zone, for PATCH /v1/devices/self (P6.3). */
  tz(): string;
  updateTimelines(snapshot: WidgetSnapshot | null, now: number): void;
  updateAndroidWidgets(snapshot: WidgetSnapshot): Promise<void>;
  syncCountdown(snapshot: WidgetSnapshot | null, now: number, wantStart: boolean): Promise<unknown>;
  startCountdown(props: LiveCountdownProps, seatId: string): Promise<unknown>;
  clearIos(now: number): Promise<void>;
  clearAndroidWidgets(): Promise<void>;
  /** The sy-link module (Android; push.md 14.8): the app's only push path. Null elsewhere. */
  link?: LinkLike | null;
}

/** What the screens see of the integration (through HubApi). */
export interface GlueView {
  pushState: RegisterState | null;
  snoozeMin: number;
  autoCountdown: boolean;
}

/** What the glue component tells the integration about the app core. */
export interface GlueContext {
  me: Me | null;
  origin: string | null;
  client: HubClient | null;
  /** Refresh now (the poller's kick). */
  kick(): void;
  /** Re-read GET /v1/me into the app core. */
  reloadMe(): void;
}

// ------------------------------------------------------------------ pure helpers

/** The needs-you count of an overview (the badge, P6.6). */
export function needsYouCount(o: Overview | null): number {
  if (!o) return 0;
  const n = o.counts?.needs_you ?? o.needs_you?.length ?? 0;
  return Math.max(0, Math.trunc(Number.isFinite(n) ? n : 0));
}

/** Private snapshot: the device's own pref, else the hub default, else off (push.md 5.4). */
export function privateOf(me: Me | null, settings: PushSettings | null): boolean {
  return me?.device.prefs?.private ?? settings?.private ?? false;
}

/** The widget snapshot of the current data, or null when there is no overview yet. */
export function snapshotOf(
  d: Pick<HubData, 'overview' | 'accounts'>,
  now: number,
  me: Me | null,
  settings: PushSettings | null,
  origin: string,
): WidgetSnapshot | null {
  if (!d.overview) return null;
  return buildSnapshot(d.overview, d.accounts, now, { private: privateOf(me, settings), origin });
}

function dataOf(response: NotificationResponseLike | null | undefined): Record<string, unknown> | null {
  let d: unknown = response?.notification?.request?.content?.data;
  if (typeof d === 'string') {
    try {
      d = JSON.parse(d);
    } catch {
      return null;
    }
  }
  return d && typeof d === 'object' && !Array.isArray(d) ? (d as Record<string, unknown>) : null;
}

/**
 * Is this push about a limit (P12): `trigger: "limit_95"`, or a rate-limit `failed`. The payload
 * (push.md 6.5) has no failure detail; a rate-limit failure is the one whose title ends in the
 * state word "rate limited" or whose body is the "Resets 17:00 · …" line (push.md 5.5, 5.7). A
 * private push says neither, so a private `failed` never counts.
 */
export function isLimitPush(data: unknown): boolean {
  const d = dataOf({ actionIdentifier: '', notification: { request: { identifier: '', content: { data } } } });
  if (!d || d.v !== 1 || d.type !== 'alert') return false;
  if (d.trigger === 'limit_95') return true;
  if (d.trigger !== 'failed' || d.private === true) return false;
  const title = typeof d.title === 'string' ? d.title.trim() : '';
  const body = typeof d.body === 'string' ? d.body.trim() : '';
  return /(^|·\s*)rate limited$/.test(title) || /^Resets \d{1,2}:\d{2}\b/.test(body);
}

/**
 * The app was opened from this response and Auto countdown should start a countdown: a tap or
 * Open (not Snooze or Seen) on a limit push, with the setting on.
 */
export function wantsAutoCountdown(response: NotificationResponseLike | null | undefined, autoCountdown: boolean): boolean {
  if (!autoCountdown || !response) return false;
  const id = response.actionIdentifier;
  if (id !== DEFAULT_ACTION && id !== ACTION_OPEN) return false;
  return isLimitPush(response.notification?.request?.content?.data);
}

/** The local settings (LOCAL_PREFS_KEY); defaults when missing or unreadable. */
export async function loadLocalPrefs(kv: KVStore): Promise<{ auto_countdown: boolean }> {
  try {
    const text = await kv.getItem(LOCAL_PREFS_KEY);
    const v = text ? (JSON.parse(text) as { auto_countdown?: unknown }) : null;
    return { auto_countdown: typeof v?.auto_countdown === 'boolean' ? v.auto_countdown : true };
  } catch {
    return { auto_countdown: true };
  }
}

export async function saveLocalPrefs(kv: KVStore, prefs: { auto_countdown: boolean }): Promise<void> {
  await kv.setItem(LOCAL_PREFS_KEY, JSON.stringify({ auto_countdown: prefs.auto_countdown }));
}

async function quietly(p: () => unknown): Promise<void> {
  try {
    await p();
  } catch {
    // widgets, badges and the Live Activity are best effort
  }
}

/**
 * The widget and Live Activity steps after a refresh (P5, P11, P12), and the badge (P6.6).
 * Widgets only when the device's `prefs.widgets` is on; the countdown whenever `me` is known
 * (a countdown the person started is kept up to date and ended by the P12 policy). Never throws.
 */
export async function widgetSteps(
  p: Pick<GluePlatform, 'notifications' | 'updateTimelines' | 'updateAndroidWidgets' | 'syncCountdown'>,
  d: Pick<HubData, 'overview' | 'accounts'>,
  now: number,
  ctx: {
    me: Me | null;
    settings: PushSettings | null;
    origin: string | null;
    wantStart: boolean;
    /** False once the pairing these data came from is gone: no further step writes anything. */
    live?: () => boolean;
  },
): Promise<void> {
  const live = ctx.live ?? (() => true);
  if (!live()) return;
  if (d.overview) await quietly(() => p.notifications.setBadgeCountAsync(needsYouCount(d.overview)));
  if (!ctx.origin || !live()) return;
  let snap: WidgetSnapshot | null;
  try {
    // Before GET /v1/me has answered the private pref is unknown: assume the hub's, else private.
    snap = snapshotOf(d, now, ctx.me, ctx.settings ?? (ctx.me ? null : { private: true }), ctx.origin);
  } catch {
    return;
  }
  if (!snap) return;
  const s = snap;
  // The Android widgets read the snapshot this app writes locally: it is written on every
  // refresh, whatever `prefs.widgets` says (that pref only asks the hub for refresh frames over
  // the link). Writing it only when the pref was true left a widget with no snapshot, so it only
  // ever said "Open Switchyard".
  if (ctx.me?.device.prefs?.widgets === true) await quietly(() => p.updateTimelines(s, now));
  if (!live()) return;
  await quietly(() => p.updateAndroidWidgets(s));
  if (!live()) return;
  if (!ctx.me) return;
  if (!live()) return;
  await quietly(() => p.syncCountdown(s, now, ctx.wantStart));
}

// ------------------------------------------------------------------ the foreground integration

export interface Glue {
  /** The current view for the screens (a new object on every change). */
  view(): GlueView;
  subscribe(fn: () => void): () => void;
  /** The glue component keeps this current. */
  setContext(ctx: GlueContext): void;
  /** Read the local settings once at start. */
  loadLocal(): Promise<void>;
  /** stateHooks.afterRefresh. */
  afterRefresh(d: HubData): Promise<void>;
  /** stateHooks.onCleared. */
  onCleared(): Promise<void>;
  /** stateHooks.onPaired: GET /v1/rules, the registration (P6.3), then categories. */
  onPaired(client: HubClient): Promise<void>;
  /** On start when paired (once per client): rules, syncRegistration, categories. */
  startUp(client: HubClient): Promise<void>;
  /** Settings "Turn on notifications". */
  enablePush(client: HubClient): Promise<void>;
  /** A notification response opened the app (before its decision runs): the auto countdown. */
  noteResponse(response: NotificationResponseLike): void;
  /** Accounts "Count down on Lock Screen" (iOS). */
  canCountdown(d: Pick<HubData, 'overview' | 'accounts'>, seatId: string): boolean;
  startCountdown(d: Pick<HubData, 'overview' | 'accounts'>, seatId: string): Promise<void>;
  setAutoCountdown(on: boolean): Promise<void>;
  /**
   * The link's events (push.md 14.8): `refresh` and `revoked` refresh the data now (a revoked
   * phone then gets the 401 that shows Pair, P3). Returns the unsubscribe.
   */
  attachLink(): () => void;
  /** The link module, when present (Android; for Settings → Notifications). */
  readonly link: LinkLike | null;
  /** Tests. */
  internals(): { wantStart: boolean; settings: PushSettings | null; categoriesMin: number | null };
}

export function createGlue(p: GluePlatform): Glue {
  let ctx: GlueContext = { me: null, origin: null, client: null, kick: () => {}, reloadMe: () => {} };
  let settings: PushSettings | null = null;
  let pushState: RegisterState | null = null;
  let autoCountdown = true;
  let wantStart = false;
  let categoriesMin: number | null = null;
  let settingsAt: number | null = null;
  let startedFor: HubClient | null = null;
  let lastData: Pick<HubData, 'overview' | 'accounts'> | null = null;
  // The pairing generation: bumped by onCleared, so a refresh of the old pairing
  // that is still running writes no widget, badge or Live Activity after it (review finding 4).
  let gen = 0;
  let view: GlueView = { pushState, snoozeMin: DEFAULT_SNOOZE_MIN, autoCountdown };
  const subs = new Set<() => void>();
  /** The sy-link module, when present (Android). */
  const link = p.link ?? null;
  let snoozeSent: number | null = null;
  const sendSnooze = async () => {
    const min = snoozeMin();
    if (!link || snoozeSent === min) return;
    await quietly(async () => {
      await link.setSnoozeMinutes(min);
      snoozeSent = min;
    });
  };

  const snoozeMin = () => {
    const n = settings?.snooze_min;
    return typeof n === 'number' && n > 0 ? n : DEFAULT_SNOOZE_MIN;
  };
  const emit = () => {
    const next = { pushState, snoozeMin: snoozeMin(), autoCountdown };
    if (next.pushState === view.pushState && next.snoozeMin === view.snoozeMin && next.autoCountdown === view.autoCountdown) return;
    view = next;
    subs.forEach((f) => f());
  };

  const permission = async (): Promise<{ granted: boolean; status: string }> => {
    try {
      return await p.notifications.getPermissionsAsync();
    } catch {
      return { granted: false, status: 'undetermined' };
    }
  };
  const granted = async () => (await permission()).granted;

  /** Categories with the hub's snooze_min (P6.2). */
  const setupCategories = async () => {
    const min = snoozeMin();
    await quietly(async () => {
      await setupChannelsAndCategories(p.notifications, p.app.platform, min);
      categoriesMin = min;
    });
  };

  /** GET /v1/rules (and GET /v1/me) at most every SETTINGS_EVERY_MS unless forced. */
  const loadSettings = async (client: HubClient, force: boolean) => {
    const now = p.clock.now();
    if (!force && settingsAt !== null && now - settingsAt < SETTINGS_EVERY_MS) return;
    settingsAt = now;
    if (!force) ctx.reloadMe();
    try {
      settings = (await client.rules()).settings ?? null;
    } catch {
      return; // keep what we had; Snooze shows the default
    }
    emit();
    await sendSnooze();
    if (categoriesMin !== null && categoriesMin !== snoozeMin()) await setupCategories();
  };

  const register = async (client: HubClient) => {
    pushState = await registerForPush({
      notifications: p.notifications,
      app: p.app,
      patchSelf: (prefs) => client.patchSelf(prefs),
      tz: p.tz(),
      // widgets: true only on the first registration (P6.3): GET /v1/me when it has answered,
      // else GET /v1/devices/self.
      currentPrefs: async () => (ctx.client === client && ctx.me?.device.prefs) || ((await client.deviceSelf()).prefs ?? {}),
    });
    emit();
    if (await granted()) {
      await setupCategories();
    }
  };

  const steps = (d: Pick<HubData, 'overview' | 'accounts'>, mine = gen) => {
    const want = wantStart && !!ctx.me && !!ctx.origin && !!d.overview;
    if (want) wantStart = false; // one shot: used by the first refresh that can build a snapshot
    return widgetSteps(p, d, p.clock.now(), { me: ctx.me, settings, origin: ctx.origin, wantStart: want, live: () => mine === gen });
  };

  return {
    view: () => view,
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    setContext(next) {
      const meArrived = !ctx.me && !!next.me;
      ctx = next;
      // The first refresh may land before GET /v1/me: run the widget steps once me is known.
      if (meArrived && lastData) void steps(lastData);
    },
    async loadLocal() {
      autoCountdown = (await loadLocalPrefs(p.local)).auto_countdown;
      emit();
    },
    async afterRefresh(d) {
      const mine = gen;
      lastData = { overview: d.overview, accounts: d.accounts };
      await steps(lastData, mine);
      if (mine !== gen) return;
      if (ctx.client) await quietly(() => loadSettings(ctx.client!, false));
    },
    async onCleared() {
      gen++;
      // The old hub's notifications go too: their Snooze and Seen tokens are that pairing's.
      await quietly(() => p.notifications.dismissAllNotificationsAsync?.());
      await quietly(() => p.clearIos(p.clock.now()));
      await quietly(() => p.clearAndroidWidgets());
      await quietly(() => p.notifications.setBadgeCountAsync(0));
      // Unpair: the link's keys, pins and storage go (push.md 14.8 reset).
      if (link) await quietly(() => link.reset());
      snoozeSent = null;
      settings = null;
      settingsAt = null;
      pushState = null;
      wantStart = false;
      startedFor = null;
      lastData = null;
      emit();
    },
    async onPaired(client) {
      startedFor = client;
      await loadSettings(client, true);
      await register(client);
      // Instant delivery is on by default after pairing (P6.7); configure() ran in the claim.
      if (link) await quietly(() => link.start());
    },
    async startUp(client) {
      if (startedFor === client) return;
      startedFor = client;
      await loadSettings(client, true);
      const r = await syncRegistration({
        notifications: p.notifications,
        app: p.app,
        patchSelf: (prefs) => client.patchSelf(prefs),
        tz: p.tz(),
      });
      // Never asked is "off" (Settings offers "Turn on notifications"), not "blocked".
      const perm = await permission();
      pushState = r === 'unchanged' ? 'registered' : r === 'blocked' && perm.status !== 'denied' ? null : r;
      emit();
      if (perm.granted) {
        await setupCategories();
      }
      // push.md 14.4: start on each app start while instant delivery is on.
      if (link) {
        await quietly(async () => {
          if ((await link.getState()).enabled) await link.start();
        });
      }
    },
    async enablePush(client) {
      await register(client);
    },
    noteResponse(response) {
      if (!wantsAutoCountdown(response, autoCountdown)) return;
      wantStart = true;
      ctx.kick();
    },
    canCountdown(d, seatId) {
      if (!ctx.origin) return false;
      const snap = snapshotOf(d, p.clock.now(), ctx.me, settings, ctx.origin);
      return !!snap && countdownFor(snap, seatId, p.clock.now()) !== null;
    },
    async startCountdown(d, seatId) {
      const now = p.clock.now();
      const snap = ctx.origin ? snapshotOf(d, now, ctx.me, settings, ctx.origin) : null;
      const props = snap ? countdownFor(snap, seatId, now) : null;
      if (!props) throw new Error('This account has no limit close to its reset');
      await p.startCountdown(props, seatId);
    },
    async setAutoCountdown(on) {
      autoCountdown = on;
      emit();
      await quietly(() => saveLocalPrefs(p.local, { auto_countdown: on }));
    },
    link,
    attachLink() {
      if (!link) return () => {};
      const subs = [link.addListener('refresh', () => ctx.kick()), link.addListener('revoked', () => ctx.kick())];
      return () => subs.forEach((x) => x.remove());
    },
    internals: () => ({ wantStart, settings, categoriesMin }),
  };
}
