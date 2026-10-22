-- Switchyard hub schema, version 1.
--
-- One SQLite file (WAL mode). Applied by the hub at start inside one transaction
-- after `PRAGMA foreign_keys = ON`. Later versions add numbered migrations; this file is
-- always the full current schema, and a test checks that migrations from v1 produce it.
--
-- Conventions
--   * Times are INTEGER milliseconds since the Unix epoch, UTC (`*_at`, `ts`).
--   * Ids made by clients are TEXT (record ULIDs, Claude session uuids, machine ids).
--     Ids made by the hub are TEXT with a prefix + ULID (`dev_…`, `seat_…`).
--   * JSON is stored as TEXT and checked with json_valid().
--   * Every table is STRICT. Booleans are INTEGER 0/1 with a CHECK.
--   * `owner_id` is on the tables a future shared hub could partition.
--     It is always 'owner' on a single-user hub.
--   * Driver: modernc.org/sqlite (pure Go, no cgo). Needs SQLite >= 3.37 for STRICT.
--
-- Groups: meta, identity, access, activity, usage, alerts.

PRAGMA foreign_keys = ON;

---------------------------------------------------------------------------- meta

CREATE TABLE schema_version (
  version     INTEGER NOT NULL PRIMARY KEY,
  applied_at  INTEGER NOT NULL,
  sy_version  TEXT    NOT NULL                      -- hub binary version that applied it
) STRICT;

INSERT INTO schema_version (version, applied_at, sy_version)
VALUES (1, CAST(unixepoch('subsec') * 1000 AS INTEGER), 'spec');

-- Hub-wide settings that are not secrets (retention days, last-message policy, …).
CREATE TABLE setting (
  key         TEXT    NOT NULL PRIMARY KEY,
  value       TEXT    NOT NULL CHECK (json_valid(value)),
  updated_at  INTEGER NOT NULL
) STRICT;

---------------------------------------------------------------------------- identity

-- The one owner of a single-user hub. Owner login is by passkey or password.
CREATE TABLE owner (
  id             TEXT    NOT NULL PRIMARY KEY,      -- 'owner'
  name           TEXT,
  password_hash  TEXT,                               -- argon2id PHC string; NULL until set
  created_at     INTEGER NOT NULL
) STRICT;

-- One Claude login.
CREATE TABLE account (
  uuid          TEXT    NOT NULL PRIMARY KEY,       -- oauthAccount.accountUuid / claude.ai account uuid
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  email         TEXT,
  display_name  TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL
) STRICT;
CREATE INDEX account_email ON account(email);

-- Who the limits belong to: a personal plan or a Team/Enterprise org.
CREATE TABLE org (
  uuid          TEXT    NOT NULL PRIMARY KEY,       -- oauthAccount.organizationUuid / claude.ai org uuid
  name          TEXT,
  plan          TEXT,                                -- normalised: free, pro, max_5x, max_20x, team, enterprise, unknown
  rate_tier     TEXT,                                -- raw organizationRateLimitTier / rate_limit_tier
  billing_type  TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL
) STRICT;

-- One account inside one org. Every usage number, card, label and colour hangs off this.
CREATE TABLE seat (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'seat_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  account_uuid  TEXT    NOT NULL REFERENCES account(uuid),
  org_uuid      TEXT    NOT NULL REFERENCES org(uuid),
  label         TEXT,                                -- "work"
  colour        TEXT    CHECK (colour IS NULL OR colour GLOB '#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'),
  sort_order    INTEGER NOT NULL DEFAULT 0,
  hidden        INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1)),
  track_web     INTEGER NOT NULL DEFAULT 1 CHECK (track_web IN (0, 1)),
  created_at    INTEGER NOT NULL,
  UNIQUE (account_uuid, org_uuid)
) STRICT;

-- A computer running sy. The id is made by sy on first run (identity.md).
CREATE TABLE machine (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'm_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  name          TEXT    NOT NULL,                    -- defaults to the hostname
  os            TEXT,                                -- linux, darwin, windows (GOOS)
  arch          TEXT,
  sy_version    TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER,                             -- receive time of its newest record
  last_record   INTEGER,                             -- client ts of its newest record
  backlog       INTEGER,                             -- outbox records queued, as last reported
  removed_at    INTEGER
) STRICT;

