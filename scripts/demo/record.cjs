// Records the README and website demo from the mock hub's sample data (no real data).
//
//   go run ./cmd/webui-dev --noauth &                          # mock hub on 127.0.0.1:8788
//   mkdir -p $TMP/pp && (cd $TMP/pp && npm init -y && npm i --ignore-scripts puppeteer-core)
//   NODE_PATH=$TMP/pp/node_modules OUT=$TMP/demo node scripts/demo/record.cjs
//   # The captions are a separate strip under the app (frames/f = app 1280x744, frames/c = caption
//   # 1280x56), stacked into 1280x800 so a caption never covers the app:
//   V="[0][1]vstack"
//   ffmpeg -framerate 12 -i $OUT/frames/f%04d.png -framerate 12 -i $OUT/frames/c%04d.png -filter_complex \
//     "$V,scale=960:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
//     -loop 0 docs/media/demo.gif
//   ffmpeg -framerate 12 -i $OUT/frames/f%04d.png -framerate 12 -i $OUT/frames/c%04d.png -filter_complex \
//     "[0][1]vstack,scale=1280:-2" -c:v libx264 -pix_fmt yuv420p -crf 26 -movflags +faststart site/media/demo.mp4
//   # poster: ffmpeg -i site/media/demo.mp4 -vf "select=eq(n\,26)" -frames:v 1 -q:v 4 site/media/demo-poster.jpg
//
// HUB (default http://127.0.0.1:8788) and CHROME (default /usr/bin/google-chrome) can be set.
const puppeteer = require('puppeteer-core');
const path = require('path');
const HUB = process.env.HUB || 'http://127.0.0.1:8788';
const OUT = process.env.OUT || path.join(require('os').tmpdir(), 'sy-demo');
const fs = require('fs');
fs.mkdirSync(path.join(OUT, 'frames'), { recursive: true });
const FPS = 12; let n = 0;
const AH = 744, CH = 56; // app area and caption strip: they stack to 1280x800, so a caption never covers the app
const name = (k, i) => path.join(OUT, 'frames', `${k}${String(i).padStart(4, '0')}.png`);
let cp = null; // the caption page
async function shot(p, f) { await p.bringToFront(); await p.screenshot({ path: name('f', f) }); await cp.bringToFront(); await cp.screenshot({ path: name('c', f) }); await p.bringToFront(); }
async function snap(p) { await shot(p, n++); }
async function hold(p, sec) { const f = n; await snap(p); for (let i = 1; i < sec * FPS; i++) { fs.linkSync(name('f', f), name('f', n)); fs.linkSync(name('c', f), name('c', n)); n++; } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const CAPHTML = `<!doctype html><meta charset="utf-8"><style>@font-face{font-family:Plex;font-weight:600;src:url(file://${path.resolve(__dirname, '../../webui/static/fonts/ibm-plex-sans-latin-600-normal.woff2')})}
html,body{margin:0;width:1280px;height:${CH}px;background:#17191B;border-top:1px solid #2a2d31;box-sizing:border-box}
#c{display:flex;align-items:center;justify-content:center;height:100%;color:#E8B04B;font:600 21px Plex,'IBM Plex Sans',system-ui,sans-serif}</style><div id="c"></div>`;
async function caption(p, text) { await cp.evaluate((t) => { document.getElementById('c').textContent = t; }, text); }
async function go(p, route, text) {
  await p.goto(HUB + '/#/' + route, { waitUntil: 'domcontentloaded' });
  await sleep(1500);
  await caption(p, text);
}
(async () => {
  const b = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new', userDataDir: path.join(OUT, 'profile') });
  const p = await b.newPage();
  await p.setBypassCSP(true);
  await p.setViewport({ width: 1280, height: AH });
  cp = await b.newPage();
  await cp.setViewport({ width: 1280, height: CH });
  await cp.setContent(CAPHTML);
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
  await p.goto('file://' + path.join(__dirname, 'phone.html')); await caption(p, 'An alert on your phone, and every account\'s limits in a widget'); await sleep(400);
  await p.evaluate(() => { n.style.top = '-200px'; w.style.opacity = 0; });
  await hold(p, 0.4);
  for (let i = 1; i <= 8; i++) { await p.evaluate((t) => { n.style.top = (-200 + 400 * (1 - Math.pow(1 - t, 3))) + 'px'; }, i / 8); await snap(p); }
  await hold(p, 1);
  for (let i = 1; i <= 6; i++) { await p.evaluate((t) => { w.style.opacity = t; }, i / 6); await snap(p); }
  await hold(p, 3);
  await b.close();
  console.log('frames', n);
})();
