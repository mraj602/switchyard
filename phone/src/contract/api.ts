// TypeScript mirrors of the hub's JSON (docs/spec/api.yaml). Only the fields the app reads are
// typed precisely; the hub may add fields (clients ignore unknown keys). Times are RFC 3339
// UTC strings with milliseconds ("2026-10-13T14:02:00.000Z"), or null.
//
// Wave 0 contract (docs/spec/phone.md). Changes go through WP-0's owner.

export type Iso = string;

/** api.yaml Error. */
export interface ApiError {
  error: string;
  code:
    | 'bad_request'
    | 'unauthorized'
    | 'forbidden'
    | 'machine_mismatch'
    | 'not_found'
    | 'conflict'
    | 'gone'
    | 'too_large'
    | 'rate_limited'
    | 'unavailable'
    | 'not_implemented'
    | 'internal';
}

/** api.yaml Health (GET /health, the 1.5 s reachability probe). */
export interface Health {
  ok: boolean;
  version: string;
  schema: number;
  time?: Iso;
  ui?: string;
}

export type SessionState = 'working' | 'needs_permission' | 'asked' | 'done' | 'failed' | 'ended' | 'gone';

export type AlertStatus = 'pending' | 'fired' | 'snoozed' | 'seen' | 'cancelled' | 'closed' | 'skipped';

export type Trigger =
  | 'needs_permission'
  | 'asked'
  | 'failed'
  | 'limit_80'
  | 'limit_95'
  | 'reset'
  | 'machine_silent';

/** api.yaml AlertBrief. */
export interface AlertBrief {
  id: string;
  trigger?: Trigger | string;
  status: AlertStatus;
  reason?: string | null;
  due_at?: Iso | null;
  fired_at?: Iso | null;
  fire_count?: number;
  snoozed_until?: Iso | null;
  seen_at?: Iso | null;
}

/** api.yaml Delivery. */
export interface Delivery {
  target: string;
  kind: ChannelKind;
  target_name?: string | null;
  fire: number;
  batch?: string | null;
  status: 'pending' | 'held' | 'sent' | 'failed' | 'skipped' | 'gone';
  reason?: string | null;
  tries?: number;
  http_status?: number | null;
  updated_at?: Iso;
}

/** api.yaml Alert (GET /v1/alerts, /v1/alerts/{id}). */
export interface Alert extends AlertBrief {
  rule?: string | null;
  session?: string | null;
  seat?: string | null;
  machine?: string | null;
  window_key?: string | null;
  title?: string;
  body?: string | null;
  url?: string;
  created_at?: Iso;
  since_at?: Iso | null;
  ended_at?: Iso | null;
  deliveries?: Delivery[];
}

/** api.yaml Session. */
export interface Session {
  id: string;
  state: SessionState;
  detail?: string | null;
  needs_you: boolean;
  state_since: Iso;
  question?: string | null;
  last_message?: string | null;
  seat?: string | null;
  seat_label?: string | null;
  seat_colour?: string | null;
  machine?: string | null;
  machine_name?: string | null;
  slot?: string | null;
  project?: string | null;
  cwd?: string | null;
  name?: string | null;
  title?: string | null;
  kind?: 'interactive' | 'background' | null;
  model?: string | null;
  context_pct?: number | null;
  started_at?: Iso | null;
  last_event_at?: Iso | null;
  ended_at?: Iso | null;
  remote_url?: string | null;
  resume?: string | null;
  stale?: boolean;
  machine_stale?: boolean;
  alert?: AlertBrief | null;
}

/** api.yaml SessionDetail (GET /v1/sessions/{id}). */
export interface SessionDetail extends Session {
  timeline: {
    ts: Iso;
    event: string;
    subtype?: string | null;
    tool_name?: string | null;
    state_after?: string | null;
    last_message?: string | null;
  }[];
  transitions?: { at: Iso; from?: string | null; to: string; detail?: string | null; cause: string }[];
}

/** api.yaml Window: one usage window of a seat. */
export interface Window {
  key: string; // five_hour, seven_day, …
  label?: string | null;
  used_pct?: number | null;
  peak_pct?: number | null;
  resets_at: Iso;
  started_at?: Iso | null;
  unused_pct?: number | null;
  source?: 'statusline' | 'web' | 'both';
  updated_at?: Iso | null;
  pace?: { projected_pct: number; full_at?: Iso | null; band: 'comfortable' | 'on_track' | 'fast' | 'runaway' } | null;
}

/** api.yaml Seat (an account card). */
export interface Seat {
  id: string;
  account: string;
  org: string;
  email?: string | null;
  org_name?: string | null;
  plan?: string | null;
  label?: string | null;
  colour?: string | null;
  order?: number;
  hidden?: boolean;
  windows?: Window[];
  live_sessions?: number;
}

/** api.yaml Machine (fields the app shows). */
export interface Machine {
  id: string;
  name: string;
  os?: string | null;
  last_seen?: Iso | null;
  last_record?: Iso | null;
  silent?: boolean;
  live_sessions?: number;
  reach?: 'direct' | 'forward' | 'pull' | null;
  version_status?: 'same' | 'older' | 'newer' | 'unknown';
  alert?: AlertBrief | null;
}

/** api.yaml Overview (GET /v1/overview). */
export interface Overview {
  time: Iso;
  seats: Seat[];
  needs_you: Session[];
  machines: Machine[];
  counts: { working?: number; needs_you?: number; done?: number; live?: number };
  stream_id?: number;
}

