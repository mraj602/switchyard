// Electron smoke S13: the top bar with 7 account tabs at 1024, 1280 and 1440 px. Tabs that do not
// fit go into the "N more" menu (never off the edge), the shown tab is always visible, the right
// side compacts on a narrow window. Also the progress line of a slow "Pair this computer".
// Screenshots go to SY_TABS_SHOTS (default: the smoke out dir).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import fs from 'node:fs';

import { sandbox, launch, waitFor, outDir, sleep } from './harness.mjs';

const WIDTHS = [1024, 1280, 1440];

test('S13 top bar overflow and pairing progress', { timeout: 3 * 60_000 }, async (t) => {
  const sb = sandbox('s13');
  const out = process.env.SY_TABS_SHOTS || outDir(sb.root);
  fs.mkdirSync(out, { recursive: true });
  const run = await launch(sb, { env: { SY_BIN: path.join(sb.root, 'no-such-sy') } });
  t.after(async () => {
    await run.close();
    if (!process.env.SY_SMOKE_KEEP) sb.cleanup();
  });

  const open = async (width, query) => {
    const url = `app://shell/dev/index.html?${query}`;
    const seen = new Set(run.app.windows());
    await run.app.evaluate(({ BrowserWindow }, a) => {
      const w = new BrowserWindow({ width: a.width, height: 700, useContentSize: true, show: true, webPreferences: { contextIsolation: true, sandbox: true, nodeIntegration: false } });
      globalThis.__devWin = w;
      return w.loadURL(a.url);
    }, { width, url });
    const page = await waitFor(() => run.app.windows().find((p) => !seen.has(p) && !p.isClosed() && p.url() === url), 20_000, `the dev page ${url}`);
    await page.waitForLoadState('load');
    await waitFor(() => page.evaluate(() => document.querySelectorAll('.topbar .tab').length > 0), 10_000, 'the renderer drew');
    await sleep(300);
    return page;
  };
  const closeWin = () => run.app.evaluate(() => globalThis.__devWin.destroy());
  const bar = (page) => page.evaluate(() => {
    const tabs = [...document.querySelectorAll('.topbar .tab')];
    const more = document.querySelector('.more-btn');
    const b = document.querySelector('.topbar');
    return {
      total: tabs.length,
      shown: tabs.filter((x) => !x.hidden).length,
      selectedShown: tabs.some((x) => !x.hidden && x.getAttribute('aria-selected') === 'true'),
      moreText: more && !more.hidden ? more.textContent : null,
      overflow: b.scrollWidth > b.clientWidth + 1,
      settingsLabelShown: getComputedStyle(document.querySelector('.settings-label')).display !== 'none',
      settingsAria: document.querySelector('.settings-btn').getAttribute('aria-label'),
    };
  });

  for (const width of WIDTHS) {
    const page = await open(width, 'scenario=owner&tabs=7&view=hub');
    const r = await bar(page);
    assert.equal(r.total, 8, 'Hub plus 7 account tabs');
    assert.equal(r.overflow, false, `${width}: the bar does not overflow`);
    assert.equal(r.settingsAria, 'App settings');
    assert.equal(r.settingsLabelShown, width > 1180, `${width}: compact below 1180`);
    if (r.shown < r.total) {
      assert.match(r.moreText, new RegExp(`^${r.total - r.shown} more`), `${width}: ${JSON.stringify(r)}`);
      // the last tab is hidden: pick it from the menu
      await page.click('.more-btn');
      await page.waitForSelector('dialog[open] .more-item');
      const names = await page.$$eval('dialog[open] .more-item', (els) => els.map((e) => e.textContent));
      assert.equal(names.length, r.total - r.shown);
      assert.ok(names.includes('claude-7'), JSON.stringify(names));
      await page.screenshot({ path: path.join(out, `menu-${width}.png`) });
      await page.click('dialog[open] .more-item:last-child');
      await waitFor(() => page.evaluate(() => document.querySelector('.topbar .tab[aria-selected="true"]')?.textContent === 'claude-7'), 5000, 'claude-7 selected');
      const r2 = await bar(page);
      assert.equal(r2.selectedShown, true, 'the selected tab stays in the strip');
      assert.equal(r2.overflow, false);
    } else {
      assert.equal(r.moreText, null);
    }
    await page.screenshot({ path: path.join(out, `tabs-${width}.png`) });
    await closeWin();
  }

  // The progress line of a slow one-click pairing through SSH, then the board.
  const page = await open(1280, 'scenario=needs_pairing&ssh=1&slow=1500&view=hub');
  await page.click('button:has-text("Pair this computer")');
  await waitFor(() => page.evaluate(() => document.getElementById('content').innerText.includes("asking the hub's computer over SSH (can take 20 seconds)")), 3000, 'the progress text');
  assert.ok(await page.$('#content .spinner'), 'a spinner');
  await page.screenshot({ path: path.join(out, 'pairing.png') });
  await waitFor(() => page.evaluate(() => !document.getElementById('content').innerText.includes('Pairing…')), 10_000, 'pairing finished');
  await closeWin();
  const failed = await open(1280, 'scenario=needs_pairing&ssh=1&slow=300&fail=1&view=hub');
  await failed.click('button:has-text("Pair this computer")');
  await waitFor(() => failed.evaluate(() => document.getElementById('content').innerText.includes('ssh: connection timed out')), 5000, 'the error text');
  await closeWin();
});
