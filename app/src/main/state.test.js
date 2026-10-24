import { test } from 'node:test';
import assert from 'node:assert/strict';

import { create, defaultHub, missingAccounts, tabState } from './state.js';
import { createBus } from './bus.js';

const A = 't_9c1e5f0a7b2d4e68';
const B = 'a1b2c3d4';

function fakeTimers() {
  let now = 0;
  const pending = new Map();
  let next = 1;
  return {
    setTimeout(fn, ms) {
      const id = next++;
      pending.set(id, { fn, at: now + ms });
      return id;
    },
    clearTimeout(id) {
      pending.delete(id);
    },
    advance(ms) {
      now += ms;
      for (const [id, t] of [...pending]) {
        if (t.at <= now) {
          pending.delete(id);
          t.fn();
        }
      }
    },
    get pending() {
      return pending.size;
    },
  };
}

function setup(extra = {}) {
  const bus = createBus({ onError: (e) => { throw e; } });
  const sent = [];
  const handlers = new Map();
  const ipc = {
    register: (ch, fn) => handlers.set(ch, fn),
    send: (ch, p) => sent.push([ch, p]),
    watchShell() {},
  };
  let selected = 'hub';
  const selectFns = new Set();
  const window = {
    shell: {},
    selected: () => selected,
    select: (id) => {
      selected = id;
      for (const fn of selectFns) fn(id);
    },
    onSelect: (fn) => (selectFns.add(fn), () => selectFns.delete(fn)),
  };
  const timers = fakeTimers();
  const st = create({ bus, ipc, window, timers, ...extra });
  return { bus, ipc, sent, handlers, window, timers, st };
}

const info = (id, more = {}) => ({
  id, name: 'work', reading: 'on', warning_accepted: 1, send_to_hub: true, stopped: null,
  seat: { account: 'acc-1', org: 'org-1' }, last_url: 'https://claude.ai/new', signed_in: true, loaded: true, ...more,
});

test('snapshot before anything is known', () => {
  const { st } = setup();
  assert.deepEqual(st.snapshot(), {
    tabs: [], missing: [], view: 'hub', hub: defaultHub(),
    settings: { keep_running: true, notifications: true, start_hidden: false, autostart: false, close_notice_shown: false },
    banners: [], update: null,
  });
});

test('the hub certificate, the http warning and the update notice are copied field by field', () => {
  const fp = 'AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89';
  const { bus, st } = setup({ about: { app: '0.6.0', electron: '1', chrome: '1', platform: 'x' } });
  bus.emit('hub', {
    status: 'unreachable', url: 'https://hub.lan:8443', version: '0.7.0', sy_version: '0.6.0', insecure: false, pinned: true,
    cert: { url: 'https://hub.lan:8443', origin: 'https://hub.lan:8443', fingerprint: fp, pinned: 'nope', changed: true, trusted: false, reason: 'X', subject: 'CN=a', issuer: 'CN=a', valid_to: 'y', pem: 'leak' },
  });
  const s = st.snapshot();
  assert.deepEqual(s.hub.cert, { url: 'https://hub.lan:8443', origin: 'https://hub.lan:8443', fingerprint: fp, pinned: null, changed: true, trusted: false, reason: 'X', subject: 'CN=a', issuer: 'CN=a', valid_to: 'y' });
  assert.equal(s.hub.pinned, true);
  assert.equal(s.hub.sy_version, '0.6.0');
  assert.deepEqual(s.update, { kind: 'update', app: '0.6.0', version: '0.7.0', from: 'hub' });
  bus.emit('hub', { status: 'ok', url: 'http://192.168.1.5:8787', insecure: true, version: '0.6.0', cert: { fingerprint: 'bad' } });
  assert.equal(st.snapshot().hub.insecure, true);
  assert.equal(st.snapshot().hub.cert, null);
  assert.equal(st.snapshot().update, null);
});

test('the About versions are copied by name, and only when boot gives them', () => {
  const { st } = setup({ about: { app: '0.9.1', electron: '33.2.0', chrome: '130.0', platform: 'linux x64', token: 'x' } });
  assert.deepEqual(st.snapshot().about, { app: '0.9.1', electron: '33.2.0', chrome: '130.0', platform: 'linux x64' });
  assert.equal('about' in setup().st.snapshot(), false);
});

test('registers state:get and view:select', async () => {
  const { handlers, st, bus, window } = setup();
  assert.deepEqual([...handlers.keys()], ['state:get', 'view:select']);
  assert.deepEqual(handlers.get('state:get')(), st.snapshot());
  bus.emit('tabs', { tabs: [info(A)] });
  handlers.get('view:select')({ view: A });
  assert.equal(window.selected(), A);
  handlers.get('view:select')({ view: 'settings' });
  assert.equal(window.selected(), 'settings');
  assert.throws(() => handlers.get('view:select')({ view: B }), /unknown tab/);
});

