package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/identity"
)

// rename.md 2.4: a folder still on the plugin of the old name is a warning that names the fix.
func TestHooksWarnOnLegacyPlugin(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	var rep Report
	checkHooks(&rep, identity.Slot{Name: "~/.claude"}, settings{exists: true, plugins: map[string]bool{"oldname@wy-local": true}}, nil)
	if len(rep.Checks) != 1 || rep.Checks[0].Level != Warn || !strings.Contains(rep.Checks[0].Detail, "oldname@wy-local from before the rename") ||
		!strings.Contains(rep.Checks[0].Fix, "wy setup --yes") {
		t.Fatalf("checks: %+v", rep.Checks)
	}
	rep = Report{}
	checkHooks(&rep, identity.Slot{Name: "~/.claude"}, settings{exists: true, plugins: map[string]bool{"oldname@wy-local": true, brand.Slug + "@wy-local": true}}, nil)
	if len(rep.Checks) != 1 || rep.Checks[0].Level != OK {
		t.Fatalf("current plugin wins: %+v", rep.Checks)
	}
}

// rename.md 2.2: system units of the old names (the hub as a system service, a backup timer)
// are not moved by wy update; doctor names them.
func TestLegacySystemUnits(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"switchyard-hub.service", "sy-backup.service", "sy-backup.timer", "watchyard-hub.service", "sshd.service", "sy-notes.txt"} { // rename:keep
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(LegacySystemUnits(dir), ",")
	if got != "switchyard-hub.service,sy-backup.service,sy-backup.timer" { // rename:keep
		t.Fatalf("got %s", got)
	}
	var rep Report
	checkLegacySystemUnits(&rep, dir)
	if len(rep.Checks) != 1 || rep.Checks[0].Level != Warn || !strings.Contains(rep.Checks[0].Fix, "RUNBOOK") {
		t.Fatalf("%+v", rep.Checks)
	}
	rep = Report{}
	checkLegacySystemUnits(&rep, "")
	checkLegacySystemUnits(&rep, filepath.Join(dir, "missing"))
	if len(rep.Checks) != 0 {
		t.Fatalf("%+v", rep.Checks)
	}
}

// Legacy compat (rename.md 2.13): a status line still wrapped by sy is recognised (no double
// wrap suggested), and sy hook lines count as hooks.
func TestStatuslineLegacyWrap(t *testing.T) {
	var rep Report
	checkStatusline(&rep, identity.Slot{Name: "~/.claude"}, settings{exists: true, hasStat: true, status: "/home/u/.local/bin/sy statusline --wrap 'x'"}, nil) // rename:keep
	if len(rep.Checks) != 1 || rep.Checks[0].Level != Warn || strings.Contains(rep.Checks[0].Fix, "--wrap") || !strings.Contains(rep.Checks[0].Fix, "wy setup") {
		t.Fatalf("%+v", rep.Checks)
	}
	if !wyHookRe.MatchString("sy hook --event Stop") || !wyHookRe.MatchString(`"/b/wy" hook`) || wyHookRe.MatchString("easy hook") { // rename:keep
		t.Error("hook regexp")
	}
	rep = Report{}
	checkStatusline(&rep, identity.Slot{Name: "~/.claude"}, settings{exists: true, hasStat: true, status: "wy statusline --wrap 'x'"}, nil)
	if rep.Checks[0].Level != OK {
		t.Fatalf("%+v", rep.Checks)
	}
}
