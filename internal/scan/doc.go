// Package scan implements `sy scan` and `sy agents`: token rows, limit hits and
// Remote Control owners from transcripts (a port of the prototype's core/transcripts.js
// and core/pricing.js), session lists from `claude agents --json`, and the one-time
// history_import of stats-cache.json.
//
// Contract: docs/spec/record.md 4.4 to 4.6.
//
// Depends on: record, outbox, identity, paths.
package scan
