// Whether the board runs inside the desktop app, and what the Notifications row says when this
// sign-in cannot get Web Push (settings.js, SE5): the desktop app shows the hub's alerts itself
// (desktop.md C9), a phone pairs with a code, a desktop browser signs in as the owner or pairs.

// The desktop app's Hub view signs in with a board session the hub records as a browser device
// made from the desktop device: /v1/me names that device in `via` (api.yaml Me). An owner
// session opened in the app keeps the same `via`. The desktop token itself is kind desktop.
export function meInApp(me) {
  if (!me) return false;
  return !!((me.via && me.via.kind === 'desktop') || (me.device && me.device.kind === 'desktop'));
}

// How to become the owner from here: the app has its own button; a browser runs owner-link.
export function ownerHow(app) {
  return app ? 'use Sign in as owner in the app’s Settings, Hub' : 'run sy hub owner-link on the hub';
}

export function notifNote({ app, mobile } = {}) {
  if (app) {
    return { who: 'This app', state: 'The desktop app shows these notifications itself.', how: 'app-settings' };
  }
  return { who: mobile ? 'This phone' : 'This browser', state: 'Off: this sign-in only reads the board.', how: mobile ? 'pair-phone' : 'owner-or-pair' };
}
