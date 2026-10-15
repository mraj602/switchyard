-- Version 10 (PE3): the `peer_thread_resume` link frame (peers.md 6.3, link.md 6.4).
--
-- link_queue.type cannot gain 'peer_thread_resume' in place (SQLite cannot change a CHECK), so
-- the queue is copied aside, dropped and made again with the same rows. Nothing references
-- link_queue, so no foreign key is ever violated (the runner keeps foreign_keys on).

CREATE TEMP TABLE m10_link_queue AS
  SELECT device_id, seq, type, tag, body, sig, exp, ref, created_at FROM link_queue;
DROP TABLE link_queue;

-- The durable queue: one sealed, signed frame per row until the device acks it (link.md 5).
CREATE TABLE link_queue (
  device_id     TEXT    NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  seq           INTEGER NOT NULL,
  type          TEXT    NOT NULL CHECK (type IN ('alert', 'refresh', 'peer_msg', 'peer_status', 'matrix', 'revoke', 'peer_thread_resume')),
  tag           TEXT    NOT NULL DEFAULT '' CHECK (length(tag) <= 64 AND tag NOT GLOB '*[^A-Za-z0-9_:.-]*'),   -- collapse key; '' never collapses
  body          BLOB    NOT NULL,                  -- sealed to the device (link.md 4)
  sig           BLOB    NOT NULL CHECK (length(sig) = 64),   -- hub signature, made at enqueue
  exp           INTEGER NOT NULL,
  ref           TEXT,                              -- 'd:<delivery id>' or 'pm:<peer message id>'
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (device_id, seq)
) STRICT, WITHOUT ROWID;
INSERT INTO link_queue (device_id, seq, type, tag, body, sig, exp, ref, created_at)
  SELECT device_id, seq, type, tag, body, sig, exp, ref, created_at FROM temp.m10_link_queue;
CREATE INDEX link_queue_tag ON link_queue(device_id, tag) WHERE tag <> '';
CREATE INDEX link_queue_exp ON link_queue(exp);

DROP TABLE temp.m10_link_queue;
