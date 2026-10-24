import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

import { create } from './security.js';
import { fakeElectron } from '../../test/fakes/electron.js';
import { SAFE_WEB_PREFERENCES, PERMISSIONS } from '../shared/constants.js';
import { lintText, lintTree, stripComments } from '../../scripts/lint-security.mjs';

const quiet = { debug() {}, info() {}, warn() {}, error() {} };
const tick = () => new Promise((r) => setImmediate(r));

function setup(opts = {}) {
  const fake = fakeElectron(opts.fake);
  const sec = create({ electron: fake.electron, log: quiet, ...opts.deps });
  sec.applyGlobal();
  return { fake, sec, e: fake.electron };
}

/** A view whose webContents went through web-contents-created, as in Electron. */
function view(e, kind, sec, extra, opts) {
  const v = new e.WebContentsView({ webPreferences: sec.webPreferences(kind, extra) });
  e.app.emit('web-contents-created', {}, v.webContents);
  if (kind) sec.guardView(v.webContents, kind, opts);
  return v.webContents;
}

const TAB = { partition: 'persist:tab-t_9c1e5f0a7b2d4e68' };

test('applyGlobal enables the sandbox and guards every new webContents', () => {
  const { fake, e } = setup();
  assert.equal(fake.callsOf('app.enableSandbox').length, 1);
  const wc = new e.WebContentsView({}).webContents;
  e.app.emit('web-contents-created', {}, wc);
  assert.equal(wc._cancellable('will-attach-webview', {}, {}), true, '<webview> refused');
  assert.deepEqual(wc._windowOpen('https://claude.ai/'), { action: 'deny' }, 'default open handler denies');
  assert.equal(wc._cancellable('will-navigate', 'https://claude.ai/'), true, 'an unguarded webContents navigates nowhere');
  assert.equal(wc._cancellable('will-redirect', 'https://claude.ai/'), true);
});

test('webPreferences: the baseline for every kind, no overrides, preload only for the shell', () => {
  const { sec } = setup();
  const shell = sec.webPreferences('shell', { preload: '/app/src/preload/shell.cjs' });
  assert.deepEqual({ ...shell, preload: undefined }, { ...SAFE_WEB_PREFERENCES, preload: undefined });
  assert.equal(shell.preload, '/app/src/preload/shell.cjs');
  assert.deepEqual(sec.webPreferences('tab', TAB), { ...SAFE_WEB_PREFERENCES, ...TAB });
  assert.equal(sec.webPreferences('hub').partition, 'persist:sy-hub');
  assert.throws(() => sec.webPreferences('tab', { ...TAB, preload: '/x.cjs' }), /only the shell/);
  assert.throws(() => sec.webPreferences('hub', { preload: '/x.cjs' }), /only the shell/);
  assert.throws(() => sec.webPreferences('tab', { ...TAB, sandbox: false }), /fixed/);
  assert.throws(() => sec.webPreferences('tab', { ...TAB, nodeIntegration: true }), /fixed/);
  assert.throws(() => sec.webPreferences('tab', { ...TAB, experimentalFeatures: true }), /not allowed/);
  assert.throws(() => sec.webPreferences('tab', { partition: 'persist:sy-hub' }), /persist:tab-/);
  assert.throws(() => sec.webPreferences('tab', {}), /persist:tab-/);
  assert.throws(() => sec.webPreferences('hub', { partition: 'persist:tab-t_9c1e5f0a7b2d4e68' }), /sy-hub/);
  assert.throws(() => sec.webPreferences('shell', { partition: 'persist:x' }), /default session/);
  assert.throws(() => sec.webPreferences('other'), /unknown view kind/);
});

test('shell: every navigation and window.open is refused', () => {
  const { sec, e, fake } = setup();
  const wc = view(e, 'shell', sec, { preload: '/p.cjs' });
  for (const u of ['app://shell/other.html', 'https://claude.ai/', 'https://example.com/', 'file:///etc/passwd']) {
    assert.equal(wc._cancellable('will-navigate', u), true, u);
    assert.equal(wc._cancellable('will-redirect', u), true, u);
    assert.deepEqual(wc._windowOpen(u), { action: 'deny' }, u);
  }
  assert.equal(fake.callsOf('shell.openExternal').length, 0);
});

