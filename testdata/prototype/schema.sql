CREATE TABLE snapshots (
  ts INTEGER NOT NULL, account_id TEXT NOT NULL, email TEXT, org_uuid TEXT, org_name TEXT, plan TEXT, raw TEXT NOT NULL);
CREATE INDEX snapshots_acct_ts ON snapshots(account_id, ts);
CREATE TABLE windows (
  ts INTEGER NOT NULL, account_id TEXT NOT NULL, key TEXT NOT NULL, label TEXT, pct REAL,
  resets_at TEXT, used_dollars REAL, limit_dollars REAL);
CREATE INDEX windows_acct_ts ON windows(account_id, ts);
CREATE TABLE breakdown (
  ts INTEGER NOT NULL, account_id TEXT NOT NULL, window_started_at TEXT, surface TEXT NOT NULL, percent REAL);
CREATE TABLE events (
  ts INTEGER NOT NULL, account_id TEXT, account TEXT, kind TEXT, text TEXT, cwd TEXT, session_id TEXT, raw TEXT, url TEXT);
CREATE INDEX events_ts ON events(ts);
CREATE TABLE sessions (
  session_id TEXT PRIMARY KEY, account_id TEXT, account TEXT, started_at INTEGER, cwd TEXT, transcript_path TEXT, model TEXT, remote_url TEXT);
CREATE TABLE accounts (
  id TEXT PRIMARY KEY, label TEXT, color TEXT, email TEXT, ord INTEGER, plan TEXT, updated INTEGER);
CREATE TABLE tokens (
  key TEXT PRIMARY KEY, ts INTEGER NOT NULL, session_id TEXT, project TEXT, model TEXT,
  input INTEGER, output INTEGER, cache_5m INTEGER, cache_1h INTEGER, cache_read INTEGER, thinking INTEGER,
  sidechain INTEGER, seq INTEGER);
CREATE INDEX tokens_ts ON tokens(ts);
CREATE INDEX tokens_session ON tokens(session_id);
CREATE INDEX tokens_seq ON tokens(seq);
CREATE TABLE session_dirs (session_id TEXT NOT NULL, config_dir TEXT NOT NULL, PRIMARY KEY (session_id, config_dir));
CREATE TABLE config_dirs (dir TEXT PRIMARY KEY, email TEXT, account_uuid TEXT, org_uuid TEXT);
CREATE TABLE session_owner (session_id TEXT PRIMARY KEY, account_uuid TEXT, org_uuid TEXT);
CREATE TABLE limit_hits (session_id TEXT NOT NULL, ts INTEGER NOT NULL, type TEXT, status TEXT, resets_at INTEGER, PRIMARY KEY (session_id, ts));
CREATE TABLE files (path TEXT PRIMARY KEY, size INTEGER, mtime INTEGER, offset INTEGER);
CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT);
CREATE UNIQUE INDEX snapshots_u ON snapshots(account_id, ts);
CREATE UNIQUE INDEX windows_u ON windows(account_id, ts, key);
CREATE UNIQUE INDEX breakdown_u ON breakdown(account_id, ts, surface);
CREATE UNIQUE INDEX events_u ON events(ts, coalesce(session_id,''), kind);
