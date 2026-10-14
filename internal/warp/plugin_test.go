package warp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginCommit(t *testing.T) {
	root, co := t.TempDir(), t.TempDir()
	sha := strings.Repeat("ab", 20)
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if PluginCommit(root, "claude-code-warp") != "" {
		t.Error("no known_marketplaces.json must give empty")
	}
	write(filepath.Join(root, "known_marketplaces.json"), `{"claude-code-warp":{"installLocation":"`+filepath.ToSlash(co)+`"}}`)
	if PluginCommit(root, "claude-code-warp") != "" {
		t.Error("no .git must give empty")
	}
	write(filepath.Join(co, ".git", "HEAD"), "ref: refs/heads/main\n")
	if PluginCommit(root, "claude-code-warp") != "" {
		t.Error("unresolvable ref must give empty")
	}
	write(filepath.Join(co, ".git", "packed-refs"), "# pack\n"+sha+" refs/heads/main\n")
	if got := PluginCommit(root, "claude-code-warp"); got != sha {
		t.Errorf("packed = %q", got)
	}
	sha2 := strings.Repeat("cd", 20)
	write(filepath.Join(co, ".git", "refs", "heads", "main"), sha2+"\n")
	if got := PluginCommit(root, "claude-code-warp"); got != sha2 {
		t.Errorf("loose = %q", got)
	}
	write(filepath.Join(co, ".git", "HEAD"), sha+"\n")
	if got := PluginCommit(root, "claude-code-warp"); got != sha {
		t.Errorf("detached = %q", got)
	}
	write(filepath.Join(co, ".git", "HEAD"), "ref: refs/../../x\n")
	if PluginCommit(root, "claude-code-warp") != "" {
		t.Error("traversing ref must give empty")
	}
	if PluginCommit(root, "other") != "" {
		t.Error("unknown marketplace must give empty")
	}
}

func TestTabCommand(t *testing.T) {
	b := Render(Spec{Slot: "~/.claude-2", Label: "work", Command: `/home/u/my "bin"/claude-2`})
	if got := TabCommand(b); got != `/home/u/my "bin"/claude-2` {
		t.Errorf("TabCommand = %q", got)
	}
	if TabCommand([]byte("name = \"x\"\n")) != "" {
		t.Error("no commands line must give empty")
	}
}
