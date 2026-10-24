// Package paths resolves the config, state and data folders for each OS, including the
// WY_CONFIG_DIR / WY_STATE_DIR / WY_DATA_DIR / WY_HOME overrides. No other package
// builds paths from $HOME.
//
// Contract: docs/spec/paths.md.
//
// Depends on: nothing inside the module.
package paths