export interface SessionsPage {
  sessions: Session[];
  next?: string | null;
}

export interface AccountsReply {
  accounts: Seat[];
}

export interface MachinesReply {
  machines: Machine[];
}

export interface AlertsPage {
  alerts: Alert[];
  next?: string | null;
}

/** api.yaml QuietHours. */
export interface QuietHours {
  enabled: boolean;
  start: string; // "HH:MM"
  end: string;
  tz?: string;
  critical?: boolean;
}

export type TapMode = 'auto' | 'session' | 'claude';

/** api.yaml DevicePrefs (push.md 5.4); `widgets` is phase 8 (push.md 12.9). */
export interface DevicePrefs {
  mute?: Trigger[] | string[];
  quiet_hours?: QuietHours | null;
  private?: boolean | null;
  tap?: TapMode;
  tz?: string | null;
  widgets?: boolean;
}

export type ChannelKind = 'webpush' | 'ntfy' | 'expo' | 'link';

export type Platform = 'ios' | 'android';

/** api.yaml PushTarget; `platform` and `app_version` are phase 8 (expo targets only). */
export interface PushTarget {
  id: string;
  kind: ChannelKind;
  device?: string | null;
  name?: string | null;
  endpoint_host?: string;
  endpoint?: string | null; // expo: the Expo push token (owning device and owner only)
  server?: string | null;
  topic?: string | null;
  token_set?: boolean;
  enabled: boolean;
  status: 'ok' | 'failing' | 'gone' | 'disabled' | 'new';
  last_ok_at?: Iso | null;
  last_error?: string | null;
  last_error_at?: Iso | null;
  prefs?: DevicePrefs;
  created_at?: Iso;
  platform?: Platform | null;
  app_version?: string | null;
  /** push.md 13.7: link targets only (link.md 9). */
  link?: { connected: boolean; last_seen_at?: Iso | null; queued?: number } | null;
}

/** POST /v1/push/targets body for the app (push.md 12.7). */
export interface ExpoTargetNew {
  kind: 'expo';
  expo_token: string;
  name?: string;
  platform: Platform;
  app?: { version: string; build: string };
}

/** api.yaml Device. */
export interface Device {
  id: string;
  kind: 'machine' | 'desktop' | 'phone' | 'browser';
  name: string;
  hint?: string;
  scopes: string[];
  owner_session?: boolean;
  machine?: string | null;
  created_at: Iso;
  created_by?: string;
  last_used_at?: Iso | null;
  expires_at?: Iso | null;
  revoked_at?: Iso | null;
  current?: boolean;
  prefs?: DevicePrefs;
  targets?: PushTarget[];
}

/** api.yaml Me (GET /v1/me). */
export interface Me {
  device: Device;
  owner: boolean;
  hub: { version?: string; public_url?: string | null };
}

/** api.yaml PairClaim, as the app sends it (P2). Exactly one of secret or code. */
export interface PairClaim {
  secret?: string;
  code?: string;
  name: string;
  expect_kind: 'phone';
  cookie: false;
  client: string; // "app/<version>+<build>"
  /** keys.md 7.1: typed-code claims only (never with a secret). */
  keys?: DeviceKeysNew;
  /** keys.md 7.1: QR claims with `k`: the keys sealed with it. */
  keys_sealed?: string;
}

/** api.yaml DeviceKeysNew (keys.md 7.1), as a phone sends it. */
export interface DeviceKeysNew {
  sign: { alg: 'p256'; pub: string };
  box: { alg: 'p256'; pub: string };
  auth?: string;
}

/** api.yaml PublicKey. */
export interface PublicKey {
  alg: string;
  pub: string;
  fp: string;
}

/** api.yaml SignedDoc (keys.md 4.1). */
export interface SignedDoc {
  type: string;
  body: string;
  sigs: { by: string; fp?: string; sig: string }[];
}

/** api.yaml PairClaimReply (200 of POST /v1/pair/claim). */
export interface PairClaimReply {
  device: string;
  kind: string;
  name: string;
  scopes: string[];
  token?: string; // shown once; kept only in the keychain (P3)
  machine?: string | null;
  hub: { url?: string; version?: string };
  /** keys.md 7.3, from F1 on. */
  hub_key?: PublicKey;
  owner_key?: SignedDoc | null;
  cert?: SignedDoc;
  link?: { urls?: string[]; ping_s?: number };
  reg?: 'qr' | 'tofu' | 'ssh' | 'local';
}

/** api.yaml PushSettings (in GET /v1/rules `settings`); `expo_refresh_min` is phase 8. */
export interface PushSettings {
  quiet_hours?: QuietHours;
  private?: boolean;
  skip_remote?: boolean;
  snooze_min?: number;
  max_age_s?: number;
  batch_s?: number;
  expo_refresh_min?: number;
}

/** api.yaml Rule (the app shows rules read-only; mute is a device pref). */
export interface Rule {
  id: string;
  trigger: Trigger | string;
  base?: boolean;
  enabled: boolean;
  delay_s: number;
  bypass_quiet?: boolean;
}

/** GET /v1/rules. */
export interface RulesReply {
  rules: Rule[];
  settings: PushSettings;
}

/** api.yaml PushTestResult. */
export interface PushTestResult {
  outcome: 'sent' | 'retry' | 'gone' | 'failed';
  http_status?: number | null;
  error?: string | null;
}

/** POST /v1/actions/{token} reply (push.md 8). */
export interface ActionReply {
  applied: boolean;
  alert?: AlertBrief;
}
