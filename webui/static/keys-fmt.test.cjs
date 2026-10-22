// node --test webui/static/*.test.cjs
// The sentences and steps of Settings → Keys (components/keys-fmt.js, keys.md 3.3, 4.2).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const mod = (f) => {
  const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
  if (/^import /m.test(src)) throw new Error(`${f} has imports; test only pure modules`);
  const names = [...src.matchAll(/^export (?:async )?(?:function|const|let|class) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  const body = src.replace(/^export /gm, '');
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { Number, String, Array, Object, Date });
};

const K = mod('components/keys-fmt.js');
const cert = { id: 'a', keys: { owner_signed: true } }, plain = { id: 'b', keys: { owner_signed: false } };

test('fingerprints are grouped in fours', () => {
  assert.equal(K.groupFP('k7q2m4xapz3cufh62wbeq5no'), 'k7q2-m4xa-pz3c-ufh6-2wbe-q5no');
  assert.equal(K.groupFP('short'), 'short');
  assert.equal(K.groupFP(null), '');
});

test('a pending key expires 24 hours after it was proposed', () => {
  assert.equal(K.pendingExpiry('2026-10-22T09:00:00Z'), '2026-10-23T09:00:00.000Z');
  assert.equal(K.pendingExpiry(''), '');
  assert.equal(K.pendingExpiry('nonsense'), '');
});

test('the proposer is named from the device list', () => {
  const devs = [{ id: 'd1', name: 'laptop' }, { id: 'd2', name: '' }];
  assert.equal(K.proposerName({ proposed_by: 'd1' }, devs), 'laptop');
  assert.equal(K.proposerName({ proposed_by: 'd2' }, devs), 'a device');
  assert.equal(K.proposerName({ proposed_by: 'zz' }, devs), 'a device');
  assert.equal(K.proposerName({}, undefined), 'a device');
});

test('certification: certified or hub-signed only', () => {
  assert.equal(K.certState(cert), 'certified');
  assert.equal(K.certState(plain), 'hub-signed');
  assert.equal(K.certState({}), 'hub-signed');
  assert.equal(K.uncertified([cert, plain, plain]), 2);
  assert.equal(K.uncertifiedList([cert, plain])[0].id, 'b');
  assert.equal(K.uncertified(undefined), 0);
  assert.equal(K.CERT_WORD['hub-signed'], 'Hub-signed only');
  assert.match(K.CERTIFY_HINT, /sy keys certify --all/);
});

test('the next step', () => {
  assert.equal(K.nextStep({ owner_key: null, pending: [] }), 'init');
  assert.equal(K.nextStep({ owner_key: null }), 'init');
  assert.equal(K.nextStep({ owner_key: null, pending: [{ fp: 'x' }] }), 'confirm');
  assert.equal(K.nextStep({ owner_key: { fp: 'x' }, devices: [cert, plain] }), 'certify');
  assert.equal(K.nextStep({ owner_key: { fp: 'x' }, devices: [cert] }), 'done');
  // a viewer sees no device list: nothing to certify from its side
  assert.equal(K.nextStep({ owner_key: { fp: 'x' } }), 'done');
  // a replacement proposal while a key is active is shown by the page itself, not as the step
  assert.equal(K.nextStep({ owner_key: { fp: 'x' }, pending: [{ fp: 'y' }], devices: [cert] }), 'done');
});

test('revocations line', () => {
  assert.equal(K.revocationsLine(0), 'No revocations yet');
  assert.equal(K.revocationsLine(3), 'Revocation list version 3');
});
