// Platform services that src/lib takes as arguments (phone.md P1): the pure code never
// imports react-native or expo-*; thin adapters in app/ and src/tasks/ pass these in, and
// tests pass the fakes of test/setup.ts or their own.

/** The subset of Response the hub client reads. */
export interface ResponseLike {
  status: number;
  ok: boolean;
  url?: string;
  redirected?: boolean;
  headers: { get(name: string): string | null };
  text(): Promise<string>;
  json(): Promise<unknown>;
}

export interface RequestInitLike {
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  signal?: AbortSignal;
  redirect?: 'follow' | 'error' | 'manual';
}

export type FetchLike = (url: string, init?: RequestInitLike) => Promise<ResponseLike>;

/** Wall clock and timers (fake timers in tests). */
export interface Clock {
  now(): number;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}

/** AsyncStorage-shaped key/value store (never holds a secret). */
export interface KVStore {
  getItem(key: string): Promise<string | null>;
  setItem(key: string, value: string): Promise<void>;
  removeItem(key: string): Promise<void>;
}

/** expo-secure-store-shaped keychain. */
export interface SecureStoreLike {
  getItemAsync(key: string, options?: SecureStoreOptions): Promise<string | null>;
  setItemAsync(key: string, value: string, options?: SecureStoreOptions): Promise<void>;
  deleteItemAsync(key: string, options?: SecureStoreOptions): Promise<void>;
}

export interface SecureStoreOptions {
  keychainAccessible?: number | string;
  keychainService?: string;
  requireAuthentication?: boolean;
}

/** AppState-shaped foreground source. */
export interface AppStateSource {
  current(): 'active' | 'background' | 'inactive' | string;
  subscribe(fn: (state: string) => void): () => void;
}

/** App identity (expo-application / expo-device / expo-constants). */
export interface AppInfo {
  version: string;
  build: string;
  model: string; // "iPhone 15"
  platform: 'ios' | 'android';
  projectId: string | null; // extra.eas.projectId
  dev: boolean; // __DEV__
}
