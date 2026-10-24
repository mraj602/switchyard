// Fallback renderer for gen.sh: node render.cjs <svg> <size> <png>, through headless Chrome.
const puppeteer = require('puppeteer-core');
const fs = require('fs');
const [svg, size, png] = process.argv.slice(2);
(async () => {
  const b = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox'],
    env: { ...process.env, DBUS_SESSION_BUS_ADDRESS: 'unix:path=/nonexistent/wy-no-bus' } }); // no desktop session bus
  const p = await b.newPage();
  await p.setViewport({ width: +size, height: +size });
  const data = fs.readFileSync(svg).toString('base64');
  await p.setContent(`<body style="margin:0;background:transparent"><img width=${size} height=${size} src="data:image/svg+xml;base64,${data}">`);
  await p.screenshot({ path: png, omitBackground: true });
  await b.close();
})();
