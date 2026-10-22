// Module-scope bridge between `initNotifications` (called once at module scope in
// app/_layout.tsx, so a cold-start response is read exactly once) and the provider, which only
// exists after the first render. Decisions that arrive early wait here, with their response,
// until the provider has loaded the keychain entry.
import type { Decision, NotificationResponseLike } from '../lib/notify';

type Handler = {
  /** Runs the decision through performDecision (src/lib/notify); an act dismisses `response`'s notification. */
  run(d: Decision, response?: NotificationResponseLike): Promise<void>;
  onReceived(): void;
  probe(): Promise<boolean>;
};

let handler: Handler | null = null;
const pendingDecisions: { d: Decision; response?: NotificationResponseLike }[] = [];
let pendingReceived = false;

export const bridge = {
  /** Passed to initNotifications as `run`. */
  async run(d: Decision, response?: NotificationResponseLike): Promise<void> {
    if (handler) return handler.run(d, response);
    pendingDecisions.push({ d, response });
  },
  /** Passed to initNotifications as `onReceived`. */
  onReceived(): void {
    if (handler) handler.onReceived();
    else pendingReceived = true;
  },
  /** Passed to initNotifications as `probe`: the paired hub's /health; false before the
   *  provider is ready or when unpaired. */
  async probe(): Promise<boolean> {
    return handler ? handler.probe() : false;
  },
  /** The provider installs its handler when the hub is known; returns the uninstall. */
  install(h: Handler): () => void {
    handler = h;
    const queued = pendingDecisions.splice(0);
    for (const q of queued) void h.run(q.d, q.response);
    if (pendingReceived) {
      pendingReceived = false;
      h.onReceived();
    }
    return () => {
      if (handler === h) handler = null;
    };
  },
  /** Tests. */
  reset(): void {
    handler = null;
    pendingDecisions.length = 0;
    pendingReceived = false;
  },
};
