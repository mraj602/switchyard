// Usage store (SQLite via node:sqlite). Shared by the desktop app and the self-hosted server.
//
//   accounts     the tabs (id, label, color, email), mirrored so the server can label data
//   snapshots    one row per usage poll per account: the org's full raw usage JSON, so any
//                field the API adds later can be analysed retroactively
//   windows      normalised limit windows per poll (5-hour, 7-day, model/spend windows…)
//   breakdown    weekly usage split by surface (Claude Code / chat / cowork / other)
//   events       Claude Code hook events (permission prompts, questions, finished turns)
//   sessions     Claude Code session → account mapping (from SessionStart hooks)
//   tokens       one row per API response found in Claude Code transcripts (deduplicated)
//   session_dirs which config dir (~/.claude-N) knew a session, for attributing old sessions
//   config_dirs  config dir → login email / account uuid
//   session_owner session → owning account/org uuid (from Remote Control bridge lines)
//   limit_hits   "You've hit your limit" responses found in transcripts
//   files        transcript scan offsets, so each scan only reads what was appended
//
// Rows that are synced to a server have a natural unique key, so re-sending is harmless.
const { DatabaseSync } = require('node:sqlite');

const SCHEMA = `
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY, label TEXT, color TEXT, email TEXT, ord INTEGER, plan TEXT, updated INTEGER);
CREATE TABLE IF NOT EXISTS snapshots (
  ts INTEGER NOT NULL, account_id TEXT NOT NULL, email TEXT, org_uuid TEXT, org_name TEXT, plan TEXT, raw TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS snapshots_acct_ts ON snapshots(account_id, ts);
CREATE TABLE IF NOT EXISTS windows (
  ts INTEGER NOT NULL, account_id TEXT NOT NULL, key TEXT NOT NULL, label TEXT, pct REAL,
  resets_at TEXT, used_dollars REAL, limit_dollars REAL);
CREATE INDEX IF NOT EXISTS windows_acct_ts ON windows(account_id, ts);
CREATE TABLE IF NOT EXISTS breakdown (
  ts INTEGER NOT NULL, account_id TEXT NOT NULL, window_started_at TEXT, surface TEXT NOT NULL, percent REAL);
CREATE TABLE IF NOT EXISTS events (
  ts INTEGER NOT NULL, account_id TEXT, account TEXT, kind TEXT, text TEXT, cwd TEXT, session_id TEXT, raw TEXT);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE TABLE IF NOT EXISTS sessions (
  session_id TEXT PRIMARY KEY, account_id TEXT, account TEXT, started_at INTEGER, cwd TEXT, transcript_path TEXT, model TEXT);
CREATE TABLE IF NOT EXISTS tokens (
  key TEXT PRIMARY KEY, ts INTEGER NOT NULL, session_id TEXT, project TEXT, model TEXT,
  input INTEGER, output INTEGER, cache_5m INTEGER, cache_1h INTEGER, cache_read INTEGER, thinking INTEGER,
  sidechain INTEGER, seq INTEGER);
CREATE INDEX IF NOT EXISTS tokens_ts ON tokens(ts);
CREATE INDEX IF NOT EXISTS tokens_session ON tokens(session_id);
CREATE INDEX IF NOT EXISTS tokens_seq ON tokens(seq);
CREATE TABLE IF NOT EXISTS session_dirs (session_id TEXT NOT NULL, config_dir TEXT NOT NULL, PRIMARY KEY (session_id, config_dir));
CREATE TABLE IF NOT EXISTS config_dirs (dir TEXT PRIMARY KEY, email TEXT);
CREATE TABLE IF NOT EXISTS session_owner (session_id TEXT PRIMARY KEY, account_uuid TEXT, org_uuid TEXT);
CREATE TABLE IF NOT EXISTS limit_hits (session_id TEXT NOT NULL, ts INTEGER NOT NULL, type TEXT, status TEXT, resets_at INTEGER, PRIMARY KEY (session_id, ts));
CREATE TABLE IF NOT EXISTS files (path TEXT PRIMARY KEY, size INTEGER, mtime INTEGER, offset INTEGER);
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT);
`;
// Added after the first release. ALTERs fail harmlessly when the column exists; unique
// indexes drop accidental duplicates first so they can always be created.
const MIGRATIONS = [
  'ALTER TABLE events ADD COLUMN url TEXT',
  'ALTER TABLE sessions ADD COLUMN remote_url TEXT',
  'ALTER TABLE config_dirs ADD COLUMN account_uuid TEXT',
  'ALTER TABLE config_dirs ADD COLUMN org_uuid TEXT',
  'DELETE FROM snapshots WHERE rowid NOT IN (SELECT min(rowid) FROM snapshots GROUP BY account_id, ts)',
  'CREATE UNIQUE INDEX IF NOT EXISTS snapshots_u ON snapshots(account_id, ts)',
  'DELETE FROM windows WHERE rowid NOT IN (SELECT min(rowid) FROM windows GROUP BY account_id, ts, key)',
  'CREATE UNIQUE INDEX IF NOT EXISTS windows_u ON windows(account_id, ts, key)',
  'DELETE FROM breakdown WHERE rowid NOT IN (SELECT min(rowid) FROM breakdown GROUP BY account_id, ts, surface)',
  'CREATE UNIQUE INDEX IF NOT EXISTS breakdown_u ON breakdown(account_id, ts, surface)',
  "DELETE FROM events WHERE rowid NOT IN (SELECT min(rowid) FROM events GROUP BY ts, coalesce(session_id,''), kind)",
  "CREATE UNIQUE INDEX IF NOT EXISTS events_u ON events(ts, coalesce(session_id,''), kind)",
];

