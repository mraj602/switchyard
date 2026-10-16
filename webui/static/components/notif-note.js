// What the Notifications row says when this sign-in cannot get Web Push (settings.js, SE5):
// the desktop app shows the hub's alerts itself (desktop.md C9), a phone pairs with a code,
// a desktop browser signs in as the owner or pairs.
export function notifNote(kind, mobile) {
  if (kind === 'desktop') {
    return { who: 'This app', state: 'The desktop app shows these notifications itself.', how: 'app-settings' };
  }
  return { who: mobile ? 'This phone' : 'This browser', state: 'Off: this sign-in only reads the board.', how: mobile ? 'pair-phone' : 'owner-or-pair' };
}
