// Package status implements `sy status`: a local table of sessions with account, state
// and age, folded from the outbox with the same rules as the hub state engine
// (hub/state). From phase 2 it reads the hub when one is configured.
//
// Phase 1. Depends on: outbox, record, hub/state (the pure fold only), identity.
package status
