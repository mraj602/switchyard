// Package brand holds the project's name and the name it had before a rename, so the code
// that finds what an older version installed (folders, service units, the timer, the plugin,
// the SSH forward file) has one place to ask (docs/spec/rename.md).
//
// scripts/rename/rename.py rewrites Name and Slug. The Legacy constants carry the marker
// rename:keep, so they keep naming what existing installs have on disk. Before any rename the
// two pairs are equal and Renamed is false: every migration is then a no-op.
//
// Depends on: nothing inside the module.
package brand

import "strings"

const (
	// Name is the display name; Slug the lower-case name used in folder, unit, package and
	// plugin names.
	Name = "Switchyard"
	Slug = "switchyard"

	// CLI is the command's name: the binary (CLI, CLI.exe), the release archives
	// (CLI_<version>_<os>_<arch>), the plugin marketplace (CLI-local) and, upper-cased, the
	// prefix of the environment variables (SY_HOME, ...).
	CLI = "sy"

	// LegacyName, LegacySlug and LegacyCLI are the names of the last release before the
	// rename.
	LegacyName = "Switchyard" // rename:keep
	LegacySlug = "switchyard" // rename:keep
	LegacyCLI  = "sy"         // rename:keep
)

// EnvPrefix is the prefix of the environment variables `sy` reads ("SY_"), and Marketplace
// the name of the local Claude Code plugin marketplace `sy` writes ("sy-local").
const (
	EnvPrefix   = CLI + "_"
	Marketplace = CLI + "-local"
)

// The legacy names the code uses: the constants, except in tests (SetLegacyForTest), which
// exercise the migrations before any rename has happened.
var oldSlug, oldName, oldCLI = LegacySlug, LegacyName, LegacyCLI

// OldSlug and OldName are the legacy names in effect (LegacySlug and LegacyName).
func OldSlug() string { return oldSlug }
func OldName() string { return oldName }

// OldCLI is the command name in effect before the rename (LegacyCLI).
func OldCLI() string { return oldCLI }

// CLIRenamed says whether the command had another name before the rename.
func CLIRenamed() bool { return CLI != oldCLI }

// OldEnvPrefix is the environment-variable prefix of the old command name ("" when the
// command was not renamed).
func OldEnvPrefix() string {
	if !CLIRenamed() {
		return ""
	}
	return strings.ToUpper(oldCLI) + "_"
}

// LegacyMarketplace is the marketplace name before the rename of the command ("" when the
// command was not renamed).
func LegacyMarketplace() string {
	if !CLIRenamed() {
		return ""
	}
	return oldCLI + "-local"
}

// LegacyPluginIDs are the plugin ids (plugin, pane) an install from before a rename has
// enabled: the old project name in the old marketplace. Nil when nothing was renamed.
func LegacyPluginIDs() []string {
	if !Renamed() && !CLIRenamed() {
		return nil
	}
	m := Marketplace
	if CLIRenamed() {
		m = LegacyMarketplace()
	}
	return []string{oldSlug + "@" + m, oldSlug + "-pane@" + m}
}

// SetLegacyCLIForTest pretends the command was called cli before; tests only.
func SetLegacyCLIForTest(cli string) (restore func()) {
	p := oldCLI
	oldCLI = cli
	return func() { oldCLI = p }
}

// MigrateEnv copies every variable with the old prefix (SY_HOME, ...) to the current name
// when that is not set, so units, launchers and shells written for the old command keep their
// settings (docs/spec/rename.md 2.13). environ is os.Environ(); setenv is os.Setenv. It
// returns the names it set.
func MigrateEnv(environ []string, setenv func(k, v string) error) []string {
	op := OldEnvPrefix()
	if op == "" {
		return nil
	}
	have := map[string]bool{}
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		have[k] = true
	}
	var set []string
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, op) {
			continue
		}
		nk := EnvPrefix + strings.TrimPrefix(k, op)
		if have[nk] {
			continue
		}
		if setenv(nk, v) == nil {
			have[nk] = true
			set = append(set, nk)
		}
	}
	return set
}

// SetLegacyForTest pretends the project was called slug/name before, and returns a function
// that undoes it. Tests only.
func SetLegacyForTest(slug, name string) (restore func()) {
	ps, pn := oldSlug, oldName
	oldSlug, oldName = slug, name
	return func() { oldSlug, oldName = ps, pn }
}

// Renamed says whether there is an older name whose installs need migrating or reading.
func Renamed() bool { return Slug != oldSlug }

// Legacy returns s with its first Slug replaced by the legacy slug (and, when it has no Slug,
// its first Name by the legacy name): the name a thing had before the rename. It returns ""
// when the project was not renamed or s carries neither name, so callers can write
// `if l := brand.Legacy(x); l != "" { ... }`.
func Legacy(s string) string {
	if !Renamed() {
		return ""
	}
	if strings.Contains(s, Slug) {
		return strings.Replace(s, Slug, oldSlug, 1)
	}
	if strings.Contains(s, Name) {
		return strings.Replace(s, Name, oldName, 1)
	}
	return ""
}

// Current is the inverse of Legacy: s with its first legacy slug (else legacy name) replaced
// by the current one. It returns s unchanged when the project was not renamed or s carries
// neither legacy name (a ledger written by an older version names its service this way).
func Current(s string) string {
	if !Renamed() || strings.Contains(s, Slug) {
		return s
	}
	if strings.Contains(s, oldSlug) {
		return strings.Replace(s, oldSlug, Slug, 1)
	}
	if strings.Contains(s, oldName) {
		return strings.Replace(s, oldName, Name, 1)
	}
	return s
}
