// node --test webui/static/*.test.cjs
// The Add phone dialog shows the hub's own refusal (api.js error handling + components/pair-error.js).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const mod = (f, globals = {}) => {
  const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
  if (/^import /m.test(src)) throw new Error(`${f} has imports; test only pure modules`);
  const names = [...src.matchAll(/^export (?:async )?(?:function|const|let|class) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  const body = src.replace(/^export /gm, '');
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`,
    { Number, String, Array, Object, Set, Map, Error, JSON, URLSearchParams, encodeURIComponent, Promise, ...globals });
};

const HUB_MSG = 'a phone needs the hub\'s https address (service workers, install and Web Push need a secure context), '
  + 'but the address in use is http://127.0.0.1:8787. A loopback address can never be '
  + 'reached by a phone. Open the board at the hub\'s https address, or give the hub an https address: ...';

const fakeFetch = (status, body) => async () => ({
  ok: status < 400, status, headers: { get: () => null },
  json: async () => { if (body === undefined) throw new Error('not json'); return body; },
});

test('api.js: the hub\'s message and code reach the caller, not "HTTP 400"', async () => {
  const api = mod('api.js', { fetch: fakeFetch(400, { error: HUB_MSG, code: 'needs_https' }), navigator: {} });
  await assert.rejects(api.makePairCode({ kind: 'phone' }), (e) => {
    assert.equal(e.message, HUB_MSG);
    assert.equal(e.code, 'needs_https');
    assert.equal(e.status, 400);
    return true;
  });
});

test('api.js: a refusal without a JSON body still says the status', async () => {
  const api = mod('api.js', { fetch: fakeFetch(400, undefined), navigator: {} });
  await assert.rejects(api.makePairCode({ kind: 'phone' }), (e) => e.message === 'Hub answered 400');
});

test('pairErrorView: needs_https gets a heading and the hub\'s whole message', () => {
  const P = mod('components/pair-error.js');
  const v = P.pairErrorView({ message: HUB_MSG, code: 'needs_https' });
  assert.equal(v.code, 'needs_https');
  assert.match(v.title, /https address/);
  assert.equal(v.message, HUB_MSG);
});

test('pairErrorView: other errors keep their message under a plain heading', () => {
  const P = mod('components/pair-error.js');
  const v = P.pairErrorView({ message: 'kind must be one of machine, desktop, phone, browser', code: 'bad_request' });
  assert.equal(v.title, 'Could not make a code');
  assert.equal(v.message, 'kind must be one of machine, desktop, phone, browser');
  assert.equal(P.pairErrorView(null).message, 'The hub could not make a code.');
  assert.equal(P.pairErrorView({ message: 'Cannot reach the hub' }).code, '');
});
