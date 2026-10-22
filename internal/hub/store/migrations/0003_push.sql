-- Version 3: pairing, owner sessions, push targets, rules, alerts, deliveries.
-- push.md section 9. The v1 placeholders for these tables were never written, so they are
-- recreated with their final shape.

DROP TABLE delivery;
DROP TABLE alert;
DROP TABLE rule;
DROP TABLE push_target;
DROP TABLE channel;
DROP TABLE pair_code;

-- Owner sessions expire (pairing.md 4); NULL = never.
ALTER TABLE device ADD COLUMN expires_at INTEGER;
-- Per-device push preferences (push.md 5.4).
ALTER TABLE device ADD COLUMN prefs TEXT CHECK (prefs IS NULL OR json_valid(prefs));

-- Secrets the hub makes for itself. Kept in the database so backups and moves keep push
-- subscriptions valid. (The pairing pepper is deliberately not here: pairing.md 2.1.)
CREATE TABLE hub_secret (
  key           TEXT    NOT NULL PRIMARY KEY,      -- vapid_private, action_key
  value         BLOB    NOT NULL,
  created_at    INTEGER NOT NULL
) STRICT;

-- One-time pairing codes and owner sign-in links (pairing.md 2 and 4).
CREATE TABLE pair_code (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'pc_' + ULID
  kind          TEXT    NOT NULL CHECK (kind IN ('machine', 'desktop', 'phone', 'browser', 'owner')),
  code_hash     BLOB    CHECK (code_hash IS NULL OR length(code_hash) = 32),   -- HMAC of the digits; NULL for owner links
  secret_hash   BLOB    NOT NULL UNIQUE CHECK (length(secret_hash) = 32),      -- HMAC of the QR/link secret
  name          TEXT,                               -- suggested device name
  machine_id    TEXT,                               -- optional, machine/desktop codes
  url           TEXT    NOT NULL,                   -- hub URL put in the QR
  created_at    INTEGER NOT NULL,
  created_by    TEXT    NOT NULL,                   -- 'cli', 'owner:<device id>'
  expires_at    INTEGER NOT NULL,
  claimed_at    INTEGER,
  cancelled_at  INTEGER,
  device_id     TEXT    REFERENCES device(id),
  claim_ip      TEXT
) STRICT;
CREATE INDEX pair_code_live ON pair_code(code_hash) WHERE claimed_at IS NULL AND cancelled_at IS NULL;

-- Channel kinds with a hub-wide switch.
CREATE TABLE channel (
  kind          TEXT    NOT NULL PRIMARY KEY CHECK (kind IN ('webpush', 'ntfy', 'expo')),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  settings      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  updated_at    INTEGER NOT NULL
) STRICT;
INSERT INTO channel (kind, enabled, settings, updated_at) VALUES
  ('webpush', 1, '{}', 0),
  ('ntfy', 1, '{}', 0);

-- One place a push goes: a browser subscription or an ntfy topic.
CREATE TABLE push_target (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'pt_' + ULID
  kind          TEXT    NOT NULL REFERENCES channel(kind),
  device_id     TEXT    REFERENCES device(id),     -- NULL: an ntfy topic added by the owner for the hub
  name          TEXT,                               -- "iPhone · Safari", "ntfy.sh/sy-4fQx…"
  endpoint      TEXT    NOT NULL,                   -- webpush: endpoint URL; ntfy: server URL + '/' + topic
  keys          TEXT    CHECK (keys IS NULL OR json_valid(keys)),     -- webpush: {"p256dh": "…", "auth": "…"} base64url
  secret        TEXT,                               -- ntfy access token; never returned by the API
  prefs         TEXT    CHECK (prefs IS NULL OR json_valid(prefs)),   -- only for targets without a device
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  last_ok_at    INTEGER,
  last_error    TEXT,
  last_error_at INTEGER,
  fail_count    INTEGER NOT NULL DEFAULT 0,
  gone_at       INTEGER,                            -- push service answered 404/410
  UNIQUE (kind, endpoint)
) STRICT;
CREATE INDEX push_target_device ON push_target(device_id);

