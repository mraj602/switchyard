package statuswrap

import (
	"testing"

	"github.com/mraj602/switchyard/internal/brand"
)

// rename.md 2.13: a status line written for the command's old name is still recognised, so
// setup rewrites it instead of wrapping it again.
func TestLegacyCommandIsOurs(t *testing.T) {
	cmd := "/home/u/.local/bin/oldcli statusline --wrap 'ccstatus'"
	if IsOurs(cmd) {
		t.Fatal("before a rename another command is not ours")
	}
	defer brand.SetLegacyCLIForTest("oldcli")()
	if !IsOurs(cmd) || !IsOurs(`C:\bin\oldcli.exe statusline`) {
		t.Fatal("legacy command not recognised")
	}
	if o, ok := Unwrap(cmd); !ok || o != "ccstatus" {
		t.Fatalf("unwrap %q %v", o, ok)
	}
	if !IsOurs(Wrap("x", "/b/"+brand.CLI)) {
		t.Fatal("current")
	}
}