// Columns of each synced table, in insert order.
const SYNC = {
  snapshots: ['ts', 'account_id', 'email', 'org_uuid', 'org_name', 'plan', 'raw'],
  windows: ['ts', 'account_id', 'key', 'label', 'pct', 'resets_at', 'used_dollars', 'limit_dollars'],
  breakdown: ['ts', 'account_id', 'window_started_at', 'surface', 'percent'],
  events: ['ts', 'account_id', 'account', 'kind', 'text', 'cwd', 'session_id', 'raw', 'url'],
};

function open(filePath, { readOnly = false } = {}) {
  const db = new DatabaseSync(filePath, readOnly ? { readOnly: true } : {});
  if (!readOnly) {
    db.exec(SCHEMA);
    for (const m of MIGRATIONS) { try { db.exec(m); } catch {} }
  }

  const q = readOnly ? {} : {
    snap: db.prepare('INSERT OR IGNORE INTO snapshots VALUES (?,?,?,?,?,?,?)'),
    win: db.prepare('INSERT OR IGNORE INTO windows VALUES (?,?,?,?,?,?,?,?)'),
    brk: db.prepare('INSERT OR IGNORE INTO breakdown VALUES (?,?,?,?,?)'),
    ev: db.prepare('INSERT OR IGNORE INTO events (ts, account_id, account, kind, text, cwd, session_id, raw, url) VALUES (?,?,?,?,?,?,?,?,?)'),
    remote: db.prepare('UPDATE sessions SET remote_url = ? WHERE session_id = ?'),
    sess: db.prepare(`INSERT INTO sessions (session_id, account_id, account, started_at, cwd, transcript_path, model) VALUES (?,?,?,?,?,?,?)
      ON CONFLICT(session_id) DO UPDATE SET account_id=coalesce(excluded.account_id, sessions.account_id), account=excluded.account,
        cwd=coalesce(excluded.cwd, sessions.cwd), transcript_path=coalesce(excluded.transcript_path, sessions.transcript_path),
        model=coalesce(excluded.model, sessions.model)`),
    acct: db.prepare(`INSERT INTO accounts (id, label, color, email, ord, plan, updated) VALUES (?,?,?,?,?,?,?)
      ON CONFLICT(id) DO UPDATE SET label=excluded.label, color=excluded.color, email=coalesce(excluded.email, accounts.email),
        ord=excluded.ord, plan=coalesce(excluded.plan, accounts.plan), updated=excluded.updated`),
    recentEvents: db.prepare('SELECT ts, account_id AS accountId, account, kind, text, cwd, session_id AS session, url FROM events ORDER BY ts DESC LIMIT ?'),
    history: db.prepare('SELECT ts, account_id AS id, key, pct FROM windows WHERE ts >= ? ORDER BY ts'),
    kvGet: db.prepare('SELECT v FROM kv WHERE k = ?'),
    kvSet: db.prepare('INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v'),
  };

  const tx = (fn) => { db.exec('BEGIN'); try { const r = fn(); db.exec('COMMIT'); return r; } catch (e) { db.exec('ROLLBACK'); throw e; } };

  return {
    raw: db,
    tx,
    recordSnapshot(ts, accountId, u, rawUsage) {
      tx(() => {
        q.snap.run(ts, accountId, u.email ?? null, u.orgUuid ?? null, u.org ?? null, u.plan ?? null, JSON.stringify(rawUsage));
        for (const w of u.windows) q.win.run(ts, accountId, w.key, w.label, w.pct, w.resetsAt ?? null, w.usedDollars ?? null, w.limitDollars ?? null);
        if (u.breakdown) for (const r of u.breakdown.rows) q.brk.run(ts, accountId, u.breakdown.startedAt ?? null, r.key, r.percent);
      });
    },
    addEvent(e, raw) { q.ev.run(e.ts, e.accountId ?? null, e.account, e.kind, e.text, e.cwd, e.session ?? null, JSON.stringify(raw), e.url ?? null); },
    setRemoteUrl(sessionId, url) { q.remote.run(url, sessionId); },
    upsertSession(s) { q.sess.run(s.sessionId, s.accountId ?? null, s.account ?? null, s.ts ?? null, s.cwd ?? null, s.transcriptPath ?? null, s.model ?? null); },
    syncAccounts(list) {
      const now = Date.now();
      tx(() => list.forEach((a, i) => q.acct.run(a.id, a.label, a.color ?? null, a.email ?? null, a.ord ?? i, a.plan ?? null, now)));
    },
    recentEvents(n = 100) { return q.recentEvents.all(n).reverse(); },
    // [{ts, id, windows:[{key,pct}]}] for the dashboard trend lines
    history(sinceTs) {
      const out = new Map();
      for (const r of q.history.all(sinceTs)) {
        const k = `${r.id}|${r.ts}`;
        if (!out.has(k)) out.set(k, { ts: r.ts, id: r.id, windows: [] });
        out.get(k).windows.push({ key: r.key, pct: r.pct });
      }
      return [...out.values()];
    },
    kv: { get: (k) => { const r = q.kvGet.get(k); return r ? r.v : null; }, set: (k, v) => q.kvSet.run(k, String(v)) },

    // ---- sync (desktop → server) ----
    // Rows of each table added after the given rowid cursors, plus everything keyed.
    changesSince(cur = {}, limit = 5000) {
      const out = { cursors: { ...cur } };
      for (const [t, cols] of Object.entries(SYNC)) {
        const rows = db.prepare(`SELECT rowid AS _r, ${cols.join(',')} FROM ${t} WHERE rowid > ? ORDER BY rowid LIMIT ?`).all(cur[t] || 0, limit);
        if (rows.length) out.cursors[t] = rows[rows.length - 1]._r;
        out[t] = rows.map((r) => cols.map((c) => r[c]));
      }
      const tok = db.prepare('SELECT key, ts, session_id, project, model, input, output, cache_5m, cache_1h, cache_read, thinking, sidechain, seq FROM tokens WHERE seq > ? ORDER BY seq LIMIT ?').all(cur.tokens || 0, limit * 4);
      if (tok.length) out.cursors.tokens = tok[tok.length - 1].seq;
      out.tokens = tok.map((r) => [r.key, r.ts, r.session_id, r.project, r.model, r.input, r.output, r.cache_5m, r.cache_1h, r.cache_read, r.thinking, r.sidechain]);
      out.accounts = db.prepare('SELECT id, label, color, email, ord, plan FROM accounts').all().map((r) => ({ ...r }));
      out.sessions = db.prepare('SELECT session_id, account_id, account, started_at, cwd, transcript_path, model, remote_url FROM sessions').all().map((r) => ({ ...r }));
      out.sessionDirs = db.prepare('SELECT session_id, config_dir FROM session_dirs').all().map((r) => [r.session_id, r.config_dir]);
      out.configDirs = db.prepare('SELECT dir, email, account_uuid, org_uuid FROM config_dirs').all().map((r) => [r.dir, r.email, r.account_uuid, r.org_uuid]);
      out.owners = db.prepare('SELECT session_id, account_uuid, org_uuid FROM session_owner').all().map((r) => [r.session_id, r.account_uuid, r.org_uuid]);
      out.hits = db.prepare('SELECT session_id, ts, type, status, resets_at FROM limit_hits WHERE ts > ?').all(cur.hits || 0).map((r) => [r.session_id, r.ts, r.type, r.status, r.resets_at]);
      if (out.hits.length) out.cursors.hits = Math.max(...out.hits.map((h) => h[1]));
      out.more = Object.keys(SYNC).some((t) => out[t].length === limit) || tok.length === limit * 4;
      return out;
    },
    applyChanges(p) {
      tx(() => {
        for (const [t, cols] of Object.entries(SYNC)) {
          if (!Array.isArray(p[t]) || !p[t].length) continue;
          const st = db.prepare(`INSERT OR IGNORE INTO ${t} (${cols.join(',')}) VALUES (${cols.map(() => '?').join(',')})`);
          for (const r of p[t]) if (Array.isArray(r) && r.length === cols.length) st.run(...r);
        }
        if (Array.isArray(p.tokens)) { const up = tokenUpsert(db); for (const r of p.tokens) if (Array.isArray(r) && r.length === 12) up(r); }
        if (Array.isArray(p.accounts)) for (const a of p.accounts) q.acct.run(String(a.id), a.label ?? null, a.color ?? null, a.email ?? null, a.ord ?? 0, a.plan ?? null, Date.now());
        if (Array.isArray(p.sessions)) {
          for (const s of p.sessions) {
            q.sess.run(s.session_id, s.account_id ?? null, s.account ?? null, s.started_at ?? null, s.cwd ?? null, s.transcript_path ?? null, s.model ?? null);
            if (s.remote_url) q.remote.run(s.remote_url, s.session_id);
          }
        }
        if (Array.isArray(p.sessionDirs)) { const st = db.prepare('INSERT OR IGNORE INTO session_dirs VALUES (?,?)'); for (const r of p.sessionDirs) st.run(r[0], r[1]); }
        if (Array.isArray(p.configDirs)) { const st = db.prepare('INSERT INTO config_dirs (dir, email, account_uuid, org_uuid) VALUES (?,?,?,?) ON CONFLICT(dir) DO UPDATE SET email=excluded.email, account_uuid=excluded.account_uuid, org_uuid=excluded.org_uuid'); for (const r of p.configDirs) st.run(r[0], r[1] ?? null, r[2] ?? null, r[3] ?? null); }
        if (Array.isArray(p.owners)) { const st = db.prepare('INSERT OR IGNORE INTO session_owner VALUES (?,?,?)'); for (const r of p.owners) st.run(r[0], r[1] ?? null, r[2] ?? null); }
        if (Array.isArray(p.hits)) { const st = db.prepare('INSERT OR IGNORE INTO limit_hits VALUES (?,?,?,?,?)'); for (const r of p.hits) st.run(r[0], r[1], r[2] ?? null, r[3] ?? null, r[4] ?? null); }
      });
    },
    close() { db.close(); },
  };
}

// Insert-or-merge one deduplicated API response. Claude Code writes a response as several
// lines (one per content block, plus streaming snapshots); counts only ever grow, so the
// merge keeps the largest value of each. seq moves on every change so sync can follow it.
function tokenUpsert(db) {
  const st = db.prepare(`INSERT INTO tokens (key, ts, session_id, project, model, input, output, cache_5m, cache_1h, cache_read, thinking, sidechain, seq)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?, (SELECT coalesce(max(seq),0)+1 FROM tokens))
    ON CONFLICT(key) DO UPDATE SET
      input=max(input, excluded.input), output=max(output, excluded.output), cache_5m=max(cache_5m, excluded.cache_5m),
      cache_1h=max(cache_1h, excluded.cache_1h), cache_read=max(cache_read, excluded.cache_read), thinking=max(thinking, excluded.thinking),
      seq=excluded.seq
    WHERE excluded.input > input OR excluded.output > output OR excluded.cache_5m > cache_5m OR excluded.cache_1h > cache_1h
      OR excluded.cache_read > cache_read OR excluded.thinking > thinking`);
  return (r) => st.run(...r);
}

module.exports = { open, tokenUpsert };
