// Headless browser smoke test for the web app. Run by TestBrowserSmoke (webui/smoke),
// gated by SY_WEBUI_BROWSER=1. Needs puppeteer-core (install it in a temp dir and point
// NODE_PATH at its node_modules) and a Chrome binary (CHROME, default /usr/bin/google-chrome).
//
// Env: BASE_URL (mock hub), TOKEN (board login token), PAIR_SECRET, PAIR_CODE, OWNER_SECRET,
//      OUT_DIR (screenshots).
//
// Per size (1440 and 390 px wide):
//   board login, every screen, live updates, keyboard      (the phase 2 checks)
//   a phone claim by QR secret, the phone screens, Seen from the row menu, swipe Snooze
//   an owner sign-in, Alerts/Settings/Devices and the Add phone dialog
// Once (phone viewport, Android user agent unless noted): manifest and installability,
// service worker control and caches, offline banner, update banner, push display, push
// registration (subscribe stubbed), iPhone pairing path (no claim, digits shown).
// Any console error, failed request or unexpected HTTP error fails the run.

const puppeteer = require('puppeteer-core');
const path = require('path');
const fs = require('fs');

const BASE = process.env.BASE_URL;
const TOKEN = process.env.TOKEN;
const SECRET = process.env.PAIR_SECRET;
const CODE = process.env.PAIR_CODE;
const OWNER = process.env.OWNER_SECRET;
const OUT = process.env.OUT_DIR;
const CHROME = process.env.CHROME || '/usr/bin/google-chrome';
const SIZES = [['desk', 1440, 900], ['phone', 390, 844]];
const ANDROID = 'Mozilla/5.0 (Linux; Android 14; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36';
const IPHONE = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1';
const ASKED = 'c41e0000-0000-4000-8000-000000000003';
const PERM = 'c41e0000-0000-4000-8000-000000000004';

const BOARD_PAGES = [
  ['now', '#/', '.needs-card'],
  ['sessions', '#/sessions', '.sessions-table'],
  ['sessions-needs', '#/sessions?filter=needs_you', '.sessions-table'],
  ['sessions-machine', '#/sessions?by=machine', '.sessions-table'],
  ['session', '#/sessions/' + ASKED, '.timeline'],
  ['accounts', '#/accounts', '.seat-card-full'],
  ['machines', '#/machines', '.machine'],
];

const problems = [];
const fail = (m) => { problems.push(m); console.error('FAIL', m); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let browser;

async function newPage(ctx, sizeName, w, hgt, opts = {}) {
  const page = await ctx.newPage();
  const phone = w < 600;
  await page.setViewport({ width: w, height: hgt, deviceScaleFactor: 1, isMobile: phone && opts.mobile !== false, hasTouch: phone });
  if (opts.ua) await page.setUserAgent(opts.ua);
  const tag = (m) => `[${sizeName}${opts.label ? ' ' + opts.label : ''}] ${m}`;
  page.tag = tag;
  page.on('console', (msg) => {
    const t = msg.text() + (msg.location().url || '');
    if (msg.type() === 'error' && !/fonts\.g|ERR_|401|409|Failed to load resource: the server responded/.test(t)) fail(tag(`console: ${msg.text()}`));
  });
  page.on('pageerror', (e) => fail(tag(`pageerror: ${e.message}`)));
  await page.setRequestInterception(true);
  page.on('request', (req) => (/fonts\.(googleapis|gstatic)\.com/.test(req.url()) ? req.abort() : req.continue()));
  page.on('requestfailed', (req) => {
    if (page.expectOffline) return;
    if (!/fonts\./.test(req.url()) && !/\/v1\/stream|session\/login/.test(req.url()) && !(req.failure() && /ABORTED/.test(req.failure().errorText))) {
      fail(tag(`request failed: ${req.method()} ${req.url()} ${req.failure() && req.failure().errorText}`));
    }
  });
  page.on('response', (res) => {
    if (res.status() >= 400 && !/\/v1\/(overview|me)$/.test(res.url()) && !/session\/login|pair\/claim|session\/owner|devices\/self|\/v1\/devices$/.test(res.url())) {
      fail(tag(`HTTP ${res.status()} ${res.url()}`));
    }
  });
  return page;
}

async function shot(page, name) {
  const vp = page.viewport();
  if (vp.width >= 600) { await page.screenshot({ path: path.join(OUT, `${name}.png`), fullPage: true }); return; }
  // Phone: grow the viewport to the page height so the fixed tab bar sits at the bottom
  // instead of floating mid-page in a full-page capture.
  const h = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewport({ ...vp, height: Math.min(Math.max(h, vp.height), 2600) });
  await sleep(120);
  await page.screenshot({ path: path.join(OUT, `${name}.png`) });
  await page.setViewport(vp);
}

async function visit(page, sizeName, name, hash, sel) {
  await page.evaluate((h) => { location.hash = h; }, hash);
  try { await page.waitForSelector(sel, { timeout: 6000 }); } catch (e) { fail(page.tag(`${name}: ${sel} never appeared`)); }
  await sleep(350);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 1) fail(page.tag(`${name}: page scrolls sideways by ${overflow}px`));
  await shot(page, `${sizeName}-${name}`);
}

