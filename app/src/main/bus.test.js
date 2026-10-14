import { test } from 'node:test';
import assert from 'node:assert/strict';

import { createBus, EVENTS } from './bus.js';

test('the event set is closed', () => {
  assert.deepEqual([...EVENTS], ['tabs', 'usage', 'hub', 'needs_you', 'alert', 'settings']);
  const bus = createBus();
  assert.throws(() => bus.on('tab', () => {}), /unknown event/);
  assert.throws(() => bus.emit('state', {}), /unknown event/);
  assert.throws(() => bus.last('nope'), /unknown event/);
  assert.throws(() => bus.on('tabs', 'x'), TypeError);
  assert.throws(() => bus.emit('tabs', null), TypeError);
  assert.throws(() => bus.emit('tabs', [1]), TypeError);
  assert.throws(() => bus.emit('tabs', 'x'), TypeError);
  assert.ok(Object.isFrozen(bus));
});

test('listeners run in order, unsubscribe, once, and last()', () => {
  const bus = createBus();
  const got = [];
  const offA = bus.on('usage', (p) => got.push(['a', p.n]));
  bus.on('usage', (p) => got.push(['b', p.n]));
  bus.once('usage', (p) => got.push(['once', p.n]));
  assert.equal(bus.last('usage'), undefined);
  bus.emit('usage', { n: 1 });
  offA();
  offA(); // twice is harmless
  bus.emit('usage', { n: 2 });
  assert.deepEqual(got, [['a', 1], ['b', 1], ['once', 1], ['b', 2]]);
  assert.deepEqual(bus.last('usage'), { n: 2 });
  assert.equal(bus.listenerCount('usage'), 1);
  assert.equal(bus.listenerCount('hub'), 0);
});

test('the same function subscribed twice gets two calls and unsubscribes one at a time', () => {
  const bus = createBus();
  let n = 0;
  const fn = () => n++;
  const off1 = bus.on('hub', fn);
  bus.on('hub', fn);
  bus.emit('hub', {});
  off1();
  bus.emit('hub', {});
  assert.equal(n, 3);
});

test('a throwing listener is reported and does not stop the others', () => {
  const errors = [];
  const bus = createBus({ onError: (err, ev) => errors.push([ev, err.message]) });
  const got = [];
  bus.on('alert', () => {
    throw new Error('boom');
  });
  bus.on('alert', (p) => got.push(p.id));
  bus.emit('alert', { id: 'al_1' });
  assert.deepEqual(got, ['al_1']);
  assert.deepEqual(errors, [['alert', 'boom']]);
});

test('a listener added during emit waits for the next emit', () => {
  const bus = createBus();
  const got = [];
  bus.on('settings', () => {
    bus.on('settings', () => got.push('late'));
    got.push('first');
  });
  bus.emit('settings', {});
  assert.deepEqual(got, ['first']);
});
