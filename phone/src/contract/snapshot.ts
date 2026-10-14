// WidgetSnapshot v1 (docs/spec/phone.md P11): what the app writes for the widgets and the Live
// Activity (App Group on iOS, AsyncStorage key sy.widget.v1 on Android). Never a secret: no
// token, no hub reply bodies beyond these fields. Golden: test/fixtures/widget-snapshot.v1.json.

export const SNAPSHOT_VERSION = 1;

/** Most seats a snapshot holds (P11). */
export const SNAPSHOT_MAX_SEATS = 4;

/** Labels are cut to this many characters (P11). */
export const SNAPSHOT_LABEL_MAX = 12;

export type WindowKey = 'five_hour' | 'seven_day';

export interface SnapshotWindow {
  pct: number;
  resets_at: string;
}

export interface SnapshotSeat {
  id: string;
  label: string;
  colour: string;
  five_hour?: SnapshotWindow | null;
  seven_day?: SnapshotWindow | null;
}

export interface SnapshotTop {
  seat: string;
  project: string;
  /** The short state word: "asked", "perm", "failed", … */
  what: string;
  since: string;
}

export interface SnapshotNextReset {
  seat: string;
  window: WindowKey;
  resets_at: string;
  pct: number;
}

export interface WidgetSnapshot {
  v: 1;
  as_of: string;
  /** The hub's host name only (no scheme, no path). */
  hub: string;
  needs_you: number;
  /** null in private mode, or when nothing needs you. */
  top: SnapshotTop | null;
  seats: SnapshotSeat[];
  next_reset: SnapshotNextReset | null;
  private: boolean;
}

/** One entry of the widget timeline (buildTimeline, P11). */
export interface TimelineEntry<P = WidgetSnapshot> {
  date: Date;
  props: P;
}

/** At most this many timeline entries (P11). */
export const TIMELINE_MAX = 8;
