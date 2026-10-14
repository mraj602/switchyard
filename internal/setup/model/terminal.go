package model

// Terminal pieces of phase 7 (docs/spec/terminal.md T10): what `sy term setup`,
// `sy term titles|warp on|off`, `sy term off` and `sy setup --terminal` ask for, and what the
// ledger records once they are applied.

// StepTerminal holds the terminal actions of T10 except the hooks-plugin refresh (a
// write_tree in StepPlugin) and the terminal.titles config_set (StepConfig): the
// `/env/CLAUDE_CODE_DISABLE_TERMINAL_TITLE` json_set/json_delete per settings target, the
// `claude plugin …` runs for switchyard-pane and Warp's plugin, and the tab-config mkdir and
// write_file actions.
const StepTerminal Step = "terminal"

// Warp channels (T8). The channel decides the folder and the URI scheme.
const (
	WarpStable  = "stable"  // warp://
	WarpPreview = "preview" // warppreview://
)

// TermChoices are the answers that, with a State, determine a terminal Plan
// (plan.Terminal). With Off false the chosen pieces are turned on; with Off true they are
// turned off (planned in reverse from Ledger.Terminal). `sy term off` is Off with every piece.
type TermChoices struct {
	Pane       bool `json:"pane,omitempty"`        // switchyard-pane@sy-local in every settings target
	WarpPlugin bool `json:"warp_plugin,omitempty"` // warp@claude-code-warp in every settings target
	Titles     bool `json:"titles,omitempty"`      // sy hook --title + CLAUDE_CODE_DISABLE_TERMINAL_TITLE
	WarpTabs   bool `json:"warp_tabs,omitempty"`   // switchyard-<label>.toml tab configs
	// Channel is WarpStable or WarpPreview ("" = stable); it matters only for WarpTabs.
	Channel string `json:"channel,omitempty"`
	Off     bool   `json:"off,omitempty"`
}

// Any reports whether at least one piece is chosen.
func (c TermChoices) Any() bool { return c.Pane || c.WarpPlugin || c.Titles || c.WarpTabs }

// LedgerTerminal is the ledger's `terminal` block (setup.md 9.3, terminal.md T10): what the
// terminal pieces have installed now. `sy term … off` and `sy uninstall` plan the reverse
// from it. A nil piece is not installed.
type LedgerTerminal struct {
	Pane       *LedgerTermTargets `json:"pane,omitempty"`
	WarpPlugin *LedgerWarpPlugin  `json:"warp_plugin,omitempty"`
	Titles     *LedgerTermTargets `json:"titles,omitempty"`
	Tabs       []LedgerTab        `json:"tabs,omitempty"`
	Warp       *LedgerWarp        `json:"warp,omitempty"`
	// TitlePrior maps a settings target to the raw JSON value its
	// env.CLAUDE_CODE_DISABLE_TERMINAL_TITLE had before `sy term titles on` set it to "1";
	// `titles off` puts that value back instead of deleting the key. It outlives the titles
	// piece (an undo of `titles off` needs it again) and is dropped when an undo of `titles
	// on` puts the value back itself.
	TitlePrior map[string]string `json:"title_prior,omitempty"`
}

// Empty reports whether nothing terminal-side is recorded (the block is then dropped).
func (t *LedgerTerminal) Empty() bool {
	return t == nil || (t.Pane == nil && t.WarpPlugin == nil && t.Titles == nil && len(t.Tabs) == 0 && t.Warp == nil &&
		len(t.TitlePrior) == 0)
}

// LedgerTermTargets lists the settings targets a piece was applied to: for the pane, where
// switchyard-pane@sy-local was enabled; for titles, where sy set
// /env/CLAUDE_CODE_DISABLE_TERMINAL_TITLE = "1". For the pane, Installed lists the plugin
// roots sy ran `claude plugin install` in, and Markets the roots sy added sy-local to (a
// root that lacked it); `off` removes only those (titles leave both empty).
type LedgerTermTargets struct {
	Targets   []string `json:"targets"`
	Installed []string `json:"installed,omitempty"`
	Markets   []string `json:"markets,omitempty"`
}

// LedgerWarpPlugin records Warp's plugin: the plugin roots the marketplace was added in,
// the settings targets it was enabled in, the marketplace ref asked for, and the commit that
// ref resolved to when it was added. `claude plugin marketplace add` pins only a branch or
// tag, never a commit (checked on 2.1.296, terminal.md "Claude Code facts"), and the
// repository has no tags, so Ref is a branch and Commit is what `sy doctor` compares the
// checkout's HEAD with (risk 7).
//
// Installed lists the plugin roots sy ran `claude plugin install` in. `off` uninstalls only
// there, disables only in Targets and removes the marketplace only in Roots: a copy the
// person installed is theirs. Commit is set when sy adds the marketplace or installs the
// plugin, or by `sy term setup --warp-plugin --accept-update` (never silently).
type LedgerWarpPlugin struct {
	Roots     []string `json:"roots,omitempty"`
	Targets   []string `json:"targets,omitempty"`
	Installed []string `json:"installed,omitempty"`
	Ref       string   `json:"ref"`
	Commit    string   `json:"commit,omitempty"`
}

// LedgerTab is a tab config sy wrote: its path, the SHA-256 of what sy wrote (a file whose
// hash differs belongs to the person now), and the account label.
type LedgerTab struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Label  string `json:"label"`
}

// LedgerWarp is the Warp install the tab configs were written for.
type LedgerWarp struct {
	Channel string `json:"channel"`
	Folder  string `json:"folder"` // the tab_configs folder
}
