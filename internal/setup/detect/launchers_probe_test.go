package detect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A config folder's name goes into the probe's shell script; shell syntax in it must never run.
func TestRealProbeIgnoresUnsafeNames(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	marker := filepath.Join(t.TempDir(), "pwned")
	evil := []string{"claude", "x;touch " + marker, "$(touch " + marker + ")", "`touch " + marker + "`", "a b", "-n", ""}
	if _, err := realProbe(context.Background(), sh, evil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a folder name ran as a shell command")
	}
	for n, ok := range map[string]bool{"claude-2": true, "work.claude": true, ".c_1": true, "a;b": false, "a b": false, "$x": false, "-x": false, "": false, "é": false, "a\nb": false} {
		if safeLauncherName(n) != ok {
			t.Errorf("safeLauncherName(%q) = %v", n, !ok)
		}
	}
}