test('account tab: allowed hosts navigate, others are prevented and https goes to the system browser', async () => {
  const { sec, e, fake } = setup();
  const wc = view(e, 'tab', sec, TAB);
  for (const u of ['https://claude.ai/new', 'https://www.claude.ai/x', 'https://claude.com/login', 'https://accounts.anthropic.com/', 'https://challenges.cloudflare.com/x']) {
    assert.equal(wc._cancellable('will-navigate', u), false, u);
    assert.equal(wc._cancellable('will-redirect', u), false, u);
  }
  assert.equal(wc._cancellable('will-navigate', 'https://example.com/'), true);
  await tick();
  assert.deepEqual(fake.callsOf('shell.openExternal').map((c) => c[0]), ['https://example.com/'], 'opened once, outside');
  for (const u of ['http://claude.ai/', 'javascript:alert(1)', 'file:///etc/passwd', 'app://shell/index.html']) {
    assert.equal(wc._cancellable('will-navigate', u), true, u);
  }
  await tick();
  assert.equal(fake.callsOf('shell.openExternal').length, 1, 'nothing else went out');
  // A subframe redirect is not the tab's navigation.
  assert.equal(wc._cancellable('will-redirect', 'https://js.stripe.com/x', false, false), false);
});

/** A will-frame-navigate as Electron emits it: one details object; true when it was prevented. */
function frameNav(wc, url, isMainFrame = false) {
  let prevented = false;
  wc.emit('will-frame-navigate', { url, isMainFrame, preventDefault: () => (prevented = true) });
  return prevented;
}

function recordingLog() {
  const lines = [];
  return { lines, log: { debug: (m) => lines.push(m), info() {}, warn() {}, error() {} } };
}

test('account tab: subframes load only allow-listed https hosts, the rest is blocked and logged by host', async () => {
  const { lines, log } = recordingLog();
  const { sec, e, fake } = setup({ deps: { log } });
  const wc = view(e, 'tab', sec, TAB);
  const allowed = [
    'https://claude.ai/x', 'https://www.claude.ai/', 'https://a.claude.com/', 'https://console.anthropic.com/',
    'https://challenges.cloudflare.com/cdn-cgi/challenge-platform/h/b/turnstile/if/ov2/', 'https://abc.claudeusercontent.com/',
    'https://js.stripe.com/v3/elements-inner-card.html', 'https://hooks.stripe.com/3d_secure_2/hosted', 'https://m.stripe.network/inner.html',
    'https://newassets.hcaptcha.com/captcha/v1/abc/static/hcaptcha.html#frame=checkbox-invisible',
    'https://accounts.google.com/gsi/iframe/select', 'about:blank', 'about:srcdoc', 'about:blank#x', 'blob:https://claude.ai/0c3e2d6a-1111-2222-3333-444455556666',
  ];
  for (const u of allowed) {
    assert.equal(frameNav(wc, u), false, u);
    assert.equal(wc._cancellable('will-redirect', u, false, false), false, `redirect ${u}`);
  }
  const blocked = [
    'https://evil.example/frame', 'http://claude.ai/', 'https://claude.ai.evil.com/', 'https://evilclaude.ai/', 'https://stripe.com/',
    'https://checkout.stripe.com/x', 'https://www.google.com/', 'https://user:pw@claude.ai/', 'data:text/html,<p>x</p>',
    'javascript:alert(1)', 'file:///etc/passwd', 'blob:https://evil.example/0c3e2d6a', 'app://shell/index.html', 'not a url',
  ];
  for (const u of blocked) {
    assert.equal(frameNav(wc, u), true, u);
    assert.equal(wc._cancellable('will-redirect', u, false, false), true, `redirect ${u}`);
  }
  assert.ok(lines.some((l) => l.includes('subframe to evil.example blocked in a tab view')), lines.join('\n'));
  assert.ok(lines.every((l) => !l.includes('/frame')), 'only the host is logged, never the path');
  // The main frame is will-navigate's: a main-frame will-frame-navigate is left alone.
  assert.equal(frameNav(wc, 'https://evil.example/', true), false);
  await tick();
  assert.equal(fake.callsOf('shell.openExternal').length, 0, 'a blocked subframe never opens anything outside');
});

