-- Version 2 (phase 2): keep every state change of a session (states.md section 3, "History
-- and since"), including the ones no hook event caused (session_list repairs, gone), so the
-- session page can show its transition history.

CREATE TABLE session_transition (
  session_id    TEXT    NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  seq           INTEGER NOT NULL,                    -- 0, 1, 2… in fold order
  at            INTEGER NOT NULL,                    -- ts of the input that caused it
  from_state    TEXT,                                -- NULL for the first one
  to_state      TEXT    NOT NULL,
  detail        TEXT,
  cause         TEXT    NOT NULL,                    -- hook event name, 'session_list' or 'gone'
  record_id     TEXT,                                -- record that caused it
  PRIMARY KEY (session_id, seq)
) STRICT;
