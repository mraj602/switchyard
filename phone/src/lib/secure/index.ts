// The keychain entry of the paired hub (phone.md P3). The token lives only here:
// never in logs, AsyncStorage, widget data, pushes, links or errors (phone.md P3).
import { SECURE_HUB_KEY, SECURE_REVOKE_KEY } from '../../contract/constants';
import type { SecureStoreLike, SecureStoreOptions } from '../../contract/platform';

/** JSON under SecureStore key wy.hub.v1. */
export interface StoredHub {
  origin: string;
  token: string;
  device: string;
  name: string;
  paired_at: string;
}

/** An unpairing the hub has not confirmed (SECURE_REVOKE_KEY). */
export interface PendingRevoke {
  origin: string;
  token: string;
}

/** At most this many unconfirmed unpairings are kept (the oldest goes first). */
export const MAX_PENDING_REVOKES = 4;

export interface SecureHub {
  load(): Promise<StoredHub | null>;
  save(hub: StoredHub): Promise<void>;
  clear(): Promise<void>;
  /** Unpairings to retry; [] when none or unreadable. Rejects while the keychain is locked. */
  loadRevokes(): Promise<PendingRevoke[]>;
  /** Replaces the list ([] deletes the entry). */
  saveRevokes(list: PendingRevoke[]): Promise<void>;
}

function parseRevokes(text: string | null): PendingRevoke[] {
  if (!text) return [];
  try {
    const v = JSON.parse(text) as unknown;
    if (!Array.isArray(v)) return [];
    return v
      .filter((r): r is PendingRevoke => !!r && typeof r.origin === 'string' && typeof r.token === 'string' && !!r.origin && !!r.token)
      .map((r) => ({ origin: r.origin, token: r.token }))
      .slice(-MAX_PENDING_REVOKES);
  } catch {
    return [];
  }
}

/**
 * The options every call passes: keychainAccessible AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY (the
 * adapter maps the name to expo-secure-store's constant).
 */
export function secureOptions(afterFirstUnlockThisDeviceOnly: number | string): SecureStoreOptions {
  return { keychainAccessible: afterFirstUnlockThisDeviceOnly };
}

function parseStored(text: string): StoredHub | null {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof v !== 'object' || v === null) return null;
  const o = v as Record<string, unknown>;
  if (typeof o.origin !== 'string' || typeof o.token !== 'string' || !o.origin || !o.token) return null;
  return {
    origin: o.origin,
    token: o.token,
    device: typeof o.device === 'string' ? o.device : '',
    name: typeof o.name === 'string' ? o.name : '',
    paired_at: typeof o.paired_at === 'string' ? o.paired_at : '',
  };
}

/** Refuses to run without keychainAccessible: the token must stay readable while the phone is locked after the first unlock (P3). */
export function createSecureHub(store: SecureStoreLike, options: SecureStoreOptions): SecureHub {
  if (options.keychainAccessible === undefined || options.keychainAccessible === null) {
    throw new Error('secure: keychainAccessible is required (AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY)');
  }
  const opts: SecureStoreOptions = { ...options };
  return {
    async load() {
      const text = await store.getItemAsync(SECURE_HUB_KEY, opts);
      return text ? parseStored(text) : null;
    },
    async save(hub) {
      await store.setItemAsync(SECURE_HUB_KEY, JSON.stringify(hub), opts);
    },
    async clear() {
      await store.deleteItemAsync(SECURE_HUB_KEY, opts);
    },
    async loadRevokes() {
      return parseRevokes(await store.getItemAsync(SECURE_REVOKE_KEY, opts));
    },
    async saveRevokes(list) {
      const keep = list.slice(-MAX_PENDING_REVOKES).map((r) => ({ origin: r.origin, token: r.token }));
      if (keep.length === 0) await store.deleteItemAsync(SECURE_REVOKE_KEY, opts);
      else await store.setItemAsync(SECURE_REVOKE_KEY, JSON.stringify(keep), opts);
    },
  };
}

/** In-memory SecureStoreLike for tests and the locked-store fake. */
export function createMemorySecureStore(opts?: { locked?: boolean }): SecureStoreLike & { dump(): Record<string, string> } {
  const items = new Map<string, string>();
  const locked = () => {
    if (opts?.locked) throw new Error('User interaction is not allowed.');
  };
  return {
    async getItemAsync(key) {
      locked();
      return items.get(key) ?? null;
    },
    async setItemAsync(key, value) {
      locked();
      items.set(key, value);
    },
    async deleteItemAsync(key) {
      items.delete(key);
    },
    dump() {
      return Object.fromEntries(items);
    },
  };
}
