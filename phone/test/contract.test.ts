// Contract test (phases/8.md wave 0): the TS validators accept what the Go side really emits.
// - Go goldens: internal/hub/push/expo/testdata/*.json (WP-H's message-*.json: every push.md 5.5
//   row, private, summary, test and refresh).
// - Devserver replies: test/fixtures/hub/ (scripts/dump-fixtures.sh).
// - The P11 snapshot example: test/fixtures/widget-snapshot.v1.json.
import * as fs from 'node:fs';
import * as path from 'node:path';

import {
  EXPO_TOKEN,
  PAYLOAD_KEYS,
  validateAlert,
  validateExpoMessage,
  validateLiveProps,
  validateMatrixInfo,
  validateMe,
  validateOverview,
  validatePairClaimReply,
  validatePayload,
  validatePeerAudit,
  validateSeat,
  validateSession,
  validateWidgetSnapshot,
} from '../src/contract';

const GOLDENS = path.resolve(__dirname, '../../internal/hub/push/expo/testdata');
const HUB = path.join(__dirname, 'fixtures/hub');
const read = (file: string): unknown => JSON.parse(fs.readFileSync(file, 'utf8'));
const hub = (name: string) => read(path.join(HUB, name)) as Record<string, unknown>;

describe('Go Expo message goldens', () => {
  const files = fs.readdirSync(GOLDENS).filter((f) => f.endsWith('.json'));

  it('exist', () => {
    expect(files.length).toBeGreaterThan(0);
  });

  it.each(files)('%s is a valid Expo message', (f) => {
    expect(validateExpoMessage(read(path.join(GOLDENS, f)))).toEqual([]);
  });

  it.each(files)('%s data is the Go payload exactly (key order, no unknown keys)', (f) => {
    const m = read(path.join(GOLDENS, f)) as { data: { type: string } };
    if (m.data.type === 'refresh') return;
    expect(validatePayload(m.data, 'expo.data', true)).toEqual([]);
  });
});

describe('validators reject what the contract forbids', () => {
  const alert = read(path.join(GOLDENS, 'message-asked-question.json')) as Record<string, unknown> & { data: Record<string, unknown> };

  it('a private Expo push with remote_url', () => {
    const data: Record<string, unknown> = { ...alert.data, remote_url: 'https://claude.ai/code/x', private: true };
    // Rebuild in Go key order so only the private rule fires.
    const ordered = Object.fromEntries(PAYLOAD_KEYS.filter((k) => k in data).map((k) => [k, data[k]]));
    expect(validateExpoMessage({ ...alert, data: ordered }).join()).toMatch(/remote_url/);
  });

  it('a payload with keys out of Go order', () => {
    const { v, ...rest } = alert.data;
    expect(validatePayload({ ...rest, v }, 'payload', true).join()).toMatch(/Go order/);
  });

  it('a refresh with a title', () => {
    const r = read(path.join(GOLDENS, 'message-refresh.json')) as Record<string, unknown>;
    expect(validateExpoMessage({ ...r, title: 'x' }).join()).toMatch(/title/);
  });

  it('a message over 4096 bytes', () => {
    expect(validateExpoMessage({ ...alert, body: 'x'.repeat(5000) }).join()).toMatch(/4096/);
  });

  it('a bad push token', () => {
    expect(EXPO_TOKEN.test('ExponentPushToken[abc]')).toBe(false);
    expect(EXPO_TOKEN.test('ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]')).toBe(true);
    expect(EXPO_TOKEN.test('ExpoPushToken[xxxxxxxxxxxxxxxxxxxxxx]')).toBe(true);
  });

  it('a private snapshot with a top item', () => {
    const s = read(path.join(__dirname, 'fixtures/widget-snapshot.v1.json')) as Record<string, unknown>;
    expect(validateWidgetSnapshot({ ...s, private: true }).join()).toMatch(/top/);
  });
});

describe('devserver fixtures', () => {
  it('me', () => expect(validateMe(hub('me.json'))).toEqual([]));
  it('pair claim reply (cookie: false carries the token)', () => {
    const claim = hub('pair-claim.json');
    expect(validatePairClaimReply(claim)).toEqual([]);
    expect(typeof claim.token).toBe('string');
  });
  it('overview', () => expect(validateOverview(hub('overview.json'))).toEqual([]));
  it('sessions', () => {
    const list = hub('sessions.json').sessions as unknown[];
    expect(list.length).toBeGreaterThan(0);
    list.forEach((s, i) => expect(validateSession(s, `sessions[${i}]`)).toEqual([]));
  });
  it('session detail', () => expect(validateSession(hub('session.json'))).toEqual([]));
  it('accounts', () => {
    const list = hub('accounts.json').accounts as unknown[];
    list.forEach((s, i) => expect(validateSeat(s, `accounts[${i}]`)).toEqual([]));
  });
  it('alerts', () => {
    const list = hub('alerts.json').alerts as unknown[];
    list.forEach((a, i) => expect(validateAlert(a, `alerts[${i}]`)).toEqual([]));
  });
  it('alert', () => expect(validateAlert(hub('alert.json'))).toEqual([]));
  it('peers matrix', () => expect(validateMatrixInfo(hub('peers-matrix.json'))).toEqual([]));
  it('peers audit', () => {
    const list = hub('peers-audit.json').entries as unknown[];
    expect(list.length).toBeGreaterThan(0);
    list.forEach((e, i) => expect(validatePeerAudit(e, `entries[${i}]`)).toEqual([]));
  });
  it('snooze_min is in GET /v1/rules settings', () => {
    const settings = hub('rules.json').settings as Record<string, unknown>;
    expect(typeof settings.snooze_min).toBe('number');
  });
});

describe('phone.md P11, P12 examples', () => {
  it('widget-snapshot.v1.json', () => {
    expect(validateWidgetSnapshot(read(path.join(__dirname, 'fixtures/widget-snapshot.v1.json')))).toEqual([]);
  });
  it('LiveCountdownProps', () => {
    expect(
      validateLiveProps({
        seat: 'home',
        colour: '#2fb380',
        window: 'five_hour',
        pct: 88,
        resets_at: '2026-10-13T15:30:00.000Z',
        started_at: '2026-10-13T14:02:00.000Z',
      }),
    ).toEqual([]);
  });
});
