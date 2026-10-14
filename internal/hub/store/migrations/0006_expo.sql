-- Version 6 (phase 8): the Expo channel for the store app. push.md 12.

-- channel.kind has allowed 'expo' since v3; the row was never seeded.
INSERT INTO channel (kind, enabled, settings, updated_at) VALUES ('expo', 1, '{}', 0);

-- Per-target facts that are not secrets: {"platform": "ios", "app_version": "0.8.0", "app_build": "12"}.
ALTER TABLE push_target ADD COLUMN meta TEXT CHECK (meta IS NULL OR json_valid(meta));

-- Expo answers with a ticket; the receipt comes later (push.md 12.4).
ALTER TABLE delivery ADD COLUMN ticket TEXT;
ALTER TABLE delivery ADD COLUMN receipt_at INTEGER;
CREATE INDEX delivery_ticket_open ON delivery(created_at) WHERE ticket IS NOT NULL AND receipt_at IS NULL;
