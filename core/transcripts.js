// Token accounting from Claude Code's local transcripts (~/.claude*/projects/**/*.jsonl).
//
// Only `type:"assistant"` lines carry message.usage. One API response is written as several
// lines (one per content block, plus streaming snapshots whose output_tokens is still small),
// so rows are keyed by message.id and merged keeping the largest counts — summing raw lines
// overcounts ~2.4x. Subagent transcripts live in <project>/<session>/subagents/ and carry the
// parent's sessionId, so the whole tree is scanned. Files are read incrementally from the
// last offset; only complete lines are consumed.
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { tokenUpsert } = require('./db');

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const CHUNK = 16 << 20;
const MAX_LINE = 256 << 20;

// Claude Code config dirs: ~/.claude plus any ~/.claude-* / ~/.claude_* siblings (one per
// login), or an explicit list. Returns [{dir, email, projects}] with projects realpath'd so
// symlinked projects/ folders are scanned once.
function configDirs(home = os.homedir(), extra = []) {
  const names = new Set();
  try { for (const n of fs.readdirSync(home)) if (/^\.claude([-_].+)?$/.test(n)) names.add(path.join(home, n)); } catch {}
  for (const d of extra) names.add(d);
  const out = [];
  for (const dir of names) {
    let st; try { st = fs.statSync(dir); } catch { continue; }
    if (!st.isDirectory()) continue;
    const stateFile = path.resolve(dir) === path.join(home, '.claude') ? path.join(home, '.claude.json') : path.join(dir, '.claude.json');
    let acct = {};
    try { acct = JSON.parse(fs.readFileSync(stateFile, 'utf8')).oauthAccount || {}; } catch {}
    let projects = null;
    try { projects = fs.realpathSync(path.join(dir, 'projects')); } catch {}
    out.push({ dir, email: acct.emailAddress || null, accountUuid: acct.accountUuid || null, orgUuid: acct.organizationUuid || null, projects });
  }
  return out;
}

// Session ids each config dir has local state for (session-env/, file-history/, sessions/*.json).
// Those folders are per login even when projects/ is shared, so they attribute sessions that
// started before the SessionStart hook was installed.
function sessionIdsOf(dir) {
  const ids = new Set();
  for (const sub of ['session-env', 'file-history']) {
    try { for (const n of fs.readdirSync(path.join(dir, sub))) if (UUID.test(n)) ids.add(n); } catch {}
  }
  try {
    for (const n of fs.readdirSync(path.join(dir, 'sessions'))) {
      if (!n.endsWith('.json')) continue;
      try { const s = JSON.parse(fs.readFileSync(path.join(dir, 'sessions', n), 'utf8')); if (UUID.test(s.sessionId || '')) ids.add(s.sessionId); } catch {}
    }
  } catch {}
  return ids;
}

function* walk(dir) {
  let ents; try { ents = fs.readdirSync(dir, { withFileTypes: true }); } catch { return; }
  for (const e of ents) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) yield* walk(p);
    else if (e.isFile() && e.name.endsWith('.jsonl')) yield p;
  }
}

// One transcript line → {tok: row (db.tokenUpsert column order)} | {owner} | {hit} | null.
//   bridge-session lines (Remote Control) name the session's owning account and org;
//   synthetic assistant lines with quotaLimits are "You've hit your limit" responses.
function parseLine(line) {
  if (line.includes('"bridge-session"')) {
    let d; try { d = JSON.parse(line); } catch { return null; }
    if (d.type !== 'bridge-session' || !d.sessionId || !(d.ownerAccountUuid || d.ownerOrganizationUuid)) return null;
    return { owner: [d.sessionId, d.ownerAccountUuid || null, d.ownerOrganizationUuid || null] };
  }
  if (!line.includes('"usage"') || !line.includes('"assistant"')) return null;
  let d; try { d = JSON.parse(line); } catch { return null; }
  if (d.type !== 'assistant') return null;
  const m = d.message; const u = m && m.usage;
  if (m && m.model === '<synthetic>') {
    const ql = d.quotaLimits;
    const ts = Date.parse(d.timestamp);
    if (!ql || !ql.rateLimitType || !d.sessionId || !Number.isFinite(ts)) return null;
    return { hit: [d.sessionId, ts, ql.rateLimitType, ql.status || null, ql.resetsAt ? ql.resetsAt * 1000 : null] };
  }
  if (!u || typeof u !== 'object') return null;
  const key = m.id || d.requestId || d.uuid;
  const ts = Date.parse(d.timestamp);
  if (!key || !Number.isFinite(ts)) return null;
  const cc = u.cache_creation || {};
  let c5 = cc.ephemeral_5m_input_tokens, c1 = cc.ephemeral_1h_input_tokens;
  if (c5 == null && c1 == null) { c5 = u.cache_creation_input_tokens || 0; c1 = 0; }
  return { tok: [key, ts, d.sessionId || null, d.cwd || null, m.model || null,
    u.input_tokens || 0, u.output_tokens || 0, c5 || 0, c1 || 0, u.cache_read_input_tokens || 0,
    (u.output_tokens_details && u.output_tokens_details.thinking_tokens) || 0, d.isSidechain ? 1 : 0] };
}

