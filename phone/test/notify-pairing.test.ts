// Notification actions across pairings (phase 8 review): an act goes only to the pairing that
// issued the notification (finding 2), and each tap acts once even when the Android background
// task and the response listener both see it (finding 9). Offline: fetch is a fake.
import * as ExpoNotifications from 'expo-notifications';
import type { Clock, FetchLike, KVStore, RequestInitLike } from '../src/contract/platform';
import { ACTED_KEY } from '../src/contract/constants';
import {
  claimAct,
  decideResponse,
  performDecision,
  resetActed,
  runNotifyTask,
  type Decision,
  type NotificationResponseLike,
  type NotificationsLike,
  type PerformDeps,
} from '../src/lib/notify';
import { callsOf } from './fakes';

const N = ExpoNotifications as unknown as NotificationsLike;
const SNOOZE_TOKEN = 'a1.Zm9v.YmFy';
const SEEN_TOKEN = 'a1.c2Vlbg.c2ln';
const ORIGIN = 'https://hub.ts.net';
const PAIRED = Date.parse('2026-10-13T12:00:00Z');

const clock: Clock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

function payload(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    v: 1,
    type: 'alert',
    alert: 'al_1',
    title: 't',
    body: 'b',
    tag: 'al_1',
    url: '/#/sessions/s_1?alert=al_1',
    tap: 'auto',
    needs_you: 1,
    actions: { snooze: SNOOZE_TOKEN, seen: SEEN_TOKEN },
    private: false,
    ts: '2026-10-13T10:00:00Z',
    ...over,
  };
}

const resp = (action: string, data: unknown, id = 'n1', date?: number): NotificationResponseLike => ({
  actionIdentifier: action,
  notification: { ...(date !== undefined ? { date } : {}), request: { identifier: id, content: { data } } },
});

function fakeFetch(): { fetch: FetchLike; sent: { url: string; init?: RequestInitLike }[] } {
  const sent: { url: string; init?: RequestInitLike }[] = [];
  const fetch: FetchLike = async (url, init) => {
    sent.push({ url, init });
    const body = { applied: true };
    return { status: 200, ok: true, headers: { get: () => null }, text: async () => JSON.stringify(body), json: async () => body };
  };
  return { fetch, sent };
}

function memKV(): KVStore & { items: Map<string, string> } {
  const items = new Map<string, string>();
  return {
    items,
    getItem: async (k) => items.get(k) ?? null,
    setItem: async (k, v) => void items.set(k, v),
    removeItem: async (k) => void items.delete(k),
  };
}

function perform(f: ReturnType<typeof fakeFetch>, over: Partial<PerformDeps> = {}): PerformDeps {
  return {
    notifications: N,
    fetch: f.fetch,
    clock,
    origin: async () => ORIGIN,
    pairedAt: async () => PAIRED,
    openUrl: async () => {},
    navigate: () => {},
    ...over,
  };
}

