// Package push holds the alert engine and the notifiers (push.md) in its subpackages:
// model (shared types), clock, rules (pure logic), action (Snooze/Seen tokens), engine
// (reconcile, scheduler, fan-out), webpush and ntfy (senders), webpushtest and ntfytest
// (fakes). This package itself only carries the dependency rule test: nothing under
// hub/push imports hub/api, hub/cli, hub/pair or webui (ARCHITECTURE.md).
//
// Phase 4.
package push
