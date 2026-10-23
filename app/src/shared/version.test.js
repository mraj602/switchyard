import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseVersion, compareVersions, updateNotice, releaseUrl, updateText } from './version.js';
import { RELEASES_URL } from './constants.js';

test('parseVersion: release forms only', () => {
  assert.deepEqual(parseVersion('0.6.0'), [0, 6, 0]);
  assert.deepEqual(parseVersion('v1.2.3'), [1, 2, 3]);
  assert.deepEqual(parseVersion('0.6.0-rc.1'), [0, 6, 0]);
  assert.deepEqual(parseVersion('0.6.0-3-gabc123'), [0, 6, 0]);
  assert.deepEqual(parseVersion(' 0.6.0 '), [0, 6, 0]);
  for (const bad of ['dev', '', 'abc1234', '1.2', '1.2.3.4x y', null, undefined, 7, 'x'.repeat(65), '0.6.0/../x']) {
    assert.equal(parseVersion(bad), null, String(bad));
  }
});

test('compareVersions', () => {
  assert.equal(compareVersions([0, 6, 0], [0, 6, 0]), 0);
  assert.equal(compareVersions([0, 6, 1], [0, 6, 0]), 1);
  assert.equal(compareVersions([0, 9, 0], [0, 10, 0]), -1);
  assert.equal(compareVersions([1, 0, 0], [0, 99, 99]), 1);
});

test('updateNotice: newer hub or sy means an update, older means update them', () => {
  assert.equal(updateNotice({ app: '0.6.0', hub: '0.6.0', sy: '0.6.0' }), null);
  assert.equal(updateNotice({ app: 'dev', hub: '0.7.0' }), null);
  assert.equal(updateNotice({ app: '0.6.0' }), null);
  assert.equal(updateNotice({ app: '0.6.0', hub: 'junk', sy: null }), null);
  assert.equal(updateNotice(), null);

  const up = updateNotice({ app: '0.6.0', hub: '0.6.2', sy: '0.7.0' });
  assert.deepEqual(up, { kind: 'update', app: '0.6.0', version: '0.7.0', from: 'sy', url: `${RELEASES_URL}/tag/v0.7.0` });
  assert.equal(updateNotice({ app: '0.6.0', hub: 'v0.6.1-rc.1' }).from, 'hub');

  const old = updateNotice({ app: '0.7.0', hub: '0.6.0', sy: '0.7.0' });
  assert.deepEqual(old, { kind: 'older', app: '0.7.0', version: '0.6.0', from: 'hub', url: RELEASES_URL });
});

test('releaseUrl: a tag page for a version, else the list', () => {
  assert.equal(releaseUrl('0.6.1'), `${RELEASES_URL}/tag/v0.6.1`);
  assert.equal(releaseUrl('v0.6.1-rc.1'), `${RELEASES_URL}/tag/v0.6.1`);
  assert.equal(releaseUrl('../../evil'), RELEASES_URL);
  assert.equal(releaseUrl(null), RELEASES_URL);
  assert.ok(RELEASES_URL.startsWith('https://github.com/'));
});

test('updateText', () => {
  assert.equal(updateText(null), '');
  assert.match(updateText({ kind: 'update', app: '0.6.0', version: '0.7.0', from: 'hub' }), /^Update available: Your hub runs Switchyard 0\.7\.0, this app is 0\.6\.0\.$/);
  assert.match(updateText({ kind: 'older', app: '0.7.0', version: '0.6.0', from: 'sy' }), /sy on this computer runs Switchyard 0\.6\.0, older than this app \(0\.7\.0\)\. Update it with sy update\./);
});
