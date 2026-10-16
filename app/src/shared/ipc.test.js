import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { CHANNELS, INVOKE_CHANNELS, PUSH_CHANNELS, FUNCTIONS, INVOKE, PUSH, validate, IpcArgError } from './ipc.js';

const specFile = fileURLToPath(new URL('../../../docs/spec/desktop.md', import.meta.url));

// The channels and directions of desktop.md C3, read from its table.
function specChannels() {
  const md = readFileSync(specFile, 'utf8');
  const start = md.indexOf('## C3.');
  const end = md.indexOf('\n## ', start + 1);
  const invoke = [];
  const push = [];
  for (const line of md.slice(start, end).split('\n')) {
    if (!line.startsWith('| ')) continue;
    const first = line.split(' | ')[0];
    const names = [...first.matchAll(/`([^`]+)`/g)].map((m) => m[1]);
    if (first.startsWith('| push ')) push.push(...names);
    else invoke.push(...names.filter((n) => /^[a-z]+:[a-z-]+$/.test(n)));
  }
  return { invoke, push };
}

test('the table is the C3 table of desktop.md', () => {
  const spec = specChannels();
  assert.deepEqual([...INVOKE_CHANNELS], spec.invoke);
  assert.deepEqual([...PUSH_CHANNELS], spec.push);
});

test('every channel has a kind, an owner and a unique function name', () => {
  const fns = new Set();
  for (const [name, ch] of Object.entries(CHANNELS)) {
    assert.ok([INVOKE, PUSH].includes(ch.kind), name);
    assert.match(ch.owner, /^[A-HS]$/, name);
    if (ch.kind === INVOKE) {
      assert.match(ch.fn, /^[a-z][A-Za-z]+$/, name);
      assert.ok(!fns.has(ch.fn), `${name}: ${ch.fn} twice`);
      assert.notEqual(ch.fn, 'on', `${name}: 'on' is the push subscription`);
      fns.add(ch.fn);
    } else {
      assert.equal(ch.fn, null, name);
    }
  }
  assert.equal(Object.keys(FUNCTIONS).length, INVOKE_CHANNELS.length);
});

test('every string in every schema is capped', () => {
  const walk = (name, s, path) => {
    if (!s) return;
    if (s.type === 'string') assert.ok(Number.isInteger(s.max) && s.max > 0, `${name} ${path}: uncapped string`);
    if (s.type === 'array') {
      assert.ok(Number.isInteger(s.max), `${name} ${path}: uncapped array`);
      walk(name, s.items, `${path}[]`);
    }
    if (s.type === 'object') for (const [k, p] of Object.entries(s.props)) walk(name, p, `${path}.${k}`);
  };
  for (const [name, ch] of Object.entries(CHANNELS)) walk(name, ch.args, '');
});

const ID = 't_9c1e5f0a7b2d4e68';
const PROTO_ID = '1a2b3c4d';
const SEAT = 'seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4K';

const good = {
  'state:get': [undefined],
  'view:select': [{ view: 'hub' }, { view: 'settings' }, { view: ID }, { view: PROTO_ID }],
  'hub:route': [{ route: '#/sessions/abc' }, { route: '#/accounts?seat=x' }, { route: '#/' }],
  'tab:add': [{ name: 'work' }, { name: 'x'.repeat(24) }],
  'tab:update': [{ id: ID }, { id: ID, name: 'home' }],
  'tab:remove': [{ id: PROTO_ID }],
  'tab:reorder': [{ ids: [] }, { ids: [ID, PROTO_ID] }],
  'tab:reload': [{ id: ID }],
  'tab:home': [{ id: ID }],
  'tab:external': [{ id: ID }],
  'usage:accept-warning': [{ id: ID, version: 1 }],
  'usage:set-reading': [{ id: ID, on: false }],
  'usage:set-send': [{ id: ID, on: true }],
  'usage:resume': [{ id: ID }],
  'usage:refresh': [{}, { id: ID }],
  'hub:pair': [{}, { code: '123-456' }, { code: '123456' }],
  'hub:owner-sign-in': [{}, { link: 'http://127.0.0.1:8787/#/owner?s=abc' }],
  'hub:owner-sign-out': [undefined],
  'hub:connect': [{ url: 'https://hub.example.com' }, { url: '192.168.1.5:8787', code: '123-456' }],
  'hub:unpair': [{}, { forget: true }],
  'hub:retry': [undefined],
  'hub:service': [{ action: 'status' }, { action: 'start' }, { action: 'stop' }],
  'seat:update': [{ seat: SEAT }, { seat: SEAT, label: 'work', colour: '#3b82f6' }, { seat: SEAT, label: null, colour: null }],
  'accounts:local': [undefined],
  'accounts:share': [{ folder: '~/.claude-2', share: 'all' }, { folder: '/home/me/.claude', share: 'projects,todos=off' }],
  'settings:set': [{}, { keep_running: true, notifications: false, autostart: true, start_hidden: false }],
  toast: [{ level: 'warn', text: 'sy not found' }],
};

const bad = {
  'state:get': [{}, 1],
  'view:select': [{}, { view: 'Hub' }, { view: 't_9C1E5F0A7B2D4E68' }, { view: 'hub', extra: 1 }, { view: 7 }],
  'hub:route': [{ route: 'sessions' }, { route: '/#/x' }, { route: `#/${'a'.repeat(199)}` }, { route: '#/a b' }, { route: '#/\n' }],
  'tab:add': [{}, { name: '' }, { name: 'x'.repeat(25) }, { name: 5 }, { name: 'a', id: ID }],
  'tab:update': [{ name: 'x' }, { id: 'nope' }, { id: ID, name: '' }, { id: ID, colour: '#fff' }],
  'tab:remove': [{ id: '../x' }, { id: `${ID} ` }],
  'tab:reorder': [{ ids: [ID, ID] }, { ids: 'x' }, { ids: [1] }, { ids: Array.from({ length: 101 }, (_, i) => i.toString(16).padStart(8, '0')) }],
  'usage:accept-warning': [{ id: ID }, { id: ID, version: 1.5 }, { id: ID, version: '1' }, { id: ID, version: 0 }],
  'usage:set-reading': [{ id: ID, on: 'false' }, { id: ID }],
  'usage:refresh': [{ id: 'x' }, null, []],
  'hub:pair': [{ code: '12-3456' }, { code: 'abcdef' }, { code: '123 456' }],
  'hub:owner-sign-in': [{ link: '' }, { link: 5 }, { link: 'x'.repeat(2049) }],
  'hub:owner-sign-out': [{}],
  'hub:connect': [{}, { url: '' }, { url: 'x', code: 'abc' }],
  'hub:unpair': [{ forget: 'yes' }],
  'hub:retry': [{}],
  'hub:service': [{ action: 'restart' }, {}],
  'seat:update': [{ label: 'x' }, { seat: 'seat_x' }, { seat: SEAT, label: 'Work' }, { seat: SEAT, colour: '#FFF' }, { seat: SEAT, colour: 'red' }],
  'accounts:share': [{ folder: '~/.claude; rm -rf /', share: 'all' }, { folder: '~/x', share: 'ALL' }, { folder: '~/x' }],
  'settings:set': [{ keep_running: 1 }, { theme: 'dark' }],
  toast: [{ level: 'debug', text: 'x' }, { level: 'info', text: '' }],
};

test('validate accepts every good call and returns a clean copy', () => {
  for (const ch of Object.keys(CHANNELS)) if (ch !== 'state') assert.ok(good[ch], `no good case for ${ch}`);
  for (const [ch, cases] of Object.entries(good)) {
    for (const args of cases) {
      const out = validate(ch, args);
      assert.deepEqual(out, args, `${ch} ${JSON.stringify(args)}`);
      if (args !== undefined) assert.notEqual(out, args, `${ch}: must return a copy`);
    }
  }
});

test('validate refuses every bad call with an IpcArgError', () => {
  for (const [ch, cases] of Object.entries(bad)) {
    for (const args of cases) {
      assert.throws(() => validate(ch, args), IpcArgError, `${ch} ${JSON.stringify(args)?.slice(0, 80)}`);
    }
  }
});

test('unknown channels, prototype keys and non-plain objects are refused', () => {
  assert.throws(() => validate('tab:destroy', { id: ID }), IpcArgError);
  assert.throws(() => validate('__proto__', {}), IpcArgError);
  assert.throws(() => validate('toString', {}), IpcArgError);
  assert.throws(() => validate('tab:add', JSON.parse('{"name":"a","__proto__":{"x":1}}')), IpcArgError);
  assert.throws(() => validate('tab:add', Object.assign(Object.create({ polluted: true }), { name: 'a' })), IpcArgError);
  assert.throws(() => validate('tab:add', new Map([['name', 'a']])), IpcArgError);
  const out = validate('tab:update', { id: ID, name: undefined });
  assert.deepEqual(out, { id: ID });
});
