package hook

import (
	"regexp"
	"strings"

	"github.com/mraj602/switchyard/internal/record"
)

// Term builds the record's term object from the environment (record.md 3.3): the app from
// TERM_PROGRAM (or the variables of terminals that do not set it), the pane id from
// WARP_TERMINAL_SESSION_UUID, the focus URL from LC_SY_FOCUS (an SSH session passes its
// laptop tab) or WARP_FOCUS_URL, and the host from LC_SY_HOST (docs/spec/terminal.md T9).
// machine is this machine's id: a LC_SY_HOST equal to it, or not shaped like a machine id,
// leaves host absent. It returns nil when nothing identifies a terminal.
func Term(getenv func(string) string, machine string) *record.Term {
	app := termApp(getenv)
	id := getenv("WARP_TERMINAL_SESSION_UUID")
	focus := getenv("LC_SY_FOCUS")
	if focus == "" {
		focus = getenv("WARP_FOCUS_URL")
	}
	host := getenv("LC_SY_HOST")
	if !machineIDRe.MatchString(host) || host == machine {
		host = ""
	}
	if app == "" && id == "" && focus == "" && host == "" {
		return nil
	}
	if app == "" {
		app = "unknown"
	}
	return &record.Term{App: app, ID: id, FocusURL: focus, Host: host}
}

// machineIDRe is the shape of a machine id: m_ and 26 Crockford base32 characters.
var machineIDRe = regexp.MustCompile(`^m_[0-9A-HJKMNP-TV-Z]{26}$`)

func termApp(getenv func(string) string) string {
	switch p := strings.ToLower(getenv("TERM_PROGRAM")); {
	case p == "warpterminal":
		return "warp"
	case p == "ghostty", p == "wezterm", p == "kitty", p == "vscode":
		return p
	case p == "iterm.app", p == "iterm":
		return "iterm"
	case p == "tmux":
		return "tmux"
	case p != "":
		return p
	}
	switch {
	case getenv("WARP_TERMINAL_SESSION_UUID") != "":
		return "warp"
	case getenv("WT_SESSION") != "":
		return "windows-terminal"
	case getenv("KITTY_WINDOW_ID") != "":
		return "kitty"
	case getenv("TMUX") != "":
		return "tmux"
	}
	return ""
}
