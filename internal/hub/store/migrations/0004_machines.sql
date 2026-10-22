-- Version 4: reach, machine info, backlog age; pull targets. machines.md section 9.

ALTER TABLE machine ADD COLUMN reach TEXT CHECK (reach IS NULL OR reach IN ('direct', 'forward', 'pull'));
ALTER TABLE machine ADD COLUMN record_v INTEGER;
ALTER TABLE machine ADD COLUMN held INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machine ADD COLUMN backlog_oldest INTEGER;          -- client ts of the oldest unanswered record
ALTER TABLE machine ADD COLUMN backlog_at INTEGER;              -- hub time backlog was reported
ALTER TABLE machine ADD COLUMN info TEXT CHECK (info IS NULL OR json_valid(info));  -- last machine_info payload
ALTER TABLE machine ADD COLUMN info_at INTEGER;

-- One remote the hub pulls over SSH (machines.md 3.4).
CREATE TABLE pull_target (
  id            TEXT    NOT NULL PRIMARY KEY,               -- 'pt_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  name          TEXT    NOT NULL UNIQUE,                    -- machine name, used by the CLI
  machine_id    TEXT    UNIQUE REFERENCES machine(id),      -- NULL until bound (first hello)
  device_id     TEXT    REFERENCES device(id),              -- the pull device; NULL until bound
  host          TEXT    NOT NULL,                           -- HostName
  port          INTEGER NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
  user          TEXT    NOT NULL,
  jump          TEXT,                                       -- ProxyJump, as typed
  remote_sy     TEXT    NOT NULL DEFAULT '.local/bin/sy',   -- used only without a forced command
  host_keys     TEXT    NOT NULL CHECK (json_valid(host_keys)),  -- ["ssh-ed25519 AAAA…", …] pinned public keys
  interval_s    INTEGER NOT NULL DEFAULT 60 CHECK (interval_s BETWEEN 15 AND 3600),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  state         TEXT    NOT NULL DEFAULT 'new'
                CHECK (state IN ('new', 'ok', 'failing', 'host_key', 'auth', 'refused', 'disabled')),
  created_at    INTEGER NOT NULL,
  last_try_at   INTEGER,
  last_ok_at    INTEGER,
  last_error    TEXT,                                       -- one line, no secrets
  fail_count    INTEGER NOT NULL DEFAULT 0,
  next_at       INTEGER                                      -- scheduler: next run
) STRICT;
CREATE INDEX pull_target_next ON pull_target(enabled, next_at);
