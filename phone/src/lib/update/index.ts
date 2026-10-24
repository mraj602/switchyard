// The in-app updater's state machine (phone.md P16.3): check at most once a day (and on demand),
// remember what was found, install through the wy-link module. The checksum and signing-key
// checks are the module's; this file only drives it and never sees the APK or the token beyond
// handing the paired hub's token to checkUpdate.
import type { UpdateInfo, UpdateLink } from '../../contract/link';
import type { Clock, KVStore } from '../../contract/platform';

export const UPDATE_KEY = 'wy.update.v1';
export const DAY_MS = 24 * 60 * 60 * 1000;

export type UpdatePhase = 'idle' | 'checking' | 'none' | 'available' | 'installing' | 'started' | 'refused' | 'error';

export type UpdateView = {
  phase: UpdatePhase;
  /** The newest offer, kept across restarts until it is installed or superseded. */
  info: UpdateInfo | null;
  /** When the last successful check ran (ms), null before the first. */
  checkedAt: number | null;
  message: string | null;
};

export interface Updater {
  view(): UpdateView;
  subscribe(cb: () => void): () => void;
  /** Settings "Check for update". */
  check(): Promise<void>;
  /** The once-a-day check on start; does nothing when one ran in the last 24 hours. */
  checkIfDue(): Promise<void>;
  /** Settings "Install": download, verify, hand to Android's installer (which asks you to confirm). */
  install(): Promise<void>;
  /** Reads the remembered offer (call once at start). */
  load(): Promise<void>;
}

export interface UpdaterDeps {
  link: UpdateLink;
  /** The paired hub's origin and token (the keychain); null when not paired. Rejects while the keychain is locked. */
  loadHub(): Promise<{ origin: string; token: string } | null>;
  kv: KVStore;
  clock: Clock;
  /** The installed app's versionCode; an offer that is not newer is dropped (an update was installed). */
  installedBuild: number;
}

export const REFUSED_TEXT = 'The update was refused: it did not match its checksum or is not signed by your key. Nothing was installed.';

const errText = (e: unknown): string => (e instanceof Error ? e.message.replace(/^wy-link:\s*/, '') : 'failed');

export function createUpdater(d: UpdaterDeps): Updater {
  let view: UpdateView = { phase: 'idle', info: null, checkedAt: null, message: null };
  const subs = new Set<() => void>();
  const set = (patch: Partial<UpdateView>) => {
    view = { ...view, ...patch };
    subs.forEach((f) => f());
  };
  const save = async () => {
    try {
      await d.kv.setItem(UPDATE_KEY, JSON.stringify({ checkedAt: view.checkedAt, info: view.info }));
    } catch {
      // a lost remembered offer only means one more check
    }
  };

  /** Asks the module (the paired hub, then GitHub) and keeps the answer; null when nothing is offered. */
  async function fetchOffer(): Promise<UpdateInfo | null> {
    const hub = await d.loadHub();
    if (!hub) throw new Error('Pair with your hub first.');
    const info = await d.link.checkUpdate(hub.origin, hub.token);
    const offer = info && info.build > d.installedBuild ? info : null;
    view = { ...view, info: offer, checkedAt: d.clock.now() };
    await save();
    return offer;
  }

  async function run(): Promise<void> {
    if (view.phase === 'checking' || view.phase === 'installing') return;
    set({ phase: 'checking', message: null });
    try {
      const offer = await fetchOffer();
      set({ phase: offer ? 'available' : 'none', message: null });
    } catch (e) {
      set({ phase: 'error', message: errText(e) });
    }
  }

  return {
    view: () => view,
    subscribe(cb) {
      subs.add(cb);
      return () => void subs.delete(cb);
    },
    check: run,
    async checkIfDue() {
      if (view.checkedAt !== null && d.clock.now() - view.checkedAt < DAY_MS && view.checkedAt <= d.clock.now()) return;
      await run();
    },
    async install() {
      if (!view.info || view.phase === 'installing' || view.phase === 'checking') return;
      set({ phase: 'installing', message: null });
      try {
        // The module keeps the hub's token for the download in memory from the last checkUpdate
        // only (push.md 14.8): check again, so an offer remembered across a restart or from a
        // previous pairing installs from the current hub, or goes when it has none.
        const info = await fetchOffer();
        if (!info) {
          set({ phase: 'none', message: null });
          return;
        }
        const r = await d.link.installUpdate(info);
        set(r === 'started' ? { phase: 'started', message: 'Android is asking you to confirm the update.' } : { phase: 'refused', info: null, message: REFUSED_TEXT });
        if (r === 'refused') await save();
      } catch (e) {
        set({ phase: 'error', message: errText(e) });
      }
    },
    async load() {
      try {
        const raw = await d.kv.getItem(UPDATE_KEY);
        if (!raw) return;
        const v = JSON.parse(raw) as { checkedAt?: unknown; info?: UpdateInfo | null };
        const info = v.info && typeof v.info.build === 'number' && v.info.build > d.installedBuild ? v.info : null;
        set({
          checkedAt: typeof v.checkedAt === 'number' ? v.checkedAt : null,
          info,
          phase: info ? 'available' : 'idle',
        });
      } catch {
        // unreadable: start clean
      }
    },
  };
}

/** One line for Settings: "Update 0.9.0 available", "Up to date", the error, … */
export function updateText(v: UpdateView): string {
  switch (v.phase) {
    case 'checking':
      return 'Checking…';
    case 'available':
      return v.info ? `Update ${v.info.version} available` : 'Update available';
    case 'installing':
      return 'Downloading and checking the update…';
    case 'none':
      return 'Up to date';
    case 'started':
    case 'refused':
    case 'error':
      return v.message ?? '';
    default:
      return v.checkedAt === null ? 'Not checked yet' : 'Up to date';
  }
}
