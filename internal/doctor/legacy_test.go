package doctor

import (
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
