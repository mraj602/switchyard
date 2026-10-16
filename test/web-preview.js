// Screenshot the self-hosted web UI: node test/web-preview.js <baseUrl> <token>  → PNGs in $OUT
const { chromium } = require('playwright-core');
const path = require('node:path');
const fs = require('node:fs');
const [base = 'http://127.0.0.1:8787', token] = process.argv.slice(2);
const out = process.env.OUT || fs.mkdtempSync('/tmp/sy-web-');
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: true,
    // no desktop session bus: the board's alerts must not pop up as real notifications
    env: { ...process.env, DBUS_SESSION_BUS_ADDRESS: 'unix:path=/nonexistent/sy-no-bus' } });
  const errs = [];
  for (const [name, vp] of [['desk', { width: 1440, height: 1000 }], ['phone', { width: 400, height: 860 }]]) {
    const ctx = await browser.newContext({ viewport: vp, deviceScaleFactor: name === 'phone' ? 2 : 1 });
    const page = await ctx.newPage();
    page.on('pageerror', (e) => errs.push(`${name}: ${e.message}`));
    page.on('console', (m) => { if (m.type() === 'error') errs.push(`${name} console: ${m.text()}`); });
    await page.goto(base);
    await page.waitForSelector('#token');
    await page.screenshot({ path: path.join(out, `${name}-login.png`) });
    await page.fill('#token', 'wrong-token-xxxxxxxx'); await page.click('button[type=submit]');
    await page.waitForFunction(() => document.querySelector('#login-err').textContent);
    await page.fill('#token', token); await page.click('button[type=submit]');
    await page.waitForSelector('.card');
    await page.screenshot({ path: path.join(out, `${name}-overview.png`), fullPage: true });
    await page.click('[data-view=analytics]');
    await page.waitForSelector('.an-kpis', { timeout: 60e3 });
    await page.waitForTimeout(800);
    await page.screenshot({ path: path.join(out, `${name}-analytics.png`), fullPage: true });
    await ctx.close();
  }
  await browser.close();
  console.log(JSON.stringify({ out, errors: errs }));
})().catch((e) => { console.error(e); process.exit(1); });
