// Package auth checks bearer tokens (SHA-256 lookup), scopes and revocation, and
// implements `sy hub token create|list|revoke`.
//
// Contract: docs/spec/tokens.md.
//
// Phase 2. Depends on: hub/store.
package auth
