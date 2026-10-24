package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentGolden(t *testing.T) {
	sp, err := normalize(AgentSpec())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "watchyard-agent.service.golden", SystemdUnit(envFor("linux", "/home/alice", nil), sp))
	golden(t, "com.watchyard.watchyard-agent.plist.golden", LaunchdPlist(envFor("darwin", "/Users/alice", nil), sp))
}

func TestAgentInstallStatusUninstall(t *testing.T) {
	home := t.TempDir()
	r := &rec{out: map[string]string{"systemctl --user is-active": "active\n"}}
	s := New(envFor("linux", home, r))
	ctx := context.Background()
	if err := s.Install(ctx, AgentSpec()); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "watchyard-agent.service")
	b, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(b), "ExecStart=/home/alice/.local/bin/wy agent run\n") {
		t.Fatalf("unit %v:\n%s", err, b)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now watchyard-agent.service"}
	if got := r.lines(); len(got) < 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("calls %v", got)
	}
	st, _ := s.Status(ctx, AgentName)
	if !st.Installed || !st.Active {
		t.Errorf("status %+v", st)
	}
	if err := s.Uninstall(ctx, AgentName); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Errorf("unit not removed: %v", err)
	}
	found := false
	for _, l := range r.lines() {
		found = found || l == "systemctl --user disable --now watchyard-agent.service"
	}
	if !found {
		t.Errorf("no disable call: %v", r.lines())
	}

	// macOS: the plist lands in the temp home's LaunchAgents folder.
	mhome := t.TempDir()
	mr := &rec{}
	if err := New(envFor("darwin", mhome, mr)).Install(ctx, AgentSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mhome, "Library", "LaunchAgents", "com.watchyard.watchyard-agent.plist")); err != nil {
		t.Error(err)
	}
}
