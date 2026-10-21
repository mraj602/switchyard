// WidgetSnapshot v1 (docs/spec/phone.md P11): what the app writes for the widgets and the Live
// Activity (App Group on iOS, AsyncStorage key sy.widget.v1 on Android). Never a secret: no
// token, no hub reply bodies beyond these fields. Golden: test/fixtures/widget-snapshot.v1.json.

export const SNAPSHOT_VERSION = 1;

/** Most seats a snapshot holds (P11): the Android Limits widget shows as many as fit; iOS draws the first 4. */
export const SNAPSHOT_MAX_SEATS = 8;

/** The iOS widgets draw this many accounts. */
export const SNAPSHOT_IOS_SEATS = 4;

/** The Needs-you list widget shows this many sessions (P11). */
export const SNAPSHOT_NEEDS_LIST = 3;

/** The Machines widget shows this many machines (P11). */
export const SNAPSHOT_MAX_MACHINES = 6;

/** Labels are cut to this many characters (P11). */
export const SNAPSHOT_LABEL_MAX = 12;

export type WindowKey = 'five_hour' | 'seven_day';

export interface SnapshotWindow {
  pct: number;
  /** null: an idle window (not started: 0 %, no reset time yet). */
  resets_at: string | null;
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

/** One waiting session of the Needs-you list widget (no message text: only what `top` already holds, plus the route id). */
export interface SnapshotNeed {
  /** The session id, for the tap target `switchyard://sessions/<id>`. */
  id: string;
  seat: string;
  colour: string;
  project: string;
  /** The short state word: "asked", "perm", "failed". */
  what: string;
  since: string;
}

/** One machine of the Machines widget. */
export interface SnapshotMachine {
  id: string;
  name: string;
  /** Not silent. */
  online: boolean;
  /** Live sessions. */
  live: number;
  last_seen: string | null;
}

export interface SnapshotNextReset {
  seat: string;
  window: WindowKey;
  resets_at: string;
  pct: number;
}

/** The Now/Sessions counts (Overview.counts) the Sessions widget shows. */
export interface SnapshotCounts {
  /** State working: "Running". */
  working: number;
  /** needs_permission, asked, failed: "Needs input". */
  needs_you: number;
  /** Finished a turn, idle: "Done". */
  done: number;
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
  /** Up to 3 waiting sessions, oldest first; empty in private mode. Absent in a snapshot written by an older app. */
  needs_list?: SnapshotNeed[];
  /** Up to 6 machines in the hub's order. Absent in a snapshot written by an older app. */
  machines?: SnapshotMachine[];
  next_reset: SnapshotNextReset | null;
  private: boolean;
  /** Absent in a snapshot written by an older app. */
  counts?: SnapshotCounts;
}

/** One entry of the widget timeline (buildTimeline, P11). */
export interface TimelineEntry<P = WidgetSnapshot> {
  date: Date;
  props: P;
}

/** At most this many timeline entries (P11). */
export const TIMELINE_MAX = 8;