test('account tab: the test claude origin may be framed only when unpackaged', () => {
  const origin = 'http://127.0.0.1:5555';
  const dev = setup({ deps: { claudeOrigin: origin } });
  assert.equal(frameNav(view(dev.e, 'tab', dev.sec, TAB), `${origin}/frame`), false);
  const pkg = setup({ fake: { isPackaged: true }, deps: { claudeOrigin: origin } });
  assert.equal(frameNav(view(pkg.e, 'tab', pkg.sec, TAB), `${origin}/frame`), true);
});

test('hub view and shell: subframes only from the hub origin; the shell frames nothing', () => {
  const { sec, e } = setup();
  let origin = 'http://127.0.0.1:8787';
  const hub = view(e, 'hub', sec, {}, { hubOrigin: () => origin });
  assert.equal(frameNav(hub, 'http://127.0.0.1:8787/x'), false);
  assert.equal(frameNav(hub, 'about:blank'), false);
  assert.equal(frameNav(hub, 'http://127.0.0.1:9999/x'), true);
  assert.equal(frameNav(hub, 'https://claude.ai/'), true);
  origin = 'https://hub.example';
  assert.equal(frameNav(hub, 'http://127.0.0.1:8787/x'), true, 'the origin is read per navigation');
  assert.equal(frameNav(hub, 'https://hub.example/a'), false);
  const shell = view(e, 'shell', sec, { preload: '/p.cjs' });
  assert.equal(frameNav(shell, 'about:blank'), true);
  assert.equal(frameNav(shell, 'https://claude.ai/'), true);
  const bare = new e.WebContentsView({}).webContents;
  e.app.emit('web-contents-created', {}, bare);
  assert.equal(frameNav(bare, 'https://claude.ai/'), true, 'an unguarded webContents frames nothing');
});

test('account tab: window.open loads allowed hosts in the tab, sends https/mailto out, drops the rest', async () => {
  const { sec, e, fake } = setup();
  const wc = view(e, 'tab', sec, TAB);
  assert.deepEqual(wc._windowOpen('https://claude.ai/settings'), { action: 'deny' });
  await tick();
  assert.equal(wc.getURL(), 'https://claude.ai/settings', 'loaded in the same tab');
  assert.deepEqual(wc._windowOpen('https://unknown.example/'), { action: 'deny' });
  assert.deepEqual(wc._windowOpen('mailto:a@b.c'), { action: 'deny' });
  assert.deepEqual(wc._windowOpen('javascript:alert(1)'), { action: 'deny' });
  assert.deepEqual(wc._windowOpen('file:///etc/passwd'), { action: 'deny' });
  await tick();
  assert.deepEqual(fake.callsOf('shell.openExternal').map((c) => c[0]), ['https://unknown.example/', 'mailto:a@b.c']);
  assert.equal(wc.getURL(), 'https://claude.ai/settings');
});

test('account tab: the test claude origin is honoured only when unpackaged', () => {
  const origin = 'http://127.0.0.1:5555';
  const dev = setup({ deps: { claudeOrigin: origin } });
  assert.equal(view(dev.e, 'tab', dev.sec, TAB)._cancellable('will-navigate', `${origin}/new`), false);
  const pkg = setup({ fake: { isPackaged: true }, deps: { claudeOrigin: origin } });
  assert.equal(pkg.sec.claudeOrigin, null);
  assert.equal(view(pkg.e, 'tab', pkg.sec, TAB)._cancellable('will-navigate', `${origin}/new`), true);
  const viaHooks = setup({ deps: { testHooks: { claudeOrigin: (env) => env.O }, env: { O: origin } } });
  assert.equal(viaHooks.sec.claudeOrigin, origin);
});

test('hub view: only the hub origin; session links hand off; other https goes out', async () => {
  const { sec, e, fake } = setup();
  const links = [];
  let origin = 'http://127.0.0.1:8787';
  const wc = view(e, 'hub', sec, {}, { hubOrigin: () => origin, onSessionLink: (u) => links.push(u) });
  assert.equal(wc._cancellable('will-navigate', 'http://127.0.0.1:8787/#/sessions'), false);
  assert.equal(wc._cancellable('will-navigate', 'http://127.0.0.1:9999/'), true);
  assert.equal(wc._cancellable('will-navigate', 'https://claude.ai/code/session_01ABC'), true);
  assert.deepEqual(links, ['https://claude.ai/code/session_01ABC']);
  assert.equal(wc._cancellable('will-navigate', 'https://docs.example/'), true);
  assert.deepEqual(wc._windowOpen('https://claude.ai/code/session_02XYZ'), { action: 'deny' });
  assert.deepEqual(links, ['https://claude.ai/code/session_01ABC', 'https://claude.ai/code/session_02XYZ']);
  assert.deepEqual(wc._windowOpen('mailto:a@b.c'), { action: 'deny' });
  assert.deepEqual(wc._windowOpen('javascript:1'), { action: 'deny' });
  await tick();
  assert.deepEqual(fake.callsOf('shell.openExternal').map((c) => c[0]), ['https://docs.example/']);
  origin = 'https://hub.example';
  assert.equal(wc._cancellable('will-navigate', 'http://127.0.0.1:8787/'), true, 'the origin is read per navigation');
  assert.equal(wc._cancellable('will-navigate', 'https://hub.example/#/'), false);
});