test('tabs merge TabInfo, the newest usage of each tab and the hub seat look', () => {
  const seats = [{ id: 'seat_01M4SH4F8KV20NK22FQCTR0RRK', account: 'acc-1', org: 'org-1', label: 'work', colour: '#112233', token: 'tok_leak' }];
  const { bus, st } = setup({ hub: { seats: () => seats } });
  bus.emit('tabs', { tabs: [info(A), info(B, { name: 'home', seat: null, reading: 'never_asked', warning_accepted: 0 })] });
  bus.emit('usage', { id: A, read_state: 'reading', stopped: null, retry_at: null, last_read: '2026-10-13T14:05:00Z', windows: [{ key: 'five_hour', label: null, used_pct: 12, resets_at: 'x', secret: 1 }], windows_source: 'hub' });
  bus.emit('usage', { id: A, read_state: 'idle', stopped: null, retry_at: null, last_read: '2026-10-13T14:10:00Z', windows: [{ key: 'five_hour', label: null, used_pct: 13, resets_at: 'y' }], windows_source: 'hub' });
  const [a, b] = st.snapshot().tabs;
  assert.deepEqual(a, {
    id: A, name: 'work', expected: null, mismatch: null, label: 'work', colour: '#112233', seat: 'seat_01M4SH4F8KV20NK22FQCTR0RRK', reading: 'on', needs_warning: false, send_to_hub: true,
    read_state: 'idle', stopped: null, retry_at: null, last_read: '2026-10-13T14:10:00Z',
    windows: [{ key: 'five_hour', label: null, used_pct: 13, resets_at: 'y' }], windows_source: 'hub', signed_in: true,
  });
  assert.equal(b.label, null);
  assert.equal(b.seat, null, 'no seat known yet');
  assert.equal(b.needs_warning, true, 'signed in, never asked');
  assert.equal(b.read_state, 'idle');
  assert.deepEqual(b.windows, []);
  assert.equal(JSON.stringify(st.snapshot()).includes('tok_leak'), false, 'unknown seat fields are not copied');
});

test('needs_warning and read_state rules', () => {
  const t = (i, u) => tabState({ id: A, name: 'x', ...i }, u, []);
  assert.equal(t({ reading: 'never_asked', warning_accepted: 0, signed_in: false }).needs_warning, false, 'not before sign-in');
  assert.equal(t({ reading: 'on', warning_accepted: 0, signed_in: true }).needs_warning, true, 'a raised version asks again');
  assert.equal(t({ reading: 'off', warning_accepted: 0, signed_in: true }).needs_warning, false, 'Not now');
  assert.equal(t({ reading: 'off', signed_in: true }, { read_state: 'reading' }).read_state, 'off');
  assert.deepEqual(t({ reading: 'on', stopped: { status: 429, at: 'z' } }).stopped, { status: 429, at: 'z' });
  assert.equal(t({ reading: 'on', stopped: { status: 429, at: 'z' } }).read_state, 'stopped');
  assert.equal(t({ reading: 'bogus' }).reading, 'never_asked');
});

test('hub state is copied field by field; needs_you from its own event; tokens never pass', () => {
  const { bus, st } = setup();
  bus.emit('hub', { url: 'http://127.0.0.1:8787', connected: true, reachable: true, paired: true, owner: false, version: '0.6.0', local: true, service: 'running', admin: 'local', needs_you: 1, status: 'ok', token: 'sy_d_secret' });
  assert.equal(st.snapshot().hub.needs_you, 1);
  bus.emit('needs_you', { count: 3, sessions: [] });
  const hub = st.snapshot().hub;
  assert.equal(hub.needs_you, 3);
  assert.equal(hub.service, 'running');
  assert.equal(hub.storage_backend, null);
  assert.equal(hub.error, null);
  bus.emit('hub', { status: 'no_sy', storage_backend: 'basic_text', error: 'wy was not found' });
  assert.equal(st.snapshot().hub.error, 'wy was not found');
  assert.equal(st.snapshot().hub.storage_backend, 'basic_text');
  assert.equal(st.snapshot().settings.storage_backend, 'basic_text', 'before the settings event, from the hub');
  assert.equal(JSON.stringify(st.snapshot()).includes('sy_d_'), false);
});

test('settings: E events win; before that the stored settings', () => {
  const store = { get: () => ({ settings: { keep_running: false, notifications: true, start_hidden: true, close_notice_shown: true } }) };
  const { bus, st } = setup({ store });
  assert.deepEqual(st.snapshot().settings, { keep_running: false, notifications: true, start_hidden: true, autostart: false, close_notice_shown: true });
  bus.emit('settings', { keep_running: true, notifications: false, start_hidden: false, autostart: true, close_notice_shown: false, storage_backend: 'basic_text' });
  assert.deepEqual(st.snapshot().settings, { keep_running: true, notifications: false, start_hidden: false, autostart: true, close_notice_shown: false, storage_backend: 'basic_text' });
});

test('pushes are debounced to one per 100 ms and carry the newest state', () => {
  const { bus, sent, timers, window } = setup();
  bus.emit('tabs', { tabs: [info(A)] });
  bus.emit('usage', { id: A, read_state: 'reading' });
  bus.emit('hub', { status: 'ok' });
  window.select(A);
  assert.equal(sent.length, 0);
  timers.advance(99);
  assert.equal(sent.length, 0);
  timers.advance(1);
  assert.equal(sent.length, 1);
  const [ch, s] = sent[0];
  assert.equal(ch, 'state');
  assert.equal(s.view, A);
  assert.equal(s.tabs[0].read_state, 'reading');
  bus.emit('usage', { id: A, read_state: 'idle' });
  timers.advance(100);
  assert.equal(sent.length, 2);
});

