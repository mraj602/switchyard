// Package api serves the read API and the live stream: /v1/overview, /v1/sessions,
// /v1/accounts, /v1/machines, /v1/stream (server-sent events) and /health, and mounts
// ingest and the embedded web board.
//
// Contract: docs/spec/api.yaml.
//
// Phase 2. Depends on: hub/store, hub/auth, hub/ingest, hub/state, hub/usage, webui.
package api