describe('acts go only to the pairing that issued them (finding 2)', () => {
  it('decideResponse keeps when the notification arrived and the origin its payload names', () => {
    expect(decideResponse(resp('seen', payload({ origin: 'https://old.ts.net' }), 'n1', PAIRED - 5), null)).toEqual({
      kind: 'act',
      token: SEEN_TOKEN,
      action: 'seen',
      at: PAIRED - 5,
      origin: 'https://old.ts.net',
    });
    // nothing to tell: the plain act, as before
    expect(decideResponse(resp('seen', payload({ origin: 'javascript:x' })), null)).toEqual({ kind: 'act', token: SEEN_TOKEN, action: 'seen' });
  });

  it('drops, and dismisses, an act on a notification that arrived before this pairing', async () => {
    const f = fakeFetch();
    await performDecision(perform(f), decideResponse(resp('snooze', payload(), 'old-1', PAIRED - 60_000), null), 'old-1');
    expect(f.sent).toHaveLength(0);
    expect(callsOf('notifications.dismissNotificationAsync').map((c) => c.args)).toEqual([['old-1']]);
  });

  it('acts on one that arrived after it, and when the pairing time is unknown', async () => {
    const f = fakeFetch();
    await performDecision(perform(f), decideResponse(resp('snooze', payload(), 'new-1', PAIRED + 1), null), 'new-1');
    await performDecision(perform(f, { pairedAt: async () => null }), decideResponse(resp('seen', payload(), 'new-2', PAIRED - 1), null), 'new-2');
    expect(f.sent.map((s) => s.url)).toEqual([`${ORIGIN}/v1/actions/${SNOOZE_TOKEN}`, `${ORIGIN}/v1/actions/${SEEN_TOKEN}`]);
  });

  it('drops an act whose payload names another hub; acts when it names this one, default port or not', async () => {
    const f = fakeFetch();
    await performDecision(perform(f), decideResponse(resp('seen', payload({ origin: 'https://old.ts.net' }), 'x1'), null), 'x1');
    expect(f.sent).toHaveLength(0);
    await performDecision(perform(f), decideResponse(resp('seen', payload({ origin: `${ORIGIN}:443` }), 'x2'), null), 'x2');
    expect(f.sent).toHaveLength(1);
  });

  it('the background task applies the same rule with the keychain paired_at', async () => {
    const f = fakeFetch();
    const r = await runNotifyTask(
      { notifications: N, fetch: f.fetch, clock, loadHub: async () => ({ origin: ORIGIN, token: 'T', pairedAt: PAIRED }), refresh: async () => 1 },
      { data: resp('seen', payload(), 'bg-old', PAIRED - 1) },
    );
    expect(r).toBe('ok');
    expect(f.sent).toHaveLength(0);
  });
});

describe('one act per tap (finding 9)', () => {
  const seen: Decision = { kind: 'act', token: SEEN_TOKEN, action: 'seen' };

  it('the Android background task and then the response listener act once on one Seen', async () => {
    const f = fakeFetch();
    const kv = memKV();
    await runNotifyTask(
      { notifications: N, fetch: f.fetch, clock, loadHub: async () => ({ origin: ORIGIN, token: 'T' }), refresh: async () => 0, acted: kv },
      { data: resp('seen', payload(), 'dup-1') },
    );
    resetActed(); // the app's own JS runtime starts later: only the shared storage remembers
    await performDecision(perform(f, { acted: kv }), seen, 'dup-1');
    expect(f.sent).toHaveLength(1);
    expect(kv.items.get(ACTED_KEY)).toBe('["dup-1|seen"]');
  });

  it('two handlers side by side in one runtime act once', async () => {
    const f = fakeFetch();
    const kv = memKV();
    await Promise.all([performDecision(perform(f, { acted: kv }), seen, 'dup-2'), performDecision(perform(f, { acted: kv }), seen, 'dup-2')]);
    expect(f.sent).toHaveLength(1);
  });

  it('another notification, or the other button, still acts; the stored list stays short', async () => {
    const f = fakeFetch();
    const kv = memKV();
    await performDecision(perform(f, { acted: kv }), seen, 'a');
    await performDecision(perform(f, { acted: kv }), { kind: 'act', token: SNOOZE_TOKEN, action: 'snooze' }, 'a');
    await performDecision(perform(f, { acted: kv }), seen, 'b');
    expect(f.sent).toHaveLength(3);
    for (let i = 0; i < 80; i++) await claimAct(kv, `k${i}`);
    expect((JSON.parse(kv.items.get(ACTED_KEY)!) as string[]).length).toBe(50);
  });

  it('a failing store never blocks an act, and never holds a token', async () => {
    const f = fakeFetch();
    const broken: KVStore = { getItem: () => Promise.reject(new Error('x')), setItem: async () => {}, removeItem: async () => {} };
    await performDecision(perform(f, { acted: broken }), seen, 'c');
    expect(f.sent).toHaveLength(1);
    const kv = memKV();
    await performDecision(perform(f, { acted: kv }), seen, 'd');
    expect(kv.items.get(ACTED_KEY)).not.toContain(SEEN_TOKEN);
  });
});
