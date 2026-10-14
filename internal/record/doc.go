// Package record defines the upload envelope and the seven record kinds
// (hook_event, limit_sample, web_usage, token_rows, session_list, history_import,
// slot_info), with ULID record ids.
//
// Contract: docs/spec/record.md. Types must keep unknown fields (raw JSON overflow) so
// that every record round-trips without losing a field.
//
// Phase 1. Depends on: nothing inside the module.
package record
