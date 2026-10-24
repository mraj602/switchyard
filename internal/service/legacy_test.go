package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/timer"
)

// rename.md 2.2: a hub or agent service installed under the name before a rename keeps
// working (Status, Start, Stop find it) and is moved to the current name by Refresh (`wy
// update`) or Install (`wy setup`), with its arguments and environment.

func TestLegacyNames(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	for in, want := range map[string]string{
		"oldname-hub": "com.oldname.hub", "oldname-agent": "com.oldname.oldname-agent",
		DefaultName: "com." + brand.Slug + ".hub", AgentName: "com." + brand.Slug + "." + AgentName,
	} {
		if got := LaunchdLabel(in); got != want {
			t.Errorf("LaunchdLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := WindowsTask("oldname-hub"); got != "Oldname Hub" {
		t.Errorf("WindowsTask = %q", got)
	}
	if got := legacyOf(DefaultName); got != "oldname-hub" {
		t.Errorf("legacyOf = %q", got)
	}
	if legacyOf("oldname-hub") != "" || legacyOf("custom") != "" {
		t.Error("legacyOf of a non-current name")
	}
}

func TestRefreshMigratesLegacyLinuxUnit(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	home := t.TempDir()
	r := &rec{}
	e := envFor("linux", home, r)
	old := e
	old.Pairs, old.PairsSet = [][2]string{{"WY_HOME", "/srv/wy home"}}, true
	oldSpec := model.ServiceSpec{Name: "oldname-hub", Args: []string{"hub", "serve", "--listen", "127.0.0.1:9999"}}
	oldPath := SystemdUnitPath(e, "oldname-hub")
	if err := writeFile(oldPath, []byte(SystemdUnit(old, oldSpec)), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(e)
	ctx := context.Background()

	// Before the move, the old unit is what Status and InstalledUnit see.
	r.out = map[string]string{"systemctl --user is-active oldname-hub.service": "active\n"}
	st, err := s.Status(ctx, DefaultName)
	if err != nil || !st.Installed || !st.Active {
		t.Fatalf("status of the old unit: %+v %v", st, err)
	}
	if args, env, ok := InstalledUnit(e, DefaultName); !ok || args[3] != "127.0.0.1:9999" || len(env) != 1 {
		t.Fatalf("InstalledUnit: %q %q %v", args, env, ok)
	}

	r.calls = nil
	r.out["systemctl --user is-enabled"] = "enabled\n"
	res, err := s.Refresh(ctx, DefaultName)
	if err != nil || !res.Installed || !res.Restarted || res.Migrated != "oldname-hub.service" {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old unit left behind")
	}
	b, err := os.ReadFile(SystemdUnitPath(e, DefaultName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ExecStart=/home/alice/.local/bin/wy hub serve --listen 127.0.0.1:9999\n", `Environment="WY_HOME=/srv/wy home"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("new unit lacks %q:\n%s", want, b)
		}
	}
	want := []string{
		"systemctl --user is-enabled oldname-hub.service",
		"systemctl --user is-active oldname-hub.service",
		"systemctl --user disable --now oldname-hub.service",
		"systemctl --user daemon-reload",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + DefaultName + ".service",
	}
	if got := r.lines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Moved: the next refresh is an ordinary one.
	r.calls = nil
	if res, err := s.Refresh(ctx, DefaultName); err != nil || res.Migrated != "" || !res.Restarted {
		t.Fatalf("second refresh: %+v %v", res, err)
	}
}

func TestRefreshMigratesLegacyDarwinPlist(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	home := t.TempDir()
	r := &rec{}
	e := envFor("darwin", home, r)
	// Before the rename `wy setup --hub here` wrote dev.<slug>.hub.plist.
	oldPath := filepath.Join(home, "Library", "LaunchAgents", "dev.oldname.hub.plist")
	if err := writeFile(oldPath, []byte(LaunchdPlist(e, model.ServiceSpec{Name: "oldname-hub", Args: []string{"hub", "serve", "--listen", "127.0.0.1:8790"}})), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(e).Refresh(context.Background(), DefaultName)
	if err != nil || res.Migrated != "com.oldname.hub" {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old plist left behind")
	}
	b, _ := os.ReadFile(LaunchAgentPath(e, DefaultName))
	if !strings.Contains(string(b), "<string>127.0.0.1:8790</string>") || !strings.Contains(string(b), "<string>com."+brand.Slug+".hub</string>") {
		t.Errorf("new plist:\n%s", b)
	}
	got := strings.Join(r.lines(), "\n")
	if !strings.Contains(got, "launchctl bootout gui/1000/com.oldname.hub") || !strings.Contains(got, "launchctl bootstrap gui/1000 "+LaunchAgentPath(e, DefaultName)) {
		t.Errorf("calls:\n%s", got)
	}
}

func TestRefreshMigratesLegacyWindowsTask(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	home := t.TempDir()
	r := &rec{}
	e := envFor("windows", home, r)
	oldSpec := model.ServiceSpec{Name: "oldname-hub", Args: []string{"hub", "serve", "--listen", "127.0.0.1:8791", "--db", `C:\a b\hub.db`}}
	oldPath := filepath.Join(e.StateDir, WindowsXMLFile("oldname-hub"))
	if err := writeFile(oldPath, timer.UTF16LE(WindowsTaskXML(e, oldSpec)), 0o600); err != nil {
		t.Fatal(err)
	}
	if args, ok := windowsTaskArgs(oldPath); !ok || strings.Join(args, "|") != strings.Join(oldSpec.Args, "|") {
		t.Fatalf("windowsTaskArgs = %q %v", args, ok)
	}
	res, err := New(e).Refresh(context.Background(), DefaultName)
	if err != nil || res.Migrated != "Oldname Hub" {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	got := strings.Join(r.lines(), "\n")
	for _, want := range []string{`schtasks /Delete /TN Oldname Hub /F`, "schtasks /Create /TN " + WindowsTask(DefaultName)} {
		if !strings.Contains(got, want) {
			t.Errorf("calls lack %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old task XML left behind")
	}
}

func TestInstallRemovesLegacyFirstAndUninstallRemovesBoth(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	home := t.TempDir()
	r := &rec{}
	e := envFor("linux", home, r)
	oldPath := SystemdUnitPath(e, "oldname-agent")
	if err := writeFile(oldPath, []byte(SystemdUnit(e, model.ServiceSpec{Name: "oldname-agent", Args: AgentArgs})), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(e)
	if err := s.Install(context.Background(), AgentSpec()); err != nil {
		t.Fatal(err)
	}
	if l := r.lines(); len(l) < 1 || l[0] != "systemctl --user disable --now oldname-agent.service" {
		t.Errorf("calls: %v", l)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old agent unit left behind")
	}
	// An old unit that reappears (an older wy reinstalled it) goes too on uninstall.
	_ = writeFile(oldPath, []byte("x"), 0o644)
	if err := s.Uninstall(context.Background(), AgentName); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{oldPath, SystemdUnitPath(e, AgentName)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p)
		}
	}
}

// Legacy compat (rename.md 2.13): a unit's SY_* variables get the WY_ prefix, upper case.
func TestCurrentEnvNamesLiteral(t *testing.T) {
	got := currentEnvNames([][2]string{{"SY_DATA_DIR", "/d"}, {"PATH", "/usr/bin"}, {"WY_X", "1"}}) // rename:keep
	want := [][2]string{{"WY_DATA_DIR", "/d"}, {"PATH", "/usr/bin"}, {"WY_X", "1"}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The move keeps what the old unit was: enabled or not, running or not. A hub that was
// disabled and stopped (it moved to a server long ago) stays so.
func TestRefreshMigrationKeepsLinuxUnitState(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	for _, c := range []struct {
		name, enabled, active string
		want                  []string // after daemon-reload
	}{
		{"enabled active", "enabled\n", "active\n", []string{"systemctl --user enable --now " + DefaultName + ".service"}},
		{"enabled inactive", "enabled\n", "inactive\n", []string{"systemctl --user enable " + DefaultName + ".service"}},
		{"disabled active", "disabled\n", "active\n", []string{"systemctl --user start " + DefaultName + ".service"}},
		{"disabled inactive", "disabled\n", "inactive\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			r := &rec{out: map[string]string{
				"systemctl --user is-enabled": c.enabled, "systemctl --user is-active": c.active,
			}}
			e := envFor("linux", home, r)
			oldPath := SystemdUnitPath(e, "oldname-hub")
			if err := writeFile(oldPath, []byte(SystemdUnit(e, model.ServiceSpec{Name: "oldname-hub", Args: []string{"hub", "serve"}})), 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := New(e).Refresh(context.Background(), DefaultName)
			if err != nil || res.Migrated != "oldname-hub.service" {
				t.Fatalf("refresh: %+v %v", res, err)
			}
			wantEnabled, wantStarted := strings.HasPrefix(c.enabled, "enabled"), c.active == "active\n"
			if res.Enabled != wantEnabled || res.Started != wantStarted || res.Restarted != wantStarted {
				t.Errorf("result %+v", res)
			}
			if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
				t.Error("old unit left behind")
			}
			if _, err := os.Stat(SystemdUnitPath(e, DefaultName)); err != nil {
				t.Errorf("new unit missing: %v", err)
			}
			var after []string
			seen := false
			for _, l := range r.lines() {
				if seen {
					after = append(after, l)
				}
				if l == "systemctl --user daemon-reload" {
					seen = true
					after = nil
				}
			}
			if strings.Join(after, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("after the last daemon-reload:\n%v\nwant %v", after, c.want)
			}
		})
	}
}

func TestRefreshMigrationKeepsAgentState(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	home := t.TempDir()
	r := &rec{out: map[string]string{"systemctl --user is-enabled": "disabled\n", "systemctl --user is-active": "inactive\n"}}
	e := envFor("linux", home, r)
	if err := writeFile(SystemdUnitPath(e, "oldname-agent"), []byte(SystemdUnit(e, model.ServiceSpec{Name: "oldname-agent", Args: AgentArgs})), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(e).Refresh(context.Background(), AgentName)
	if err != nil || res.Migrated == "" || res.Enabled || res.Started {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	for _, l := range r.lines() {
		if strings.Contains(l, " enable ") || strings.Contains(l, " start ") {
			t.Errorf("unexpected call %q", l)
		}
	}
}

func TestRefreshMigrationKeepsDarwinState(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	home := t.TempDir()
	r := &rec{fail: map[string]bool{"launchctl print": true}} // the old agent is not loaded
	e := envFor("darwin", home, r)
	oldPath := filepath.Join(home, "Library", "LaunchAgents", "com.oldname.hub.plist")
	if err := writeFile(oldPath, []byte(LaunchdPlist(e, model.ServiceSpec{Name: "oldname-hub", Args: []string{"hub", "serve"}})), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(e).Refresh(context.Background(), DefaultName)
	if err != nil || res.Migrated == "" || res.Started {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	if _, err := os.Stat(LaunchAgentPath(e, DefaultName)); err != nil {
		t.Error("new plist missing")
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old plist left behind")
	}
	for _, l := range r.lines() {
		if strings.Contains(l, "bootstrap") {
			t.Errorf("loaded the new agent: %q", l)
		}
	}
}

func TestRefreshMigrationKeepsWindowsState(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	home := t.TempDir()
	r := &rec{out: map[string]string{"schtasks /Query": "Oldname Hub    N/A    Disabled\n"}}
	e := envFor("windows", home, r)
	oldPath := filepath.Join(e.StateDir, WindowsXMLFile("oldname-hub"))
	if err := writeFile(oldPath, timer.UTF16LE(WindowsTaskXML(e, model.ServiceSpec{Name: "oldname-hub", Args: []string{"hub", "serve"}})), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := New(e).Refresh(context.Background(), DefaultName)
	if err != nil || res.Enabled || res.Started {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	got := strings.Join(r.lines(), "\n")
	if !strings.Contains(got, "schtasks /Change /TN "+WindowsTask(DefaultName)+" /DISABLE") || strings.Contains(got, "/Run") {
		t.Errorf("calls:\n%s", got)
	}
}
