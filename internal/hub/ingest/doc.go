// Package ingest handles POST /v1/ingest: authenticate, validate each envelope, store
// every record raw and idempotently by id, reply with accepted, duplicate and rejected
// ids, then hand new records to the typed folds (state, usage).
//
// Contract: docs/spec/record.md section 5, docs/spec/api.yaml.
//
// Depends on: record, hub/store, hub/auth, hub/state, hub/usage.
package ingest
