// Package brand holds the project's names, so code that spells a folder, unit, package, plugin
// or environment-variable name has one place to ask.
//
// Depends on: nothing inside the module.
package brand

const (
	// Name is the display name; Slug the lower-case name used in folder, unit, package and
	// plugin names.
	Name = "Watchyard"
	Slug = "watchyard"

	// CLI is the command's name: the binary (CLI, CLI.exe), the release archives
	// (CLI_<version>_<os>_<arch>), the plugin marketplace (CLI-local) and, upper-cased, the
	// prefix of the environment variables (WY_HOME, ...).
	CLI = "wy"
)

// EnvPrefix is the prefix of the environment variables `wy` reads ("WY_"), and Marketplace
// the name of the local Claude Code plugin marketplace `wy` writes ("wy-local").
const (
	EnvPrefix   = "WY_" // strings.ToUpper(CLI) + "_"; a constant cannot call ToUpper (brand_test checks)
	Marketplace = CLI + "-local"
)
