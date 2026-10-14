// Text of the Machines screen (docs/spec/machines.md 6.1), as pure functions so node --test
// can check every sentence. No imports: the caller passes the formatters it uses (format.js).

// 1240 -> "1 240" (a no-break space, as the spec writes it).
export const spaced = (n) => String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

const intervalText = (s) => {
  if (!s || s <= 0) return '';
  if (s < 60) return `${s} s`;
  if (s < 3600) return s % 60 === 0 ? `${s / 60} min` : `${s} s`;
  return s % 3600 === 0 ? `${s / 3600} h` : `${Math.round(s / 60)} min`;
};

// The reach chip: {text, title, cls} or null when the hub does not know.
export function reachChip(m) {
  switch (m.reach) {
    case 'direct': return { text: 'Direct', title: 'Sends to the hub itself (Tailscale or HTTPS)', cls: 'reach-direct' };
    case 'forward': return { text: 'via laptop SSH', title: 'Sends through an SSH forward from your laptop; records wait while the laptop is away', cls: 'reach-forward' };
    case 'pull': {
      const every = m.pull && m.pull.interval_s ? ` every ${intervalText(m.pull.interval_s)}` : '';
      return { text: `pulled by hub${every}`, title: 'The hub connects over SSH and collects its records', cls: 'reach-pull' };
    }
    default: return null;
  }
}

// The status line: Live / Quiet 1 h 12 min / Seen 3 min ago / Never seen.
export function statusOf(m, now, f) {
  const at = m.last_record || m.last_seen || '';
  const seen = Date.parse(at);
  if (m.silent) return { cls: 'warn', text: `Quiet ${f.ago(at, now)}` };
  if (Number.isNaN(seen)) return { cls: 'muted', text: 'Never seen' };
  if (now - seen < 5 * 60e3) return { cls: 'ok', text: 'Live' };
  return { cls: 'muted', text: `Seen ${f.agoLabel(at, now)}` };
}

// "1 240 queued, oldest 14:02", or '' when nothing is queued.
export function backlogText(m, now, f) {
  if (!(m.backlog > 0)) return '';
  const oldest = m.backlog_oldest ? `, oldest ${f.clock(m.backlog_oldest, now)}` : '';
  return `${spaced(m.backlog)} queued${oldest}`;
}

// The version line: {text, cls} with cls '' | 'warn' | 'bad'.
export function versionLine(m, hubVersion) {
  const v = m.sy_version;
  if (!v) return { text: 'not reported', cls: '' };
  if (m.held > 0) return { text: `${v} · holding ${spaced(m.held)} records: hub too old`, cls: 'bad' };
  const hub = hubVersion ? ` hub ${hubVersion}` : ' the hub';
  switch (m.version_status) {
    case 'same': return { text: `${v} · same as hub`, cls: '' };
    case 'older': return { text: `${v} · older than${hub}`, cls: 'warn' };
    case 'newer': return { text: `${v} · newer than${hub}`, cls: 'warn' };
    default: return { text: `${v} · cannot compare with the hub`, cls: '' };
  }
}

// Pull state: {text, cls: ok|warn|bad|muted, canRun}.
export function pullLine(p, now, f) {
  if (!p) return null;
  const err = p.last_error ? `: ${p.last_error}` : '';
  switch (p.state) {
    case 'ok': return { text: `OK, last pull ${f.agoLabel(p.last_ok, now) || 'just now'}`, cls: 'ok', canRun: true };
    case 'new': return { text: 'Not pulled yet', cls: 'muted', canRun: true };
    case 'failing': return { text: `Failing${err}`, cls: 'warn', canRun: true };
    case 'host_key': return { text: `Host key changed${err}`, cls: 'bad', canRun: false };
    case 'auth': return { text: `Login refused${err}`, cls: 'bad', canRun: true };
    case 'refused': return { text: `Refused${err}`, cls: 'bad', canRun: false };
    case 'disabled': return { text: 'Pulling is turned off', cls: 'muted', canRun: false };
    case 'frozen': return { text: 'Paused: the hub is frozen for a move', cls: 'muted', canRun: false };
    default: return { text: String(p.state), cls: 'muted', canRun: false };
  }
}

// Silent first, then by name; removed machines last.
export function sortMachines(list) {
  return [...list].sort((a, b) => (!!a.removed_at - !!b.removed_at) || (!!b.silent - !!a.silent) || String(a.name).localeCompare(String(b.name)));
}

// The silent alert as a sentence, or ''.
export function alertText(a, now, f) {
  if (!a) return '';
  const when = a.fired_at ? ` ${f.agoLabel(a.fired_at, now)}` : '';
  switch (a.status) {
    case 'fired': return `Silent alert sent${when}`;
    case 'pending': return 'Silent alert is about to go out';
    case 'snoozed': return 'Silent alert snoozed';
    case 'seen': return 'Silent alert seen';
    default: return `Silent alert ${a.status}`;
  }
}
