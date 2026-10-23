// Helpers for the Electron smoke tests (docs/spec/desktop.md) and the packaged e2e.
//
// Every run lives in a temp folder: HOME, SY_HOME, the XDG folders, SY_APP_USER_DATA and the
// D-Bus address all point into it, so nothing touches the real home, the real ~/.claude* or the
// user's services. The hub is a real `sy hub serve` built from this
// repo (SY_BIN, or a `go build` into the temp folder), claude.ai is the fake of
// test/fakes/claude/, and the app is launched with playwright-core's _electron.

import { spawn, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';


const here = path.dirname(fileURLToPath(import.meta.url));
export const APP_DIR = path.resolve(here, '../..');
export const REPO_DIR = path.resolve(APP_DIR, '..');
const require = createRequire(import.meta.url);

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Polls cond() until it returns a truthy value (returned) or ms pass (throws, naming what). */
export async function waitFor(cond, ms = 15_000, what = 'condition', every = 100) {
  const end = Date.now() + ms;
  let last;
  for (;;) {
    try {
      last = await cond();
      if (last) return last;
    } catch (err) {
      last = err;
    }
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what} (last: ${last?.message ?? JSON.stringify(last)})`);
    await sleep(every);
  }
}

/** A free loopback TCP port. */
export async function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const p = s.address().port;
      s.close(() => resolve(p));
    });
  });
}

/** Where artifacts (screenshots, logs) go: SY_SMOKE_OUT or a folder in the temp root. */
export function outDir(root) {
  const d = process.env.SY_SMOKE_OUT || path.join(root, 'out');
  fs.mkdirSync(d, { recursive: true });
  return d;
}

const BAD_HOME_PREFIXES = () => {
  const real = os.userInfo().homedir;
  return [path.join(real, '.claude'), path.join(real, '.config', 'switchyard'), path.join(real, '.local', 'state', 'switchyard')];
};

/**
 * A sandbox: a temp root with home, user data and the environment for sy and the app.
 * @param {string} name
 */
export function sandbox(name) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), `sy-${name}-`));
  const home = path.join(root, 'home');
  const userData = path.join(root, 'ud');
  for (const d of [home, userData, path.join(root, 'run')]) fs.mkdirSync(d, { recursive: true, mode: 0o700 });
  const env = {};
  for (const [k, v] of Object.entries(process.env)) {
    // Nothing of the real Claude, Switchyard, Electron or Node setup leaks in.
    if (/^(CLAUDE_|SY_|ELECTRON_|NODE_|XDG_|DBUS_|WAYLAND_|GNOME_|KDE_)/.test(k) || ['HOME', 'APPIMAGE', 'DESKTOP_SESSION'].includes(k)) continue;
    env[k] = v;
  }
  Object.assign(env, {
    HOME: home,
    SY_HOME: home,
    XDG_CONFIG_HOME: path.join(home, '.config'),
    XDG_STATE_HOME: path.join(home, '.local', 'state'),
    XDG_DATA_HOME: path.join(home, '.local', 'share'),
    XDG_CACHE_HOME: path.join(home, '.cache'),
    XDG_RUNTIME_DIR: path.join(root, 'run'),
    // No session bus: systemctl --user and the keyring are out of reach (safeStorage falls
    // back to basic_text), so no real service or secret store is ever touched.
    DBUS_SESSION_BUS_ADDRESS: `unix:path=${path.join(root, 'run', 'no-bus')}`,
  });
  for (const bad of BAD_HOME_PREFIXES()) if (home.startsWith(bad)) throw new Error(`sandbox inside ${bad}`);
  return { root, home, userData, env, cleanup: () => fs.rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 300 }) };
}

/** The sy binary: SY_BIN when set, else a `go build` of ../cmd/sy into dir. */
export function syBinary(dir) {
  if (process.env.SY_BIN) return process.env.SY_BIN;
  const out = path.join(dir, process.platform === 'win32' ? 'sy.exe' : 'sy');
  const r = spawnSync(process.env.GO || 'go', ['build', '-o', out, './cmd/sy'], {
    cwd: REPO_DIR, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' },
  });
  if (r.status !== 0) throw new Error('go build ./cmd/sy failed; set SY_BIN');
  return out;
}

/** Runs sy in the sandbox; resolves {code, stdout, stderr}. */
export function runSy(sb, bin, args, { stdin, timeout = 60_000, env = {} } = {}) {
  return new Promise((resolve, reject) => {
    const p = spawn(bin, args, { env: { ...sb.env, ...env }, stdio: ['pipe', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    const t = setTimeout(() => p.kill('SIGKILL'), timeout);
    p.stdout.on('data', (c) => (stdout += c));
    p.stderr.on('data', (c) => (stderr += c));
    p.on('error', reject);
    p.on('close', (code) => {
      clearTimeout(t);
      resolve({ code, stdout, stderr });
    });
    p.stdin.end(stdin ?? '');
  });
}

async function syJson(sb, bin, args, opts) {
  const r = await runSy(sb, bin, args, opts);
  if (r.code !== 0) throw new Error(`sy ${args.join(' ')}: exit ${r.code}: ${r.stderr}`);
  return JSON.parse(r.stdout);
}

/**
 * A real hub in the sandbox: `sy hub serve` on a free loopback port, this machine connected
 * to it by a machine pairing code (`sy connect --code-stdin --no-timer`: no timer, no service).
 */
export async function startHub(sb, bin) {
  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const log = fs.openSync(path.join(sb.root, 'hub.log'), 'a');
  const proc = spawn(bin, ['hub', 'serve', '--listen', `127.0.0.1:${port}`, '--no-pull'], { env: sb.env, stdio: ['ignore', log, log] });
  await waitFor(async () => (await fetch(`${url}/health`)).ok, 20_000, 'the hub to answer /health');
  const code = (await syJson(sb, bin, ['hub', 'pair', '--kind', 'machine', '--url', url, '--json'])).code;
  const c = await runSy(sb, bin, ['connect', url, '--code-stdin', '--no-timer'], { stdin: `${code}\n` });
  if (c.code !== 0) throw new Error(`sy connect: ${c.stderr}`);
  // A reader for the test itself (a desktop device of its own).
  const reader = (await syJson(sb, bin, ['desktop', 'token', '--json', '--name', 'smoke-reader'])).token;
  const hub = {
    url,
    port,
    proc,
    bin,
    async get(p, token = reader) {
      const r = await fetch(`${url}${p}`, { headers: { authorization: `Bearer ${token}` } });
      if (!r.ok) throw new Error(`GET ${p}: ${r.status}`);
      return r.json();
    },
    /** An owner cookie for the test (sy desktop owner-link → POST /v1/session/owner). */
    async ownerCookie() {
      const link = await syJson(sb, bin, ['desktop', 'owner-link', '--json']);
      const secret = new URL(link.url.replace('#/owner?', 'owner?')).searchParams.get('s');
      const r = await fetch(`${url}/v1/session/owner`, {
        method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ secret, name: 'smoke owner' }),
      });
      if (r.status !== 204) throw new Error(`owner sign-in: ${r.status}`);
      return r.headers.getSetCookie().map((c) => c.split(';')[0]).join('; ');
    },
    async ownerFetch(cookie, method, p, body) {
      const r = await fetch(`${url}${p}`, {
        method, headers: { cookie, 'content-type': 'application/json', origin: url }, body: body === undefined ? undefined : JSON.stringify(body),
      });
      const text = await r.text();
      if (!r.ok) throw new Error(`${method} ${p}: ${r.status} ${text}`);
      return text ? JSON.parse(text) : null;
    },
    sy: (args, opts) => runSy(sb, bin, args, opts),
    async outboxBytes() {
      return (await syJson(sb, bin, ['outbox', 'stat', '--json'])).bytes ?? 0;
    },
    async outboxDir() {
      return (await syJson(sb, bin, ['outbox', 'stat', '--json'])).dir;
    },
    stop: () =>
      new Promise((resolve) => {
        if (proc.exitCode !== null) return resolve();
        proc.once('exit', () => resolve());
        proc.kill('SIGTERM');
        setTimeout(() => proc.kill('SIGKILL'), 5000).unref();
      }),
  };
  return hub;
}

/** Every record line in the outbox folder (flushed or not) that matches pred. */
export function outboxRecords(dir, pred = () => true) {
  const out = [];
  const walk = (d) => {
    let ents = [];
    try {
      ents = fs.readdirSync(d, { withFileTypes: true });
    } catch {
      return;
    }
    for (const e of ents) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (/\.jsonl$/.test(e.name) && !/^(answered|rejected)/.test(e.name)) {
        for (const line of fs.readFileSync(p, 'utf8').split('\n')) {
          if (!line.trim()) continue;
          try {
            const r = JSON.parse(line);
            if (pred(r)) out.push(r);
          } catch {
            // a partial line
          }
        }
      }
    }
  };
  walk(dir);
  return out;
}

/** The Electron binary of app/node_modules (npm ci without --ignore-scripts, or `npx electron install`). */
export function electronPath() {
  const p = require('electron');
  if (typeof p !== 'string' || !fs.existsSync(p)) throw new Error('no Electron binary: run npm ci (with scripts) in app/');
  return p;
}

/** Electron arguments for headless Linux runs (X11 under xvfb, never Wayland). */
// --password-store=basic: never the real keyring (safeStorage uses basic_text, as on a box
// without a secret service; the token file is then protected by its mode).
export const LINUX_ARGS = process.platform === 'linux' ? ['--ozone-platform=x11', '--password-store=basic'] : [];

/**
 * Launches the app (unpackaged unless executablePath is given). Collects console errors of
 * every page and the main process output.
 * @returns {Promise<{app, shell, errors: string[], mainLog: string[], close(): Promise<void>}>}
 */
export async function launch(sb, { env = {}, args = [], executablePath, appArgs } = {}) {
  const { _electron } = await import('playwright-core');
  const launchEnv = { ...sb.env, SY_APP_USER_DATA: sb.userData, SY_APP_TEST_EXPOSE: '1', ...env };
  const app = await _electron.launch({
    executablePath: executablePath ?? electronPath(),
    args: appArgs ?? [APP_DIR, ...LINUX_ARGS, ...args],
    cwd: APP_DIR,
    env: launchEnv,
    timeout: 60_000,
  });
  const errors = [];
  const mainLog = [];
  const watch = (page) => {
    page.on('console', (m) => {
      if (m.type() === 'error') errors.push(`${page.url()}: ${m.text()}`);
    });
    page.on('pageerror', (e) => errors.push(`${page.url()}: pageerror ${e.message}`));
  };
  for (const p of app.windows()) watch(p);
  app.on('window', watch);
  app.process().stdout?.on('data', (c) => mainLog.push(String(c)));
  app.process().stderr?.on('data', (c) => mainLog.push(String(c)));
  const shell = await waitFor(() => app.windows().find((p) => p.url().startsWith('app://shell/')), 30_000, 'the shell window');
  await shell.waitForLoadState('load');
  await waitFor(() => app.evaluate(() => !!globalThis.__switchyard), 30_000, 'boot to finish');
  return {
    app,
    shell,
    errors,
    mainLog,
    async close() {
      try {
        await app.close();
      } catch {
        // already gone
      }
    },
  };
}

/** Runs fn(modules, electron, arg) in the main process. */
export function main(app, fn, arg) {
  return app.evaluate(
    async (_electron, { src, arg: a }) => {
      const g = globalThis.__switchyard;
      // eslint-disable-next-line no-new-func
      const f = new Function(`return (${src})`)();
      return f(g.modules, g.electron, a);
    },
    { src: fn.toString(), arg },
  );
}

/** Runs code (a string) in an account tab's page, by tab id. */
export function inTab(app, id, code) {
  return main(app, (m, _e, a) => m.tabs.view(a.id).webContents.executeJavaScript(a.code), { id, code });
}

/** The tab's current URL. */
export function tabUrl(app, id) {
  return main(app, (m, _e, tid) => m.tabs.view(tid)?.webContents.getURL() ?? '', id);
}

/** The shell's ShellState (through window.sy, as the renderer sees it). */
export function shellState(shell) {
  return shell.evaluate(() => window.sy.state());
}

/** A usage fixture whose windows reset in the future (the committed ones go stale). */
export function freshFixture(dir, { five = 42, week = 61 } = {}) {
  const src = JSON.parse(fs.readFileSync(path.join(REPO_DIR, 'testdata', 'usage', 'response-01.json'), 'utf8'));
  const at = (ms) => new Date(Date.now() + ms).toISOString().replace('Z', '+00:00');
  src.five_hour = { ...src.five_hour, utilization: five, resets_at: at(3 * 3600_000) };
  src.seven_day = { ...src.seven_day, utilization: week, resets_at: at(5 * 86400_000) };
  const file = path.join(dir, 'smoke-usage.json');
  fs.writeFileSync(file, JSON.stringify(src, null, 2));
  return file;
}
