// node --test webui/static/*.test.cjs
// Pure helpers of the app (route.js, platform.js, format.js): hash routes, platform guesses,
// notification state, pairing codes.

const test = require('node:test');
const assert = require('node:assert/strict');

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// The helpers are ES modules with no imports. Load them without Node's ESM loader (so this
// runs on Node 20 too, where a .js file without "type": "module" is CommonJS): drop the
// `export` keywords and return the exported names from a function wrapper.
const mod = async (f) => {
  const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
  if (/^import /m.test(src)) throw new Error(`${f} has imports; test only pure modules`);
  const names = [...src.matchAll(/^export (?:async )?(?:function|const|let|class) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  const body = src.replace(/^export /gm, '');
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { URL, URLSearchParams, atob, btoa, Uint8Array, Intl, Date, Number, String, Math, JSON, Set, Map, Array, Object, Promise, isNaN: Number.isNaN });
};

const UA = {
  pixel: 'Mozilla/5.0 (Linux; Android 14; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36',
  reduced: 'Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36',
  iphone: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1',
  iphoneOld: 'Mozilla/5.0 (iPhone; CPU iPhone OS 16_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.3 Mobile/15E148 Safari/604.1',
  ipad: 'Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
  mac: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15',
  linuxChrome: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36',
  firefoxAndroid: 'Mozilla/5.0 (Android 14; Mobile; rv:131.0) Gecko/131.0 Firefox/131.0',
};

test('parseRoute covers every screen', async () => {
  const { parseRoute } = await mod('route.js');
  const r = (h) => { const x = parseRoute(h); return [x.view, x.page, x.id || '']; };
  assert.deepEqual(r(''), ['now', 'now', '']);
  assert.deepEqual(r('#/'), ['now', 'now', '']);
  assert.deepEqual(r('#/sessions'), ['sessions', 'sessions', '']);
  assert.deepEqual(r('#/sessions/abc%20d?alert=al_1'), ['session', 'sessions', 'abc d']);
  assert.equal(parseRoute('#/sessions/abc?alert=al_1').query.get('alert'), 'al_1');
  assert.deepEqual(r('#/accounts?seat=s1'), ['accounts', 'accounts', '']);
  assert.deepEqual(r('#/machines'), ['machines', 'machines', '']);
  assert.deepEqual(r('#/more'), ['more', 'more', '']);
  assert.deepEqual(r('#/more/alerts'), ['alerts', 'alerts', '']);
  assert.deepEqual(r('#/more/settings'), ['settings', 'settings', '']);
  assert.deepEqual(r('#/more/devices'), ['devices', 'devices', '']);
  assert.deepEqual(r('#/pair?s=abc&c=482913'), ['pair', 'pair', '']);
  assert.equal(parseRoute('#/pair?s=abc&c=482913').query.get('c'), '482913');
  assert.deepEqual(r('#/owner?s=xyz'), ['owner', 'owner', '']);
  assert.deepEqual(r('#/alerts/al_9'), ['alertRedirect', 'alerts', 'al_9']);
  assert.deepEqual(r('#/login'), ['login', 'login', '']);
  assert.deepEqual(r('#/nonsense'), ['now', 'now', '']);
});

test('public views and tabs', async () => {
  const { PUBLIC_VIEWS, TAB_OF, hashOfLink } = await mod('route.js');
  assert.deepEqual([...PUBLIC_VIEWS].sort(), ['login', 'owner', 'pair']);
  for (const p of ['machines', 'alerts', 'settings', 'devices', 'more']) assert.equal(TAB_OF[p], 'more');
  assert.equal(hashOfLink('/#/sessions/1?alert=a'), '#/sessions/1?alert=a');
  assert.equal(hashOfLink('/'), '#/');
  assert.equal(hashOfLink(null), '#/');
});

test('deviceName guesses a readable name', async () => {
  const { deviceName } = await mod('platform.js');
  assert.equal(deviceName(UA.pixel), 'Pixel 9 · Chrome');
  assert.equal(deviceName(UA.reduced), 'Android · Chrome');
  assert.equal(deviceName(UA.iphone), 'iPhone · Safari');
  assert.equal(deviceName(UA.ipad), 'iPad · Safari');
  assert.equal(deviceName(UA.mac), 'Mac · Safari');
  assert.equal(deviceName(UA.linuxChrome), 'Linux · Chrome');
  assert.equal(deviceName(UA.firefoxAndroid), 'Android · Firefox');
  assert.equal(deviceName(''), 'This device · Browser');
  assert.ok(deviceName('x'.repeat(200)).length <= 60);
});

test('platform checks', async () => {
  const p = await mod('platform.js');
  assert.equal(p.isIOS(UA.iphone), true);
  assert.equal(p.isIOS(UA.pixel), false);
  assert.equal(p.isIOS(UA.mac, 5, 'MacIntel'), true); // iPadOS posing as a Mac
  assert.equal(p.isIOS(UA.mac, 0, 'MacIntel'), false);
  assert.equal(p.isMobile(UA.pixel), true);
  assert.equal(p.isMobile(UA.linuxChrome), false);
  assert.equal(JSON.stringify(p.iosVersion(UA.iphone)), '[17,4]');
  assert.equal(p.iosVersion(UA.pixel), null);
  assert.equal(p.iosNeedsUpgrade(UA.iphoneOld), true);
  assert.equal(p.iosNeedsUpgrade(UA.iphone), false);
});

test('notificationState follows pwa.md 6', async () => {
  const { notificationState } = await mod('platform.js');
  const base = { ua: UA.pixel, standalone: false, hasPush: true, hasNotification: true, permission: 'default', subscribed: false };
  assert.equal(notificationState(base), 'off');
  assert.equal(notificationState({ ...base, permission: 'denied' }), 'blocked');
  assert.equal(notificationState({ ...base, permission: 'granted', subscribed: true }), 'on');
  assert.equal(notificationState({ ...base, permission: 'granted', subscribed: false }), 'off');
  assert.equal(notificationState({ ...base, hasPush: false }), 'unsupported');
  assert.equal(notificationState({ ...base, ua: UA.iphone, standalone: false, hasPush: false, hasNotification: false }), 'install-first');
  assert.equal(notificationState({ ...base, ua: UA.iphone, standalone: true }), 'off');
  assert.equal(notificationState({ ...base, ua: UA.iphoneOld, standalone: true }), 'ios-old');
});

test('pairing code helpers', async () => {
  const p = await mod('platform.js');
  assert.equal(p.normalizeCode('482913'), '482-913');
  assert.equal(p.normalizeCode('482 913'), '482-913');
  assert.equal(p.normalizeCode('48-2'), '482');
  assert.equal(p.normalizeCode('4829139999'), '482-913');
  assert.equal(p.codeDigits('482-913'), '482913');
  assert.equal(p.validCode('482-913'), true);
  assert.equal(p.validCode('482-91'), false);
});

test('VAPID key helpers round-trip', async () => {
  const p = await mod('platform.js');
  const bytes = Uint8Array.from({ length: 65 }, (_, i) => (i * 7 + 3) % 256);
  const b64 = p.bytesToUrlBase64(bytes.buffer);
  assert.ok(!/[+/=]/.test(b64));
  assert.deepEqual([...p.urlBase64ToBytes(b64)], [...bytes]);
  assert.equal(p.sameKey(bytes.buffer, b64), true);
  assert.equal(p.sameKey(null, b64), false);
  assert.equal(p.sameKey(bytes.buffer, 'AAAA'), false);
});

test('ntfyLink', async () => {
  const { ntfyLink } = await mod('platform.js');
  assert.equal(ntfyLink('https://ntfy.sh', 'sy-abc'), 'ntfy://ntfy.sh/sy-abc');
  assert.equal(ntfyLink('not a url', 'x'), '');
});

test('format: untilShort style countdown helpers stay stable', async () => {
  const f = await mod('format.js');
  assert.equal(f.ago('2026-10-12T14:00:00Z', Date.parse('2026-10-12T14:04:00Z')), '4 min');
  assert.equal(f.pct(61.6), '62%');
});

test('platform: expo target label', async () => {
  const p = await mod('platform.js');
  assert.equal(p.expoTargetLabel({ kind: 'expo', platform: 'ios', app_version: '0.8.0' }), 'App · iPhone · 0.8.0');
  assert.equal(p.expoTargetLabel({ kind: 'expo', platform: 'android', app_version: null }), 'App · Android');
  assert.equal(p.expoTargetLabel({ kind: 'expo' }), 'App');
});

test('platform: app pair link encodes origin, secret and code', async () => {
  const p = await mod('platform.js');
  assert.equal(p.appPairLink('https://hub.example.ts.net', 'sec/ret+1', '482-913'),
    'switchyard://pair?u=https%3A%2F%2Fhub.example.ts.net&s=sec%2Fret%2B1&c=482913');
  assert.equal(p.appPairLink('http://100.64.0.7:7777', 'abc', ''), 'switchyard://pair?u=http%3A%2F%2F100.64.0.7%3A7777&s=abc');
  assert.equal(p.appPairLink('https://h', '', '482913'), '');
});

test('platform: expo channel payload keeps the token write-only', async () => {
  const p = await mod('platform.js');
  const j = (o) => JSON.parse(JSON.stringify(o));
  assert.deepEqual(j(p.expoChannelBody({ enabled: true, baseUrl: ' https://exp.host/ ', token: '' })),
    { enabled: true, settings: { base_url: 'https://exp.host' } });
  assert.deepEqual(j(p.expoChannelBody({ enabled: true, baseUrl: 'https://exp.host', token: ' tok ' })),
    { enabled: true, settings: { base_url: 'https://exp.host', access_token: 'tok' } });
  assert.deepEqual(j(p.expoChannelBody({ enabled: false, baseUrl: '', token: 'x', clear: true })),
    { enabled: false, settings: { access_token: '' } });
});
