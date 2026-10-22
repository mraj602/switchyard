// node --test webui/static/*.test.cjs
// Accounts page logic: "most room right now", the tighter window, what needs a look.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, 'components/accounts-fmt.js'), 'utf8');
if (/^import /m.test(src)) throw new Error('accounts-fmt.js must stay pure');
const names = [...src.matchAll(/^export (?:function|const) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
const raw = vm.runInNewContext(`(function () {\n${src.replace(/^export /gm, '')}\nreturn { ${names.join(', ')} };\n})()`, { Date, Number, Math, Set });
const plain = (v) => (v == null ? v : JSON.parse(JSON.stringify(v)));
const { mostRoom, tighter, attention, effectiveUse, windowPhase } = raw;

const NOW = Date.parse('2026-10-17T12:00:00Z');
const at = (h) => new Date(NOW + h * 36e5).toISOString();
const win = (key, used, resetsInH) => ({ key, used_pct: used, resets_at: resetsInH == null ? null : at(resetsInH), updated_at: at(-1) });
const seat = (id, ...windows) => ({ id, hidden: false, windows });
const room = (seats) => { const r = mostRoom(seats, NOW); return r && { id: r.seat.id, five: r.five, seven: r.seven }; };

test('idle accounts at 0% beat a recently used one', () => {
  const used = seat('claude-6', win('five_hour', 20, 3), win('seven_day', 30, 70));
  const idle1 = seat('claude-2', win('five_hour', 0, 4), win('seven_day', 3, 70));
  const idle2 = seat('claude-3', win('five_hour', 0, 4), win('seven_day', 0, 70));
  assert.deepEqual(plain(room([used, idle1, idle2])), { id: 'claude-3', five: 0, seven: 0 });
  assert.equal(room([used, idle1]).id, 'claude-2');
});

test('a window whose reset has passed counts as 0, even from an old reading', () => {
  const old = seat('a', win('five_hour', 70, -2), win('seven_day', 10, 50));
  assert.equal(effectiveUse(old, 'five_hour', NOW), 0);
  const other = seat('b', win('five_hour', 15, 2), win('seven_day', 15, 50));
  assert.equal(room([other, old]).id, 'a');
  // an old reading that has not reset yet still counts
  const stale = seat('c', { ...win('five_hour', 60, 1), updated_at: at(-9) }, win('seven_day', 5, 50));
  assert.equal(room([stale, other]).id, 'b');
});

test('no reading for a window but one for the other counts as 0 for it', () => {
  const s = seat('a', win('seven_day', 25, 60));
  assert.deepEqual(plain(room([s])), { id: 'a', five: 0, seven: 25 });
});

test('an account with no reading at all is skipped; hidden ones too; none under 80 gives null', () => {
  assert.equal(room([seat('none'), seat('empty', { key: 'five_hour', used_pct: null })]), null);
  assert.equal(room([seat('none'), seat('b', win('five_hour', 10, 2), win('seven_day', 10, 9))]).id, 'b');
  assert.equal(room([{ ...seat('h', win('five_hour', 0, 2), win('seven_day', 0, 9)), hidden: true }]), null);
  assert.equal(room([seat('x', win('five_hour', 85, 2), win('seven_day', 10, 9)), seat('y', win('five_hour', 10, 2), win('seven_day', 80, 9))]), null);
});

test('ties: lower 7 d, then lower 5 h, then list order', () => {
  const a = seat('a', win('five_hour', 10, 2), win('seven_day', 10, 9));
  const b = seat('b', win('five_hour', 5, 2), win('seven_day', 10, 9));
  const c = seat('c', win('five_hour', 10, 2), win('seven_day', 4, 9));
  const d = seat('d', win('five_hour', 10, 2), win('seven_day', 10, 9));
  assert.equal(room([a, b]).id, 'b'); // same score and 7 d, lower 5 h
  assert.equal(room([a, d]).id, 'a'); // identical: list order
  assert.equal(room([d, a]).id, 'd');
  assert.equal(room([a, c]).id, 'c'); // same score (10), lower 7 d
});

test('tighter picks the higher of 5 h and 7 d, with its own reset', () => {
  const s = seat('a', win('five_hour', 91, 2), win('seven_day', 40, 60));
  assert.deepEqual(plain(tighter(s, NOW)), { key: 'five_hour', label: '5 h', used: 91, phase: 'current', resets_at: at(2) });
  const t = seat('b', win('five_hour', 10, 2), win('seven_day', 61, 60));
  assert.equal(tighter(t, NOW).label, '7 d');
  const reset = seat('c', win('five_hour', 80, -1), win('seven_day', 10, 60));
  assert.equal(tighter(reset, NOW).label, '7 d');
  assert.equal(tighter(seat('n'), NOW), null);
});

test('attention: near limit comes before signed out; extra issues follow', () => {
  const places = [{ machine: 'm1', machine_name: 'laptop', slot: '~/.claude' }];
  const so = [{ machine: 'm2', machine_name: 'desktop', slot: '~/.claude-2' }, { machine: 'm1', machine_name: 'laptop', slot: '~/.claude-3' }];
  const near = { ...seat('a', { ...win('five_hour', 91, 2), pace: { band: 'runaway', full_at: at(1) } }, win('seven_day', 10, 60)), places, signed_out: so };
  const list = plain(attention(near, NOW));
  assert.deepEqual(list.map((x) => x.kind), ['near', 'signed_out']);
  assert.deepEqual(list[1].machines, ['desktop']); // laptop is also signed in there
  const week = seat('b', win('five_hour', 10, 2), win('seven_day', 92, 60));
  assert.equal(attention(week, NOW)[0].key, 'seven_day');
  assert.deepEqual(plain(attention(seat('c', win('five_hour', 10, 2), win('seven_day', 85, 60)), NOW)), []);
  assert.equal(plain(attention({ ...seat('d'), signed_out: so.slice(0, 1) }, NOW))[0].kind, 'signed_out');
});

// An idle window (claude.ai: 0 %, resets_at null, the window has not started; api.yaml
// Window.state 'idle') is a real reading of 0, never "unknown".
const idleWin = (key) => ({ key, used_pct: 0, resets_at: null, state: 'idle', updated_at: at(-0.1) });

test('windowPhase: idle (with or without state), reset once passed, else current', () => {
  assert.equal(windowPhase(idleWin('five_hour'), NOW), 'idle');
  assert.equal(windowPhase({ key: 'five_hour', used_pct: 0, resets_at: null }, NOW), 'idle');
  assert.equal(windowPhase(win('five_hour', 0, -70), NOW), 'reset');
  assert.equal(windowPhase(win('five_hour', 40, 2), NOW), 'current');
  assert.equal(windowPhase(win('five_hour', 40, null), NOW), 'current');
});

test('an idle account counts 0 for most room, and its row says not started', () => {
  const idle = seat('claude-2', idleWin('five_hour'), win('weekly_fable', 0, 100));
  const busy = seat('claude-6', win('five_hour', 8, 3), win('seven_day', 7, 120));
  assert.equal(effectiveUse(idle, 'five_hour', NOW), 0);
  assert.deepEqual(plain(room([busy, idle])), { id: 'claude-2', five: 0, seven: 0 });
  assert.deepEqual(plain(tighter(idle, NOW)), { key: 'five_hour', label: '5 h', used: 0, phase: 'idle', resets_at: null });
});

test('a passed reset shows 0 % with the time it reset (phase reset), not a countdown', () => {
  const s = seat('claude-3', win('five_hour', 0, -76), win('weekly_fable', 0, 10));
  const t = tighter(s, NOW);
  assert.deepEqual(plain(t), { key: 'five_hour', label: '5 h', used: 0, phase: 'reset', resets_at: at(-76) });
  // an older hub that still sends the pre-reset number: it counts as 0 too
  assert.equal(effectiveUse(seat('x', win('five_hour', 64, -76)), 'five_hour', NOW), 0);
});