test('hub view without a handoff sends session links to the system browser', async () => {
  const { sec, e, fake } = setup();
  const wc = view(e, 'hub', sec, {}, { hubOrigin: 'http://127.0.0.1:8787' });
  wc._windowOpen('https://claude.ai/code/session_01ABC');
  await tick();
  assert.deepEqual(fake.callsOf('shell.openExternal').map((c) => c[0]), ['https://claude.ai/code/session_01ABC']);
});

test('a page cannot loop external opens or handoffs: a small burst, refilled over time, without activating', async () => {
  let t = 1_000;
  const { sec, e, fake } = setup({ deps: { now: () => t } });
  const links = [];
  const hub = view(e, 'hub', sec, {}, { hubOrigin: 'http://127.0.0.1:8787', onSessionLink: (u) => links.push(u) });
  const tab = view(e, 'tab', sec, TAB);
  for (let i = 0; i < 10; i++) {
    hub._windowOpen(`https://docs.example/${i}`);
    tab._windowOpen(`https://docs.example/t${i}`);
  }
  await tick();
  const calls = fake.callsOf('shell.openExternal');
  assert.equal(calls.length, 6, '3 for each page');
  assert.ok(calls.every((c) => c[1]?.activate === false), 'a page never activates the browser');
  for (let i = 0; i < 10; i++) hub._windowOpen('https://claude.ai/code/session_01ABC');
  assert.equal(links.length, 0, 'the hub page already used its budget');
  t += 10_001;
  hub._windowOpen('https://claude.ai/code/session_01ABC');
  hub._windowOpen('https://docs.example/again');
  await tick();
  assert.equal(links.length, 1, 'the budget refills');
  assert.equal(fake.callsOf('shell.openExternal').length, 7);
  // a person's own click is not limited
  for (let i = 0; i < 5; i++) await sec.openExternalSafe('https://example.com/');
  assert.equal(fake.callsOf('shell.openExternal').length, 12);
  assert.equal(fake.callsOf('shell.openExternal').at(-1)[1], undefined);
});

test('a webContents cannot be re-guarded as another kind', () => {
  const { sec, e } = setup();
  const wc = view(e, 'tab', sec, TAB);
  assert.throws(() => sec.guardView(wc, 'hub'), /already guarded/);
});

const ALL_PERMISSIONS = [
  'clipboard-read', 'clipboard-sanitized-write', 'display-capture', 'fullscreen', 'geolocation', 'idle-detection',
  'media', 'mediaKeySystem', 'midi', 'midiSysex', 'notifications', 'pointerLock', 'keyboardLock', 'openExternal',
  'speaker-selection', 'storage-access', 'top-level-storage-access', 'window-management', 'unknown', 'fileSystem',
  'hid', 'serial', 'usb', 'background-sync',
];

