-- Version 14: device_accounts (keys.md 4.3, peers.md 5.3): the owner-signed list of accounts
-- whose sessions a machine's device may speak for in peer messages.

CREATE TABLE device_accounts (
  device_id     TEXT    NOT NULL PRIMARY KEY REFERENCES device(id) ON DELETE CASCADE,
  body          BLOB    NOT NULL,                  -- machine_accounts body (JSON bytes, keys.md 4.3)
  owner_fp      TEXT    NOT NULL,
  owner_sig     BLOB    NOT NULL CHECK (length(owner_sig) = 64),
  created_at    INTEGER NOT NULL
) STRICT;