// Scan all transcripts once. store: core/db handle. Returns {files, bytes, rows, ms}.
function scan(store, { home, extraDirs = [], onProgress } = {}) {
  const t0 = Date.now();
  const db = store.raw;
  const upsert = tokenUpsert(db);
  const getFile = db.prepare('SELECT size, mtime, offset FROM files WHERE path = ?');
  const setFile = db.prepare('INSERT INTO files (path, size, mtime, offset) VALUES (?,?,?,?) ON CONFLICT(path) DO UPDATE SET size=excluded.size, mtime=excluded.mtime, offset=excluded.offset');
  const setDir = db.prepare(`INSERT INTO config_dirs (dir, email, account_uuid, org_uuid) VALUES (?,?,?,?) ON CONFLICT(dir) DO UPDATE SET
    email=coalesce(excluded.email, config_dirs.email), account_uuid=coalesce(excluded.account_uuid, config_dirs.account_uuid), org_uuid=coalesce(excluded.org_uuid, config_dirs.org_uuid)`);
  const setOwner = db.prepare('INSERT OR IGNORE INTO session_owner (session_id, account_uuid, org_uuid) VALUES (?,?,?)');
  const addHit = db.prepare('INSERT OR IGNORE INTO limit_hits (session_id, ts, type, status, resets_at) VALUES (?,?,?,?,?)');
  const addSessDir = db.prepare('INSERT OR IGNORE INTO session_dirs (session_id, config_dir) VALUES (?,?)');

  const dirs = configDirs(home, extraDirs);
  store.tx(() => {
    for (const d of dirs) { setDir.run(d.dir, d.email, d.accountUuid, d.orgUuid); for (const id of sessionIdsOf(d.dir)) addSessDir.run(id, d.dir); }
  });

  const roots = [...new Set(dirs.map((d) => d.projects).filter(Boolean))];
  const todo = [];
  for (const r of roots) {
    for (const p of walk(r)) {
      let st; try { st = fs.statSync(p); } catch { continue; }
      const prev = getFile.get(p);
      const offset = prev && prev.offset <= st.size ? prev.offset : 0;
      if (prev && prev.size === st.size && prev.mtime === Math.floor(st.mtimeMs) && offset === st.size) continue;
      todo.push({ p, size: st.size, mtime: Math.floor(st.mtimeMs), offset });
    }
  }

  let bytes = 0, rows = 0, done = 0;
  let buf = Buffer.allocUnsafe(CHUNK);
  for (const f of todo) {
    let fd; try { fd = fs.openSync(f.p, 'r'); } catch { continue; }
    let off = f.offset;
    try {
      while (off < f.size) {
        const want = Math.min(buf.length, f.size - off);
        const n = fs.readSync(fd, buf, 0, want, off);
        if (n <= 0) break;
        let end = buf.lastIndexOf(10, n - 1);
        if (end < 0) {
          if (off + n >= f.size) break; // trailing partial line: wait for the rest
          if (buf.length < MAX_LINE) { buf = Buffer.allocUnsafe(buf.length * 2); continue; }
          off += n; continue; // absurdly long line: skip it
        }
        const lines = buf.toString('utf8', 0, end).split('\n');
        store.tx(() => {
          for (const l of lines) {
            const r = parseLine(l);
            if (!r) continue;
            if (r.tok) { upsert(r.tok); rows++; } else if (r.owner) setOwner.run(...r.owner); else if (r.hit) addHit.run(...r.hit);
          }
        });
        off += end + 1; bytes += end + 1;
      }
    } finally { fs.closeSync(fd); }
    setFile.run(f.p, f.size, f.mtime, off);
    done++;
    if (onProgress && (done % 50 === 0 || done === todo.length)) onProgress({ done, files: todo.length, bytes });
  }
  if (buf.length > CHUNK) buf = null;
  return { files: todo.length, bytes, rows, ms: Date.now() - t0 };
}

module.exports = { scan, parseLine, configDirs, sessionIdsOf };