-- One config folder on one machine.
CREATE TABLE slot (
  id            INTEGER NOT NULL PRIMARY KEY,
  machine_id    TEXT    NOT NULL REFERENCES machine(id) ON DELETE CASCADE,
  folder        TEXT    NOT NULL,                    -- '~/.claude-2'
  real_path     TEXT,
  is_default    INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
  launcher      TEXT,                                -- 'claude-2'
  claude_version TEXT,
  sharing       TEXT    CHECK (sharing IS NULL OR json_valid(sharing)),
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL,
  removed_at    INTEGER,
  UNIQUE (machine_id, folder)
) STRICT;

-- Which seat a slot was logged in to, and when. Periods never overlap per slot;
-- the open period has until_at NULL.
CREATE TABLE slot_login (
  slot_id       INTEGER NOT NULL REFERENCES slot(id) ON DELETE CASCADE,
  since_at      INTEGER NOT NULL,                    -- ts of the first record that showed this login
  seat_id       TEXT    REFERENCES seat(id),         -- NULL = logged out during this period
  until_at      INTEGER,
  source_record TEXT    NOT NULL,                    -- record id that opened the period
  PRIMARY KEY (slot_id, since_at),
  CHECK (until_at IS NULL OR until_at >= since_at)
) STRICT;
CREATE UNIQUE INDEX slot_login_open ON slot_login(slot_id) WHERE until_at IS NULL;
CREATE INDEX slot_login_seat ON slot_login(seat_id);

-- One account tab in the desktop app, joined to a seat once signed in.
CREATE TABLE web_seat (
  tab_id        TEXT    NOT NULL,
  machine_id    TEXT    NOT NULL REFERENCES machine(id) ON DELETE CASCADE,
  seat_id       TEXT    REFERENCES seat(id),
  label         TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL,
  stopped_reason TEXT,                               -- 401/403/429 stop (decision 1)
  PRIMARY KEY (machine_id, tab_id)
) STRICT;

---------------------------------------------------------------------------- access

-- One device = one token. See tokens.md.
CREATE TABLE device (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'dev_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  kind          TEXT    NOT NULL CHECK (kind IN ('machine', 'desktop', 'phone', 'browser')),
  name          TEXT    NOT NULL,
  machine_id    TEXT    REFERENCES machine(id),      -- required for machine and desktop
  token_hash    BLOB    NOT NULL UNIQUE CHECK (length(token_hash) = 32),  -- SHA-256 of the full token string
  token_hint    TEXT    NOT NULL,                    -- 'sy_m_…k3Qz' (prefix + last 4) for display
  scopes        TEXT    NOT NULL CHECK (json_valid(scopes)),              -- '["write"]'
  created_at    INTEGER NOT NULL,
  created_by    TEXT    NOT NULL,                    -- 'cli', 'pair:<code id>', 'setup'
  last_used_at  INTEGER,
  last_used_ip  TEXT,
  revoked_at    INTEGER,
  CHECK (kind NOT IN ('machine', 'desktop') OR machine_id IS NOT NULL)
) STRICT;
CREATE INDEX device_machine ON device(machine_id);

-- One-time pairing codes. The code itself is never stored, only its hash.
CREATE TABLE pair_code (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'pc_' + ULID
  code_hash     BLOB    NOT NULL UNIQUE CHECK (length(code_hash) = 32),
  kind          TEXT    NOT NULL CHECK (kind IN ('machine', 'desktop', 'phone', 'browser')),
  created_at    INTEGER NOT NULL,
  expires_at    INTEGER NOT NULL,
  attempts      INTEGER NOT NULL DEFAULT 0,
  claimed_at    INTEGER,
  device_id     TEXT    REFERENCES device(id)
) STRICT;