test('permissions: only the table is allowed, per session kind', async () => {
  const { sec, e } = setup();
  const sessions = {
    tab: e.session.fromPartition(TAB.partition),
    hub: e.session.fromPartition('persist:sy-hub'),
    shell: e.session.defaultSession,
  };
  for (const [kind, s] of Object.entries(sessions)) sec.guardSession(s, kind);
  const pageOf = { tab: 'https://claude.ai/new', hub: 'http://127.0.0.1:8787/', shell: 'app://shell/index.html' };
  for (const [kind, s] of Object.entries(sessions)) {
    for (const perm of ALL_PERMISSIONS) {
      const details = { requestingUrl: pageOf[kind], mediaTypes: ['audio'], mediaType: 'audio' };
      const want = PERMISSIONS[kind].includes(perm);
      assert.equal(await s.requestPermission(perm, details), want, `${kind} request ${perm}`);
      assert.equal(s.checkPermission(perm, pageOf[kind], details), want, `${kind} check ${perm}`);
    }
  }
  const tab = sessions.tab;
  const req = (perm, d) => tab.requestPermission(perm, { requestingUrl: 'https://claude.ai/new', ...d });
  assert.equal(await req('media', { mediaTypes: ['video'] }), false, 'video refused');
  assert.equal(await req('media', { mediaTypes: ['audio', 'video'] }), false);
  assert.equal(await req('media', { mediaTypes: [] }), false);
  assert.equal(await req('media', {}), false);
  assert.equal(tab.checkPermission('media', 'https://claude.ai', { requestingUrl: 'https://claude.ai/', mediaType: 'video' }), false);
  // Only claude.ai pages get the tab's permissions, not a sign-in hop or another frame.
  assert.equal(await tab.requestPermission('notifications', { requestingUrl: 'https://claude.com/x' }), false);
  assert.equal(await tab.requestPermission('notifications', { requestingUrl: 'https://js.stripe.com/x' }), false);
  assert.equal(await tab.requestPermission('notifications', {}), false);
});

test('devices and display media are always denied', () => {
  const { sec, e } = setup();
  for (const [kind, s] of [['tab', e.session.fromPartition(TAB.partition)], ['hub', e.session.fromPartition('persist:sy-hub')], ['shell', e.session.defaultSession]]) {
    sec.guardSession(s, kind);
    assert.equal(s.handlers.devicePermission({ deviceType: 'usb', origin: 'https://claude.ai' }), false);
    let args = null;
    s.handlers.displayMedia({}, (...a) => (args = a));
    assert.deepEqual(args, [], `${kind}: callback with nothing = denied`);
    assert.equal(s.handlers.certificateVerify, undefined, 'no certificate override');
  }
});

test('downloads: tabs keep the default dialog, the hub and the shell cancel', () => {
  const { sec, e } = setup();
  const kinds = [['tab', e.session.fromPartition(TAB.partition)], ['hub', e.session.fromPartition('persist:sy-hub')], ['shell', e.session.defaultSession]];
  for (const [kind, s] of kinds) {
    sec.guardSession(s, kind);
    let prevented = false;
    let cancelled = false;
    s.emit('will-download', { preventDefault: () => (prevented = true) }, { cancel: () => (cancelled = true) }, null);
    assert.equal(prevented, kind !== 'tab', kind);
    assert.equal(cancelled, kind !== 'tab', kind);
  }
  assert.throws(() => sec.guardSession(e.session.defaultSession, 'tab'), /already guarded/);
  assert.doesNotThrow(() => sec.guardSession(e.session.defaultSession, 'shell'));
});

test('no certificate-error handler is installed, and no client certificate is ever offered', () => {
  const { sec, e } = setup();
  sec.applyGlobal();
  assert.equal(e.app.listenerCount('certificate-error'), 0);
  assert.equal(e.app.listenerCount('select-client-certificate'), 1);
  let prevented = false;
  const args = [];
  e.app.emit('select-client-certificate', { preventDefault: () => (prevented = true) }, {}, 'https://x', [{ subjectName: 'me' }], (...a) => args.push(a));
  assert.equal(prevented, true);
  assert.deepEqual(args, [[]]);
});

test('openExternalSafe allows https and mailto only', async () => {
  const { sec, fake } = setup();
  assert.equal(await sec.openExternalSafe('https://example.com/a'), true);
  assert.equal(await sec.openExternalSafe('mailto:a@b.c'), true);
  for (const u of ['http://example.com', 'file:///etc/passwd', 'javascript:alert(1)', 'smb://x/y', 'ms-settings:privacy', '', null]) {
    assert.equal(await sec.openExternalSafe(u), false, String(u));
  }
  assert.equal(await sec.openExternalSafe('mailto:a@b.c?subject=x&attach=/etc/passwd'), true);
  assert.deepEqual(fake.callsOf('shell.openExternal').map((c) => c[0]), ['https://example.com/a', 'mailto:a@b.c', 'mailto:a@b.c?subject=x']);
});

// ---- scripts/lint-security.mjs

