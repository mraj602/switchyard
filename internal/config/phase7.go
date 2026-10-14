package config

import "fmt"

// terminal.source values (docs/spec/terminal.md T3, T12): where `sy board` reads from.
const (
	TerminalSourceAuto  = "auto"
	TerminalSourceHub   = "hub"
	TerminalSourceLocal = "local"
)

// CheckTerminalSource validates a terminal.source value.
func CheckTerminalSource(v string) error {
	switch v {
	case TerminalSourceAuto, TerminalSourceHub, TerminalSourceLocal:
		return nil
	}
	return fmt.Errorf("terminal.source: want auto, hub or local, got %q", v)
}
