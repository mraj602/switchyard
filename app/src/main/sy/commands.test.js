import { test } from 'node:test';
import assert from 'node:assert/strict';
import { build, PURPOSES } from './commands.js';

// The C11 table, as the only argv shapes that may exist.
const ALLOWED = [
  (a) => a.join(' ') === '--version',
  (a) => a.join(' ') === 'machine show --json',
  (a) => a.join(' ') === 'outbox append',
  (a) => a.join(' ') === 'flush --quiet',
  (a) => a.join(' ') === 'desktop info --json',
  (a) => a.length >= 5 && a.slice(0, 4).join(' ') === 'desktop token --json --name' && /^[A-Za-z0-9._-]{1,40}$/.test(a[4]) && (a.length === 5 || (a.length === 6 && a[5] === '--code-stdin')),
  (a) => a.join(' ') === 'desktop owner-link --json',
  (a) => a.length === 4 && a[0] === 'desktop' && a[1] === 'hub' && ['status', 'start', 'stop'].includes(a[2]) && a[3] === '--json',
  (a) => a.join(' ') === 'account list --json',
  (a) => a.length === 7 && a[0] === 'account' && a[1] === 'share' && /^~?[A-Za-z0-9._/-]{1,200}$/.test(a[2]) && !a[2].startsWith('-') && a[3] === '--share' && /^[a-z,=:+-]{1,200}$/.test(a[4]) && a[5] === '--yes' && a[6] === '--json',
];
const inTable = (argv) => ALLOWED.some((f) => f(argv));

// A small deterministic PRNG so a failure reproduces.
function rng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}
const ALPHABET = 'abcxyzAZ09 ._-/~,=:+*;|&$`"\'\\\n\t\0-<>(){}[]%#@!?';
function randString(r) {
  const n = Math.floor(r() * 12);
  let out = '';
  for (let i = 0; i < n; i++) out += ALPHABET[Math.floor(r() * ALPHABET.length)];
  return out;
}

test('every purpose builds exactly its table row', () => {
  assert.deepEqual(build('version').argv, ['--version']);
  assert.deepEqual(build('machine').argv, ['machine', 'show', '--json']);
  assert.deepEqual(build('append'), { argv: ['outbox', 'append'], stdin: true, timeout: 15_000 });
  assert.deepEqual(build('flush'), { argv: ['flush', '--quiet'], stdin: false, timeout: 60_000 });
  assert.deepEqual(build('info').argv, ['desktop', 'info', '--json']);
  assert.deepEqual(build('token', { name: 'My-PC' }).argv, ['desktop', 'token', '--json', '--name', 'My-PC']);
  assert.deepEqual(build('token', { name: 'a', code_stdin: true }), { argv: ['desktop', 'token', '--json', '--name', 'a', '--code-stdin'], stdin: true, timeout: 60_000 });
  assert.equal(build('owner').timeout, 60_000);
  assert.deepEqual(build('service', { action: 'stop' }).argv, ['desktop', 'hub', 'stop', '--json']);
  assert.deepEqual(build('accounts').argv, ['account', 'list', '--json']);
  assert.deepEqual(build('share', { folder: '~/.claude-2', share: 'skills,agents=copy:x+y' }).argv, ['account', 'share', '~/.claude-2', '--share', 'skills,agents=copy:x+y', '--yes', '--json']);
});

test('unknown purposes, inherited names and bad params object throw', () => {
  for (const p of ['', 'rm', 'toString', '__proto__', 'constructor', undefined, null, 5, ['info']]) assert.throws(() => build(p), /sy:/);
  assert.throws(() => build('info', null));
  assert.throws(() => build('info', 'x'));
});

test('property: random strings for every parameter either throw or give a table argv', () => {
  const r = rng(20261013);
  let built = 0;
  let refused = 0;
  for (let i = 0; i < 6000; i++) {
    const params = {
      name: randString(r), action: randString(r), folder: randString(r), share: randString(r),
      code_stdin: [true, false, undefined, 'yes', 1][Math.floor(r() * 5)],
    };
    for (const purpose of PURPOSES) {
      try {
        const { argv } = build(purpose, params);
        built++;
        assert.ok(inTable(argv), `not in the table: ${JSON.stringify(argv)}`);
        assert.ok(argv.every((x) => typeof x === 'string'));
      } catch (err) {
        assert.match(err.message, /^sy:/);
        refused++;
      }
    }
  }
  assert.ok(built > 0 && refused > 0);
});

test('property: parameters that are valid on their own are accepted, any one invalid character is refused', () => {
  const r = rng(7);
  const good = { name: 'Switchyard-desktop-m-1', action: 'start', folder: '~/.claude-3/x', share: 'a,b=c' };
  assert.ok(inTable(build('token', good).argv) && inTable(build('service', good).argv) && inTable(build('share', good).argv));
  for (let i = 0; i < 2000; i++) {
    const bad = randString(r) + ['\n', ' ', '\0', ';', '`', '$', '|', '"', '*', '\\'][i % 10] + randString(r);
    assert.throws(() => build('share', { ...good, folder: bad }), /sy:/);
    assert.throws(() => build('share', { ...good, share: bad }), /sy:/);
    assert.throws(() => build('token', { name: bad.replace(/[A-Za-z0-9]/g, '') + '\n' }), /sy:/);
  }
});

test('boundaries: lengths and a leading dash', () => {
  assert.doesNotThrow(() => build('token', { name: 'a'.repeat(40) }));
  assert.throws(() => build('token', { name: 'a'.repeat(41) }));
  assert.throws(() => build('token', { name: '' }));
  assert.throws(() => build('token', { name: 'ok\n' }));
  assert.doesNotThrow(() => build('share', { folder: 'a'.repeat(200), share: 'a' }));
  assert.throws(() => build('share', { folder: 'a'.repeat(201), share: 'a' }));
  assert.throws(() => build('share', { folder: '--yes', share: 'a' }));
  assert.throws(() => build('share', { folder: '-x', share: 'a' }));
  assert.throws(() => build('share', { folder: 'x', share: 'A' }));
  assert.throws(() => build('share', { folder: ['x'], share: 'a' }));
  assert.throws(() => build('service', { action: 'restart' }));
  assert.throws(() => build('token', { name: 'a', code_stdin: 'yes' }));
});

test('no parameter can add flags: params outside the table are not copied into argv', () => {
  const { argv } = build('info', { extra: '--evil', argv: ['x'] });
  assert.deepEqual(argv, ['desktop', 'info', '--json']);
});

test('token name: no spaces (an ssh admin channel refuses them as a remote word)', () => {
  assert.throws(() => build('token', { name: 'Switchyard desktop laptop' }), /sy: bad token name/);
  assert.deepEqual(build('token', { name: 'Switchyard-desktop-laptop' }).argv, ['desktop', 'token', '--json', '--name', 'Switchyard-desktop-laptop']);
});