test('lint: each rule fires on a sample and respects its allowed files', () => {
  const bad = {
    'node-integration': 'new BrowserWindow({webPreferences: {nodeIntegration: true}})',
    'context-isolation': 'x = {contextIsolation: false}',
    sandbox: 'x = { sandbox:false }',
    'web-security': 'x = {webSecurity: false}',
    'insecure-content': 'x = {allowRunningInsecureContent: true}',
    'webview-tag': 'x = {webviewTag: true}',
    'remote-module': "x = {enableRemoteModule: false}; import r from '@electron/remote'",
    eval: 'const y = eval(code)',
    'new-function': 'const f = new Function("return 1")',
    'inner-html': 'el.innerHTML = data',
    'insert-adjacent-html': "el.insertAdjacentHTML('beforeend', x)",
    'document-write': 'document.write(x)',
    'execute-javascript': 'await wc.executeJavaScript(code)',
    'open-external': 'shell.openExternal(url)',
    'child-process': "import { execFile } from 'node:child_process'",
    'ipc-on': "ipcMain.on('x', fn)",
    'certificate-override': "app.on('certificate-error', ok)",
    'certificate-verify-proc': 'ses.setCertificateVerifyProc((r, cb) => cb(0))',
    'crash-reporter': 'crashReporter.start({})',
  };
  for (const [rule, code] of Object.entries(bad)) {
    const found = lintText('main/x.js', code).map((f) => f.rule);
    assert.ok(found.includes(rule), `${rule}: ${found}`);
  }
  assert.deepEqual(lintText('main/usage/reader.js', 'wc.executeJavaScriptInIsolatedWorld(1001, [{code}])'), []);
  assert.equal(lintText('main/tabs/index.js', 'wc.executeJavaScriptInIsolatedWorld(1001, [{code}])').length, 1);
  assert.deepEqual(lintText('main/security.js', 'electron.shell.openExternal(u)'), []);
  assert.deepEqual(lintText('main/wy/run.js', "import { execFile } from 'node:child_process'"), []);
  assert.deepEqual(lintText('main/tabs/index.js', 'await security.openExternalSafe(url)'), []);
  assert.deepEqual(lintText('main/hub/pin.js', 'session.setCertificateVerifyProc(fn)'), []);
  assert.equal(lintText('main/hub/index.js', 'session.setCertificateVerifyProc(fn)').length, 1);
  assert.equal(lintText('main/hub/pin.js', "app.on('certificate-error', ok)").length, 1, 'the pin file still cannot override errors');
});

test('lint: the safe forms and comments do not fire', () => {
  const ok = [
    'x = {nodeIntegration: false, contextIsolation: true, sandbox: true, webSecurity: true}',
    'x = {allowRunningInsecureContent: false, webviewTag: false}',
    '// el.innerHTML = x; eval(y)',
    '/* new Function("x") */ const a = 1;',
    "const s = 'https://claude.ai/x'; // a URL is not a comment",
    'if (el.innerHTML === x) {}',
    'el.textContent = data',
    'retrieval(x); medieval(y)',
    'const re = /\\/\\/ not a comment/; el.textContent = re;',
  ];
  for (const code of ok) assert.deepEqual(lintText('renderer/app.js', code), [], code);
  assert.equal(stripComments('a // b\nc /* d\ne */ f'), 'a \nc \n f');
});

test('lint: renderer HTML rules', () => {
  const page = (body) => `<!doctype html><html><head><script type="module" src="app.js"></script></head><body>${body}</body></html>`;
  assert.deepEqual(lintText('renderer/index.html', page('<main id="app"></main>')), []);
  const cases = {
    'inline-script': '<script>alert(1)</script>',
    'inline-style': '<style>body{}</style>',
    'style-attribute': '<div style="color:red"></div>',
    'event-attribute': '<img src="x.png" onerror="go()">',
    'javascript-url': '<a href="javascript:go()">x</a>',
  };
  for (const [rule, body] of Object.entries(cases)) {
    assert.ok(lintText('renderer/dev/index.html', page(body)).some((f) => f.rule === rule), rule);
  }
  assert.deepEqual(lintText('renderer/index.html', page('<!-- <script>x</script> -->')), []);
});

test('lint: the app source tree is clean', () => {
  const { files, findings } = lintTree(fileURLToPath(new URL('..', import.meta.url)));
  assert.ok(files > 10);
  assert.deepEqual(findings, []);
});
