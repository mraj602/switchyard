import { test } from 'node:test';
import assert from 'node:assert/strict';

import { partitionFolder, partitionOf, userAgentFor } from './partition.js';
import { createCookieFlusher, FLUSH_INTERVAL_MS } from './cookies.js';

test('partitionOf: persist:tab-<id>, for both id shapes, nothing else', () => {
  assert.equal(partitionOf('t_9c1e5f0a7b2d4e68'), 'persist:tab-t_9c1e5f0a7b2d4e68');
  assert.equal(partitionOf('9c1e5f0a'), 'persist:tab-9c1e5f0a');
  assert.equal(partitionFolder('9c1e5f0a'), 'tab-9c1e5f0a');
  for (const bad of ['', '../x', 't_9C1E5F0A7B2D4E68', 't_9c1e5f0a7b2d4e6', '9c1e5f0', null, undefined, 7]) {
    assert.throws(() => partitionOf(bad), /bad tab id/, String(bad));
  }
});

test('userAgentFor: Chrome with the real platform, never the Electron token', () => {
  const ua = (platform) => userAgentFor({ platform, chrome: '130.0.6723.0' });
  assert.equal(ua('linux'), 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.6723.0 Safari/537.36');
  assert.match(ua('darwin'), /Macintosh; Intel Mac OS X 10_15_7/);
  assert.match(ua('win32'), /Windows NT 10\.0; Win64; x64/);
  assert.match(ua('freebsd'), /X11; Linux/);
  for (const p of ['linux', 'darwin', 'win32']) assert.doesNotMatch(ua(p), /Electron/);
});

test('cookie flusher: every 30 s, stop, errors are contained', async () => {
  const flushed = [];
  const sessions = [{ cookies: { flushStore: async () => void flushed.push('a') } }, { cookies: { flushStore: async () => Promise.reject(new Error('disk')) } }];
  let tick;
  let cleared = false;
  const warns = [];
  const f = createCookieFlusher({
    sessions: () => sessions,
    log: { warn: (...a) => warns.push(a.join(' ')) },
    setInterval: (fn, ms) => {
      tick = { fn, ms };
      return 1;
    },
    clearInterval: () => (cleared = true),
  });
  f.start();
  f.start();
  assert.equal(tick.ms, FLUSH_INTERVAL_MS);
  assert.equal(FLUSH_INTERVAL_MS, 30_000);
  tick.fn();
  await f.flush();
  assert.deepEqual(flushed, ['a', 'a']);
  assert.equal(warns.length, 2);
  f.stop();
  assert.equal(cleared, true);
});
