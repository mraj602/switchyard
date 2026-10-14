-- Version 8 (N1): the link channel for the Android app (push.md 13).
--
-- channel.kind cannot gain 'link' in place (SQLite cannot change a CHECK) and push_target
-- references channel(kind), so both tables are copied aside, dropped and made again with the
-- same rows. Nothing references push_target, so dropping it first leaves channel without
-- children, and no foreign key is ever violated (the runner keeps foreign_keys on).

CREATE TEMP TABLE m8_channel AS SELECT kind, enabled, settings, updated_at FROM channel;
CREATE TEMP TABLE m8_push_target AS
  SELECT id, kind, device_id, name, endpoint, keys, secret, prefs, enabled, created_at, updated_at,
         last_ok_at, last_error, last_error_at, fail_count, gone_at, meta
  FROM push_target;
DROP TABLE push_target;
DROP TABLE channel;

-- Channel kinds with a hub-wide switch.
CREATE TABLE channel (
  kind          TEXT    NOT NULL PRIMARY KEY CHECK (kind IN ('webpush', 'ntfy', 'expo', 'link')),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  settings      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  updated_at    INTEGER NOT NULL
) STRICT;
INSERT INTO channel (kind, enabled, settings, updated_at)
  SELECT kind, enabled, settings, updated_at FROM temp.m8_channel;
INSERT INTO channel (kind, enabled, settings, updated_at) VALUES ('link', 1, '{}', 0);

-- One place a push goes: a browser subscription, an ntfy topic, an Expo token or a link device.
CREATE TABLE push_target (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'pt_' + ULID
  kind          TEXT    NOT NULL REFERENCES channel(kind),
  device_id     TEXT    REFERENCES device(id),     -- NULL: an ntfy topic added by the owner for the hub
  name          TEXT,                               -- "iPhone · Safari", "ntfy.sh/sy-4fQx…", "Pixel 9 · app"
  endpoint      TEXT    NOT NULL,                   -- webpush: endpoint URL; ntfy: server URL + '/' + topic; expo: token; link: 'link:' + device id
  keys          TEXT    CHECK (keys IS NULL OR json_valid(keys)),     -- webpush and link: {"p256dh": "…", "auth": "…"} base64url
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
  meta          TEXT    CHECK (meta IS NULL OR json_valid(meta)),     -- expo, link: {"platform", "app_version", "app_build"}
  UNIQUE (kind, endpoint)
) STRICT;
INSERT INTO push_target (id, kind, device_id, name, endpoint, keys, secret, prefs, enabled, created_at, updated_at,
                         last_ok_at, last_error, last_error_at, fail_count, gone_at, meta)
  SELECT id, kind, device_id, name, endpoint, keys, secret, prefs, enabled, created_at, updated_at,
         last_ok_at, last_error, last_error_at, fail_count, gone_at, meta
  FROM temp.m8_push_target;
CREATE INDEX push_target_device ON push_target(device_id);

DROP TABLE temp.m8_push_target;
DROP TABLE temp.m8_channel;

-- link: when the device acked the frame that carried this delivery (push.md 13.4).
ALTER TABLE delivery ADD COLUMN delivered_at INTEGER;
