// Records the README and website demo from the mock hub's sample data (no real data).
//
//   go run ./cmd/webui-dev --noauth &                          # mock hub on 127.0.0.1:8788
//   mkdir -p $TMP/pp && (cd $TMP/pp && npm init -y && npm i --ignore-scripts puppeteer-core)
//   NODE_PATH=$TMP/pp/node_modules OUT=$TMP/demo node scripts/demo/record.cjs
//   ffmpeg -framerate 12 -i $OUT/frames/f%04d.png -vf "scale=960:-1:flags=lanczos,split[a][b];\
//     [a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
//     -loop 0 docs/media/demo.gif
//   ffmpeg -framerate 12 -i $OUT/frames/f%04d.png -vf scale=1280:-2 -c:v libx264 -pix_fmt yuv420p -crf 26 \
//     -movflags +faststart site/media/demo.mp4
//
// HUB (default http://127.0.0.1:8788) and CHROME (default /usr/bin/google-chrome) can be set.
const puppeteer = require('puppeteer-core');
const path = require('path');
const HUB = process.env.HUB || 'http://127.0.0.1:8788';
const OUT = process.env.OUT || path.join(require('os').tmpdir(), 'sy-demo');
const fs = require('fs');
fs.mkdirSync(path.join(OUT, 'frames'), { recursive: true });
const FPS = 12; let n = 0;
const out = () => path.join(OUT, 'frames', `f${String(n++).padStart(4, '0')}.png`);
async function snap(p) { await p.screenshot({ path: out() }); }
async function hold(p, sec) { const f = out(); await p.screenshot({ path: f }); for (let i = 1; i < sec * FPS; i++) fs.linkSync(f, out()); }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const CAP = `#sy-cap{position:fixed;left:50%;bottom:22px;transform:translateX(-50%);background:#E8B04B;color:#17191B;font:600 18px 'IBM Plex Sans',system-ui,sans-serif;padding:10px 18px;border-radius:10px;box-shadow:0 8px 30px #0009;z-index:99999;white-space:nowrap}`;
async function caption(p, text) {
  await p.evaluate((t) => { let c = document.getElementById('sy-cap'); if (!c) { c = document.createElement('div'); c.id = 'sy-cap'; document.body.appendChild(c); } c.textContent = t; }, text);
}
async function go(p, route, text) {
  await p.goto(HUB + '/#/' + route, { waitUntil: 'domcontentloaded' });
  await sleep(1500);
  await p.addStyleTag({ content: CAP });
  await caption(p, text);
}
(async () => {
  const b = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new', userDataDir: path.join(OUT, 'profile') });
  const p = await b.newPage();
  await p.setBypassCSP(true);
  await p.setViewport({ width: 1280, height: 800 });
  await p.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'dark' }]);
  await go(p, '', 'Every session that needs you, across accounts and machines');
  await hold(p, 2.2);
  const main = await p.evaluateHandle(() => { const els = [...document.querySelectorAll('main, [role=main], .main, #main')]; return els.find((e) => e.scrollHeight > e.clientHeight + 10) || document.scrollingElement; });
  for (let i = 1; i <= 14; i++) { await main.evaluate((e, y) => { e.scrollTop = y; }, Math.round(380 * (1 - Math.cos(Math.PI * i / 14)) / 2)); await snap(p); }
  await caption(p, "Each account's 5-hour and 7-day limits, with reset times");
  await hold(p, 2);
  await go(p, 'sessions', 'Every session on every machine, live');
  await hold(p, 2.4);
  await go(p, 'accounts', 'Which account has room right now');
  await hold(p, 2);
  await go(p, 'more/peers', 'Sessions in different accounts can talk, only where you allow it');
  await hold(p, 2.2);
  await p.goto('file://' + path.join(__dirname, 'phone.html')); await sleep(400);
  await p.evaluate(() => { n.style.top = '-200px'; w.style.opacity = 0; });
  await hold(p, 0.4);
  for (let i = 1; i <= 8; i++) { await p.evaluate((t) => { n.style.top = (-200 + 400 * (1 - Math.pow(1 - t, 3))) + 'px'; }, i / 8); await snap(p); }
  await hold(p, 1);
  for (let i = 1; i <= 6; i++) { await p.evaluate((t) => { w.style.opacity = t; }, i / 6); await snap(p); }
  await hold(p, 3);
  await b.close();
  console.log('frames', n);
})();
