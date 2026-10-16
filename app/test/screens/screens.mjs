// Screenshot audit of the desktop app (not a test): every view and state, light and dark, at
// 1440x900 and 1024x700, against a sandbox hub filled by test/e2e/android/seed and the fake
// claude.ai (one extra run opens the real claude.ai sign-in page, never typing into it).
//
//   SY_BIN=<sy> xvfb-run -a -s "-screen 0 1440x900x24" dbus-run-session -- \
//     node test/screens/screens.mjs <out dir> [phase ...]
//
// Phases: main (pairing, board routes, tabs, Settings, owner, error states), notify (an alert
// on the sandbox hub, the libnotify call caught by test/screens/notifyd.py on the private bus),
// claude (the real claude.ai sign-in page, no hub: also the "sy missing" state). Default: all.
// Everything runs in the smoke harness's sandbox (temp HOME, user data and D-Bus address), so
// the real home, keyring and desktop are never touched. Needs `import` (ImageMagick) for the
// window grabs: the native views are composited only on the X screen.

import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { sandbox, syBinary, startHub, launch, main, inTab, tabUrl, shellState, waitFor, sleep, freshFixture, REPO_DIR } from '../smoke/harness.mjs';
import { startFakeClaude, USAGE_DIR } from '../fakes/claude/server.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(process.argv[2] || path.join(process.cwd(), 'screens-out'));
const PHASES = new Set(process.argv.slice(3).length ? process.argv.slice(3) : ['main', 'notify', 'claude']);
fs.mkdirSync(OUT, { recursive: true });
const BIG = { w: 1440, h: 900 };
const SMALL = { w: 1024, h: 700 };
const log = (...a) => console.log('[screens]', ...a);
const sid = (n) => `c41e5eed-0000-4000-8000-${String(n).padStart(12, '0')}`;
const notes = [];

async function grab(name, size) {
  const file = path.join(OUT, `${name}.png`);
  const r = spawnSync('import', ['-window', 'root', '-crop', `${size.w}x${size.h}+0+0`, '+repage', file]);
  if (r.status !== 0) log(`grab ${name} failed: ${r.stderr}`);
  else log(`shot ${name}`);
}

async function setSize(app, size) {
  await main(app, (m, _e, a) => {
    const w = m.window.win;
    w.setBounds({ x: 0, y: 0, width: a.w, height: a.h });
  }, size);
  await sleep(400);
}

// nativeTheme.themeSource alone does not reach the pages under xvfb (no portal to read the
// colour scheme from), so every open page also gets prefers-color-scheme emulated over CDP.
async function setTheme(app, t) {
  await main(app, (_m, e, theme) => { e.nativeTheme.themeSource = theme; }, t);
  for (const p of app.windows()) {
    try { await p.emulateMedia({ colorScheme: t }); } catch { /* a page closing under us */ }
  }
}

/** Shots of the current screen in each (size, theme) asked for. */
async function shots(run, base, { sizes = [BIG, SMALL], themes = ['light', 'dark'], before } = {}) {
  for (const theme of themes) {
    await setTheme(run.app, theme);
    for (const size of sizes) {
      await setSize(run.app, size);
      if (before) await before();
      await sleep(700);
      await grab(`${base}-${size.w}-${theme}`, size);
    }
  }
  await setTheme(run.app, 'light');
  await setSize(run.app, BIG);
}

function hubPage(run, hubUrl) {
  return run.app.windows().find((p) => p.url().startsWith(hubUrl));
}

async function route(run, hub, r) {
  await main(run.app, (m, _e, rt) => m.hub.route(rt), r);
  await sleep(300);
  const page = await waitFor(() => hubPage(run, hub.url), 20_000, 'the hub view page');
  await waitFor(() => page.evaluate(() => !!document.querySelector('#main')?.children.length), 15_000, `board ${r} drawn`).catch(() => {});
  await sleep(900);
  return page;
}

