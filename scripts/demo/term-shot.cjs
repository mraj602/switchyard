// Renders a recorded terminal session (raw pty bytes) to a PNG with xterm.js in headless Chrome.
//   node scripts/demo/term-shot.cjs <raw file> <out.png> [cols rows]
// NODE_PATH must hold @xterm/xterm and puppeteer-core. Used for docs/media/terminal-board.png:
// `wy board` run in a pty (100x28) against the mock hub's sample data.
const puppeteer = require('puppeteer-core');
const fs = require('fs');
const path = require('path');
const [raw, out, cols = '100', rows = '28'] = process.argv.slice(2);
const xt = path.dirname(require.resolve('@xterm/xterm/lib/xterm.js'));
const css = fs.readFileSync(path.join(xt, '..', 'css', 'xterm.css'), 'utf8');
const js = fs.readFileSync(path.join(xt, 'xterm.js'), 'utf8');
const data = fs.readFileSync(raw).toString('base64');
const html = `<!doctype html><html><head><meta charset="utf-8"><style>${css}
html,body{margin:0;background:#121416}
.win{margin:0;padding:18px 20px;background:#17191B;width:max-content}
</style></head><body><div class="win"><div id="t"></div></div><script>${js}</script><script>
const t=new Terminal({cols:${cols},rows:${rows},fontFamily:'Liberation Mono, monospace',fontSize:16,lineHeight:1.15,allowProposedApi:true,cursorBlink:false,
 theme:{background:'#17191B',foreground:'#e8e6e1',black:'#2a2d31',red:'#f07178',green:'#7fd18b',yellow:'#E8B04B',blue:'#6cb6ff',magenta:'#c792ea',cyan:'#5fd3c8',white:'#e8e6e1',brightBlack:'#8a877f'}});
t.open(document.getElementById('t'));
const b=Uint8Array.from(atob('${data}'),c=>c.charCodeAt(0));
t.write(b,()=>{document.title='done'});
</script></body></html>`;
(async () => {
  const b = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new', args: ['--no-sandbox'] });
  const p = await b.newPage();
  await p.setBypassCSP(true);
  await p.setViewport({ width: 1100, height: 700, deviceScaleFactor: 1 });
  await p.setContent(html);
  await p.waitForFunction('document.title==="done"');
  await new Promise((r) => setTimeout(r, 400));
  const el = await p.$('.win');
  await el.screenshot({ path: out });
  await b.close();
})();