async function checkSidebar(page, sizeName) {
  // The sidebar must run the full height of the page (known leftover from phase 2).
  const r = await page.evaluate(() => {
    const nav = document.getElementById('nav').getBoundingClientRect();
    return { nav: Math.round(nav.height), page: document.documentElement.scrollHeight };
  });
  if (r.nav < r.page - 1) fail(page.tag(`sidebar is ${r.nav}px tall, page is ${r.page}px`));
}

async function resetHub() { await fetch(BASE + '/_dev/reset', { method: 'POST' }); }

// ---- the board login flow (phase 2 checks) -------------------------------------------------

async function boardFlow(sizeName, w, hgt) {
  const ctx = await browser.createBrowserContext();
  const page = await newPage(ctx, sizeName, w, hgt, { ua: undefined, label: 'board', mobile: false });
  await page.goto(BASE + '/#/login', { waitUntil: 'networkidle2' });
  await page.waitForSelector('form.login-card');
  await shot(page, `${sizeName}-login`);
  await page.type('#token', 'sy_b_wrong');
  await page.click('button[type=submit]');
  await page.waitForSelector('.form-error:not([hidden])');
  await page.evaluate(() => { document.getElementById('token').value = ''; });
  await page.type('#token', TOKEN);
  await Promise.all([page.waitForSelector('#nav a[aria-current=page]'), page.click('button[type=submit]')]);
  for (const [name, hash, sel] of BOARD_PAGES) await visit(page, sizeName, name, hash, sel);
  for (const [name, hash, sel] of [['more', '#/more', '.more-list'], ['alerts-readonly', '#/more/alerts', '.log'], ['settings-readonly', '#/more/settings', '[data-key=build]'], ['devices-readonly', '#/more/devices', '.calm']]) {
    await visit(page, sizeName, name, hash, sel);
  }
  // A plain board login has no Rename / Remove / Pull now, but sees reach, version and pull state.
  await visit(page, sizeName, 'machines-readonly', '#/machines', '.machine');
  if (await page.$('.machine-actions')) fail(page.tag('read-only session shows the machine actions'));
  if (!(await page.$('[data-key="reach-m_gpu"]'))) fail(page.tag('machine card has no reach chip'));
  // A plain board login cannot Seen/Snooze: no controls.
  await page.evaluate(() => { location.hash = '#/'; });
  await page.waitForSelector('.needs-card');
  if (await page.$('.alert-actions')) fail(page.tag('read-only session shows Seen/Snooze'));
  if (sizeName === 'desk') await checkSidebar(page, sizeName);

  // Live update: promote a working session on the mock; the needs-you count must grow.
  const before = await page.$$eval('.needs-card', (e) => e.length);
  await page.evaluate(() => fetch('/_dev/promote', { method: 'POST' }));
  try {
    await page.waitForFunction((n) => document.querySelectorAll('.needs-card').length > n, { timeout: 5000 }, before);
  } catch (e) { fail(page.tag('live update did not reach the page via /v1/stream')); }
  await shot(page, `${sizeName}-now-live`);
  await page.keyboard.press('Tab');
  const focused = await page.evaluate(() => document.activeElement && document.activeElement.tagName);
  if (!focused || focused === 'BODY') fail(page.tag('Tab did not move focus'));
  await ctx.close();
  await resetHub();
}

