package plan

import (
	"path/filepath"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/model"
)

func TestInside(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"h", ".local", "bin")
	for name, want := range map[string]bool{
		"claude-2":                          true,
		"claude-2.cmd":                      true,
		"claude-x/../../evil\ntouch /tmp/p": false,
		"../claude-2":                       false,
		"a/b":                               false,
		"..":                                false,
		".":                                 false,
		"":                                  false,
	} {
		if got := inside(dir, filepath.Join(dir, name)); got != want {
			t.Errorf("inside(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDefaultLabel(t *testing.T) {
	for dir, want := range map[string]string{
		"/h/.claude":                     "claude",
		"/h/.claude-2":                   "claude-2",
		"/h/.Claude Work":                "claude-work",
		"/h/.claude\ntouch":              "claude-touch",
		"/h/.--x":                        "x",
		"/h/.é":                          "account",
		"/h/.abcdefghijklmnopqrstuvwxyz": "abcdefghijklmnopqrstuvwx",
	} {
		got := defaultLabel(dir)
		if got != want || !model.ValidLabel(got) {
			t.Errorf("defaultLabel(%q) = %q, want %q", dir, got, want)
		}
	}
}
