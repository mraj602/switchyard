// The push payload (push.md 6.5): exactly the Go model.Payload JSON, the same object the
// service worker reads. The Expo channel puts it in the message's `data` (push.md 12.2),
// minus `remote_url` when `private` is true. Plus the Expo message itself and the silent
// widget refresh (push.md 12.9).

import type { TapMode, Trigger } from './api';

export const PAYLOAD_VERSION = 1;

export type PayloadType = 'alert' | 'summary' | 'test';

/** Action tokens (push.md 8); absent for tests and summaries. */
export interface PayloadActions {
  snooze?: string;
  seen?: string;
}

/**
 * model.Payload. Key order of the Go encoder: v, type, alert, alerts, trigger, title, body, tag,
 * url, remote_url, tap, session, needs_you, actions, private, ts. Optional keys are omitted
 * (Go omitempty), never null.
 */
export interface Payload {
  v: 1;
  type: PayloadType;
  alert?: string;
  alerts?: string[];
  trigger?: Trigger;
  title: string;
  body: string;
  tag: string;
  url: string;
  remote_url?: string;
  tap: TapMode;
  session?: string;
  needs_you: number;
  actions?: PayloadActions;
  private: boolean;
  ts: string | null;
}

/** Keys of Payload in Go's order (the contract test checks goldens against it). */
export const PAYLOAD_KEYS = [
  'v',
  'type',
  'alert',
  'alerts',
  'trigger',
  'title',
  'body',
  'tag',
  'url',
  'remote_url',
  'tap',
  'session',
  'needs_you',
  'actions',
  'private',
  'ts',
] as const;

/** `data` of a silent refresh push (push.md 12.2, 12.9): never shown, never an alert. */
export interface RefreshData {
  v: 1;
  type: 'refresh';
  needs_you: number;
  ts: string;
}

/** What an Expo push carries in `data`. */
export type PushData = Payload | RefreshData;

/** Android channels (phone.md P6.1). */
export type AndroidChannelId = 'sy_needs_you' | 'sy_limits' | 'sy_machines' | 'sy_test';

/** One message to Expo's push service (push.md 12.2), as the hub sends it. */
export interface ExpoMessage {
  to: string;
  title?: string;
  body?: string;
  data: PushData;
  categoryId?: 'sy_alert';
  channelId?: AndroidChannelId;
  sound?: 'default';
  badge?: number;
  priority: 'high' | 'normal';
  ttl: number;
  contentAvailable?: boolean;
}

/** Expo's send answer (push.md 12.3), as the fake and the Go goldens model it. */
export interface ExpoTicket {
  status: 'ok' | 'error';
  id?: string;
  message?: string;
  details?: { error?: string };
}