// ---- a paired phone ---------------------------------------------------------------------------

async function phoneFlow(sizeName, w, hgt) {
  const ctx = await browser.createBrowserContext();
  const page = await newPage(ctx, sizeName, w, hgt, { ua: ANDROID, label: 'phone' });
  await page.goto(`${BASE}/#/pair?s=${SECRET}&c=${CODE}`, { waitUntil: 'networkidle2' });
  try { await page.waitForSelector('[data-key=paired-as]', { timeout: 6000 }); } catch (e) { fail(page.tag('pairing: never showed "Paired as"')); }
  const addr = await page.evaluate(() => location.hash);
  if (/s=|c=/.test(addr)) fail(page.tag(`pairing left the secret in the address bar: ${addr}`));
  await shot(page, `${sizeName}-pair-done`);
  if (!(await page.cookies()).some((c) => c.name === 'sy_session')) fail(page.tag('claim did not set the sy_session cookie'));
  await page.click('[data-key=pair-continue]');
  await page.waitForSelector('.needs-card');
  const who = await page.evaluate(async () => (await (await fetch('/v1/me')).json()));
  if (who.owner || !who.device.scopes.includes('push')) fail(page.tag(`claimed device has wrong scopes: ${JSON.stringify(who.device.scopes)}`));

  await visit(page, sizeName, 'p-now', '#/', '.needs-card');
  await visit(page, sizeName, 'p-sessions', '#/sessions', '.sessions-table');
  await visit(page, sizeName, 'p-sessions-machine', '#/sessions?by=machine', '.sessions-table');
  await visit(page, sizeName, 'p-session', `#/sessions/${ASKED}?alert=al_pending`, '.session-actions');
  await visit(page, sizeName, 'p-accounts', '#/accounts?seat=seat_work', '.room');
  await visit(page, sizeName, 'p-more', '#/more', '.more-list');
  await visit(page, sizeName, 'p-alerts', '#/more/alerts', '.log');
  await visit(page, sizeName, 'p-settings', '#/more/settings', '[data-key=notif-state]');

  // Rules on a phone edit this device's mute list.
  await page.evaluate(() => { location.hash = '#/more/alerts'; });
  await page.waitForSelector('[data-key=rule-limits]');
  await page.click('[data-key=rule-limits]');
  await page.waitForFunction(async () => ((await (await fetch('/v1/devices/self')).json()).prefs.mute || []).includes('limit_80'), { timeout: 4000 }).catch(() => fail(page.tag('rule switch did not save to prefs.mute')));

  // Seen from the row menu.
  await page.evaluate(() => { location.hash = '#/'; });
  await page.waitForSelector('.needs-card');
  if (w < 600) await page.click(`[data-key="menu-${PERM}"]`);
  await page.click(`[data-key="seen-${PERM}"]`);
  try { await page.waitForSelector(`[data-session="${PERM}"].is-seen`, { timeout: 4000 }); } catch (e) { fail(page.tag('Seen did not dim the row')); }
  await shot(page, `${sizeName}-p-now-seen`);

  // Snooze by swiping left (touch) or the button.
  if (w < 600) {
    const box = await (await page.$(`[data-session="${ASKED}"]`)).boundingBox();
    const cdp = await page.createCDPSession();
    const y = box.y + box.height / 2;
    const send = (type, x) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints: type === 'touchEnd' ? [] : [{ x, y }] });
    await send('touchStart', box.x + box.width - 30);
    for (let i = 1; i <= 8; i++) { await send('touchMove', box.x + box.width - 30 - i * 16); await sleep(16); }
    await shot(page, `${sizeName}-p-now-swipe`);
    await send('touchEnd', 0);
  } else {
    await page.click(`[data-key="snooze-${ASKED}"]`);
  }
  try { await page.waitForFunction((id) => /snoozed/.test((document.querySelector(`[data-session="${id}"]`) || {}).textContent || ''), { timeout: 4000 }, ASKED); } catch (e) { fail(page.tag('Snooze did not show "snoozed until"')); }
  await sleep(300);
  await shot(page, `${sizeName}-p-now-snoozed`);

  // Unpair returns to the pair screen.
  await page.evaluate(() => { window.confirm = () => true; location.hash = '#/more/settings'; });
  await page.waitForSelector('[data-key=unpair]');
  await page.click('[data-key=unpair]');
  try { await page.waitForSelector('.pair-screen', { timeout: 5000 }); } catch (e) { fail(page.tag('Unpair did not open the pair screen')); }
  await shot(page, `${sizeName}-pair`);
  await ctx.close();
  await resetHub();
}

