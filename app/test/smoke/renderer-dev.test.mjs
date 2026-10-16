// Electron smoke S12 (docs/phases/6.md): the renderer's dev page (renderer/dev/index.html, the
// real renderer against dev/mock-sy.js) loaded in Electron under app://shell/ at 1440×940 and
// 720×500, with no console errors; screenshots are saved as artifacts.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';

import { sandbox, launch, waitFor, outDir, sleep } from './harness.mjs';

const SIZES = [[1440, 940], [720, 500]];
const SCENARIOS = ['default', 'empty', 'no_sy', 'needs_pairing', 'stopped', 'warning', 'removed', 'direct_pairing', 'unreachable', 'board_blocked', 'owner'];
// the Hub tab's own cards (and the read-only strip) are what these scenarios are about
const HUB_VIEW = new Set(['no_sy', 'needs_pairing', 'removed', 'direct_pairing', 'unreachable', 'board_blocked', 'default', 'owner']);

test('S12 renderer dev page at 1440×940 and 720×500', { timeout: 5 * 60_000 }, async (t) => {
  const sb = sandbox('s12');
  const out = outDir(sb.root);
  // No sy at all: the app shows its no-sy banner; the dev page does not need it.
  const run = await launch(sb, { env: { SY_BIN: path.join(sb.root, 'no-such-sy') } });
  t.after(async () => {
    await run.close();
    if (!process.env.SY_SMOKE_KEEP) sb.cleanup();
  });
  for (const scenario of SCENARIOS) {
    for (const [width, height] of SIZES) {
      const url = `app://shell/dev/index.html?scenario=${scenario}&view=${scenario === 'stopped' ? 't_0000000000000001' : HUB_VIEW.has(scenario) ? 'hub' : 'settings'}`;
      const seen = new Set(run.app.windows());
      await run.app.evaluate(({ BrowserWindow }, a) => {
        const w = new BrowserWindow({
          width: a.width, height: a.height, useContentSize: true, show: true,
          webPreferences: { contextIsolation: true, sandbox: true, nodeIntegration: false },
        });
        globalThis.__devWin = w;
        return w.loadURL(a.url);
      }, { width, height, url });
      const page = await waitFor(() => run.app.windows().find((p) => !seen.has(p) && !p.isClosed() && p.url() === url), 20_000, `the dev page ${url}`);
      await page.waitForLoadState('load');
      await waitFor(() => page.evaluate(() => document.getElementById('app')?.children.length > 0), 10_000, 'the renderer drew');
      await sleep(300);
      if (HUB_VIEW.has(scenario)) {
        // the Hub tab states of the target: the card text, never the board's login wording
        const text = await page.evaluate(() => document.getElementById('content').innerText);
        const want = {
          no_sy: /Connect to a hub\./, needs_pairing: /Pair this computer\./, removed: /This computer was removed from the hub\./,
          direct_pairing: /Pair this computer\./, unreachable: /Can't reach the hub\./, board_blocked: /could not be signed in/, default: /Read-only\. Sign in as owner/,
        }[scenario];
        if (want) assert.match(text, want, `${scenario}: ${text}`);
        assert.doesNotMatch(text, /owner-link|run sy hub/i, scenario);
        if (scenario === 'owner') assert.doesNotMatch(text, /Read-only\. Sign in as owner/);
      }
      await page.screenshot({ path: path.join(out, `s12-${scenario}-${width}x${height}.png`) });
      const size = await page.evaluate(() => [window.innerWidth, window.innerHeight]);
      assert.ok(Math.abs(size[0] - width) <= 2 && Math.abs(size[1] - height) <= 2, `viewport ${size} for ${width}x${height}`);
      await run.app.evaluate(() => globalThis.__devWin.destroy());
    }
  }
  // The real shell at both sizes too.
  for (const [width, height] of SIZES) {
    await run.app.evaluate(({ BrowserWindow }, a) => {
      const w = BrowserWindow.getAllWindows().find((x) => x.webContents.getURL().startsWith('app://shell/index.html'));
      w.setContentSize(a.width, a.height);
    }, { width, height });
    await sleep(300);
    await run.shell.screenshot({ path: path.join(out, `s12-shell-${width}x${height}.png`) });
  }
  assert.deepEqual(run.errors, [], 'no console errors');
});
