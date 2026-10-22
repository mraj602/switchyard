// Package outbox is the local append-only queue of records waiting for the hub: one
// JSONL file per UTC day under <state>/outbox, appended under an exclusive file lock
// with one write per record, fsynced, plus a cursor of what the hub has answered.
//
// Contract: docs/spec/outbox.md.
//
// Depends on: record, paths.
package outbox
