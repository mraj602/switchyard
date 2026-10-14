// Package usage is the usage model: it merges status-line and web limit samples per
// seat into windows (resets, peak, unused at reset, pace) and keeps raw web usage whole.
//
// Contract: docs/spec/schema.sql (limit_sample, web_usage, usage_window).
//
// Phase 2. Depends on: record, hub/store.
package usage
