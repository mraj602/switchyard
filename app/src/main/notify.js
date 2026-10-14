// Notifications (C9) from bus 'alert' events only: dedupe per alert id, the focus rule,
// click routing through hub.route. Text from shared/notify-text.js.
//
// There are no Snooze or Seen buttons: the desktop token has no `push` scope.

import { notifyText } from '../shared/notify-text.js';

/**
 * @param {{electron: object, bus: object, hub: object, window: object, store: object, log: object}} deps
 * @returns {{start(): void, stop(): void}}
 */
export function create(deps) {
  const { electron, bus, hub, window, store, log } = deps;
  const notified = new Set(); // alert ids shown in this run
  const live = new Set(); // shown notifications, held so they are not collected before a click
  let off = null;

  /** The focus rule: the window is focused, showing the Hub view, on that session's route. */
  function focusedOnSession(session) {
    if (!session?.id) return false;
    try {
      if (!window.isShowing('hub')) return false;
      const url = String(hub.view?.()?.webContents?.getURL?.() ?? '');
      return url.includes(`#/sessions/${encodeURIComponent(session.id)}`) || url.includes(`#/sessions/${session.id}`);
    } catch {
      return false;
    }
  }

  function seatOf(alert, session) {
    const id = session?.seat ?? alert?.seat;
    if (!id) return null;
    try {
      return (hub.seats?.() ?? []).find((s) => s.id === id) ?? null;
    } catch {
      return null;
    }
  }

  function onAlert(payload) {
    const alert = payload?.alert;
    const session = payload?.session ?? null;
    if (!alert?.id || notified.has(alert.id)) return;
    if (store.get().settings?.notifications === false) return;
    if (focusedOnSession(session)) return;
    const { Notification } = electron;
    if (!Notification.isSupported()) return;

    const { title, body, route } = notifyText({ alert, session, seat: seatOf(alert, session) });
    const n = new Notification({ title, body, silent: false });
    notified.add(alert.id);
    live.add(n);
    n.on('click', () => {
      live.delete(n);
      window.showWindow();
      Promise.resolve(hub.route(route)).catch((err) => log.error('notify: route failed:', err));
    });
    n.on('close', () => live.delete(n));
    n.show();
  }

  return {
    start() {
      if (!off) off = bus.on('alert', onAlert);
    },
    stop() {
      if (off) off();
      off = null;
      for (const n of live) {
        try {
          n.close();
        } catch {
          /* already gone */
        }
      }
      live.clear();
    },
  };
}