-- Global rules: one base rule per trigger plus scoped overrides (push.md 3).
CREATE TABLE rule (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'rule_<trigger>' for base rules, 'rule_' + ULID for overrides
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  trigger       TEXT    NOT NULL,                   -- unknown triggers are ignored by older hubs
  base          INTEGER NOT NULL DEFAULT 0 CHECK (base IN (0, 1)),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  delay_s       INTEGER NOT NULL DEFAULT 0 CHECK (delay_s BETWEEN 0 AND 86400),
  bypass_quiet  INTEGER NOT NULL DEFAULT 0 CHECK (bypass_quiet IN (0, 1)),
  scope         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(scope)),   -- {"seats","projects","machines","windows"}
  updated_at    INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX rule_base ON rule(trigger) WHERE base = 1;
-- The owner row normally appears after the migrations; the seeded rules below need it now.
INSERT OR IGNORE INTO owner (id, created_at) VALUES ('owner', 0);
INSERT INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES
  ('rule_needs_permission', 'needs_permission', 1, 1,   30, 0, '{}', 0),
  ('rule_asked',            'asked',            1, 1,   60, 0, '{}', 0),
  ('rule_failed',           'failed',           1, 1,    0, 1, '{}', 0),
  ('rule_limit_80',         'limit_80',         1, 1,    0, 0, '{"windows":["five_hour","seven_day"]}', 0),
  ('rule_limit_95',         'limit_95',         1, 1,    0, 0, '{"windows":["five_hour","seven_day"]}', 0),
  ('rule_reset',            'reset',            1, 0,    0, 0, '{"windows":["five_hour","seven_day"]}', 0),
  ('rule_machine_silent',   'machine_silent',   1, 1, 3600, 0, '{}', 0);

-- One row per episode (push.md 3.2 and 4).
CREATE TABLE alert (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'al_' + ULID
  episode       TEXT    NOT NULL UNIQUE,            -- dedupe key
  trigger       TEXT    NOT NULL,
  rule_id       TEXT,                               -- rule in force at creation (not an FK: overrides can be deleted)
  session_id    TEXT    REFERENCES session(id),
  seat_id       TEXT    REFERENCES seat(id),
  machine_id    TEXT    REFERENCES machine(id),
  window_key    TEXT,
  status        TEXT    NOT NULL CHECK (status IN ('pending', 'fired', 'snoozed', 'seen', 'cancelled', 'closed', 'skipped')),
  reason        TEXT,                               -- answered, ended, superseded, stale, rule_off, remote_control, baseline, recovered, reset_passed, direct
  since_at      INTEGER,                            -- when the condition began (client ts for sessions)
  created_at    INTEGER NOT NULL,
  due_at        INTEGER,                            -- next scheduler action (pending, snoozed); NULL otherwise
  fired_at      INTEGER,                            -- first push
  fire_count    INTEGER NOT NULL DEFAULT 0,
  snoozed_until INTEGER,
  seen_at       INTEGER,
  seen_by       TEXT,                               -- device id or 'ntfy:<target id>'
  ended_at      INTEGER,                            -- when it became cancelled, closed or skipped
  facts         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(facts)),   -- text inputs at creation: labels, question, pct, resets_at, remote_url
  updated_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX alert_due ON alert(due_at) WHERE due_at IS NOT NULL;
CREATE INDEX alert_open ON alert(session_id) WHERE status IN ('pending', 'fired', 'snoozed', 'seen');
CREATE INDEX alert_created ON alert(created_at);

-- One attempt to bring one alert (fire) to one target.
CREATE TABLE delivery (
  id            INTEGER NOT NULL PRIMARY KEY,
  alert_id      TEXT    NOT NULL REFERENCES alert(id) ON DELETE CASCADE,
  target_id     TEXT    NOT NULL,                   -- not an FK: the log outlives a removed target
  target_kind   TEXT    NOT NULL,
  fire          INTEGER NOT NULL,                   -- alert.fire_count this belongs to
  batch         TEXT,                               -- 'pb_' + ULID; deliveries sent as one push share it
  status        TEXT    NOT NULL CHECK (status IN ('pending', 'held', 'sent', 'failed', 'skipped', 'gone')),
  reason        TEXT,                               -- muted, quiet, flood, resolved, seen, channel_off
  tries         INTEGER NOT NULL DEFAULT 0,
  next_at       INTEGER,                            -- pending: next try; held: release time
  http_status   INTEGER,
  last_error    TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE (alert_id, target_id, fire)
) STRICT;
CREATE INDEX delivery_next ON delivery(next_at) WHERE next_at IS NOT NULL;
CREATE INDEX delivery_batch ON delivery(batch);
