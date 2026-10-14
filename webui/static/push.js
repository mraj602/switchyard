// Notifications on this device (pwa.md 6): permission, Web Push subscription, registering it
// with the hub, re-subscribing when the browser or the hub lost it.

import { getPushConfig, getSelf, addTarget, patchDevice, testTarget } from './api.js';
import { notificationState, urlBase64ToBytes, sameKey, deviceName } from './platform.js';
import { isStandalone } from './pwa.js';

export function environment() {
  return {
    ua: navigator.userAgent,
    platform: navigator.platform,
    touch: navigator.maxTouchPoints || 0,
    standalone: isStandalone(),
    hasPush: 'PushManager' in window && 'serviceWorker' in navigator,
    hasNotification: 'Notification' in window,
    permission: 'Notification' in window ? Notification.permission : 'denied',
    subscribed: false,
  };
}

async function currentSubscription() {
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) return null;
  try {
    const reg = await navigator.serviceWorker.ready;
    return await reg.pushManager.getSubscription();
  } catch (_) {
    return null;
  }
}

// { state, targets, subscription }: state is one of unsupported | ios-old | install-first |
// off | blocked | on (platform.js notificationState). `targets` are this device's.
export async function pushStatus() {
  const env = environment();
  const sub = await currentSubscription();
  env.subscribed = !!sub;
  let targets = [];
  try { targets = ((await getSelf()).targets || []); } catch (_) { /* offline or unauthorised */ }
  const mine = sub ? targets.find((t) => t.kind === 'webpush' && t.endpoint === sub.endpoint) : null;
  // A live subscription the hub does not know (or marked gone) counts as off until repaired.
  const state = notificationState({ ...env, subscribed: !!sub && !!mine && mine.status !== 'gone' });
  return { state, env, targets, subscription: sub, target: mine };
}

// Make (or replace) the Web Push subscription for the hub's key and register it. Throws
// with a message the UI shows.
export async function subscribeAndRegister() {
  const cfg = await getPushConfig();
  const reg = await navigator.serviceWorker.ready;
  let sub = await reg.pushManager.getSubscription();
  // The hub moved and made new keys: the old subscription cannot be used.
  if (sub && !sameKey(sub.options && sub.options.applicationServerKey, cfg.vapid_public_key)) {
    await sub.unsubscribe();
    sub = null;
  }
  if (!sub) {
    sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToBytes(cfg.vapid_public_key) });
  }
  const j = sub.toJSON();
  const target = await addTarget({
    kind: 'webpush', endpoint: j.endpoint, keys: { p256dh: j.keys.p256dh, auth: j.keys.auth },
    name: deviceName(navigator.userAgent, null),
  });
  try {
    await patchDeviceTz();
  } catch (_) { /* prefs are optional */ }
  return target;
}

async function patchDeviceTz() {
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (!tz) return;
  const self = await getSelf();
  const prefs = { ...(self.prefs || {}), tz };
  await patchDevice('self', { prefs });
}

// Turn on: must be called from a tap handler (iOS). Resolves { state, target } or throws.
export async function turnOn() {
  const env = environment();
  const st = notificationState(env);
  if (st === 'unsupported' || st === 'ios-old' || st === 'install-first') return { state: st };
  const perm = await Notification.requestPermission();
  if (perm !== 'granted') return { state: perm === 'denied' ? 'blocked' : 'off' };
  const target = await subscribeAndRegister();
  return { state: 'on', target };
}

export async function sendTest(targetId) {
  return testTarget(targetId);
}

// On every start, installed or not, when permission is already granted: repair silently if
// the browser lost the subscription or the hub marked it gone. Never prompts.
export async function resubscribeIfNeeded() {
  if (!('Notification' in window) || Notification.permission !== 'granted') return false;
  if (!('PushManager' in window) || !('serviceWorker' in navigator)) return false;
  try {
    const sub = await currentSubscription();
    let known = false;
    if (sub) {
      const mine = ((await getSelf()).targets || []).find((t) => t.kind === 'webpush' && t.endpoint === sub.endpoint);
      known = !!mine && mine.status !== 'gone';
    }
    if (known) return false;
    await subscribeAndRegister();
    return true;
  } catch (_) {
    return false;
  }
}

