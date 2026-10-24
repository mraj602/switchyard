package paths

import (
	"testing"

	"github.com/mraj602/watchyard/internal/brand"
)

// rename.md 2.1: after a rename, a default folder that does not exist falls back to the
// folder of the older name when that one exists (config: when it holds config.toml).
func TestLegacyFolders(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	have := func(paths ...string) func(string) bool {
		m := map[string]bool{}
		for _, p := range paths {
			m[p] = true
		}
		return func(p string) bool { return m[p] }
	}
	type want struct{ config, state, data string }
	tests := []struct {
		name   string
		e      Env
		exists func(string) bool
		want   want
		legacy [3]bool
	}{
		{"fresh install: new names", env("linux", "/home/u", nil), have(),
			want{"/home/u/.config/watchyard", "/home/u/.local/state/watchyard", "/home/u/.local/share/watchyard"}, [3]bool{}},
		{"old install: old folders", env("linux", "/home/u", nil),
			have("/home/u/.config/oldname/config.toml", "/home/u/.config/oldname", "/home/u/.local/state/oldname", "/home/u/.local/share/oldname"),
			want{"/home/u/.config/oldname", "/home/u/.local/state/oldname", "/home/u/.local/share/oldname"}, [3]bool{true, true, true}},
		{"old config folder without config.toml is not ours", env("linux", "/home/u", nil),
			have("/home/u/.config/oldname", "/home/u/.local/state/oldname"),
			want{"/home/u/.config/watchyard", "/home/u/.local/state/oldname", "/home/u/.local/share/watchyard"}, [3]bool{false, true, false}},
		{"new folder wins when both exist", env("linux", "/home/u", nil),
			have("/home/u/.local/state/watchyard", "/home/u/.local/state/oldname"),
			want{"/home/u/.config/watchyard", "/home/u/.local/state/watchyard", "/home/u/.local/share/watchyard"}, [3]bool{}},
		{"xdg", env("linux", "/home/u", map[string]string{"XDG_STATE_HOME": "/x/s"}), have("/x/s/oldname"),
			want{"/home/u/.config/watchyard", "/x/s/oldname", "/home/u/.local/share/watchyard"}, [3]bool{false, true, false}},
		{"overrides never fall back", env("linux", "/home/u", map[string]string{"WY_STATE_DIR": "/s", "WY_HOME": "/h"}), have("/home/u/.local/state/oldname", "/h/data"),
			want{"/h/config", "/s", "/h/data"}, [3]bool{}},
		{"windows", env("windows", `C:\Users\u`, map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`}),
			have(`C:\Users\u\AppData\Local\Oldname\data`),
			want{`C:\Users\u\AppData\Local\Watchyard\config`, `C:\Users\u\AppData\Local\Watchyard\state`, `C:\Users\u\AppData\Local\Oldname\data`}, [3]bool{false, false, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.e
			e.Exists = tt.exists
			d, err := Resolve(e)
			if err != nil {
				t.Fatal(err)
			}
			got := want{d.Config.Path, d.State.Path, d.Data.Path}
			if got != tt.want {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
			if l := [3]bool{d.Config.Legacy, d.State.Legacy, d.Data.Legacy}; l != tt.legacy {
				t.Fatalf("legacy %v, want %v", l, tt.legacy)
			}
		})
	}
	// Not renamed: the old folders are never looked at.
	restore := brand.SetLegacyForTest(brand.Slug, brand.Name)
	defer restore()
	e := env("linux", "/home/u", nil)
	e.Exists = func(string) bool { t.Fatal("Exists called without a rename"); return false }
	if _, err := Resolve(e); err != nil {
		t.Fatal(err)
	}
}