// ---- the owner -------------------------------------------------------------------------------

async function ownerFlow(sizeName, w, hgt) {
  const ctx = await browser.createBrowserContext();
  const page = await newPage(ctx, sizeName, w, hgt, { label: 'owner', mobile: false });
  await page.goto(`${BASE}/#/owner?s=${OWNER}`, { waitUntil: 'networkidle2' });
  try { await page.waitForSelector('[data-key=hub-address]', { timeout: 6000 }); } catch (e) { fail(page.tag('owner link did not land on Settings')); }
  if (/s=/.test(await page.evaluate(() => location.hash))) fail(page.tag('owner secret left in the address'));
  await visit(page, sizeName, 'o-settings', '#/more/settings', '[data-key=hub-address]');
  await visit(page, sizeName, 'o-alerts', '#/more/alerts', '[data-key=rule-asked]');
  await visit(page, sizeName, 'o-devices', '#/more/devices', '.dev-list');
  await machinesFlow(page, sizeName);
  // Owner switches edit the global rule.
  await page.click('[data-key=nav-alerts], [data-key=nav-more]').catch(() => {});
  await page.evaluate(() => { location.hash = '#/more/alerts'; });
  await page.waitForSelector('[data-key=delay-asked]');
  await page.select('[data-key=delay-asked]', '120');
  await page.waitForFunction(async () => (await (await fetch('/v1/rules')).json()).rules.find((r) => r.id === 'rule_asked').delay_s === 120, { timeout: 4000 }).catch(() => fail(page.tag('owner delay change did not save')));
  // Add phone dialog.
  await page.evaluate(() => { location.hash = '#/more/devices'; });
  await page.waitForSelector('[data-key=add-phone]');
  await page.click('[data-key=add-phone]');
  try { await page.waitForSelector('dialog[open] svg.qr rect', { timeout: 4000 }); } catch (e) { fail(page.tag('Add phone did not draw a QR code')); }
  await shot(page, `${sizeName}-o-add-phone`);
  // Another browser claims the code: the dialog flips to Paired.
  await fetch(BASE + '/v1/pair/claim', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ secret: SECRET, name: 'Test phone' }) });
  try { await page.waitForSelector('[data-key=pair-claimed]', { timeout: 6000 }); } catch (e) { fail(page.tag('dialog never showed Paired after the claim')); }
  await ctx.close();
  await resetHub();
}

// ---- the Machines screen as the owner -----------------------------------------------------------

// The board redraws on stream events; click through the DOM so a redraw between lookup and click cannot detach the target.
const press = (page, key) => page.evaluate((k) => document.querySelector(`[data-key="${k}"]`).click(), key);

