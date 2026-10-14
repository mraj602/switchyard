package board

import (
	"strings"
	"testing"
)

func TestSafeResume(t *testing.T) {
	for _, ok := range []string{
		"claude --resume abc-123_X", "claude-2 --resume abc",
		"CLAUDE_CONFIG_DIR=~/.claude-work claude --resume abc",
	} {
		if safeResume(ok) != ok {
			t.Errorf("%q was dropped", ok)
		}
	}
	for _, bad := range []string{
		"", "claude --resume abc; rm -rf ~", "claude --resume $(id)", "claude --resume abc\n",
		"claude --resume 'abc'", "evil --resume abc", "claude-2x --resume abc",
		"CLAUDE_CONFIG_DIR=$(id) claude --resume abc", "claude --resume abc\x1b]52;c;x",
		"claude --resume " + strings.Repeat("a", 65),
	} {
		if safeResume(bad) != "" {
			t.Errorf("%q was kept", bad)
		}
	}
}
