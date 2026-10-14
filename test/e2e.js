// End-to-end smoke test: launches the real app with a throwaway data dir and checks
// tabs, per-account session isolation, claude.ai loading, and the hook receiver.
//   node test/e2e.js            (needs a display; claude.ai reachable)
const { _electron: electron } = require('playwright-core');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const out = process.env.OUT || fs.mkdtempSync(path.join(os.tmpdir(), 'sy-e2e-'));
const data = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-data-'));
const port = 47900 + Math.floor(Math.random() * 50);
const ok = (c, m) => { console.log(`${c ? 'PASS' : 'FAIL'}  ${m}`); if (!c) process.exitCode = 1; };

(async () => {
  const app = await electron.launch({
    args: [path.join(__dirname, '..')],
    executablePath: require('electron'),
    env: { ...process.env, SWITCHYARD_DATA: data, SWITCHYARD_HOOK_PORT: String(port) },
  });
  // account views are pages too; find the shell
  let shell;
  for (let i = 0; i < 100 && !shell; i++) { shell = app.windows().find((p) => p.url().includes('shell/index.html')); if (!shell) await new Promise((r) => setTimeout(r, 200)); }
  await shell.waitForSelector('#dlg-add[open]');
  ok(true, 'first launch opens "Add account"');

  for (const [label, n] of [['personal', 0], ['work', 1]]) {
    if (n) { await shell.click('[data-act="add"] >> nth=0'); await shell.waitForSelector('#dlg-add[open]'); }
    await shell.fill('#add-label', label);
    await shell.click('#form-add button[type="submit"]');
    await shell.waitForSelector(`.tab[aria-selected="true"]:has-text("${label}")`);
  }
  const ids = await shell.evaluate(() => window.sy.state().then((s) => s.accounts.map((a) => a.id)));
  ok(ids.length === 2, 'two account tabs created');

  // Isolation: a cookie set in one account's partition must not exist in the other.
  const iso = await app.evaluate(async ({ session }, ids) => {
    const [a, b] = ids.map((id) => session.fromPartition(`persist:acct-${id}`));
    await a.cookies.set({ url: 'https://claude.ai', name: 'sy_probe', value: 'A', secure: true });
    const inA = (await a.cookies.get({ name: 'sy_probe' })).length;
    const inB = (await b.cookies.get({ name: 'sy_probe' })).length;
    await a.cookies.remove('https://claude.ai', 'sy_probe');
    return { inA, inB };
  }, ids);
  ok(iso.inA === 1 && iso.inB === 0, `cookie isolation between accounts (A=${iso.inA}, B=${iso.inB})`);

  // Both views load claude.ai and stay live while hidden.
  await shell.waitForTimeout(12000);
  const views = await app.evaluate(({ BrowserWindow }) => {
    const w = BrowserWindow.getAllWindows()[0];
    return w.contentView.children.map((v) => ({ url: v.webContents.getURL(), title: v.webContents.getTitle(), visible: v.getVisible ? v.getVisible() : null }));
  });
  console.log('      views:', JSON.stringify(views));
  ok(views.length === 2 && views.every((v) => /claude\.ai/.test(v.url)), 'both account views on claude.ai');
  ok(views.filter((v) => v.visible).length === 1, `exactly one view visible (${views.map((v) => v.visible).join(',')})`);

  const shot = await app.evaluate(async ({ BrowserWindow }) => {
    const w = BrowserWindow.getAllWindows()[0];
    const img = await w.webContents.capturePage();
    const v = w.contentView.children.find((c) => c.getVisible());
    const vi = v ? await v.webContents.capturePage() : null;
    return { shell: img.toPNG().toString('base64'), view: vi ? vi.toPNG().toString('base64') : null };
  });
  fs.writeFileSync(path.join(out, 'tab-shell.png'), Buffer.from(shot.shell, 'base64'));
  if (shot.view) fs.writeFileSync(path.join(out, 'tab-view.png'), Buffer.from(shot.view, 'base64'));

  // Views on Cloudflare's interstitial must clear (the app reloads a stalled one after 25s).
  let titles = [];
  for (let i = 0; i < 12; i++) {
    titles = await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].contentView.children.map((v) => v.webContents.getTitle()));
    if (titles.every((t) => !/just a moment/i.test(t))) break;
    await shell.waitForTimeout(5000);
  }
  console.log('      titles:', JSON.stringify(titles));
  ok(titles.every((t) => !/just a moment/i.test(t)), 'no view stuck on the Cloudflare check');

  // Usage on a signed-out account should report signed-out, not crash.
  await shell.evaluate((id) => window.sy.refresh(id), ids[0]);
  await shell.waitForTimeout(3000);
  const st = await shell.evaluate(() => window.sy.state());
  console.log('      usage:', JSON.stringify(st.accounts.map((a) => a.usage)));
  ok(st.accounts.every((a) => ['signed-out', 'error', 'loading', 'ok'].includes(a.usage.status)), 'usage state resolves for signed-out accounts');

  // Hook receiver: rejects a bad token, accepts a good one, event lands in the feed.
  const post = (tok, body) => fetch(`http://127.0.0.1:${port}/hook?account=work`, { method: 'POST', headers: { authorization: `Bearer ${tok}`, 'content-type': 'application/json' }, body: JSON.stringify(body) });
  await shell.click('.tab.dash');
  ok((await post('nope', {})).status === 401, 'hook receiver rejects bad token');
  const r = await post(st.hook.token, { hook_event_name: 'Notification', notification_type: 'permission_prompt', message: 'Claude needs your permission to use Bash', cwd: '/home/x/work-api', session_id: 's1' });
  ok(r.status === 204, 'hook receiver accepts good token');
  await shell.waitForSelector('.ev:has-text("permission")');
  ok(true, 'hook event appears in dashboard feed');
  ok(await shell.isVisible('.tab:has-text("work") .badge'), 'unread badge on the work tab');

  await shell.screenshot({ path: path.join(out, 'dashboard.png') });

  // "Jump in" opens that account's claude.ai/code.
  await shell.click('.ev:has-text("permission") [data-act="code"]');
  await shell.waitForSelector('.tab[aria-selected="true"]:has-text("work")');
  await shell.waitForTimeout(4000);
  const codeUrl = await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].contentView.children.find((v) => v.getVisible()).webContents.getURL());
  ok(/claude\.ai\/(code|login)/.test(codeUrl) && /code/.test(decodeURIComponent(codeUrl)), `"Jump in" opens claude.ai/code in the work tab (${codeUrl})`);
  // Analytics tab renders (empty data dir: no tokens yet, but no errors either).
  const errs = [];
  shell.on('pageerror', (e) => errs.push(e.message));
  await shell.click('[data-tab="analytics"]');
  await shell.waitForSelector('#analytics .an-filters', { timeout: 60e3 });
  await shell.waitForFunction(() => !/Updating…/.test(document.querySelector('#analytics .an-filters').textContent), null, { timeout: 90e3 }).catch(() => {});
  await shell.screenshot({ path: path.join(out, 'analytics.png') });
  ok(errs.length === 0, `analytics tab renders without page errors${errs.length ? ': ' + errs.join('; ') : ''}`);

  // Background & sync settings round-trip.
  const set = await shell.evaluate(() => window.sy.setSettings({ keepRunning: true, server: { url: 'http://127.0.0.1:9', token: 'tok' } }));
  ok(set.server.url === 'http://127.0.0.1:9' && set.server.hasToken && set.keepRunning, 'server sync settings saved');
  const bad = await shell.evaluate(() => window.sy.setSettings({ server: { url: 'ftp://x' } }).then(() => 'saved', (e) => 'rejected'));
  ok(bad === 'rejected', 'non-http server URL rejected');
  await shell.evaluate(() => window.sy.setSettings({ server: { url: '' } }));

  console.log('      screenshots in', out);
  await app.close();

  // Background start: --background keeps the window hidden while tracking runs.
  const bg = await electron.launch({
    args: [path.join(__dirname, '..'), '--background'],
    executablePath: require('electron'),
    env: { ...process.env, SWITCHYARD_DATA: data, SWITCHYARD_HOOK_PORT: String(port) },
  });
  await new Promise((r) => setTimeout(r, 4000));
  const vis = await bg.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().map((w) => w.isVisible()));
  ok(vis.length === 1 && vis[0] === false, `--background starts hidden (${JSON.stringify(vis)})`);
  const alive = await fetch(`http://127.0.0.1:${port}/hook`, { method: 'POST', headers: { authorization: 'Bearer nope' } }).then((r) => r.status);
  ok(alive === 401, 'hook receiver runs while hidden');
  await bg.close();
})().catch((e) => { console.error(e); process.exit(1); });
