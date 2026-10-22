-- Version 9: the account matrix, peer message metadata, threads and the audit log.
-- peers.md 10. Message text never reaches these tables: bodies are sealed to the receiving
-- machine and exist at the hub only inside link_queue while queued.

-- Every version of the signed account matrix (peers.md 5).
CREATE TABLE matrix_doc (
  version       INTEGER NOT NULL PRIMARY KEY,
  body          BLOB    NOT NULL,                  -- the signed JSON bytes
  signer        TEXT    NOT NULL CHECK (signer IN ('owner', 'hub')),
  key_fp        TEXT    NOT NULL,
  sig           BLOB    NOT NULL CHECK (length(sig) = 64),
  widens        INTEGER NOT NULL CHECK (widens IN (0, 1)),   -- compared with the version before it
  made_by       TEXT    NOT NULL,                  -- device id or 'cli'
  created_at    INTEGER NOT NULL
) STRICT;

-- One conversation between two sessions (peers.md 6.2). a_session opened it.
CREATE TABLE peer_thread (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 't_' + 16 hex
  a_session     TEXT    NOT NULL,
  b_session     TEXT    NOT NULL,
  a_account     TEXT    NOT NULL,                  -- account uuids
  b_account     TEXT    NOT NULL,
  opened_at     INTEGER NOT NULL,
  last_at       INTEGER NOT NULL,
  human_at      INTEGER,                           -- newest human prompt reported in the thread
  turns         INTEGER NOT NULL DEFAULT 0,        -- messages since human_at (loop guard)
  paused_at     INTEGER,                           -- the loop guard stopped it
  closed_at     INTEGER,
  CHECK (a_session <> b_session)
) STRICT;
CREATE INDEX peer_thread_open ON peer_thread(last_at) WHERE closed_at IS NULL;

-- One peer message, metadata only (peers.md 10).
CREATE TABLE peer_message (
  id            TEXT    NOT NULL PRIMARY KEY,      -- msg_id (uuid v4), also the inbox frame's msg_id
  thread_id     TEXT    NOT NULL,                  -- not an FK: the log outlives a pruned thread
  from_session  TEXT    NOT NULL,
  from_device   TEXT    NOT NULL,
  from_account  TEXT    NOT NULL,
  to_session    TEXT    NOT NULL,
  to_device     TEXT    NOT NULL,
  to_account    TEXT    NOT NULL,
  kind          TEXT    NOT NULL CHECK (kind IN ('start', 'reply')),
  size          INTEGER NOT NULL CHECK (size >= 0), -- body bytes before sealing
  local         INTEGER NOT NULL DEFAULT 0 CHECK (local IN (0, 1)),   -- same machine; reported by the agent
  status        TEXT    NOT NULL CHECK (status IN ('queued', 'delivered', 'refused', 'rejected', 'expired', 'target_gone', 'failed')),
  detail        TEXT,                              -- reason word: not_allowed, paused, loop_guard, bad_signature, …
  created_at    INTEGER NOT NULL,
  expires_at    INTEGER NOT NULL,
  final_at      INTEGER
) STRICT;
CREATE INDEX peer_message_thread ON peer_message(thread_id, created_at);
CREATE INDEX peer_message_created ON peer_message(created_at);

-- Everything else worth a line: matrix versions, pause, loop guard, refusals at a machine.
CREATE TABLE peer_audit (
  id            INTEGER NOT NULL PRIMARY KEY,
  at            INTEGER NOT NULL,
  actor         TEXT    NOT NULL,                  -- device id, 'hub' or 'cli'
  event         TEXT    NOT NULL,                  -- matrix, pause, resume, loop_guard, rejected, off, on, revoke
  thread_id     TEXT,
  msg_id        TEXT,
  detail        TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(detail))
) STRICT;
CREATE INDEX peer_audit_at ON peer_audit(at);
