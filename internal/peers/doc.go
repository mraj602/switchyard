// Package peers is the local half of Switchyard's cross-session messaging (docs/spec/peers.md):
// the inbox writer and its wrapper builder (8.1, 8.5), the session directory (3), who is
// sending (4.1), content and rate limits (9), threads and the loop guard (6), the send
// pipeline (4.2), the evaluation of the signed matrix (5.3) and the envelopes of messages to
// other machines (8.2, 8.4). The link itself is reached through Remote.
//
// Everything about Claude Code's inbox that the spec marks uncertain (the wrapper bytes and
// escaping, the gate, the registry's procStart, process names) lives in a few clearly named
// constants and functions (wrapper.go, inbox.go, proc.go, registry.go) so the PE0 lab
// (peers.md 13) can adjust them in one place.
//
// Linux first; macOS where the spec says; Windows returns ErrUnsupported (peers.md 2.1).
//
// Phase PE1-PE2. Depends on: identity, atomicfile, keys, link.
package peers
