// Which devices can get a notification, in plain words. No imports, so node can test it
// (pwa.test.cjs). A device's targets are its ways of being reached: the Android app's own
// connection ("link"), the browser's Web Push ("webpush"), ntfy, or the old Expo app.

// A target that can still deliver: switched on and not given up by the push service.
export const usable = (t) => !!t && t.enabled !== false && t.status !== 'gone' && t.status !== 'disabled';

// [{device, targets}] for every live device with at least one usable target.
export function notifiable(devices) {
  return (devices || [])
    .filter((d) => d && !d.revoked_at)
    .map((d) => ({ device: d, targets: (d.targets || []).filter(usable) }))
    .filter((x) => x.targets.length);
}

const KIND = { link: 'Android app', webpush: 'browser notifications', ntfy: 'ntfy', expo: 'Expo app (old)' };

export const targetKindText = (t) => KIND[t && t.kind] || String((t && t.kind) || 'unknown');

// The Android app counts as paired when some live device has a link target (or reports a
// link connection).
export const appPaired = (devices) => (devices || []).some((d) => d && !d.revoked_at && (d.link || (d.targets || []).some((t) => t.kind === 'link')));
