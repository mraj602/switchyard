// Text of the Machines screen (docs/spec/machines.md 6.1), as pure functions so node --test
// can check every sentence. No imports: the caller passes the formatters it uses (format.js).

// 1240 -> "1,240", grouped the way format.js num() writes every count on the board.
export const grouped = (n) => String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ',');

const intervalText = (s) => {
  if (!s || s <= 0) return '';
  if (s < 60) return `${s} s`;
  if (s < 3600) return s % 60 === 0 ? `${s / 60} min` : `${s} s`;
  return s % 3600 === 0 ? `${s / 3600} h` : `${Math.round(s / 60)} min`;
};

// How the machine reaches the hub: {text, title, cls} or null when the hub does not know.
export function reachChip(m) {
  switch (m.reach) {
    case 'direct': return { text: 'Connects directly', title: 'Sends its updates to the hub itself (over Tailscale or HTTPS)', cls: 'reach-direct' };
    case 'forward': return { text: 'Through your laptop', title: 'Sends through an SSH forward from your laptop; updates wait while the laptop is away', cls: 'reach-forward' };
    case 'pull': {
      const every = m.pull && m.pull.interval_s ? ` every ${intervalText(m.pull.interval_s)}` : '';
      return { text: `Collected by the hub${every}`, title: 'The hub connects over SSH and collects its updates', cls: 'reach-pull' };
    }
    default: return null;
  }
}

// The status line: Live / Quiet 1 h / Seen 3 min ago / Never seen.
export function statusOf(m, now, f) {
  const at = m.last_record || m.last_seen || '';
  const seen = Date.parse(at);
  if (m.silent) return { cls: 'warn', text: `Quiet ${f.ago(at, now)}` };
  if (Number.isNaN(seen)) return { cls: 'muted', text: 'Never seen' };
  if (now - seen < 5 * 60e3) return { cls: 'ok', text: 'Live' };
  return { cls: 'muted', text: `Seen ${f.agoLabel(at, now)}` };
}

// Updates on their way are normal for a minute or two; the line shows only when they pile
// up: the oldest is older than 2 min, or (age unknown) there are 50 or more.
export const BACKLOG_AGE_MS = 2 * 60e3;
export const BACKLOG_MIN = 50;

// "1,240 waiting to send (oldest 40 min)", or '' while nothing is stuck.
export function backlogText(m, now, f) {
  if (!(m.backlog > 0)) return '';
  const t = Date.parse(m.backlog_oldest || '');
  if (Number.isNaN(t)) return m.backlog >= BACKLOG_MIN ? `${grouped(m.backlog)} waiting to send` : '';
  if (now - t < BACKLOG_AGE_MS) return '';
  return `${grouped(m.backlog)} waiting to send (oldest ${f.ago(m.backlog_oldest, now)})`;
}

// The version line: {text, cls} with cls '' | 'warn' | 'bad'.
export function versionLine(m, hubVersion) {
  const v = m.sy_version;
  const out = (version, note, cls) => ({ text: note ? `${version} · ${note}` : version, version, note, cls });
  if (!v) return out('not reported', '', '');
  if (m.held > 0) return out(v, `holding ${grouped(m.held)} updates because the hub is too old for them`, 'bad');
  // The same version string is the same version, whatever the hub could compare (M1).
  if (hubVersion && v === hubVersion) return out(v, 'same as hub', '');
  const hub = hubVersion ? ` hub ${hubVersion}` : ' the hub';
  switch (m.version_status) {
    case 'same': return out(v, 'same as hub', '');
    case 'older': return out(v, `older than${hub}`, 'warn');
    case 'newer': return out(v, `newer than${hub}`, 'warn');
    default: return out(v, '', '');
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

// The "machine went quiet" alert as a sentence, or ''.
export function alertText(a, now, f) {
  if (!a) return '';
  const when = a.fired_at ? ` ${f.agoLabel(a.fired_at, now)}` : '';
  switch (a.status) {
    case 'fired': return `Quiet-machine notification sent${when}`;
    case 'pending': return 'Quiet-machine notification about to go out';
    case 'snoozed': return 'Quiet-machine notification snoozed';
    case 'seen': return 'Quiet-machine notification seen';
    default: return `Quiet-machine alert ${a.status}`;
  }
}