-- Where pushes go for a phone or browser.
CREATE TABLE channel (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'ch_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  kind          TEXT    NOT NULL CHECK (kind IN ('ntfy', 'webpush', 'expo', 'desktop')),
  name          TEXT,
  settings      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),  -- ntfy URL, private mode…
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  created_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE push_target (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'pt_' + ULID
  device_id     TEXT    NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  channel_id    TEXT    NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
  endpoint      TEXT    NOT NULL,                    -- web push endpoint / Expo push token
  keys          TEXT    CHECK (keys IS NULL OR json_valid(keys)),          -- web push p256dh + auth
  created_at    INTEGER NOT NULL,
  last_ok_at    INTEGER,
  failed_at     INTEGER,
  UNIQUE (channel_id, endpoint)
) STRICT;

---------------------------------------------------------------------------- activity

-- Every upload, exactly as received. The typed tables below are rebuilt from this.
CREATE TABLE record (
  id            TEXT    NOT NULL PRIMARY KEY,       -- client-made ULID / uuid (record.md 3.1)
  device_id     TEXT    NOT NULL REFERENCES device(id),
  machine_id    TEXT    NOT NULL,                    -- envelope machine (not an FK: kept even if the machine is removed)
  kind          TEXT    NOT NULL,
  v             INTEGER NOT NULL,
  ts            INTEGER NOT NULL,                    -- client time
  received_at   INTEGER NOT NULL,                    -- hub time
  slot          TEXT,                                -- envelope slot, as sent
  body          TEXT    NOT NULL CHECK (json_valid(body)),  -- the whole record, every field
  typed         INTEGER NOT NULL DEFAULT 0 CHECK (typed IN (0, 1)),  -- 1 once folded into typed tables
  type_error    TEXT                                 -- why typing failed, retried on replay
) STRICT;
CREATE INDEX record_kind_ts ON record(kind, ts);
CREATE INDEX record_machine_ts ON record(machine_id, ts);
CREATE INDEX record_untyped ON record(received_at) WHERE typed = 0;

-- One Claude Code conversation.
CREATE TABLE session (
  id              TEXT    NOT NULL PRIMARY KEY,     -- Claude's session_id
  seat_id         TEXT    REFERENCES seat(id),       -- from the first record that named a seat; kept for life
  slot_id         INTEGER REFERENCES slot(id),
  machine_id      TEXT    REFERENCES machine(id),
  cwd             TEXT,
  project         TEXT,                              -- basename of cwd, for display
  name            TEXT,                              -- session_name / agents "name"
  title           TEXT,
  kind            TEXT,                              -- interactive, background
  transcript_path TEXT,
  remote_url      TEXT,                              -- Remote Control link
  term            TEXT    CHECK (term IS NULL OR json_valid(term)),  -- latest record.term
  started_at      INTEGER,
  ended_at        INTEGER,
  end_reason      TEXT,                              -- SessionEnd reason, or 'gone'
  last_event_at   INTEGER,
  last_listed_at  INTEGER,                           -- last complete session_list that listed it
  missed_lists    INTEGER NOT NULL DEFAULT 0,        -- complete lists since then that did not
  context_pct     REAL,
  model           TEXT
) STRICT;
CREATE INDEX session_seat ON session(seat_id, last_event_at);
CREATE INDEX session_machine ON session(machine_id, last_event_at);
CREATE INDEX session_slot ON session(slot_id);

