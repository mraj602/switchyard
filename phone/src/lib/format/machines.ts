// Text of the Machines screen (docs/spec/machines.md 6.1; webui components/machines-fmt.js).
import type { Machine } from '../../contract/api';
import { ago, agoLabel, clock } from './core';

/** The fields of a machine the contract does not type but the hub sends. */
export type MachineFull = Machine & {
  arch?: string | null;
  backlog?: number | null;
  backlog_oldest?: string | null;
  held?: number;
  sy_version?: string | null;
  direct_push?: boolean;
  removed_at?: string | null;
  pull?: { interval_s?: number } | null;
};

export const spaced = (n: number): string => String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

export type Tone = 'ok' | 'warn' | 'bad' | 'muted';

export function reachText(m: MachineFull): string | null {
  switch (m.reach) {
    case 'direct': return 'Direct';
    case 'forward': return 'via laptop SSH';
    case 'pull': return m.pull?.interval_s ? `pulled by hub every ${m.pull.interval_s} s` : 'pulled by hub';
    default: return null;
  }
}

/** Live / Quiet 1 h / Seen 3 min ago / Never seen. */
export function machineStatus(m: Machine, now: number): { tone: Tone; text: string } {
  const at = m.last_record || m.last_seen || '';
  const seen = Date.parse(at);
  if (m.silent) return { tone: 'warn', text: `Quiet ${ago(at, now)}` };
  if (Number.isNaN(seen)) return { tone: 'muted', text: 'Never seen' };
  if (now - seen < 5 * 60e3) return { tone: 'ok', text: 'Live' };
  return { tone: 'muted', text: `Seen ${agoLabel(at, now)}` };
}

/** "1 240 queued, oldest 14:02", or "". */
export function backlogText(m: MachineFull, now: number, tz?: string): string {
  if (!((m.backlog ?? 0) > 0)) return '';
  const oldest = m.backlog_oldest ? `, oldest ${clock(m.backlog_oldest, now, tz)}` : '';
  return `${spaced(m.backlog as number)} queued${oldest}`;
}

export function versionLine(m: MachineFull, hubVersion?: string): { text: string; tone: Tone } {
  const v = m.sy_version;
  if (!v) return { text: 'not reported', tone: 'muted' };
  if ((m.held ?? 0) > 0) return { text: `${v} · holding ${spaced(m.held as number)} records: hub too old`, tone: 'bad' };
  const hub = hubVersion ? ` hub ${hubVersion}` : ' the hub';
  switch (m.version_status) {
    case 'same': return { text: `${v} · same as hub`, tone: 'ok' };
    case 'older': return { text: `${v} · older than${hub}`, tone: 'warn' };
    case 'newer': return { text: `${v} · newer than${hub}`, tone: 'warn' };
    default: return { text: `${v} · cannot compare with the hub`, tone: 'muted' };
  }
}

/** Silent first, then by name; removed machines last. */
export function sortMachines<T extends MachineFull>(list: T[]): T[] {
  return [...list].sort(
    (a, b) => Number(!!a.removed_at) - Number(!!b.removed_at) || Number(!!b.silent) - Number(!!a.silent) || String(a.name).localeCompare(String(b.name)),
  );
}

/** The silent alert as a sentence, or "". */
export function machineAlertText(a: Machine['alert'], now: number): string {
  if (!a) return '';
  const when = a.fired_at ? ` ${agoLabel(a.fired_at, now)}` : '';
  switch (a.status) {
    case 'fired': return `Silent alert sent${when}`;
    case 'pending': return 'Silent alert is about to go out';
    case 'snoozed': return 'Silent alert snoozed';
    case 'seen': return 'Silent alert seen';
    default: return `Silent alert ${a.status}`;
  }
}
