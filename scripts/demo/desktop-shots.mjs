// Captures the README/site desktop-app stills from demo data only (not a test).
//   xvfb-run -a -s "-screen 0 1280x800x24" dbus-run-session -- env TMPDIR=<tmp> WY_BIN=<wy> \
//     node scripts/demo/desktop-shots.mjs <out dir>
// Runs in the smoke harness's sandbox (temp HOME, user data and D-Bus address) against a
// sandbox hub seeded by test/e2e/android/seed and the fake claude.ai: nothing real is read.
// Writes desktop.png (account tabs + usage strip) and desktop-hub.png (Hub view), dark theme.
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const H = path.resolve(here, '../../app/test/smoke/harness.mjs');
const { sandbox, wyBinary, startHub, launch, main, inTab, tabUrl, shellState, waitFor, sleep, freshFixture, REPO_DIR } = await import(H);
const { startFakeClaude, USAGE_DIR } = await import(path.resolve(here, '../../app/test/fakes/claude/server.js'));
const OUT = path.resolve(process.argv[2] || 'desktop-out');
fs.mkdirSync(OUT, { recursive: true });
const W = 1280, HT = 800;

const grab = (name) => {
  const r = spawnSync('import', ['-window', 'root', '-crop', `${W}x${HT}+0+0`, '+repage', path.join(OUT, name)]);
  if (r.status !== 0) throw new Error(`grab ${name}: ${r.stderr}`);
};

const sb = sandbox('shots');
const bin = wyBinary(sb.root);
const hub = await startHub(sb, bin);
const seedBin = path.join(sb.root, 'seed');
const b = spawnSync(process.env.GO || 'go', ['build', '-o', seedBin, './test/e2e/android/seed'], { cwd: REPO_DIR, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' } });
if (b.status !== 0) throw new Error('seed build');
const machines = [];
for (const [id, name] of [['m_01J9SEED000000000000000001', 'laptop'], ['m_01J9SEED000000000000000002', 'gpu-box']]) {
  const r = await hub.wy(['hub', 'token', 'create', '--kind', 'machine', '--name', name, '--machine', id, '--json']);
  machines.push(`${id}=${JSON.parse(r.stdout).token}`);
}
const seed = spawnSync(seedBin, ['-hub', hub.url, ...machines.flatMap((m) => ['-m', m])], { env: sb.env, encoding: 'utf8' });
if (seed.status !== 0) throw new Error(`seed: ${seed.stderr}`);
const fixture = freshFixture(sb.root, { five: 38, week: 54 });
const claude = await startFakeClaude({ fixtures: [path.relative(USAGE_DIR, fixture)] });
let run = null;
try {
  run = await launch(sb, { env: { WY_BIN: bin, WY_APP_TEST_CLAUDE_ORIGIN: claude.origin, WY_APP_TEST_CADENCE_MS: '4000' } });
  await main(run.app, (m, e) => { e.nativeTheme.themeSource = 'dark'; const w = m.window.win; w.setBounds({ x: 0, y: 0, width: 1280, height: 800 }); });
  for (const p of run.app.windows()) await p.emulateMedia({ colorScheme: 'dark' }).catch(() => {});
  const r = await run.shell.evaluate(() => window.wy.pair({}));
  if (!r.ok) throw new Error(`pair ${JSON.stringify(r)}`);
  await waitFor(async () => (await shellState(run.shell)).hub.status === 'ok', 20_000, 'hub ok');
  const page = (code) => code;
  const NEUTRAL = `document.documentElement.style.cssText='background:#1b1c1e;color:#a8a49c';document.body.style.cssText='margin:0;height:100vh;display:grid;place-items:center;font:16px system-ui;background:#1b1c1e;color:#a8a49c';document.body.innerHTML='<div style="text-align:center"><div style="font-size:22px;color:#e8e6e1;margin-bottom:8px">claude.ai</div>This account\\'s own tab, with its own sign-in.</div>'`;
  const ids = {};
  for (const name of ['alex', 'platform-team', 'contractor']) {
    ids[name] = (await run.shell.evaluate((n) => window.wy.addTab({ name: n }), name)).id;
    await waitFor(async () => /\/login$/.test(await tabUrl(run.app, ids[name])), 20_000, 'login');
    await inTab(run.app, ids[name], "document.getElementById('go').click()");
    await waitFor(async () => /\/new$/.test(await tabUrl(run.app, ids[name])), 20_000, 'signed in');
  }
  await run.shell.evaluate((id) => window.wy.selectView({ view: id }), ids.alex);
  await waitFor(() => run.shell.evaluate(() => !!document.querySelector('dialog[open]')), 15_000, 'warning').catch(() => {});
  await run.shell.getByRole('button', { name: 'Read usage for this account', exact: true }).click();
  await sleep(6000);
  await inTab(run.app, ids.alex, NEUTRAL);
  await sleep(800);
  grab('desktop.png');
  await run.shell.evaluate(() => window.wy.selectView({ view: 'hub' }));
  // The demo emails are long enough to be cut off in this strip: use a shorter demo wording.
  await sleep(2500);
  const hp = run.app.windows().find((p) => p.url().startsWith(hub.url));
  if (hp) await hp.emulateMedia({ colorScheme: 'dark' });
  const shorten = () => { const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT); for (let n = w.nextNode(); n; n = w.nextNode()) if (/no tab here/.test(n.nodeValue)) n.nodeValue = 'platform-team and contractor have no tab here. '; };
  await sleep(1500);
  await run.shell.evaluate(shorten);
  grab('desktop-hub.png');
} finally {
  if (run) await run.close();
  await claude.close();
  await hub.stop();
  sb.cleanup();
}