async function seedHub(sb, hub, bin, part) {
  const seedBin = path.join(sb.root, 'seed');
  if (!fs.existsSync(seedBin)) {
    const b = spawnSync(process.env.GO || 'go', ['build', '-o', seedBin, './test/e2e/android/seed'], { cwd: REPO_DIR, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' } });
    if (b.status !== 0) throw new Error('go build seed failed');
  }
  if (!sb.machines) {
    sb.machines = [];
    for (const [id, name] of [['m_01J9SEED000000000000000001', 'laptop'], ['m_01J9SEED000000000000000002', 'gpu-box']]) {
      const r = await hub.sy(['hub', 'token', 'create', '--kind', 'machine', '--name', name, '--machine', id, '--json']);
      if (r.code !== 0) throw new Error(`token create: ${r.stderr}`);
      sb.machines.push(`${id}=${JSON.parse(r.stdout).token}`);
    }
  }
  const args = ['-hub', hub.url, ...sb.machines.flatMap((m) => ['-m', m]), ...(part ? ['-only', part] : [])];
  const r = spawnSync(seedBin, args, { env: sb.env, encoding: 'utf8' });
  if (r.status !== 0) throw new Error(`seed: ${r.stderr}`);
}

const BOARD_ROUTES = [
  ['now', '#/'],
  ['sessions', '#/sessions'],
  ['sessions-all', '#/sessions?filter=all'],
  ['session-working', `#/sessions/${sid(1)}`],
  ['session-permission', `#/sessions/${sid(2)}`],
  ['session-question', `#/sessions/${sid(3)}`],
  ['session-askuser', `#/sessions/${sid(4)}`],
  ['session-failed', `#/sessions/${sid(5)}`],
  ['session-done', `#/sessions/${sid(6)}`],
  ['session-ended', `#/sessions/${sid(8)}`],
  ['session-longname', `#/sessions/${sid(9)}`],
  ['accounts', '#/accounts'],
  ['machines', '#/machines'],
  ['more', '#/more'],
  ['alerts', '#/more/alerts'],
  ['settings', '#/more/settings'],
  ['analytics', '#/more/analytics'],
  ['peers', '#/more/peers'],
  ['devices', '#/more/devices'],
  ['notfound', '#/nope'],
];

async function boardTour(run, hub, prefix, routes = BOARD_ROUTES, opts = {}) {
  for (const [name, r] of routes) {
    try {
      const page = await route(run, hub, r);
      await shots(run, `${prefix}-${name}`, opts);
      if (name === 'settings') {
        // the lower half of Settings (Install, Hub, Sign-in)
        await shots(run, `${prefix}-settings-lower`, { ...opts, themes: ['light'], before: () => page.evaluate(() => window.scrollTo(0, document.body.scrollHeight)) });
        const text = await page.evaluate(() => document.querySelector('#main')?.innerText ?? '');
        fs.writeFileSync(path.join(OUT, `${prefix}-settings.txt`), text);
      }
    } catch (err) {
      notes.push(`${prefix} ${name}: ${err.message}`);
      log(`route ${name} failed: ${err.message}`);
    }
  }
}

async function shellSection(run, id) {
  await run.shell.evaluate(() => window.sy.selectView({ view: 'settings' }));
  await sleep(300);
  await run.shell.evaluate((sec) => {
    const b = document.querySelector(`[data-fk="nav-${sec}"]`);
    if (b) b.click();
  }, id);
}

async function phaseMain() {
  const sb = sandbox('screens');
  const bin = syBinary(sb.root);
  const hub = await startHub(sb, bin);
  await seedHub(sb, hub, bin);
  const fixture = freshFixture(sb.root, { five: 42, week: 61 });
  const claude = await startFakeClaude({ fixtures: [path.relative(USAGE_DIR, fixture)] });
  const env = { SY_BIN: bin, SY_APP_TEST_CLAUDE_ORIGIN: claude.origin, SY_APP_TEST_CADENCE_MS: '4000' };
  let run = null;
  try {
    run = await launch(sb, { env });
    await sleep(1500);
    // ---- pairing
    await shots(run, 'pair-needs-pairing');
    await shellSection(run, 'hub');
    await shots(run, 'pair-settings-hub-unpaired', { themes: ['light'] });
    // code entry (no local hub, no admin channel): the state main would send for a remote hub
    await main(run.app, (m) => m.bus.emit('hub', { ...m.bus.last('hub'), local: false, admin: null }));
    await run.shell.evaluate(() => window.sy.selectView({ view: 'hub' }));
    await sleep(500);
    await shots(run, 'pair-code-entry', { themes: ['light', 'dark'] });
    await run.shell.evaluate(async () => {
      const i = document.querySelector('input[id^="pair-code-"]');
      if (i) { i.value = '000-000'; i.dispatchEvent(new Event('input')); }
      document.querySelector('[data-fk^="pair-btn-"]')?.click();
    });
    await waitFor(() => run.shell.evaluate(() => !!document.querySelector('.banner .err')), 30_000, 'the pair error').catch((e) => notes.push(`pair error: ${e.message}`));
    await shots(run, 'pair-code-error', { themes: ['light'] });
    const r = await run.shell.evaluate(() => window.sy.pair({}));
    if (!r.ok) throw new Error(`pair: ${JSON.stringify(r)}`);
    await waitFor(async () => (await shellState(run.shell)).hub.status === 'ok', 20_000, 'hub ok');
    await run.shell.evaluate(() => window.sy.selectView({ view: 'hub' }));
    await sleep(2500);
    // ---- the board, read-only sign-in
    await boardTour(run, hub, 'board');
    await boardTour(run, hub, 'board', [['login', '#/login'], ['pair', '#/pair'], ['owner-nosecret', '#/owner']], { themes: ['light'] });
    // ---- account tabs
    const ids = {};
    ids.work = (await run.shell.evaluate(() => window.sy.addTab({ name: 'work' }))).id;
    await sleep(1500);
    await waitFor(async () => /\/login$/.test(await tabUrl(run.app, ids.work)), 20_000, 'fake login').catch(() => {});
    await shots(run, 'tab-fake-login', { themes: ['light'], sizes: [BIG] });
    await inTab(run.app, ids.work, "document.getElementById('go').click()");
    await waitFor(async () => /\/new$/.test(await tabUrl(run.app, ids.work)), 20_000, 'signed in');
    await run.shell.evaluate((id) => window.sy.selectView({ view: id }), ids.work);
    await waitFor(() => run.shell.evaluate(() => !!document.querySelector('dialog[open]')), 15_000, 'the warning').catch(() => {});
    await shots(run, 'tab-warning-dialog');
    await run.shell.getByRole('button', { name: 'Read usage for this account', exact: true }).click().catch((e) => notes.push(`accept: ${e.message}`));
    await sleep(6000);
    await shots(run, 'tab-work-strip');
    ids.home = (await run.shell.evaluate(() => window.sy.addTab({ name: 'home' }))).id;
    await waitFor(async () => /\/login$/.test(await tabUrl(run.app, ids.home)), 20_000, 'fake login 2').catch(() => {});
    await inTab(run.app, ids.home, "document.getElementById('go').click()");
    ids.client = (await run.shell.evaluate(() => window.sy.addTab({ name: 'client-long-name' }))).id;
    await sleep(1500);
    await run.shell.evaluate((id) => window.sy.selectView({ view: id }), ids.home);
    await sleep(1500);
    await shots(run, 'tab-home-not-accepted', { themes: ['light'] });
    await run.shell.evaluate(() => document.querySelector('dialog[open] [data-fk="warn-not-now"]')?.click());
    await sleep(800);
    await shots(run, 'tab-home-after-not-now', { themes: ['light', 'dark'] });
    // add-tab dialog
    await run.shell.evaluate(() => document.querySelector('[data-fk="add-tab"]')?.click());
    await sleep(700);
    await shots(run, 'dialog-add-tab', { themes: ['light', 'dark'], sizes: [BIG] });
    await run.shell.keyboard.press('Escape');
    await sleep(500);
    // drag to reorder: the drop marker, then the drop
    await run.shell.evaluate((a) => {
      const src = document.querySelector(`#tab-${a.client}`) || [...document.querySelectorAll('.tab[draggable="true"]')].pop();
      const dst = document.querySelector(`#tab-${a.work}`) || document.querySelector('.tab[draggable="true"]');
      const dt = new DataTransfer();
      src.dispatchEvent(new DragEvent('dragstart', { bubbles: true, dataTransfer: dt }));
      const r = dst.getBoundingClientRect();
      dst.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: r.left + 4, clientY: r.top + r.height / 2 }));
    }, ids);
    await sleep(300);
    await grab('tab-drag-marker-1440-light', BIG);
    await run.shell.evaluate((a) => {
      const src = document.querySelector(`#tab-${a.client}`) || [...document.querySelectorAll('.tab[draggable="true"]')].pop();
      const dst = document.querySelector(`#tab-${a.work}`) || document.querySelector('.tab[draggable="true"]');
      const dt = new DataTransfer();
      dt.setData('text/plain', a.client);
      const r = dst.getBoundingClientRect();
      dst.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: r.left + 4, clientY: r.top + r.height / 2 }));
      src.dispatchEvent(new DragEvent('dragend', { bubbles: true, dataTransfer: dt }));
    }, ids);
    await sleep(1000);
    await grab('tab-drag-dropped-1440-light', BIG);
    // ---- app Settings, every section
    for (const sec of ['tabs', 'folders', 'hub', 'notify', 'links']) {
      await shellSection(run, sec);
      await sleep(600);
      await shots(run, `settings-${sec}`, { themes: sec === 'tabs' || sec === 'hub' ? ['light', 'dark'] : ['light'] });
    }
    // remove dialog
    await shellSection(run, 'tabs');
    await run.shell.evaluate((id) => document.querySelector(`[data-fk="remove-${id}"]`)?.click(), ids.client);
    await sleep(600);
    await shots(run, 'dialog-remove-tab', { themes: ['light'], sizes: [BIG] });
    await run.shell.keyboard.press('Escape');
    // ---- owner
    const o = await run.shell.evaluate(() => window.sy.ownerSignIn({}));
    if (!o.ok) notes.push(`owner: ${JSON.stringify(o)}`);
    await sleep(3000);
    await boardTour(run, hub, 'owner', BOARD_ROUTES.filter(([n]) => ['now', 'settings', 'devices', 'accounts', 'alerts', 'more'].includes(n)));
    await shellSection(run, 'hub');
    await sleep(500);
    await shots(run, 'settings-hub-owner', { themes: ['light'] });
    await shellSection(run, 'tabs');
    await sleep(500);
    await shots(run, 'settings-tabs-owner', { themes: ['light'] });
    // devices as the hub records them (the session-device leak)
    // (an owner cookie of the audit's own: listing devices needs the owner)
    const devs = await hub.ownerCookie().then((c) => hub.ownerFetch(c, 'GET', '/v1/devices')).catch((e) => ({ error: String(e) }));
    const names = (devs && devs.devices || []).filter((d) => !d.revoked_at).map((d) => `${d.kind} ${d.name}`);
    notes.push(`devices live: ${names.length}: ${names.join(' | ')}`);
    fs.writeFileSync(path.join(OUT, 'devices.json'), JSON.stringify(devs, null, 2));
    // tray menu labels (no tray host under xvfb)
    const menu = await main(run.app, (m) => {
      const t = m.tray.buildMenu?.();
      const items = t?.items ?? t ?? [];
      return (Array.isArray(items) ? items : []).map((i) => (i.type === 'separator' ? '---' : `${i.label}${i.enabled === false ? ' (disabled)' : ''}`));
    }).catch((e) => [`error: ${e.message}`]);
    fs.writeFileSync(path.join(OUT, 'tray-menu.txt'), menu.join('\n'));
    // ---- error states: the hub goes down
    await run.shell.evaluate(() => window.sy.selectView({ view: 'hub' }));
    await hub.stop();
    await sleep(9000);
    await shots(run, 'error-hub-down-board', { themes: ['light'] });
    await main(run.app, (m) => m.hub.start && m.bus.emit('hub', { ...m.bus.last('hub'), status: 'unreachable', reachable: false }));
    await sleep(800);
    await shots(run, 'error-hub-unreachable-panel', { themes: ['light', 'dark'] });
    await shellSection(run, 'hub');
    await shots(run, 'error-hub-unreachable-settings', { themes: ['light'] });
  } finally {
    if (run) {
      fs.writeFileSync(path.join(OUT, 'main.log'), run.mainLog.join(''));
      if (run.errors.length) fs.writeFileSync(path.join(OUT, 'console-errors.txt'), run.errors.join('\n'));
      await run.close();
    }
    await claude.close();
    await hub.stop();
    sb.cleanup();
  }
}

