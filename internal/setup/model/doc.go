// Package model holds the shared contract of `wy setup` (docs/spec/setup.md): the types the
// detector, planner, applier, sharer, plugin installer and service code pass to each other,
// and the interfaces that cross package boundaries. It contains data and tiny pure helpers
// only; behaviour lives in the packages that implement the interfaces.
//
// JSON tags are the wire format of `wy setup --json` (setup.md 6.3), the backup manifest
// (7.2) and the ledger (9.3). Optional fields use omitempty; fields the spec always prints
// have no omitempty. Golden files: testdata/setup/*.json.
//
// Stability: other packages code against this one. Add fields freely (always with
// omitempty and a doc comment); renaming or removing one changes the wire format.
//
// Contract: docs/spec/setup.md. Depends on: internal/claudecli (types only).
package model
