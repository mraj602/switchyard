// App-side adapter for the iOS widgets and the Live Activity (phone.md P11, P12). The app (and
// the background notification task) calls these after every refresh; nothing here reads the
// keychain or the hub. iOS only: on other platforms every function is a no-op.
import { Platform } from 'react-native';
import { after, createLiveActivity, createWidget } from 'expo-widgets';

import { LIVE_ACTIVITY_NAME, type LiveCountdownProps } from '../../src/contract/live';
import type { WidgetSnapshot } from '../../src/contract/snapshot';
import { endAt, planLive } from '../../src/lib/live';
import { buildTimeline, emptyTimeline } from '../../src/lib/widget';
import { AccountsMedium } from './AccountsMedium';
import { NeedsYouSmall } from './NeedsYouSmall';
import { ResetCountdown } from './ResetCountdown';

export const IOS_WIDGET_NAMES = ['NeedsYouSmall', 'AccountsMedium'] as const;

type Handles = {
  small: ReturnType<typeof createWidget>;
  medium: ReturnType<typeof createWidget>;
  live: ReturnType<typeof createLiveActivity<LiveCountdownProps>>;
};

let handles: Handles | null = null;
/** What the running activity last showed (an activity instance cannot be read back). */
let lastLive: LiveCountdownProps | null = null;

const isIos = () => Platform.OS === 'ios';

function get(): Handles {
  if (!handles) {
    handles = {
      small: createWidget('NeedsYouSmall', NeedsYouSmall as never),
      medium: createWidget('AccountsMedium', AccountsMedium as never),
      live: createLiveActivity<LiveCountdownProps>(LIVE_ACTIVITY_NAME, ResetCountdown as never),
    };
  }
  return handles;
}

/** Writes the timeline of both widgets (snapshot null: "Open Switchyard to set up"). */
export function updateTimelines(snapshot: WidgetSnapshot | null, now: number, tz?: string): void {
  if (!isIos()) return;
  const h = get();
  const entries = snapshot ? buildTimeline(snapshot, now, tz) : emptyTimeline(now);
  h.small.updateTimeline(entries as never);
  h.medium.updateTimeline(entries as never);
}

/** The deep link a countdown opens (P7): the account card. */
export function countdownUrl(seatId: string | undefined): string {
  return seatId ? `switchyard://accounts?seat=${encodeURIComponent(seatId)}` : 'switchyard://accounts';
}

export type CountdownResult = 'started' | 'updated' | 'ended' | 'none';

/** Ends every running countdown. `immediate` is for unpair; otherwise it lingers until `at`. */
export async function endCountdown(immediate: boolean, at?: Date): Promise<void> {
  if (!isIos()) return;
  const running = get().live.getInstances();
  const date = at ?? (lastLive ? endAt(lastLive) : new Date(Date.now() + 15 * 60 * 1000));
  await Promise.all(running.map((a) => a.end(immediate ? 'immediate' : after(date))));
  lastLive = null;
}

/**
 * Starts the countdown the person asked for (or the auto one). At most one activity runs: if
 * one does, it is updated instead (and any extra ones are ended).
 */
export async function startCountdown(props: LiveCountdownProps, seatId?: string): Promise<CountdownResult> {
  if (!isIos()) return 'none';
  const live = get().live;
  const running = live.getInstances();
  const [first, ...extra] = running;
  await Promise.all(extra.map((a) => a.end('immediate')));
  const stale = new Date(Date.parse(props.resets_at));
  if (first) {
    await first.update(props, stale);
    lastLive = props;
    return 'updated';
  }
  live.start(props, countdownUrl(seatId), stale);
  lastLive = props;
  return 'started';
}

/**
 * After a refresh: update a running countdown, or end it by the P12 policy; with `wantStart`
 * it also starts one when nothing runs and something qualifies. The caller passes the seat id
 * lookup for the start link through the snapshot (labels are unique enough per hub).
 */
export async function syncCountdown(snapshot: WidgetSnapshot | null, now: number, wantStart = false): Promise<CountdownResult> {
  if (!isIos()) return 'none';
  const running = get().live.getInstances();
  const step = planLive(snapshot, now, running.length > 0 ? lastLive ?? placeholder(now) : null, wantStart);
  switch (step.kind) {
    case 'start': {
      const id = snapshot?.seats.find((s) => s.label === step.props.seat)?.id;
      return startCountdown(step.props, id);
    }
    case 'update':
      return startCountdown(step.props);
    case 'end':
      await endCountdown(false, step.at);
      return 'ended';
    default:
      return 'none';
  }
}

/** An activity survived an app restart, so its props are unknown: treat it as ending soon. */
function placeholder(now: number): LiveCountdownProps {
  return { seat: '', colour: '#8a8a8a', window: 'five_hour', pct: 0, resets_at: new Date(now + 1).toISOString(), started_at: new Date(now).toISOString() };
}

/** Unpair: nothing about the hub may stay on the Lock Screen or the Home Screen. */
export async function clearIos(now: number): Promise<void> {
  if (!isIos()) return;
  await endCountdown(true);
  updateTimelines(null, now);
}

/** Test seam: forget the created handles and the last props. */
export function resetAdapter(): void {
  handles = null;
  lastLive = null;
}