test('usage of removed tabs is forgotten', () => {
  const { bus, st } = setup();
  bus.emit('tabs', { tabs: [info(A)] });
  bus.emit('usage', { id: A, read_state: 'reading' });
  bus.emit('tabs', { tabs: [] });
  bus.emit('tabs', { tabs: [info(A)] });
  assert.equal(st.snapshot().tabs[0].read_state, 'idle');
});

test('banners and stop', () => {
  const { bus, st, sent, timers } = setup();
  st.setBanner('wy', { level: 'warn', text: 'wy not found: run wy setup' });
  assert.deepEqual(st.snapshot().banners, [{ level: 'warn', text: 'wy not found: run wy setup' }]);
  assert.throws(() => st.setBanner('x', { level: 'loud', text: 'x' }), /bad banner/);
  st.setBanner('wy', null);
  assert.deepEqual(st.snapshot().banners, []);
  st.stop();
  assert.equal(timers.pending, 0);
  bus.emit('hub', { status: 'ok' });
  timers.advance(1000);
  assert.equal(sent.length, 0);
  assert.equal(bus.listenerCount('hub'), 0);
});

test('missingAccounts: hub accounts with no tab, by account uuid; hidden and unnamed seats are left out', () => {
  const S = (n, extra = {}) => ({ id: `seat_01M4SH4F8KV20NK22FQCTR0RR${n}`, account: `acc-${n}`, org: `org-${n}`, label: `claude-${n}`, ...extra });
  const seats = [S(1), S(2), S(3, { hidden: true }), S(4, { label: null, email: null }), S(5), S(5, { org: 'org-5b' })];
  const tabs = [{ seat: { account: 'acc-1', org: 'org-1' } }, { seat: null, expect: { account: 'acc-5' } }];
  assert.deepEqual(missingAccounts(seats, tabs), [{ seat: seats[1].id, label: 'claude-2' }]);
  assert.deepEqual(missingAccounts(seats, []).map((m) => m.label), ['claude-1', 'claude-2', 'claude-5']);
  assert.deepEqual(missingAccounts(undefined, tabs), []);
});

test('mismatch.shared: another tab really signed in as the same account', () => {
  const seats = [{ id: 'seat_01M4SH4F8KV20NK22FQCTR0RR1', account: 'acc-1', org: 'org-1', label: 'claude-1' }];
  const expect = { seat: 'seat_01M4SH4F8KV20NK22FQCTR0RR6', account: 'acc-6', org: 'org-6', label: 'claude-6' };
  const wrong = info(A, { seat: { account: 'acc-1', org: 'org-1' }, expect });
  const own = info(B, { seat: { account: 'acc-1', org: 'org-1' } });
  assert.equal(tabState(wrong, null, seats, [wrong, own]).mismatch.shared, true);
  assert.equal(tabState(wrong, null, seats, [wrong]).mismatch.shared, false, 'itself does not count');
  assert.equal(tabState(wrong, null, seats, [wrong, { ...own, signed_in: false }]).mismatch.shared, false, 'a signed-out tab does not count');
  const alsoWrong = info(B, { seat: { account: 'acc-1', org: 'org-1' }, expect });
  assert.equal(tabState(wrong, null, seats, [wrong, alsoWrong]).mismatch.shared, false, 'a tab made for another account is not that account\'s tab');
});

test('a tab made for claude-6 and signed in as claude-1 is a mismatch (uuid, not email); signed in as 6 is not', () => {
  const seats = [
    { id: 'seat_01M4SH4F8KV20NK22FQCTR0RR1', account: 'acc-1', org: 'org-1', label: 'claude-1', email: 'same@example.com' },
    { id: 'seat_01M4SH4F8KV20NK22FQCTR0RR6', account: 'acc-6', org: 'org-6', label: 'claude-6', email: 'same@example.com' },
  ];
  const expect = { seat: seats[1].id, account: 'acc-6', org: 'org-6', label: 'claude-6' };
  const wrong = tabState(info(A, { seat: { account: 'acc-1', org: 'org-1' }, expect }), null, seats);
  assert.deepEqual(wrong.mismatch, { expected: 'claude-6', actual: 'claude-1', shared: false });
  assert.equal(wrong.expected, 'claude-6');
  assert.deepEqual([wrong.label, wrong.colour, wrong.seat], [null, null, null], 'the landed-in account\'s look is not the tab\'s');
  assert.equal(tabState(info(A, { seat: { account: 'acc-6', org: 'org-6' }, expect }), null, seats).mismatch, null);
  assert.equal(tabState(info(A, { seat: null, expect }), null, seats).mismatch, null, 'not signed in yet');
  assert.equal(tabState(info(A, { seat: { account: 'zzz', org: 'o' }, expect }), null, seats).mismatch.actual, null, 'an account the hub does not know');
});
