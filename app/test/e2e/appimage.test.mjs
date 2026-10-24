// End to end with the bundled `wy` (docs/spec/desktop.md C11): the
// packaged AppImage, the Chromium sandbox on (no --no-sandbox), no `wy` on PATH, a temp HOME.
//
//   1. the asar holds the app's code and nothing else (no tests, no dev page, no test hooks,
//      no node_modules), the bundled wy is outside it and runs, and the fuses are flipped;
//   2. ELECTRON_RUN_AS_NODE=1 ./Watchyard.AppImage -e … does not run Node;
//   3. the app resolves the bundled wy and shows the not-connected banner; after `wy connect`
//      (through the bundled binary, against a real hub served by that same binary) it pairs,
//      stores the token and the Hub view loads the signed-in board.
//
// The packaged app ignores every WY_APP_TEST_* variable (test-hooks.js is not shipped), so it
// talks to the real claude.ai only: the web-usage read itself is covered by smoke S4, not here.
// The app is driven over the Chrome DevTools protocol (--remote-debugging-port), since the
// fuses turn off the Node inspector that playwright's _electron.launch needs. A packaged app
// refuses that switch (src/main/cdp.js, C2) unless its build baked `watchyard.allowCdp: true`
// into the asar: so this test runs the e2e build of scripts/dist-e2e.sh (dist/e2e/), never a
// release build, and checks the flag is there.
//
// Run (after `scripts/dist-e2e.sh`, or with WY_APPIMAGE=<file>): xvfb-run -a node --test test/e2e/

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

import { sandbox, startHub, waitFor, sleep, freePort, outDir, APP_DIR, LINUX_ARGS } from '../smoke/harness.mjs';
import { HUB_TOKEN_FILE } from '../../src/shared/constants.js';

const NOT_CONNECTED = 'Connect to a hub.';

function findAppImage() {
  if (process.env.WY_APPIMAGE) return path.resolve(process.env.WY_APPIMAGE);
  const dist = path.join(APP_DIR, 'dist', 'e2e');
  const found = fs.existsSync(dist) ? fs.readdirSync(dist).filter((f) => f.endsWith('.AppImage')).sort() : [];
  return found.length ? path.join(dist, found[found.length - 1]) : null;
}

/** One file of an asar archive, by its path in the archive ('/package.json'). */
function asarRead(file, name) {
  const fd = fs.openSync(file, 'r');
  try {
    const head = Buffer.alloc(16);
    fs.readSync(fd, head, 0, 16, 0);
    const len = head.readUInt32LE(12);
    const json = Buffer.alloc(len);
    fs.readSync(fd, json, 0, len, 16);
    let node = JSON.parse(json.toString('utf8'));
    for (const part of name.split('/').filter(Boolean)) node = node?.files?.[part];
    if (!node || node.files || node.unpacked) return null;
    const out = Buffer.alloc(node.size);
    fs.readSync(fd, out, 0, node.size, 8 + head.readUInt32LE(4) + Number(node.offset));
    return out.toString('utf8');
  } finally {
    fs.closeSync(fd);
  }
}

/** The file list of an asar archive (its JSON header: 16 bytes of pickle sizes, then the JSON). */
function asarFiles(file) {
  const fd = fs.openSync(file, 'r');
  try {
    const head = Buffer.alloc(16);
    fs.readSync(fd, head, 0, 16, 0);
    const len = head.readUInt32LE(12);
    const json = Buffer.alloc(len);
    fs.readSync(fd, json, 0, len, 16);
    const out = [];
    const walk = (node, prefix) => {
      for (const [name, child] of Object.entries(node.files ?? {})) {
        const p = `${prefix}/${name}`;
        if (child.files) walk(child, p);
        else out.push(p);
      }
    };
    walk(JSON.parse(json.toString('utf8')), '');
    return out;
  } finally {
    fs.closeSync(fd);
  }
}

/** Starts the AppImage in its own process group; kill() ends the whole group (Chromium's helpers too). */
function startAppImage(appImage, sb, { env = {}, args = [] } = {}) {
  const log = fs.openSync(path.join(sb.root, 'app.log'), 'a');
  const proc = spawn(appImage, args, { env: { ...sb.env, ...env }, cwd: sb.root, detached: true, stdio: ['ignore', log, log] });
  const group = (sig) => {
    try {
      process.kill(-proc.pid, sig);
      return true;
    } catch {
      return false; // ESRCH: the group is gone
    }
  };
  return {
    proc,
    async kill() {
      if (!group(0)) return;
      group('SIGTERM');
      const end = Date.now() + 10_000;
      while (group(0) && Date.now() < end) await sleep(200);
      if (group(0)) group('SIGKILL');
      while (group(0)) await sleep(100);
    },
  };
}