async function phaseNotify() {
  const sb = sandbox('screens-notify');
  const bin = syBinary(sb.root);
  const hub = await startHub(sb, bin);
  await seedHub(sb, hub, bin, 'sessions');
  const bus = process.env.DBUS_SESSION_BUS_ADDRESS;
  if (!bus) throw new Error('notify phase: run under dbus-run-session');
  const logFile = path.join(OUT, 'notify-calls.jsonl');
  fs.rmSync(logFile, { force: true });
  const daemon = spawn('python3', ['-I', path.join(here, 'notifyd.py'), logFile], { stdio: 'inherit' });
  await sleep(1500);
  let run = null;
  try {
    // The private bus of dbus-run-session (never the desktop's): the fake daemon is on it.
    run = await launch(sb, { env: { SY_BIN: bin, DBUS_SESSION_BUS_ADDRESS: bus } });
    const r = await run.shell.evaluate(() => window.sy.pair({}));
    if (!r.ok) throw new Error(`pair: ${JSON.stringify(r)}`);
    await waitFor(async () => (await shellState(run.shell)).hub.status === 'ok', 20_000, 'hub ok');
    await run.shell.evaluate(() => window.sy.selectView({ view: 'settings' }));
    await sleep(3000);
    await seedHub(sb, hub, bin, 'alerts');
    await waitFor(() => fs.existsSync(logFile) && fs.readFileSync(logFile, 'utf8').trim(), 120_000, 'a Notify call').catch((e) => notes.push(`notify: ${e.message}`));
    const wid = await main(run.app, (m, e) => ({ appName: e.app.getName(), desktop: process.env.CHROME_DESKTOP ?? null }));
    fs.writeFileSync(path.join(OUT, 'notify-app.json'), JSON.stringify(wid, null, 2));
    const xprop = spawnSync('bash', ['-c', 'for w in $(xdotool search --pid ' + run.app.process().pid + ' 2>/dev/null); do xprop -id $w WM_CLASS _NET_WM_ICON 2>/dev/null | head -c 300; echo; done'], { encoding: 'utf8' });
    fs.writeFileSync(path.join(OUT, 'notify-xprop.txt'), xprop.stdout + xprop.stderr);
  } finally {
    if (run) await run.close();
    daemon.kill('SIGTERM');
    await hub.stop();
    sb.cleanup();
  }
}

