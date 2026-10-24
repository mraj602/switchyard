package warp

import (
	"testing"

	"github.com/mraj602/switchyard/internal/brand"
)

// rename.md 2.8: tab configs with the prefix from before a rename are still recognised and
// opened; new ones get the current prefix.
func TestLegacyTabPrefix(t *testing.T) {
	if l := LabelOf("/x/tab_configs/oldname-work.toml"); l != "" {
		t.Fatalf("legacy prefix read before a rename: %q", l)
	}
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	p := "/x/tab_configs/oldname-work.toml"
	if LabelOf(p) != "work" || !IsTabPath(p) || !IsLegacyTab(p) {
		t.Fatalf("legacy: %q %v %v", LabelOf(p), IsTabPath(p), IsLegacyTab(p))
	}
	if cur := "/x/tab_configs/" + FileName("work"); IsLegacyTab(cur) || LabelOf(cur) != "work" {
		t.Fatalf("current: %s", cur)
	}
	if u := FileURI(Stable, p); u != "warp://tab_config/oldname-work" || !ValidTabURI(u) {
		t.Fatalf("uri %q", u)
	}
	for _, bad := range []string{"warp://tab_config/other-work", "warp://tab_config/oldname-", "warp://tab_config/oldname-Work"} {
		if ValidTabURI(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
