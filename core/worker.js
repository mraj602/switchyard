// Background worker (worker_threads): scans Claude Code transcripts, answers analytics
// queries, and pushes new rows to a self-hosted server. Runs off the main thread so a 3 GB
// first scan never stalls the desktop UI or the hook receiver. Used by the desktop app and
// by server/server.js.
//
// workerData: { dbPath, scan?: false, home?, extraDirs?, scanMs?, server?: {url, token} }
// messages in:  {type:'scan'} {type:'compute', id, q} {type:'server', server} {type:'sync'}
// messages out: {type:'scanned', ...} {type:'result', id, data|error} {type:'sync', ok, at, error?, sent?}
const { parentPort, workerData } = require('node:worker_threads');
const store = require('./db').open(workerData.dbPath);
const { scan } = require('./transcripts');
const { compute } = require('./analytics');

const post = (m) => parentPort.postMessage(m);
let scanning = false, again = false;
function doScan() {
  if (scanning) { again = true; return; }
  scanning = true;
  try {
    const r = scan(store, { home: workerData.home, extraDirs: workerData.extraDirs || [], onProgress: (p) => post({ type: 'progress', ...p }) });
    post({ type: 'scanned', ...r });
  } catch (e) { post({ type: 'error', where: 'scan', error: e.message }); }
  scanning = false;
  if (again) { again = false; setTimeout(doScan, 1000); }
}

// ---- push to a self-hosted server ----
let server = workerData.server || null;
let syncing = false;
async function sync() {
  if (!server || !server.url || !server.token || syncing) return;
  syncing = true;
  let sent = 0;
  try {
    for (let round = 0; round < 50; round++) {
      const cur = JSON.parse(store.kv.get(`sync:${server.url}`) || '{}');
      const p = store.changesSince(cur);
      const res = await fetch(new URL('/api/ingest', server.url), {
        method: 'POST',
        headers: { 'content-type': 'application/json', authorization: `Bearer ${server.token}` },
        body: JSON.stringify(p),
        signal: AbortSignal.timeout(30e3),
      });
      if (!res.ok) throw new Error(`server answered ${res.status}`);
      store.kv.set(`sync:${server.url}`, JSON.stringify(p.cursors));
      sent += ['snapshots', 'windows', 'breakdown', 'events', 'tokens'].reduce((s, t) => s + p[t].length, 0);
      if (!p.more) break;
    }
    post({ type: 'sync', ok: true, at: Date.now(), sent });
  } catch (e) { post({ type: 'sync', ok: false, at: Date.now(), error: e.message }); }
  syncing = false;
}

parentPort.on('message', (m) => {
  if (m.type === 'scan') doScan();
  else if (m.type === 'compute') {
    try { post({ type: 'result', id: m.id, data: compute(store, m.q || {}) }); } catch (e) { post({ type: 'result', id: m.id, error: e.message }); }
  } else if (m.type === 'server') { server = m.server; sync(); }
  else if (m.type === 'sync') sync();
});

if (workerData.scan !== false) {
  setTimeout(doScan, workerData.firstScanMs ?? 3000);
  setInterval(doScan, workerData.scanMs || 60e3);
}
setInterval(sync, 60e3);