async function phaseClaude() {
  const sb = sandbox('screens-claude');
  let run = null;
  try {
    // No sy at all: the "sy missing" state, then a tab on the real claude.ai (nothing typed).
    run = await launch(sb, { env: { SY_BIN: path.join(sb.root, 'no-such-sy') } });
    await sleep(2500);
    await shots(run, 'error-sy-missing');
    await shellSection(run, 'hub');
    await shots(run, 'error-sy-missing-settings', { themes: ['light'] });
    await shellSection(run, 'tabs');
    await shots(run, 'settings-tabs-empty', { themes: ['light', 'dark'] });
    const id = (await run.shell.evaluate(() => window.sy.addTab({ name: 'personal' }))).id;
    await run.shell.evaluate((t) => window.sy.selectView({ view: t }), id);
    await sleep(12_000);
    await shots(run, 'tab-claude-login', { themes: ['light', 'dark'] });
  } finally {
    if (run) {
      fs.writeFileSync(path.join(OUT, 'main-claude.log'), run.mainLog.join(''));
      await run.close();
    }
    sb.cleanup();
  }
}

try {
  if (PHASES.has('main')) await phaseMain();
  if (PHASES.has('notify')) await phaseNotify();
  if (PHASES.has('claude')) await phaseClaude();
} catch (err) {
  notes.push(`fatal: ${err.stack ?? err}`);
  process.exitCode = 1;
}
fs.writeFileSync(path.join(OUT, 'notes.txt'), notes.join('\n'));
log('done', notes.length ? `with ${notes.length} notes` : '');
