-- Version 7: the owner key, device keys and certificates, revocations and the link queue.
-- keys.md 9, link.md 5. The hub signing key is not here: it lives in <state>/hub_sign.key.

-- The owner key's public half (keys.md 3). At most one is active; a key proposed by a device
-- waits as 'pending' until an owner session or the hub's CLI confirms it. On confirmation the
-- hub signs the document too (hub_sig); the hub and the devices use a key only with both
-- signatures, so a row written straight into the database is ignored.
CREATE TABLE owner_key (
  fp            TEXT    NOT NULL PRIMARY KEY,      -- keys.md 2: 24 base32 characters
  pub           BLOB    NOT NULL CHECK (length(pub) = 32),   -- Ed25519 public key (copy of the body's)
  body          BLOB    NOT NULL,                  -- the owner_key document body, as signed (keys.md 4.1)
  self_sig      BLOB    NOT NULL CHECK (length(self_sig) = 64),
  hub_sig       BLOB    CHECK (hub_sig IS NULL OR length(hub_sig) = 64),   -- the hub's confirmation (keys.md 3.3)
  state         TEXT    NOT NULL CHECK (state IN ('pending', 'active', 'retired')),
  proposed_by   TEXT    NOT NULL,                  -- device id, or 'cli' (hub machine)
  created_at    INTEGER NOT NULL,
  confirmed_at  INTEGER,
  confirmed_by  TEXT,                              -- owner session device id, or 'cli'
  retired_at    INTEGER,
  CHECK ((state = 'pending') = (hub_sig IS NULL))
) STRICT;
CREATE UNIQUE INDEX owner_key_active ON owner_key(state) WHERE state = 'active';

-- One device's link keys and its certificate (keys.md 4). The certificate body is kept as the
-- exact bytes that were signed; the columns beside it are copies for queries. The hub takes
-- keys only from cert after checking hub_sig (link.md 3.2), never from the copies.
CREATE TABLE device_key (
  device_id     TEXT    NOT NULL PRIMARY KEY REFERENCES device(id) ON DELETE CASCADE,
  sign_alg      TEXT    NOT NULL CHECK (sign_alg IN ('ed25519', 'p256')),
  sign_pub      BLOB    NOT NULL,                  -- 32 bytes (ed25519) or 65 bytes SEC1 uncompressed (p256)
  box_alg       TEXT    NOT NULL CHECK (box_alg IN ('x25519', 'p256')),
  box_pub       BLOB    NOT NULL,                  -- 32 bytes (x25519) or 65 bytes (p256, RFC 8291 p256dh)
  reg           TEXT    NOT NULL CHECK (reg IN ('qr', 'tofu', 'ssh', 'local')),
  cert          BLOB    NOT NULL,                  -- certificate body (JSON bytes, keys.md 4.2)
  hub_sig       BLOB    NOT NULL CHECK (length(hub_sig) = 64),
  owner_fp      TEXT,                              -- set once the owner key certified it
  owner_sig     BLOB    CHECK (owner_sig IS NULL OR length(owner_sig) = 64),
  created_at    INTEGER NOT NULL,
  certified_at  INTEGER,
  CHECK ((owner_fp IS NULL) = (owner_sig IS NULL))
) STRICT;

-- Every version of the signed revocation list (keys.md 6). The list only grows.
CREATE TABLE revocation_doc (
  version       INTEGER NOT NULL PRIMARY KEY,
  body          BLOB    NOT NULL,                  -- the signed JSON bytes
  signer        TEXT    NOT NULL CHECK (signer IN ('owner', 'hub')),
  key_fp        TEXT    NOT NULL,
  sig           BLOB    NOT NULL CHECK (length(sig) = 64),
  made_by       TEXT    NOT NULL,                  -- device id or 'cli'
  created_at    INTEGER NOT NULL
) STRICT;

-- Per-device position on the link (link.md 5).
CREATE TABLE link_state (
  device_id     TEXT    NOT NULL PRIMARY KEY REFERENCES device(id) ON DELETE CASCADE,
  next_seq      INTEGER NOT NULL DEFAULT 1 CHECK (next_seq >= 1),
  acked_seq     INTEGER NOT NULL DEFAULT 0 CHECK (acked_seq >= 0),
  transport     TEXT    CHECK (transport IS NULL OR transport IN ('ws', 'poll')),
  connected_at  INTEGER,                           -- current connection; NULL while disconnected
  last_seen_at  INTEGER,                           -- last frame, ping or poll
  last_close    TEXT,                              -- close code and word: '4409 superseded'
  last_ip       TEXT,
  hello         TEXT    CHECK (hello IS NULL OR json_valid(hello))   -- last hello's app/sy version and known doc versions
) STRICT;

-- The durable queue: one sealed, signed frame per row until the device acks it (link.md 5).
CREATE TABLE link_queue (
  device_id     TEXT    NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  seq           INTEGER NOT NULL,
  type          TEXT    NOT NULL CHECK (type IN ('alert', 'refresh', 'peer_msg', 'peer_status', 'matrix', 'revoke')),
  tag           TEXT    NOT NULL DEFAULT '' CHECK (length(tag) <= 64 AND tag NOT GLOB '*[^A-Za-z0-9_:.-]*'),   -- collapse key; '' never collapses
  body          BLOB    NOT NULL,                  -- sealed to the device (link.md 4)
  sig           BLOB    NOT NULL CHECK (length(sig) = 64),   -- hub signature, made at enqueue
  exp           INTEGER NOT NULL,
  ref           TEXT,                              -- 'd:<delivery id>' or 'pm:<peer message id>'
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (device_id, seq)
) STRICT, WITHOUT ROWID;
CREATE INDEX link_queue_tag ON link_queue(device_id, tag) WHERE tag <> '';
CREATE INDEX link_queue_exp ON link_queue(exp);

-- Owner sessions now store HMAC-SHA256(pair_pepper, "token:" + token) instead of SHA-256
-- (tokens.md 2, keys.md 5.4); a session made before cannot be found any more, so it is
-- revoked here. Sign in again with a new `sy hub owner-link`.
UPDATE device SET revoked_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
  WHERE created_by LIKE 'owner-link:%' AND revoked_at IS NULL;
