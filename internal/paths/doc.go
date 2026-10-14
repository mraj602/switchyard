// Package paths resolves the config, state and data folders for each OS, including the
// SY_CONFIG_DIR / SY_STATE_DIR / SY_DATA_DIR / SY_HOME overrides. No other package
// builds paths from $HOME.
//
// Contract: docs/spec/paths.md.
//
// Phase 1. Depends on: nothing inside the module.
package paths
