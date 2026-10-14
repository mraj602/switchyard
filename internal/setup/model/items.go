package model

// Switch is one sharing switch of setup.md section 3 step 3 (the `--share` names).
type Switch string

// The seven switches, in screen order.
const (
	SwitchSettings     Switch = "settings"
	SwitchInstructions Switch = "instructions"
	SwitchAddons       Switch = "addons"
	SwitchPlugins      Switch = "plugins"
	SwitchMCP          Switch = "mcp"
	SwitchHistory      Switch = "history"
	SwitchPrompts      Switch = "prompts"
)

// AllSwitches lists every switch in screen order.
var AllSwitches = []Switch{
	SwitchSettings, SwitchInstructions, SwitchAddons, SwitchPlugins,
	SwitchMCP, SwitchHistory, SwitchPrompts,
}

// ItemKind is whether a shareable item is a file or a directory.
type ItemKind string

// Item kinds.
const (
	KindFile ItemKind = "file"
	KindDir  ItemKind = "dir"
)

// ItemSpec is one row of the item table (setup.md 5.1) that is shared by linking.
type ItemSpec struct {
	Name   string   // path relative to the config folder, e.g. "skills"
	Kind   ItemKind // file or dir
	Switch Switch   // the switch that turns it on
	// Merge says what a `merge` resolution means: "lines" (history.jsonl only), "dir", or ""
	// (refused; use primary or folder). settings.json is never merged.
	Merge string
}

// ShareItems is the item table of setup.md 5.1 restricted to linkable items (the plugin root
// and mcpServers are handled separately; "plugins" is listed because an existing plugins/
// link is adopted). It is data: adding a row is a spec change. internal/share builds its own
// table from this one.
var ShareItems = []ItemSpec{
	{Name: "settings.json", Kind: KindFile, Switch: SwitchSettings},
	{Name: "CLAUDE.md", Kind: KindFile, Switch: SwitchInstructions},
	{Name: "rules", Kind: KindDir, Switch: SwitchInstructions, Merge: "dir"},
	{Name: "skills", Kind: KindDir, Switch: SwitchAddons, Merge: "dir"},
	{Name: "agents", Kind: KindDir, Switch: SwitchAddons, Merge: "dir"},
	{Name: "commands", Kind: KindDir, Switch: SwitchAddons, Merge: "dir"},
	{Name: "keybindings.json", Kind: KindFile, Switch: SwitchAddons},
	{Name: "plugins", Kind: KindDir, Switch: SwitchPlugins},
	{Name: "projects", Kind: KindDir, Switch: SwitchHistory, Merge: "dir"},
	{Name: "history.jsonl", Kind: KindFile, Switch: SwitchPrompts, Merge: "lines"},
	{Name: "plans", Kind: KindDir, Switch: SwitchPrompts, Merge: "dir"},
}

// NeverShared lists the names setup.md 5.1 says are never shared and never touched
// (`*.lock` files are covered by the caller; `.claude.json` is touched only at mcpServers).
var NeverShared = []string{
	".credentials.json", ".claude.json", "sessions", "daemon", "jobs", "file-history",
	"session-env", "shell-snapshots", "stats-cache.json", "backups", "cache", "paste-cache",
	"ide", "state", "tasks", "telemetry", "todos", "policy-limits.json", "remote-settings.json",
}

// ItemsFor returns the table rows controlled by switch s.
func ItemsFor(s Switch) []ItemSpec {
	var out []ItemSpec
	for _, it := range ShareItems {
		if it.Switch == s {
			out = append(out, it)
		}
	}
	return out
}

// Colour is a named palette entry.
type Colour struct {
	Name string
	Hex  string
}

// Palette is the colour palette of setup.md step 2, in order.
var Palette = []Colour{
	{"teal", "#14b8a6"}, {"amber", "#f59e0b"}, {"violet", "#8b5cf6"}, {"rose", "#f43f5e"},
	{"sky", "#0ea5e9"}, {"lime", "#84cc16"}, {"orange", "#f97316"}, {"slate", "#64748b"},
}
