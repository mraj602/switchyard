// Package statusline implements `sy statusline --wrap <cmd>`: print the account label,
// 5-hour and 7-day use and context %, run and print the wrapped command, and write a
// limit_sample record at most once a minute per session when a value changes.
//
// Contract: docs/spec/record.md 4.2.
//
// Depends on: record, outbox, identity, config, paths.
package statusline
