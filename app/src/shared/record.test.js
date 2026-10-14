import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

import { ACCOUNT_KEYS, buildErrorRecords, buildRecords, clientOf, ORG_KEYS, pickKeys, seatsOf } from './record.js';
import { TAB_ID_RE } from './constants.js';

const dir = new URL('../../../testdata/usage/', import.meta.url);
const files = JSON.parse(fs.readFileSync(new URL('index.json', dir), 'utf8')).map((e) => e.file);
const raw = (f) => fs.readFileSync(new URL(f, dir), 'utf8');

const ORG_A = '9b20d4f1-0000-4000-8000-000000000001';
const ORG_B = '9b20d4f1-0000-4000-8000-000000000002';
const ACCT = '3f1c6a2e-0000-4000-8000-000000000001';
const org = (uuid, extra = {}) => ({ uuid, name: 'n', capabilities: ['chat'], rate_limit_tier: 't', billing_type: 'b', raven_type: null, ...extra });
const account = { uuid: ACCT, email_address: 'a@example.com', full_name: 'A', display_name: 'a' };
const base = { machine: 'm_01JA2W5N0G8H3Q7S2V6X9Y1Z4K', client: clientOf('0.6.0'), ts: '2026-10-11T14:05:00.120Z', tabId: 't_9c1e5f0a7b2d4e68' };

test('client is switchyard-desktop/<version>', () => {
  assert.equal(clientOf('0.6.0'), 'switchyard-desktop/0.6.0');
});

for (const f of files) {
  test(`record from ${f}: usage is kept whole, byte for byte`, () => {
    const usage = JSON.parse(raw(f));
    const [r] = buildRecords({ ...base, orgs: [{ org: org(ORG_A), usage }], account, activeOrg: ORG_A, fetchMs: 812 });
    assert.equal(JSON.stringify(r.payload.usage), JSON.stringify(JSON.parse(raw(f))));
    assert.equal(r.payload.usage, usage, 'not copied or filtered');
    // The record.md 4.3 field table.
    assert.equal(r.kind, 'web_usage');
    assert.deepEqual(Object.keys(r).sort(), ['client', 'kind', 'machine', 'payload', 'seat', 'ts']);
    assert.equal('slot' in r, false);
    assert.deepEqual(r.seat, { account: ACCT, org: ORG_A, src: 'claude_ai', email: 'a@example.com' });
    assert.match(r.payload.tab_id, TAB_ID_RE);
    assert.deepEqual(Object.keys(r.payload).sort(), ['account', 'active', 'fetch_ms', 'org', 'tab_id', 'usage']);
    assert.equal(r.payload.active, true);
    assert.equal(r.payload.fetch_ms, 812);
    assert.equal('error' in r.payload, false);
  });
}

test('org and account carry only their listed keys', () => {
  const [r] = buildRecords({
    ...base,
    orgs: [{ org: org(ORG_A, { settings: { x: 1 }, api_disabled_reason: null }), usage: {} }],
    account: { ...account, phone_number: '+1', settings: {} },
    activeOrg: null,
  });
  assert.deepEqual(Object.keys(r.payload.org).sort(), [...ORG_KEYS].sort());
  assert.deepEqual(Object.keys(r.payload.account).sort(), [...ACCOUNT_KEYS].sort());
  assert.equal(r.payload.active, false);
  assert.equal('fetch_ms' in r.payload, false, 'omitted when unknown');
});

test('one record per org, active marks the tab\'s last active org', () => {
  const rs = buildRecords({ ...base, orgs: [{ org: org(ORG_A), usage: {} }, { org: org(ORG_B), usage: {} }], account, activeOrg: ORG_B });
  assert.deepEqual(rs.map((r) => [r.seat.org, r.payload.active]), [[ORG_A, false], [ORG_B, true]]);
  assert.equal(new Set(rs.map((r) => r.seat.account)).size, 1);
});

test('error records: one per known seat, error {status, message}, no usage', () => {
  const seats = seatsOf([{ org: org(ORG_A) }, { org: org(ORG_B) }], account, ORG_A);
  const rs = buildErrorRecords({ ...base, seats, status: 429, message: 'rate limited' });
  assert.equal(rs.length, 2);
  for (const r of rs) {
    assert.deepEqual(r.payload.error, { status: 429, message: 'rate limited' });
    assert.equal('usage' in r.payload, false);
    assert.equal(r.payload.account.uuid, ACCT);
    assert.equal(r.seat.src, 'claude_ai');
  }
  assert.deepEqual(rs.map((r) => r.payload.org.uuid), [ORG_A, ORG_B]);
  assert.deepEqual(buildErrorRecords({ ...base, seats: [], status: 401, message: 'signed out' }), [], 'unknown seat: none');
});

test('error record from a bare stored seat', () => {
  const [r] = buildErrorRecords({ ...base, seats: [{ account: ACCT, org: ORG_A }], status: 401, message: 'signed out' });
  assert.deepEqual(r.seat, { account: ACCT, org: ORG_A, src: 'claude_ai' });
  assert.deepEqual(r.payload.org, { uuid: ORG_A });
  assert.deepEqual(r.payload.account, { uuid: ACCT });
});

test('pickKeys keeps present keys only', () => {
  assert.deepEqual(pickKeys({ a: 1, b: undefined, c: 3 }, ['a', 'b', 'd']), { a: 1, b: undefined });
  assert.deepEqual(pickKeys(null, ['a']), {});
});
