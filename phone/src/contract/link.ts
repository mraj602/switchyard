// The sy-link module's JS API (push.md 14.8) as src/lib sees it: src/lib may not import the
// module (P1), so these mirror phone/modules/sy-link/src/SyLink.types.ts, and
// test/link-contract.test.ts checks that both stay assignable to each other.

import type { SignedDoc } from './api';

export type LinkConfig = {
  origin: string;
  device: string;
  urls: string[];
  pingS: number;
  hubKey: { pub: string; fp: string };
  reg: 'qr' | 'tofu';
  cert?: SignedDoc | null;
  qrFp?: string | null;
};

export type LinkStatus = 'off' | 'connecting' | 'connected' | 'backoff' | 'revoked' | 'upgrade' | 'error';

export type LinkState = {
  enabled: boolean;
  status: LinkStatus;
  transport: 'ws' | 'poll' | null;
  connectedSince: number | null;
  nextRetryAt: number | null;
  lastError: string | null;
  lastSeq: number;
  received: number;
  rejected: number;
  batteryExempt: boolean;
  notificationsAllowed: boolean;
  verified: 'qr' | 'tofu' | 'changed';
  pingS: number;
  /** push.md 14.7: the first 8 characters of the sign key's fingerprint, "k7q2-m4xa"; null without keys. */
  checkCode: string | null;
};

export type PublicKeys = { sign: { alg: 'p256'; pub: string }; box: { alg: 'p256'; pub: string }; auth: string };

/** The `refresh` frame body (link.md 6.4). */
export type LinkRefresh = {
  v: 1;
  needs_you: number;
  seats: { seat: string; window: string; used_pct: number; resets_at: string | null }[];
  ts: string;
};

export type LinkSubscription = { remove(): void };

/** What pairing needs of the module (phone.md P2). */
export interface ClaimLink {
  prepareKeys(): Promise<PublicKeys>;
  sealKeys(k: string, secret: string): Promise<string>;
  configure(cfg: LinkConfig): Promise<void>;
}

/** What the app integration and Settings → Notifications need of the module. */
export interface LinkLike extends ClaimLink {
  start(): Promise<void>;
  stop(): Promise<void>;
  reconnect(): Promise<void>;
  getState(): Promise<LinkState>;
  setPingInterval(seconds: number): Promise<void>;
  setSnoozeMinutes(minutes: number): Promise<void>;
  requestBatteryExemption(): Promise<boolean>;
  openBatterySettings(): Promise<void>;
  sendLocalTest(): Promise<void>;
  getRefresh(): Promise<LinkRefresh | null>;
  reset(): Promise<void>;
  addListener(event: 'state', cb: (s: LinkState) => void): LinkSubscription;
  addListener(event: 'refresh' | 'revoked' | 'upgrade', cb: () => void): LinkSubscription;
}

/** configure()'s rejection codes (the native module's CodedException codes). */
export const LINK_ERR_HUB_KEY = 'ERR_SY_HUB_KEY';
export const LINK_ERR_TAMPERED = 'ERR_SY_TAMPERED';