-- Current state only (states.md). History is in hook_event.
CREATE TABLE session_state (
  session_id    TEXT    NOT NULL PRIMARY KEY REFERENCES session(id) ON DELETE CASCADE,
  state         TEXT    NOT NULL CHECK (state IN ('working', 'needs_permission', 'asked', 'done', 'failed', 'ended', 'gone')),
  since_at      INTEGER NOT NULL,
  detail        TEXT,                              -- notification type, failure reason, 'started', …
  question      TEXT,                              -- question line, when state = asked and allowed
  needs_you     INTEGER NOT NULL CHECK (needs_you IN (0, 1)),
  last_event    TEXT,                              -- hook event name that set it
  last_record   TEXT,                              -- record id that set it
  detector      TEXT,                              -- question detector version, e.g. 'q1'
  updated_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX session_state_needs ON session_state(needs_you, since_at) WHERE needs_you = 1;

CREATE TABLE hook_event (
  record_id     TEXT    NOT NULL PRIMARY KEY REFERENCES record(id) ON DELETE CASCADE,
  session_id    TEXT    NOT NULL REFERENCES session(id),
  event         TEXT    NOT NULL,                    -- hook_event_name
  subtype       TEXT,                                -- notification_type / SessionStart source / SessionEnd reason / StopFailure error
  tool_name     TEXT,
  ts            INTEGER NOT NULL,
  last_message  TEXT,                                -- derived.last_message, if allowed
  state_after   TEXT                                 -- state the engine set after this event
) STRICT;
CREATE INDEX hook_event_session_ts ON hook_event(session_id, ts);
CREATE INDEX hook_event_ts ON hook_event(ts);

-- One API response from transcripts, merged by keeping the largest counts.
CREATE TABLE token_row (
  key           TEXT    NOT NULL PRIMARY KEY,       -- API message id
  session_id    TEXT    NOT NULL,                    -- not an FK: rows can arrive before the session's hook events
  seat_id       TEXT    REFERENCES seat(id),         -- resolved from the session; NULL = Unattributed
  ts            INTEGER NOT NULL,
  project       TEXT,
  model         TEXT,
  input         INTEGER NOT NULL DEFAULT 0,
  output        INTEGER NOT NULL DEFAULT 0,
  cache_5m      INTEGER NOT NULL DEFAULT 0,
  cache_1h      INTEGER NOT NULL DEFAULT 0,
  cache_read    INTEGER NOT NULL DEFAULT 0,
  thinking      INTEGER NOT NULL DEFAULT 0,
  sidechain     INTEGER NOT NULL DEFAULT 0 CHECK (sidechain IN (0, 1)),
  record_id     TEXT    NOT NULL                     -- last record that raised a count
) STRICT;
CREATE INDEX token_row_ts ON token_row(ts);
CREATE INDEX token_row_session ON token_row(session_id);
CREATE INDEX token_row_seat_ts ON token_row(seat_id, ts);

-- "You've hit your limit" replies found in transcripts.
CREATE TABLE limit_hit (
  session_id    TEXT    NOT NULL,
  ts            INTEGER NOT NULL,
  type          TEXT,
  status        TEXT,
  resets_at     INTEGER,
  seat_id       TEXT    REFERENCES seat(id),
  PRIMARY KEY (session_id, ts)
) STRICT;

-- Claude Code's own daily totals from before Switchyard (history_import).
CREATE TABLE history_day (
  slot_id       INTEGER NOT NULL REFERENCES slot(id) ON DELETE CASCADE,
  day           TEXT    NOT NULL CHECK (day GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
  model         TEXT    NOT NULL,                    -- '' for activity-only rows
  seat_id       TEXT    REFERENCES seat(id),
  tokens        INTEGER,
  messages      INTEGER,
  sessions      INTEGER,
  tool_calls    INTEGER,
  record_id     TEXT    NOT NULL,
  PRIMARY KEY (slot_id, day, model)
) STRICT;

---------------------------------------------------------------------------- usage

-- One limit reading: from the status line (per session) or from claude.ai (per seat).
CREATE TABLE limit_sample (
  seat_id       TEXT    NOT NULL REFERENCES seat(id),
  window_key    TEXT    NOT NULL,                    -- five_hour, seven_day, seven_day_opus, weekly_fable, iguana_necktie, …
  ts            INTEGER NOT NULL,
  source        TEXT    NOT NULL CHECK (source IN ('statusline', 'web')),
  used_pct      REAL,
  resets_at     INTEGER,
  used_dollars  REAL,
  limit_dollars REAL,
  session_id    TEXT,                                -- statusline only
  record_id     TEXT    NOT NULL,
  PRIMARY KEY (seat_id, window_key, ts, source)
) STRICT;
CREATE INDEX limit_sample_ts ON limit_sample(ts);

-- The full claude.ai usage response per seat per poll. Every field kept (iguana_necktie…).
CREATE TABLE web_usage (
  seat_id       TEXT    NOT NULL REFERENCES seat(id),
  ts            INTEGER NOT NULL,
  tab_id        TEXT,
  raw           TEXT    NOT NULL CHECK (json_valid(raw)),  -- payload.usage, whole
  record_id     TEXT    NOT NULL,
  PRIMARY KEY (seat_id, ts)
) STRICT;

-- One limit window per seat: built from limit_sample by the usage model.
CREATE TABLE usage_window (
  seat_id       TEXT    NOT NULL REFERENCES seat(id),
  window_key    TEXT    NOT NULL,
  resets_at     INTEGER NOT NULL,
  started_at    INTEGER,                             -- resets_at minus the window length, or first sample
  peak_pct      REAL,
  final_pct     REAL,                                -- last sample before reset
  unused_pct    REAL,                                -- 100 - final_pct, once reset has passed
  samples       INTEGER NOT NULL DEFAULT 0,
  alerted_80    INTEGER NOT NULL DEFAULT 0 CHECK (alerted_80 IN (0, 1)),
  alerted_95    INTEGER NOT NULL DEFAULT 0 CHECK (alerted_95 IN (0, 1)),
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (seat_id, window_key, resets_at)
) STRICT;

-- Rebuilt on demand from token_row (and history_day for older days).
CREATE TABLE rollup_daily (
  day           TEXT    NOT NULL,                    -- local day of the hub's configured time zone
  seat_id       TEXT    NOT NULL DEFAULT '',         -- '' = Unattributed
  model         TEXT    NOT NULL DEFAULT '',
  project       TEXT    NOT NULL DEFAULT '',
  input         INTEGER NOT NULL DEFAULT 0,
  output        INTEGER NOT NULL DEFAULT 0,
  cache_5m      INTEGER NOT NULL DEFAULT 0,
  cache_1h      INTEGER NOT NULL DEFAULT 0,
  cache_read    INTEGER NOT NULL DEFAULT 0,
  replies       INTEGER NOT NULL DEFAULT 0,
  api_usd       REAL    NOT NULL DEFAULT 0,          -- API list-price equivalent
  built_at      INTEGER NOT NULL,
  PRIMARY KEY (day, seat_id, model, project)
) STRICT;

---------------------------------------------------------------------------- alerts

CREATE TABLE rule (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'rule_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  trigger       TEXT    NOT NULL,                    -- needs_permission, asked, failed, limit_80, limit_95, reset, machine_silent, context_90
  delay_s       INTEGER NOT NULL DEFAULT 0,
  scope         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(scope)),     -- {"seats":[…],"projects":[…]}
  quiet_hours   TEXT    CHECK (quiet_hours IS NULL OR json_valid(quiet_hours)),
  channels      TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(channels)),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  updated_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE alert (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'al_' + ULID
  rule_id       TEXT    NOT NULL REFERENCES rule(id),
  session_id    TEXT    REFERENCES session(id),
  seat_id       TEXT    REFERENCES seat(id),
  machine_id    TEXT    REFERENCES machine(id),
  dedupe_key    TEXT    NOT NULL,
  created_at    INTEGER NOT NULL,
  due_at        INTEGER NOT NULL,
  fired_at      INTEGER,
  cancelled_at  INTEGER,
  cancel_reason TEXT,
  snoozed_until INTEGER,
  seen_at       INTEGER
) STRICT;
CREATE INDEX alert_due ON alert(due_at) WHERE fired_at IS NULL AND cancelled_at IS NULL;
CREATE UNIQUE INDEX alert_dedupe ON alert(dedupe_key);

CREATE TABLE delivery (
  alert_id      TEXT    NOT NULL REFERENCES alert(id) ON DELETE CASCADE,
  channel_id    TEXT    NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
  status        TEXT    NOT NULL CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
  tries         INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (alert_id, channel_id)
) STRICT;
