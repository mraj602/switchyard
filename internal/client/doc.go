// Package client talks to the hub: `wy connect` (check the address, store the token)
// and `wy flush` (send outbox batches to POST /v1/ingest, advance the cursor on the
// reply, back off on errors). It never executes anything the hub sends.
//
// Contract: docs/spec/api.yaml, docs/spec/outbox.md section 5, docs/spec/tokens.md.
//
// Depends on: outbox, record, config, paths.
package client
