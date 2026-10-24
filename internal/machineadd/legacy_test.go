package machineadd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/testtree"
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
		cmd := exec.Command("sh", "-c", writeScript(), "sh", "update", "wy-1.tar.gz", "x1")
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
	if out := run(h); !strings.Contains(out, "path="+h+"/.local/state/oldname/update/.wy-1.tar.gz.x1.tmp") {
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

// Legacy compat: an authorized_keys line written before the rename ends `sy-pull:<id>`. Adding
// the target again replaces it (with a wy-pull line), and removing the target removes it.
func TestAuthorizePullReplacesLegacyMarker(t *testing.T) {
	home := t.TempDir()
	mine := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMine me@laptop\n"
	old := `restrict,command="/home/me/.local/bin/sy outbox drain --stdio" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOld switchyard-pull@hub sy-pull:pt_01ABC` + "\n"
	testtree.New(t, home).Dir(".ssh", 0o700).File(".ssh/authorized_keys", mine+old, 0o600)
	line, key, err := PullKeyLine("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINew sy-pull:pt_01ABC", "/home/me/.local/bin/wy", "pt_01ABC", "", true)
	if err != nil || strings.Contains(line, "sy-pull") || !strings.HasSuffix(line, " wy-pull:pt_01ABC") {
		t.Fatalf("line %q %v", line, err)
	}
	if _, err := AuthorizePull(home, line, key, "pt_01ABC"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".ssh/authorized_keys"))
	if string(b) != mine+line+"\n" {
		t.Fatalf("authorized_keys\n%s", b)
	}
	testtree.New(t, home).File(".ssh/authorized_keys", mine+old, 0o600)
	if ch, err := RemovePull(home, "pt_01ABC"); err != nil || !ch {
		t.Fatalf("remove: %v %v", ch, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".ssh/authorized_keys")); string(b) != mine {
		t.Fatalf("after remove\n%q", b)
	}
}

// Legacy compat: a forward block written before the rename (# sy-forward v1 ... # end
// sy-forward) is found, replaced by a wy-forward block, and removed.
func TestForwardLegacyBlock(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	f := Forward{Host: "gpu-box", Machine: testMID, Name: "gpu-box", Port: 47841, Target: "hub.example:443"}
	legacy := strings.ReplaceAll(f.Block(), "wy-forward", "sy-forward")
	if !strings.HasPrefix(f.Block(), "# wy-forward v1 ") || !strings.HasPrefix(legacy, "# sy-forward v1 ") {
		t.Fatalf("blocks:\n%s\n%s", f.Block(), legacy)
	}
	testtree.New(t, home).Dir(".ssh", 0o700).
		File(".ssh/config", IncludeLine+"Host a\n", 0o600).
		File(".ssh/"+ForwardConf, legacy, 0o600)
	if p, _, ok := OwnedForward(home, testMID); !ok || p != 47841 {
		t.Fatalf("OwnedForward of a legacy block: %d %v", p, ok)
	}
	f.Port = 47842
	if _, err := WriteForward(home, state, f, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".ssh", ForwardConf)); string(b) != f.Block() {
		t.Fatalf("after write:\n%s", b)
	}
	testtree.New(t, home).File(".ssh/"+ForwardConf, legacy, 0o600)
	if done, err := RemoveForward(home, state, testMID, "", "test", time.Now()); err != nil || !done {
		t.Fatalf("remove: %v %v", done, err)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".ssh", ForwardConf)); err == nil && strings.Contains(string(b), "forward") {
		t.Fatalf("legacy block stays:\n%s", b)
	}
}

// Legacy compat: machines.json written before the rename and a hub older than it say
// sy_version; both are read.
func TestLegacySyVersionRead(t *testing.T) {
	var e Entry
	if err := json.Unmarshal([]byte(`{"name":"gpu","sy_version":"0.9.0"}`), &e); err != nil || e.WyVersion != "0.9.0" {
		t.Fatalf("registry entry: %+v %v", e, err)
	}
	if err := json.Unmarshal([]byte(`{"name":"gpu","wy_version":"1.0.0","sy_version":"0.9.0"}`), &e); err != nil || e.WyVersion != "1.0.0" {
		t.Fatalf("registry entry, both: %+v %v", e, err)
	}
	if b, _ := json.Marshal(Entry{WyVersion: "1.0.0"}); !strings.Contains(string(b), `"wy_version":"1.0.0"`) || strings.Contains(string(b), "sy_version") {
		t.Fatalf("registry entry written as %s", b)
	}
	var m HubMachine
	if err := json.Unmarshal([]byte(`{"id":"m_1","sy_version":"0.9.0"}`), &m); err != nil || m.WyVersion == nil || *m.WyVersion != "0.9.0" {
		t.Fatalf("hub machine: %+v %v", m, err)
	}
	if err := json.Unmarshal([]byte(`{"id":"m_1","wy_version":"1.0.0"}`), &m); err != nil || m.WyVersion == nil || *m.WyVersion != "1.0.0" {
		t.Fatalf("hub machine, new: %+v %v", m, err)
	}
}