/** A PATH with no `wy` on it (the bundled copy must be the one found). */
function pathWithoutWy() {
  return (process.env.PATH ?? '/usr/bin:/bin')
    .split(path.delimiter)
    .filter((d) => d && !fs.existsSync(path.join(d, 'wy')))
    .join(path.delimiter);
}

/**
 * The app's environment: no wy on PATH; APPIMAGE_EXTRACT_AND_RUN so no FUSE is needed (CI
 * runners have no libfuse2), extracting into the sandbox's own TMPDIR so nothing is left behind.
 */
function appEnv(sb) {
  const tmp = path.join(sb.root, 'tmp');
  fs.mkdirSync(tmp, { recursive: true });
  return { PATH: pathWithoutWy(), APPIMAGE_EXTRACT_AND_RUN: '1', TMPDIR: tmp };
}

const appImage = findAppImage();
// Locally a missing AppImage skips; on CI (after packaging) it is a failure, never a silent pass.
const skip = process.platform !== 'linux' ? 'Linux only' : !appImage && !process.env.CI ? 'no AppImage in app/dist/e2e (scripts/dist-e2e.sh, or WY_APPIMAGE)' : false;

test('packaged AppImage: asar, fuses, bundled wy, connect and pair', { skip, timeout: 10 * 60_000 }, async (t) => {
  assert.ok(appImage, 'no AppImage in app/dist: the package step did not produce one');
  const sb = sandbox('e2e');
  const out = outDir(sb.root);
  const runs = [];
  let hub = null;
  let browser = null;
  t.after(async () => {
    await browser?.close().catch(() => {});
    for (const r of runs) await r.kill();
    await hub?.stop();
    for (const f of ['app.log', 'hub.log']) {
      if (fs.existsSync(path.join(sb.root, f))) fs.copyFileSync(path.join(sb.root, f), path.join(out, `e2e-${f}`));
    }
    if (!process.env.WY_SMOKE_KEEP) sb.cleanup();
  });

  // 1. The contents, read from the extracted image (no FUSE needed for this part).
  const x = spawnSync(appImage, ['--appimage-extract'], { cwd: sb.root, env: sb.env, stdio: 'ignore' });
  assert.equal(x.status, 0, 'the AppImage extracts');
  const root = path.join(sb.root, 'squashfs-root');
  const bundled = path.join(root, 'resources', 'wy', 'wy');
  await t.test('asar contents and the bundled wy', () => {
    const files = asarFiles(path.join(root, 'resources', 'app.asar'));
    for (const want of ['/package.json', '/src/main/index.js', '/src/main/boot.js', '/src/preload/shell.cjs', '/src/renderer/index.html']) {
      assert.ok(files.includes(want), `asar has ${want}`);
    }
    const bad = files.filter((f) => /\.test\.(c|m)?js$/.test(f) || f.startsWith('/src/renderer/dev/') || f === '/src/main/test-hooks.js' || f.startsWith('/node_modules/') || f.startsWith('/test/'));
    assert.deepEqual(bad, [], 'no tests, dev page, test hooks or node_modules in the asar');
    assert.ok(!files.some((f) => f.includes('/wy/wy')), 'wy is not inside the asar');
    // The e2e build's allowance for --remote-debugging-port; the rest of package.json is kept.
    const pkg = JSON.parse(asarRead(path.join(root, 'resources', 'app.asar'), '/package.json'));
    assert.equal(pkg.watchyard?.allowCdp, true, 'not the e2e build (scripts/dist-e2e.sh): CDP would be refused');
    assert.equal(pkg.watchyard?.minWy, '0.6.0');
    assert.ok(fs.statSync(bundled).mode & 0o111, 'the bundled wy is executable');
    const v = spawnSync(bundled, ['--version'], { env: sb.env, encoding: 'utf8' });
    assert.equal(v.status, 0, v.stderr);
  });

  await t.test('fuses', async () => {
    const { getCurrentFuseWire, FuseV1Options, FuseState } = await import('@electron/fuses');
    const wire = await getCurrentFuseWire(path.join(root, 'watchyard-desktop'));
    assert.equal(wire[FuseV1Options.RunAsNode], FuseState.DISABLE);
    assert.equal(wire[FuseV1Options.EnableNodeOptionsEnvironmentVariable], FuseState.DISABLE);
    assert.equal(wire[FuseV1Options.EnableNodeCliInspectArguments], FuseState.DISABLE);
    assert.equal(wire[FuseV1Options.EnableEmbeddedAsarIntegrityValidation], FuseState.ENABLE);
    assert.equal(wire[FuseV1Options.OnlyLoadAppFromAsar], FuseState.ENABLE);
    assert.equal(wire[FuseV1Options.GrantFileProtocolExtraPrivileges], FuseState.DISABLE);
  });

  // 2. RunAsNode is off at run time: the script never runs (the app starts instead; stopped here).
  await t.test('ELECTRON_RUN_AS_NODE does not run Node', async () => {
    const marker = path.join(sb.root, 'ran-as-node');
    const r = startAppImage(appImage, sb, {
      env: { ...appEnv(sb), ELECTRON_RUN_AS_NODE: '1' },
      args: ['-e', `require('fs').writeFileSync(${JSON.stringify(marker)}, 'node')`, ...LINUX_ARGS],
    });
    runs.push(r);
    await sleep(8000);
    assert.equal(fs.existsSync(marker), false, 'the -e script ran: RunAsNode is on');
    await r.kill();
    // That launch made a user data folder; start the real run from a clean one.
    fs.rmSync(path.join(sb.env.XDG_CONFIG_HOME, 'watchyard-desktop'), { recursive: true, force: true });
  });

  // 3. The packaged app, sandbox on, no wy on PATH.
  await t.test('bundled wy: not connected, then connect and pair', async () => {
    const { chromium } = await import('playwright-core');
    const port = await freePort();
    const r = startAppImage(appImage, sb, {
      env: appEnv(sb),
      args: [...LINUX_ARGS, `--remote-debugging-port=${port}`],
    });
    runs.push(r);
    browser = await waitFor(() => chromium.connectOverCDP(`http://127.0.0.1:${port}`), 60_000, 'the DevTools endpoint');
    const pages = () => browser.contexts().flatMap((c) => c.pages());
    const shell = await waitFor(() => pages().find((p) => p.url().startsWith('app://shell/index.html')), 60_000, 'the shell page');
    const errors = [];
    shell.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
    await waitFor(async () => (await shell.evaluate(() => window.wy.state())).hub.status === 'not_connected', 30_000, 'hub status not_connected (bundled wy found)');
    await waitFor(() => shell.evaluate((t) => document.body.innerText.includes(t), NOT_CONNECTED), 15_000, 'the not-connected banner');
    assert.deepEqual(await shell.evaluate(() => [typeof require, typeof process]), ['undefined', 'undefined']);
    await shell.screenshot({ path: path.join(out, 'e2e-not-connected.png') });

    // A real hub and `wy connect`, both through the bundled binary, in the same temp HOME.
    hub = await startHub(sb, bundled);
    const paired = await shell.evaluate(() => window.wy.pair({}));
    assert.equal(paired.ok, true, JSON.stringify(paired));
    const st = await waitFor(async () => {
      const s = await shell.evaluate(() => window.wy.state());
      return s.hub.status === 'ok' && s;
    }, 30_000, 'hub status ok');
    assert.equal(st.hub.url, hub.url);
    const tokenFile = path.join(sb.env.XDG_CONFIG_HOME, 'watchyard-desktop', HUB_TOKEN_FILE);
    assert.equal(fs.statSync(tokenFile).mode & 0o777, 0o600);
    const devices = await hub.ownerFetch(await hub.ownerCookie(), 'GET', '/v1/devices');
    assert.ok(JSON.stringify(devices).includes('Watchyard-desktop'), 'the hub lists the desktop device');

    // The Hub view: the board, signed in by main (not on the login route).
    await shell.evaluate(() => window.wy.selectView({ view: 'hub' }));
    const board = await waitFor(() => pages().find((p) => p.url().startsWith(hub.url)), 30_000, 'the hub board page');
    await waitFor(async () => {
      const u = board.url();
      return u.startsWith(hub.url) && !/#\/(login|pair)/.test(u) && (await board.evaluate(() => document.body.innerText.length > 0));
    }, 30_000, 'the board on its own route');
    // The partition holds the read session main made: /v1/me answers for the desktop device.
    const me = await waitFor(async () => {
      const m = await board.evaluate(() => fetch('/v1/me', { credentials: 'include' }).then(async (r) => ({ status: r.status, body: r.ok ? await r.json() : null })));
      return m.status === 200 && m;
    }, 30_000, '/v1/me in the hub view');
    assert.equal(me.body.owner, false);
    const seen = await board.evaluate(() => [document.cookie, JSON.stringify({ ...localStorage, ...sessionStorage })]);
    assert.equal(seen[0], '', 'the session cookie is not readable by the page');
    assert.ok(!/wy_[a-z]_/.test(seen[1]), 'no token in the page storage');
    await board.screenshot({ path: path.join(out, 'e2e-hub.png') });
    assert.deepEqual(errors.filter((e) => !/fonts\.(googleapis|gstatic)/.test(e)), [], 'no console errors in the shell');
  });
});
