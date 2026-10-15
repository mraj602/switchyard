-- Version 11: the expo channel is gone (the store app's pushes through Expo's push service
-- never shipped). Pushes go over Web Push, ntfy and the Android app's own link.
--
-- Removed: the expo channel row and every expo push target, their deliveries, the Expo access
-- token (hub_secret expo_access_token), push_target.meta (only expo targets ever set it),
-- delivery.ticket/receipt_at and their index, and the self-hosted profile's one-time marker
-- (setting push.self_hosted_applied, which only switched the expo channel off). The push
-- setting expo_refresh_min is renamed refresh_min (widget refresh frames over the link).
--
-- channel.kind loses 'expo' (SQLite cannot change a CHECK) and push_target references
-- channel(kind), so both are copied aside, dropped and made again; delivery is made again
-- without its two expo columns. Nothing references push_target or delivery, so no foreign key
-- is ever violated (the runner keeps foreign_keys on).

DELETE FROM delivery WHERE target_kind = 'expo';
DELETE FROM push_target WHERE kind = 'expo';
DELETE FROM channel WHERE kind = 'expo';
DELETE FROM hub_secret WHERE key = 'expo_access_token';
DELETE FROM setting WHERE key = 'push.self_hosted_applied';
UPDATE setting
  SET value = CASE
    WHEN json_type(value, '$.refresh_min') IS NULL
      THEN json_remove(json_set(value, '$.refresh_min', json_extract(value, '$.expo_refresh_min')), '$.expo_refresh_min')
    ELSE json_remove(value, '$.expo_refresh_min') END
  WHERE key = 'push.settings' AND json_type(value) = 'object' AND json_type(value, '$.expo_refresh_min') IS NOT NULL;

CREATE TEMP TABLE m11_channel AS SELECT kind, enabled, settings, updated_at FROM channel;
CREATE TEMP TABLE m11_push_target AS
  SELECT id, kind, device_id, name, endpoint, keys, secret, prefs, enabled, created_at, updated_at,
         last_ok_at, last_error, last_error_at, fail_count, gone_at
  FROM push_target;
CREATE TEMP TABLE m11_delivery AS
  SELECT id, alert_id, target_id, target_kind, fire, batch, status, reason, tries, next_at, http_status,
         last_error, created_at, updated_at, delivered_at
  FROM delivery;
DROP TABLE push_target;
DROP TABLE channel;
DROP TABLE delivery;

-- Channel kinds with a hub-wide switch.
CREATE TABLE channel (
  kind          TEXT    NOT NULL PRIMARY KEY CHECK (kind IN ('webpush', 'ntfy', 'link')),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  settings      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  updated_at    INTEGER NOT NULL
) STRICT;
INSERT INTO channel (kind, enabled, settings, updated_at)
  SELECT kind, enabled, settings, updated_at FROM temp.m11_channel;

-- One place a push goes: a browser subscription, an ntfy topic or a link device.
CREATE TABLE push_target (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'pt_' + ULID
  kind          TEXT    NOT NULL REFERENCES channel(kind),
  device_id     TEXT    REFERENCES device(id),     -- NULL: an ntfy topic added by the owner for the hub
  name          TEXT,                               -- "iPhone · Safari", "ntfy.sh/sy-4fQx…", "Pixel 9 · app"
  endpoint      TEXT    NOT NULL,                   -- webpush: endpoint URL; ntfy: server URL + '/' + topic; link: 'link:' + device id
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
  UNIQUE (kind, endpoint)
) STRICT;
INSERT INTO push_target (id, kind, device_id, name, endpoint, keys, secret, prefs, enabled, created_at, updated_at,
                         last_ok_at, last_error, last_error_at, fail_count, gone_at)
  SELECT id, kind, device_id, name, endpoint, keys, secret, prefs, enabled, created_at, updated_at,
         last_ok_at, last_error, last_error_at, fail_count, gone_at
  FROM temp.m11_push_target;
CREATE INDEX push_target_device ON push_target(device_id);

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
  delivered_at  INTEGER,                            -- link only: when the phone acked the frame that carried it
  UNIQUE (alert_id, target_id, fire)
) STRICT;
INSERT INTO delivery (id, alert_id, target_id, target_kind, fire, batch, status, reason, tries, next_at, http_status,
                      last_error, created_at, updated_at, delivered_at)
  SELECT id, alert_id, target_id, target_kind, fire, batch, status, reason, tries, next_at, http_status,
         last_error, created_at, updated_at, delivered_at
  FROM temp.m11_delivery;
CREATE INDEX delivery_next ON delivery(next_at) WHERE next_at IS NOT NULL;
CREATE INDEX delivery_batch ON delivery(batch);

DROP TABLE temp.m11_delivery;
DROP TABLE temp.m11_push_target;
DROP TABLE temp.m11_channel;
