// Package state is the session state engine: a pure fold of a session's records into
// working, needs_permission, asked, done, failed, ended or gone, plus the question
// detector (q2) and gone detection from session lists.
//
// Contract: docs/spec/states.md.
//
// The fold is pure (no I/O, no clock) and takes its own input types (Input, Hook, ListObs),
// so it does not import internal/record; an adapter turns records into Inputs. It is used by
// `sy status` on a machine and by the hub.
package state
