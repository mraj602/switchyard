// Types of the sy-link module's JS API (docs/spec/push.md 14.8). src/contract/link.ts mirrors
// them for src/lib (which may not import a module); test/link-contract.test.ts keeps the two equal.

/** A signed document in wire form (keys.md 4.1), as the claim reply carries `cert`. */
export type SignedDoc = { type: string; body: string; sigs: { by: string; fp?: string; sig: string }[] };

export type LinkConfig = {
  origin: string;
  device: string;
  urls: string[];
  pingS: number;
  hubKey: { pub: string; fp: string };
  reg: 'qr' | 'tofu';
  /**
   * The claim reply's certificate (keys.md 7.3). configure() checks it natively (the app's JS
   * has no Ed25519): hub-signed, this device and hub, this phone's own keys. Refused without it.
   */
  cert?: SignedDoc | null;
  /** The QR's `h=`, when the claim was a QR scan: hubKey.fp must equal it and reg must be qr. */
  qrFp?: string | null;
};

export type LinkStatus = 'off' | 'connecting' | 'connected' | 'backoff' | 'revoked' | 'upgrade' | 'error';

export type LinkState = {
  enabled: boolean; // the "Instant delivery" switch
  status: LinkStatus;
  transport: 'ws' | 'poll' | null;
  connectedSince: number | null; // ms since epoch
  nextRetryAt: number | null;
  lastError: string | null; // close code and word, or a network error class; never a body
  lastSeq: number;
  received: number;
  rejected: number; // since install
  batteryExempt: boolean;
  notificationsAllowed: boolean;
  verified: 'qr' | 'tofu' | 'changed';
  pingS: number;
  /** push.md 14.7: the first 8 characters of the sign key's fingerprint ("k7q2-m4xa"); null without keys. */
  checkCode: string | null;
};

export type PublicKeys = { sign: { alg: 'p256'; pub: string }; box: { alg: 'p256'; pub: string }; auth: string };

/** The `refresh` frame body (link.md 6.4), kept for the widgets (push.md 13.5). */
export type LinkRefresh = {
  v: 1;
  needs_you: number;
  seats: { seat: string; window: string; used_pct: number; resets_at: string | null }[];
  ts: string;
};

/** The updater (phone.md P16), N2. */
export type UpdateInfo = {
  version: string;
  build: number;
  url: string;
  sha256: string;
  certSha256: string;
  size: number;
  notes: string;
  source: 'hub' | 'github';
};

export type Subscription = { remove(): void };

/** Codes configure() rejects with. */
export const CONFIGURE_ERRORS = {
  hubKey: 'ERR_SY_HUB_KEY',
  tampered: 'ERR_SY_TAMPERED',
} as const;
