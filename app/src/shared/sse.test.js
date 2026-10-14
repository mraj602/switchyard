import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createParser } from './sse.js';

function run(chunks, opts) {
  const events = [];
  const comments = [];
  const retries = [];
  const p = createParser({ onEvent: (e) => events.push(e), onComment: (c) => comments.push(c), onRetry: (r) => retries.push(r), ...opts });
  for (const c of chunks) p.feed(c);
  return { events, comments, retries, p };
}

// Each corpus entry: the stream text and what a conforming parser yields.
const CORPUS = [
  { name: 'one event', text: 'id: 1\nevent: session\ndata: {"a":1}\n\n', events: [{ event: 'session', data: '{"a":1}', id: '1' }] },
  { name: 'default event type is message', text: 'data: hi\n\n', events: [{ event: 'message', data: 'hi', id: '' }] },
  { name: 'CRLF line ends', text: 'id: 2\r\nevent: x\r\ndata: y\r\n\r\n', events: [{ event: 'x', data: 'y', id: '2' }] },
  { name: 'bare CR line ends', text: 'event: x\rdata: y\r\r', events: [{ event: 'x', data: 'y', id: '' }] },
  { name: 'multi-line data', text: 'data: a\ndata: b\ndata\ndata: c\n\n', events: [{ event: 'message', data: 'a\nb\n\nc', id: '' }] },
  { name: 'no space after colon', text: 'data:x\n\n', events: [{ event: 'message', data: 'x', id: '' }] },
  { name: 'only one leading space is dropped', text: 'data:  x\n\n', events: [{ event: 'message', data: ' x', id: '' }] },
  { name: 'comments are not events', text: ': hi\n: keepalive\n\n', events: [] },
  { name: 'an id without data dispatches nothing but is remembered', text: 'id: 9\n\nevent: ping\ndata: {}\n\n', events: [{ event: 'ping', data: '{}', id: '9' }] },
  { name: 'event without data is dropped and the type does not leak', text: 'event: a\n\ndata: b\n\n', events: [{ event: 'message', data: 'b', id: '' }] },
  { name: 'unknown fields are ignored', text: 'foo: bar\ndata: z\n\n', events: [{ event: 'message', data: 'z', id: '' }] },
  { name: 'an id with NUL is ignored', text: 'id: a\0b\ndata: z\n\n', events: [{ event: 'message', data: 'z', id: '' }] },
  { name: 'BOM at start', text: '﻿data: z\n\n', events: [{ event: 'message', data: 'z', id: '' }] },
  { name: 'last id sticks across events', text: 'id: 5\ndata: a\n\ndata: b\n\n', events: [{ event: 'message', data: 'a', id: '5' }, { event: 'message', data: 'b', id: '5' }] },
  { name: 'unterminated event is not dispatched', text: 'data: a\n', events: [] },
];

test('corpus: whole text, every split into two chunks, and byte by byte', () => {
  for (const c of CORPUS) {
    assert.deepEqual(run([c.text]).events, c.events, `${c.name}: whole`);
    for (let i = 1; i < c.text.length; i++) {
      assert.deepEqual(run([c.text.slice(0, i), c.text.slice(i)]).events, c.events, `${c.name}: split at ${i}`);
    }
    assert.deepEqual(run([...c.text]).events, c.events, `${c.name}: char by char`);
  }
});

test('a CR at the end of a chunk and an LF at the start of the next are one line end', () => {
  const r = run(['data: a\r', '\ndata: b\r\n\r', '\n']);
  assert.deepEqual(r.events, [{ event: 'message', data: 'a\nb', id: '' }]);
});

test('retry: digits only', () => {
  const r = run(['retry: 1500\n\nretry: 12x\nretry: -3\nretry:\n\n']);
  assert.deepEqual(r.retries, [1500]);
});

test('comments reach onComment', () => {
  assert.deepEqual(run([': a\n:b\n']).comments, ['a', 'b']);
});

test('lastEventId includes ids of events without data', () => {
  const r = run(['id: 42\n\n']);
  assert.equal(r.p.lastEventId, '42');
  assert.deepEqual(r.events, []);
});

test('reset forgets a half event and keeps the id', () => {
  const r = run(['id: 3\ndata: half']);
  r.p.reset();
  r.p.feed('data: whole\n\n');
  assert.deepEqual(r.events, [{ event: 'message', data: 'whole', id: '3' }]);
});

test('an over-long line is dropped without growing memory', () => {
  const r = run(['data: ' + 'x'.repeat(100), 'x'.repeat(100), '\ndata: ok\n\n'], { maxLine: 50 });
  assert.deepEqual(r.events, [{ event: 'message', data: 'ok', id: '' }]);
});

test('a realistic hub stream', () => {
  const text =
    'id: 1\nevent: hello\ndata: {"version":"0.6.0","schema":5}\n\n' +
    ': ping comment\n\n' +
    'id: 2\nevent: ping\ndata: {}\n\n' +
    'retry: 3000\nid: 3\nevent: session\ndata: {"session":{"id":"c1","needs_you":true},\ndata: "change":"state"}\n\n';
  const r = run([text.slice(0, 77), text.slice(77, 140), text.slice(140)]);
  assert.deepEqual(r.events.map((e) => e.event), ['hello', 'ping', 'session']);
  assert.equal(r.events[2].data, '{"session":{"id":"c1","needs_you":true},\n"change":"state"}');
  assert.deepEqual(r.retries, [3000]);
});
