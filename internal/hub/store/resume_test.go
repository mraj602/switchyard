package store

import (
	"database/sql"
	"testing"
)

func TestResumeCommandIsShellQuoted(t *testing.T) {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
	for _, c := range []struct {
		launcher, folder, id, want string
	}{
		{"claude-2", "~/.claude-2", "c41e0000-0000-4000-8000-000000000003", "claude-2 --resume c41e0000-0000-4000-8000-000000000003"},
		{"", "~/.claude", "abc12345", "claude --resume abc12345"},
		{"", "~/.claude-2", "abc12345", "CLAUDE_CONFIG_DIR=~/.claude-2 claude --resume abc12345"},
		{"", "~/my dir/.claude; reboot", "abc12345", `CLAUDE_CONFIG_DIR=~/'my dir/.claude; reboot' claude --resume abc12345`},
		{"", "/home/o'brien/.claude-x", "abc12345", `CLAUDE_CONFIG_DIR='/home/o'\''brien/.claude-x' claude --resume abc12345`},
		{"", "~/.claude-2", "x; reboot", `CLAUDE_CONFIG_DIR=~/.claude-2 claude --resume 'x; reboot'`},
		{"rm -rf /; x", "~/.claude-2", "abc12345", "CLAUDE_CONFIG_DIR=~/.claude-2 claude --resume abc12345"},
	} {
		if got := resumeCommand(ns(c.launcher), ns(c.folder), c.id); got != c.want {
			t.Errorf("resumeCommand(%q, %q, %q)\n got %s\nwant %s", c.launcher, c.folder, c.id, got, c.want)
		}
	}
}
