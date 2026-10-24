// Format parity: src/lib/format and webui/static/format.js give the same
// output over one case table. The table is written by scripts/dump-format-cases.mjs from the
// webui's own functions, under TZ=UTC. The port must equal format.js in any zone; it must also
// equal the table's stored output when the device zone is UTC (the zone the table was made in).
import * as port from '../src/lib/format';
import { httpsUrl, stateWord } from '../src/lib/format';
import table from './fixtures/format-cases.json';
import type { Session } from '../src/contract/api';

interface Case {
  fn: string;
  args: unknown[];
  want: unknown;
  webui?: boolean;
}

// JSON cannot carry NaN or undefined; the dump script tags them.
const dec = (v: unknown): unknown => {
  if (Array.isArray(v)) return v.map(dec);
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>;
    if (o.$ === 'NaN') return NaN;
    if (o.$ === 'undefined') return undefined;
    return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, dec(x)]));
  }
  return v;
};

const cases = (table.cases as Case[]).map((c) => ({ ...c, args: dec(c.args) as unknown[], want: dec(c.want) }));
type Fn = (...a: unknown[]) => unknown;
const run = (m: Record<string, unknown>, c: Case) => (m[c.fn] as Fn)(...c.args);

const LOCAL_FNS = new Set(['clock', 'dateTime', 'day', 'timeOfDay', 'num', 'tzLabel', 'sameTz']);
const isUtc = new Date().getTimezoneOffset() === 0;
// Local-time functions read the device zone, so their stored output only applies in UTC.
const storedApplies = (c: Case) => isUtc || !LOCAL_FNS.has(c.fn) || (c.webui === false && !!c.args[2] && c.args[2] !== 'No/Such_Zone');

describe('format port', () => {
  it('has a table', () => {
    expect(cases.length).toBeGreaterThan(150);
    expect(new Set(cases.map((c) => c.fn))).toEqual(
      new Set(['toMs', 'ago', 'agoLabel', 'clock', 'until', 'pct', 'plural', 'humanKey', 'compact', 'projectOf', 'httpsUrl', 'dateTime', 'day', 'timeOfDay', 'num', 'tzLabel', 'sameTz']),
    );
  });

  it.each(cases.map((c) => [`${c.fn}(${JSON.stringify(c.args)})`, c] as const))('%s', (_n, c) => {
    if (!storedApplies(c)) return;
    expect(run(port as unknown as Record<string, unknown>, c)).toEqual(c.want);
  });
});

describe('format.js itself (the table is current)', () => {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const webui = require('../../webui/static/format.js') as Record<string, unknown>;
  it.each(cases.filter((c) => c.webui !== false).map((c) => [`${c.fn}(${JSON.stringify(c.args)})`, c] as const))('%s', (_n, c) => {
    // The webui and the port agree in this zone; the stored output also matches in UTC.
    expect(run(port as unknown as Record<string, unknown>, c)).toEqual(run(webui, c));
    if (storedApplies(c)) expect(run(webui, c)).toEqual(c.want);
  });
});

describe('tz', () => {
  it('is only a way to read the same instant', () => {
    const now = Date.parse('2026-10-13T14:02:00Z');
    expect(port.clock('2026-10-13T17:00:00Z', now, 'Asia/Kolkata')).toBe('22:30');
    expect(port.clock('2026-10-13T17:00:00Z', now, 'UTC')).toBe('17:00');
  });
});

describe('httpsUrl', () => {
  it('refuses everything but https', () => {
    expect(httpsUrl('javascript:alert(1)')).toBeNull();
    expect(httpsUrl('http://claude.ai')).toBeNull();
    expect(httpsUrl('https://claude.ai/code/x')).toBe('https://claude.ai/code/x');
  });
});

describe('stateWord', () => {
  const base: Session = { id: 's', state: 'working', needs_you: false, state_since: '2026-10-13T14:00:00Z' };
  it('names the state as the PWA does', () => {
    expect(stateWord(base, 0)).toBe('Working');
    expect(stateWord({ ...base, state: 'asked', needs_you: true }, 0)).toBe('Asked');
    expect(stateWord({ ...base, state: 'needs_permission', needs_you: true }, 0)).toBe('Permission');
    expect(stateWord({ ...base, state: 'failed', detail: 'rate_limit', needs_you: true }, 0)).toBe('Rate limited');
    expect(stateWord({ ...base, state: 'done', detail: 'started' }, 0)).toBe('New');
    expect(stateWord({ ...base, state: 'gone' }, 0)).toBe('Gone');
  });
});

describe('planLabel', () => {
  it('says the plan as people do, never the raw key', () => {
    const { planLabel } = jest.requireActual('../src/lib/format') as typeof import('../src/lib/format');
    expect(planLabel('max_20x')).toBe('Max 20x');
    expect(planLabel('pro')).toBe('Pro');
    expect(planLabel('team')).toBe('Team');
    expect(planLabel(null)).toBe('');
    expect(planLabel('some_new_plan')).not.toContain('_');
  });
});

// Legacy compat: a hub from before the rename sends a machine's wy version as sy_version (rename.md 2.12).
describe('versionLine', () => {
  const { versionLine } = jest.requireActual('../src/lib/format') as typeof import('../src/lib/format');
  const m = { id: 'm_1', name: 'lap', version_status: 'same' } as unknown as import('../src/lib/format').MachineFull;
  it('reads wy_version, and sy_version from an older hub', () => {
    expect(versionLine({ ...m, wy_version: '1.0.0' }).text).toBe('1.0.0 · same as hub');
    expect(versionLine({ ...m, sy_version: '0.9.0' }).text).toBe('0.9.0 · same as hub'); // rename:keep
    expect(versionLine({ ...m, wy_version: '1.0.0', sy_version: '0.9.0' }).text).toBe('1.0.0 · same as hub'); // rename:keep
    expect(versionLine(m).text).toBe('not reported');
  });
});
