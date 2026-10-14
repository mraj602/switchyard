// Screenshot the Analytics tab against a snapshot of real data, without touching the live app:
//   node test/preview.js [srcDataDir]      (default ~/.config/switchyard) → PNGs in $OUT
// Copies usage.db (consistent snapshot via VACUUM INTO) and accounts.json into a temp data
// dir; claude.ai sign-ins are NOT copied, so account tabs show as signed out.
const { _electron: electron } = require('playwright-core');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { DatabaseSync } = require('node:sqlite');

const src = process.argv[2] || path.join(os.homedir(), '.config', 'switchyard');
const out = process.env.OUT || fs.mkdtempSync(path.join(os.tmpdir(), 'sy-preview-'));
const data = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-pdata-'));
fs.mkdirSync(out, { recursive: true });
new DatabaseSync(path.join(src, 'usage.db'), { readOnly: true }).exec(`VACUUM INTO '${path.join(data, 'usage.db').replace(/'/g, "''")}'`);
fs.copyFileSync(path.join(src, 'accounts.json'), path.join(data, 'accounts.json'));

(async () => {
  const app = await electron.launch({
    args: [path.join(__dirname, '..')],
    executablePath: require('electron'),
    env: { ...process.env, SWITCHYARD_DATA: data, SWITCHYARD_HOOK_PORT: String(47960 + Math.floor(Math.random() * 30)) },
  });
  // account views are pages too; find the shell
  let shell;
  for (let i = 0; i < 100 && !shell; i++) { shell = app.windows().find((p) => p.url().includes('shell/index.html')); if (!shell) await new Promise((r) => setTimeout(r, 200)); }
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].setSize(1440, 1000));
  await shell.waitForSelector('[data-tab="analytics"]');
  await shell.click('[data-tab="analytics"]');
  // first run scans every transcript (~15 s for 3.7 GB) before the first answer
  await shell.waitForSelector('.an-kpis', { timeout: 180e3 });
  await shell.waitForTimeout(1500);
  const errs = [];
  shell.on('pageerror', (e) => errs.push(e.message));
  const total = await shell.evaluate(() => document.querySelector('#analytics').scrollHeight);
  const view = await shell.evaluate(() => document.querySelector('#analytics').clientHeight);
  let i = 0;
  for (let y = 0; y < total; y += view - 60, i++) {
    await shell.evaluate((y) => { document.querySelector('#analytics').scrollTop = y; }, y);
    await shell.waitForTimeout(250);
    await shell.screenshot({ path: path.join(out, `analytics-${i}.png`) });
  }
  // hover a bar to check the tooltip
  await shell.evaluate(() => { document.querySelector('#analytics').scrollTop = 0; });
  const spend = await shell.$('[data-chart="spend"] svg');
  if (spend) {
    await spend.scrollIntoViewIfNeeded();
    const b = await spend.boundingBox();
    await shell.mouse.move(b.x + b.width * 0.8, b.y + b.height * 0.5);
    await shell.waitForTimeout(200);
    await shell.screenshot({ path: path.join(out, 'analytics-hover.png') });
  }
  console.log(JSON.stringify({ out, shots: i, errors: errs }));
  await app.close();
  fs.rmSync(data, { recursive: true, force: true });
})().catch((e) => { console.error(e); process.exit(1); });