async function machinesFlow(page, sizeName) {
  const text = (sel) => page.$eval(sel, (e) => e.textContent.trim()).catch(() => null);
  await visit(page, sizeName, 'o-machines', '#/machines', '[data-key="pull-m_vps"]');
  const want = async (sel, re, what) => { const t = await text(sel); if (!t || !re.test(t)) fail(page.tag(`machines: ${what}: got ${JSON.stringify(t)}`)); };
  await want('[data-key="reach-m_laptop"]', /^Direct$/, 'direct chip');
  await want('[data-key="reach-m_gpu"]', /^via laptop SSH$/, 'forward chip');
  await want('[data-key="reach-m_vps"]', /^pulled by hub every 1 min$/, 'pull chip');
  await want('#machine-m_gpu .kv', /1.240 queued, oldest \d\d:\d\d/, 'backlog with its age');
  await want('#machine-m_gpu .kv', /0\.4\.2 . older than hub 0\.5\.0/, 'older version');
  await want('#machine-m_oci .kv', /holding 37 records: hub too old/, 'held records');
  await want('[data-key="pull-state-m_vps"]', /^Pull: Failing: ssh: connect/, 'failing pull');
  await want('[data-key="alert-m_vps"]', /^Silent alert sent/, 'silent alert');
  await want('[data-key="status-m_vps"]', /^Quiet /, 'quiet status');
  // Silent machines sort first.
  const first = await page.$eval('.grid-machines .machine', (e) => e.dataset.machine);
  if (first !== 'm_vps') fail(page.tag(`machines: silent machine is not first (${first})`));

  // Pull now (accepted), Rename, Show removed.
  await press(page, 'pull-m_oci');
  try { await page.waitForFunction(() => /Pulling home-box now/.test(document.body.textContent), { timeout: 4000 }); } catch (e) { fail(page.tag('Pull now showed no confirmation')); }
  await page.evaluate(() => { window.prompt = () => 'gpu-renamed'; window.confirm = () => true; });
  await press(page, 'rename-m_gpu');
  try { await page.waitForFunction(() => /gpu-renamed/.test((document.querySelector('#machine-m_gpu h2') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(page.tag('Rename did not show the new name')); }
  await press(page, 'show-removed');
  try { await page.waitForSelector('.removed-machines .machine.is-removed', { timeout: 4000 }); } catch (e) { fail(page.tag('Show removed listed no removed machine')); }
  if (await page.$('.removed-machines .machine-actions')) fail(page.tag('a removed machine offers actions'));
  await visit(page, sizeName, 'o-machines-removed', '#/machines', '.removed-machines .machine');

  // Every pull state on one screen.
  await page.evaluate(() => fetch('/_dev/pull-states', { method: 'POST' }));
  try { await page.waitForSelector('[data-key="pull-state-m_pull-host_key"]', { timeout: 4000 }); } catch (e) { fail(page.tag('pull state fixtures did not arrive on the stream')); }
  for (const st of ['new', 'ok', 'failing', 'host_key', 'auth', 'refused', 'disabled', 'frozen']) {
    if (!(await page.$(`[data-key="pull-state-m_pull-${st}"]`))) fail(page.tag(`no card for pull state ${st}`));
  }
  if (!(await page.$eval('[data-key="pull-m_pull-host_key"]', (b) => b.disabled))) fail(page.tag('Pull now is enabled for a host_key target'));
  await visit(page, sizeName, 'o-machines-states', '#/machines', '[data-key="pull-state-m_pull-frozen"]');

  // Remove: the card leaves the list.
  await press(page, 'remove-m_oci');
  try { await page.waitForFunction(() => !document.querySelector('.page > .grid-machines #machine-m_oci'), { timeout: 4000 }); } catch (e) { fail(page.tag('Remove left the card on the list')); }

  // Add machine dialog: the two commands.
  await page.evaluate(() => { location.hash = '#/more/devices'; });
  await page.waitForSelector('[data-key=add-machine]');
  await page.click('[data-key=add-machine]');
  try { await page.waitForSelector('dialog[open] [data-key=cmd-machine-add]', { timeout: 4000 }); } catch (e) { fail(page.tag('Add machine did not show the sy machine add command')); }
  await want('[data-key=cmd-machine-add]', /^sy machine add user@host$/, 'machine add command');
  await want('[data-key=cmd-install]', /install\.sh \| sh -s -- -- --hub join:http.*--code 482-913/, 'install command');
  await shot(page, `${sizeName}-o-add-machine`);
  await page.click('[data-key=dialog-close]');
  await page.evaluate(() => { location.hash = '#/more/devices'; });
}

// ---- installability, worker, offline, update, push, iPhone ---------------------------------------

async function pwaFlow() {
  const ctx = await browser.createBrowserContext();
  await ctx.overridePermissions(BASE, ['notifications']);
  const page = await newPage(ctx, 'pwa', 390, 844, { ua: ANDROID, label: 'pwa' });
  // Subscribing for real needs Google's push service: stub it with a subscription whose
  // keys were generated here, and check the app posts it.
  await page.evaluateOnNewDocument(() => {
    const b64 = (a) => btoa(String.fromCharCode(...new Uint8Array(a))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const point = new Uint8Array(65); point[0] = 4; for (let i = 1; i < 65; i++) point[i] = (i * 13) % 251;
    const fake = { endpoint: 'https://fcm.googleapis.com/fcm/send/smoke-endpoint', options: { applicationServerKey: null },
      toJSON() { return { endpoint: this.endpoint, keys: { p256dh: b64(point), auth: b64(new Uint8Array(16).fill(7)) } }; }, unsubscribe: async () => true };
    let current = null;
    if (window.PushManager) {
      PushManager.prototype.subscribe = async function (o) { fake.options = { applicationServerKey: o.applicationServerKey.buffer || o.applicationServerKey }; current = fake; return fake; };
      PushManager.prototype.getSubscription = async function () { return current; };
    }
  });
  const posted = [];
  page.on('request', (r) => { if (r.method() === 'POST' && /\/v1\/push\/targets$/.test(r.url())) posted.push(JSON.parse(r.postData() || '{}')); });
  const cdp = await page.createCDPSession();
  const trace = [];
  page.on('response', (r) => { if (/\/v1\/(me|overview|pair)/.test(r.url())) trace.push(`${r.status()} ${r.url().replace(BASE, '')} sw=${r.fromServiceWorker()}`); });

  await page.goto(`${BASE}/#/pair?s=${SECRET}`, { waitUntil: 'networkidle2' });
  await page.waitForSelector('[data-key=paired-as]');
  await page.click('[data-key=pair-continue]');
  await page.waitForSelector('.needs-card');

  // manifest and installability
  await cdp.send('Page.enable');
  const m = await cdp.send('Page.getAppManifest');
  if (m.errors && m.errors.length) fail(`manifest errors: ${JSON.stringify(m.errors)}`);
  if (!m.data || !m.data.includes('"Switchyard"')) fail('manifest did not load');
  const inst = await cdp.send('Page.getInstallabilityErrors');
  const instErrs = (inst.installabilityErrors || []).filter((x) => x.errorId !== 'in-incognito'); // the test browser context is incognito
  if (instErrs.length) fail(`installability errors: ${JSON.stringify(instErrs)}`);

  // worker controls the page after one reload; the shell cache exists
  await page.evaluate(() => navigator.serviceWorker.ready);
  await page.reload({ waitUntil: 'networkidle2' });
  await page.waitForSelector('.needs-card', { timeout: 8000 }).catch(async () => { fail('after SW reload: ' + await page.evaluate(() => document.body.innerText.slice(0, 60) + ' | ' + location.href) + ' ' + trace.join('; ') + ' cookies=' + (await page.cookies()).map((c) => c.name) + ' st=' + await page.evaluate(() => JSON.stringify({ u: __sy.store.unauthorized, e: __sy.store.error, me: !!__sy.store.me })) + ' me=' + await page.evaluate(async () => { const r = await fetch('/v1/me'); return r.status + ' ' + (await r.text()).slice(0, 120); })); });
  const ctl = await page.evaluate(() => !!navigator.serviceWorker.controller);
  if (!ctl) fail('service worker does not control the page after a reload');
  const keys = await page.evaluate(() => caches.keys());
  if (!keys.some((k) => k.startsWith('sy-shell-'))) fail(`no shell cache: ${keys}`);
  if (!keys.includes('sy-data')) fail(`no data cache: ${keys}`);
  const build0 = await page.evaluate(() => new Promise((res) => { navigator.serviceWorker.addEventListener('message', (e) => e.data.type === 'build' && res(e.data.build)); navigator.serviceWorker.controller.postMessage({ type: 'build' }); }));

  // offline: the last known state with the banner, controls off
  page.expectOffline = true;
  // Page emulation does not reach the worker's own requests: cut the worker's network too.
  const swTarget = await browser.waitForTarget((t) => t.type() === 'service_worker' && t.url().startsWith(BASE), { timeout: 5000 });
  const swNet = await swTarget.createCDPSession();
  await swNet.send('Network.enable');
  const cond = (offline) => ({ offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  await swNet.send('Network.emulateNetworkConditions', cond(true));
  await page.setOfflineMode(true);
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.banner-offline', { timeout: 15000 }).catch(() => fail('no offline banner after an offline reload'));
  await page.waitForSelector('.needs-card', { timeout: 15000 }).catch(async () => fail('Now did not render from the stored copy while offline: ' + await page.evaluate(() => location.hash + ' ' + document.body.innerText.slice(0, 300) + JSON.stringify(window.__sy.store.error)).catch(String)));
  const bannerText = await page.$eval('.banner-offline', (e) => e.textContent).catch(() => '');
  if (!/Offline · showing \d\d:\d\d/.test(bannerText)) fail(`offline banner text: ${bannerText}`);
  await shot(page, 'phone-offline');
  await swNet.send('Network.emulateNetworkConditions', cond(false)).catch(() => {});
  for (const t of browser.targets().filter((x) => x.type() === 'service_worker')) { const c = await t.createCDPSession(); await c.send('Network.enable'); await c.send('Network.emulateNetworkConditions', cond(false)).catch(() => {}); }
  await page.setOfflineMode(false);
  page.expectOffline = false;
  try { await page.waitForFunction(() => !document.querySelector('.banner-offline'), { timeout: 30000 }); } catch (e) { fail('offline banner stayed after coming back online: ' + await page.evaluate(async () => { const r = await fetch('/v1/overview'); return r.status + ' ' + r.headers.get('x-sy-cached-at') + ' ' + JSON.stringify(window.__sy.store.live); }).catch(String)); }

  // notifications: turn on from Settings
  await page.evaluate(() => { location.hash = '#/more/settings'; });
  // The mock already lists a target for this device, so the app may be On straight away (and
  // re-subscribed at start); otherwise turn it on here.
  await page.waitForSelector('[data-key=turn-on], [data-key=test-push]', { timeout: 8000 });
  if (await page.$('[data-key=turn-on]')) await page.click('[data-key=turn-on]');
  try { await page.waitForFunction(() => /^On/.test((document.querySelector('[data-key=notif-state]') || {}).textContent || ''), { timeout: 6000 }); } catch (e) { fail('Turn on did not reach the On state'); }
  if (!posted.some((b) => b.kind === 'webpush' && /smoke-endpoint/.test(b.endpoint || '') && b.keys && b.keys.p256dh)) fail(`app did not post the subscription: ${JSON.stringify(posted)}`);
  await page.click('[data-key=test-push]');
  try { await page.waitForFunction(() => /Sent/.test((document.querySelector('[data-key=test-result]') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail('Send test push showed no result'); }
  await shot(page, 'phone-settings-on');

  // push display through the service worker
  const regs = new Map();
  await cdp.send('ServiceWorker.enable');
  cdp.on('ServiceWorker.workerRegistrationUpdated', (e) => e.registrations.forEach((r) => regs.set(r.registrationId, r)));
  await page.reload({ waitUntil: 'networkidle2' });
  await sleep(500);
  const reg = [...regs.values()].find((r) => r.scopeURL.startsWith(BASE));
  if (!reg) fail('no service worker registration seen over CDP');
  else {
    await page.evaluate(() => { window.__badges = []; });
    const payload = { v: 1, type: 'alert', alert: 'al_x', alerts: ['al_x'], trigger: 'asked', title: 'work · api · asked a question', body: 'Should I also update the migration?', tag: 'sy-s-smoke', url: '/#/sessions/' + ASKED, tap: 'session', session: ASKED, needs_you: 3, actions: { snooze: 'a1.dev.snooze', seen: 'a1.dev.seen' }, private: false, ts: new Date().toISOString() };
    await cdp.send('ServiceWorker.deliverPushMessage', { origin: BASE, registrationId: reg.registrationId, data: JSON.stringify(payload) });
    await sleep(800);
    const shown = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).getNotifications()).map((n) => ({ title: n.title, tag: n.tag, actions: (n.actions || []).map((a) => a.action) })));
    const n = shown.find((x) => x.tag === 'sy-s-smoke');
    if (!n) fail(`pushed notification not shown: ${JSON.stringify(shown)}`);
    else {
      if (n.title !== payload.title) fail(`notification title ${n.title}`);
      if (n.actions.length && n.actions.join() !== 'snooze,seen') fail(`notification actions ${n.actions}`);
    }
    // an unreadable payload still shows something
    await cdp.send('ServiceWorker.deliverPushMessage', { origin: BASE, registrationId: reg.registrationId, data: 'not json' });
    await sleep(500);
    const after = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).getNotifications()).length);
    if (after < 2) fail('a push with a bad payload showed no notification');
  }

  // update: the hub switches to another build
  await fetch(BASE + '/_dev/build', { method: 'POST', body: JSON.stringify({ hash: 'ffffffffffff' }), headers: { 'Content-Type': 'application/json' } });
  await page.evaluate(() => { location.hash = '#/'; });
  await page.evaluate(async () => { await (await navigator.serviceWorker.getRegistration()).update(); });
  try { await page.waitForSelector('.banner-update', { timeout: 8000 }); } catch (e) { fail('"New version ready" never appeared after the build changed'); }
  await shot(page, 'phone-update-banner');
  await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle2' }).catch(() => {}), page.click('[data-key=update-reload]')]);
  await page.waitForSelector('.needs-card');
  await sleep(500);
  const build1 = await page.evaluate(() => new Promise((res) => { navigator.serviceWorker.addEventListener('message', (e) => e.data.type === 'build' && res(e.data.build)); navigator.serviceWorker.controller.postMessage({ type: 'build' }); }));
  if (build1 !== 'ffffffffffff' || build1 === build0) fail(`reloaded page runs build ${build1}, expected ffffffffffff (was ${build0})`);
  await fetch(BASE + '/_dev/build', { method: 'POST', body: JSON.stringify({ hash: '' }), headers: { 'Content-Type': 'application/json' } });
  await ctx.close();

  // iPhone: Safari does not claim; it shows the steps and the digits.
  const ictx = await browser.createBrowserContext();
  const ipage = await newPage(ictx, 'pwa', 390, 844, { ua: IPHONE, label: 'iphone' });
  await ipage.goto(`${BASE}/#/pair?s=${SECRET}&c=${CODE}`, { waitUntil: 'networkidle2' });
  try { await ipage.waitForSelector('.steps', { timeout: 5000 }); } catch (e) { fail('iPhone pairing did not show the install steps'); }
  const digits = await ipage.$eval('[data-key=pair-digits]', (e) => e.textContent).catch(() => '');
  if (digits.trim() !== '482-913') fail(`iPhone pair digits: ${digits}`);
  if ((await ipage.cookies()).some((c) => c.name === 'sy_session')) fail('iPhone Safari path claimed the code');
  await shot(ipage, 'phone-pair-ios');
  // The installed app (standalone) has no secret: typed code.
  // The installed app opens standalone (iOS reports navigator.standalone) with no secret in the link.
  await ipage.evaluateOnNewDocument(() => { Object.defineProperty(navigator, 'standalone', { get: () => true }); });
  await ipage.goto('about:blank');
  await ipage.goto(`${BASE}/#/pair`, { waitUntil: 'networkidle2' });
  await ipage.waitForSelector('[data-key=pair-code]');
  await ipage.type('[data-key=pair-code]', '000000');
  await ipage.click('[data-key=pair-submit]');
  try { await ipage.waitForSelector('[data-key=pair-error]', { timeout: 4000 }); } catch (e) { fail('a wrong typed code showed no error'); }
  await ipage.$eval('[data-key=pair-code]', (e) => { e.value = ''; });
  await ipage.type('[data-key=pair-code]', CODE);
  await ipage.click('[data-key=pair-submit]');
  try { await ipage.waitForSelector('[data-key=paired-as]', { timeout: 5000 }); } catch (e) { fail('typed code did not pair'); }
  await ictx.close();
  await resetHub();
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  browser = await puppeteer.launch({ executablePath: CHROME, headless: true, args: ['--no-sandbox', '--disable-gpu'] });
  try {
    for (const [sizeName, w, hgt] of SIZES) {
      await boardFlow(sizeName, w, hgt);
      await phoneFlow(sizeName, w, hgt);
      await ownerFlow(sizeName, w, hgt);
    }
    await pwaFlow();
  } finally {
    await browser.close();
  }
  if (problems.length) { console.error(`${problems.length} problem(s)`); process.exit(1); }
  console.log('smoke ok; screenshots in', OUT);
})().catch((e) => { console.error(e); process.exit(1); });
