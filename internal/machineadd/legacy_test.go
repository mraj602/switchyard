package machineadd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/brand"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

// rename.md 2.5: the forward file of the project's name before a rename keeps being used
// while it exists and the new one does not; nothing is written next to it.
func TestForwardKeepsLegacyFile(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	home, state := t.TempDir(), t.TempDir()
	old := Forward{Host: "nas", Machine: "m_old", Name: "nas", Port: 47840, Target: "hub.example:443"}
	testtree.New(t, home).Dir(".ssh", 0o700).
		File(".ssh/config", "Include oldname.conf\nHost a\n", 0o600).
		File(".ssh/oldname.conf", old.Block(), 0o600)
	if p, _, ok := OwnedForward(home, "m_old"); !ok || p != 47840 {
		t.Fatalf("OwnedForward in the old file: %d %v", p, ok)
	}
	f := Forward{Host: "gpu-box", Machine: testMID, Name: "gpu-box", Port: 47841, Target: "hub.example:443"}
	if _, err := WriteForward(home, state, f, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", ForwardConf)); !os.IsNotExist(err) {
		t.Fatal("a new forward file was written next to the old one")
	}
	b, _ := os.ReadFile(filepath.Join(home, ".ssh", "oldname.conf"))
	if string(b) != old.Block()+f.Block() {
		t.Fatalf("old file:\n%s", b)
	}
	if cfg, _ := os.ReadFile(filepath.Join(home, ".ssh", "config")); string(cfg) != "Include oldname.conf\nHost a\n" {
		t.Fatalf("config gained another Include: %q", cfg)
	}
	for _, m := range []string{testMID, "m_old"} {
		if done, err := RemoveForward(home, state, m, "", "test", time.Now()); err != nil || !done {
			t.Fatalf("remove %s: %v %v", m, done, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "oldname.conf")); !os.IsNotExist(err) {
		t.Error("the emptied old file stays")
	}
	if cfg, _ := os.ReadFile(filepath.Join(home, ".ssh", "config")); string(cfg) != "Host a\n" {
		t.Errorf("config: %q", cfg)
	}
}

// rename.md 2.1: an update pushed to a remote that only has the state folder of the old name
// lands there, and never creates the new one (which would hide the remote's state).
func TestLegacyRemoteUpdateFolder(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	run := func(home string) string {
		cmd := exec.Command("sh", "-c", writeScript(), "sh", "update", "sy-1.tar.gz", "x1")
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		cmd.Dir = home
		cmd.Stdin = strings.NewReader("data")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	h := t.TempDir()
	if err := os.MkdirAll(filepath.Join(h, ".local/state/oldname"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out := run(h); !strings.Contains(out, "path="+h+"/.local/state/oldname/update/.sy-1.tar.gz.x1.tmp") {
		t.Fatalf("legacy: %s", out)
	}
	if _, err := os.Stat(filepath.Join(h, ".local/state", brand.Slug)); !os.IsNotExist(err) {
		t.Fatalf("new state folder created: %v", err)
	}
	h2 := t.TempDir()
	if out := run(h2); !strings.Contains(out, "path="+h2+"/.local/state/"+brand.Slug+"/update/") {
		t.Fatalf("fresh: %s", out)
	}
}
