package paths

import "testing"

func env(goos, home string, vars map[string]string) Env {
	return Env{GOOS: goos, Home: home, Getenv: func(k string) string { return vars[k] }}
}

func TestResolve(t *testing.T) {
	type want struct{ config, state, data string }
	tests := []struct {
		name string
		e    Env
		want want
		src  [3]string // Describe() of config, state, data
	}{
		{"linux defaults", env("linux", "/home/u", nil),
			want{"/home/u/.config/switchyard", "/home/u/.local/state/switchyard", "/home/u/.local/share/switchyard"},
			[3]string{"default", "default", "default"}},
		{"darwin uses XDG layout", env("darwin", "/Users/u", nil),
			want{"/Users/u/.config/switchyard", "/Users/u/.local/state/switchyard", "/Users/u/.local/share/switchyard"},
			[3]string{"default", "default", "default"}},
		{"xdg absolute", env("linux", "/home/u", map[string]string{"XDG_CONFIG_HOME": "/x/c", "XDG_STATE_HOME": "/x/s", "XDG_DATA_HOME": "/x/d"}),
			want{"/x/c/switchyard", "/x/s/switchyard", "/x/d/switchyard"},
			[3]string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"}},
		{"xdg relative ignored", env("linux", "/home/u", map[string]string{"XDG_CONFIG_HOME": "rel"}),
			want{"/home/u/.config/switchyard", "/home/u/.local/state/switchyard", "/home/u/.local/share/switchyard"},
			[3]string{"default", "default", "default"}},
		{"xdg applies on macOS too", env("darwin", "/Users/u", map[string]string{"XDG_STATE_HOME": "/s"}),
			want{"/Users/u/.config/switchyard", "/s/switchyard", "/Users/u/.local/share/switchyard"},
			[3]string{"default", "XDG_STATE_HOME", "default"}},
		{"SY_HOME beats XDG", env("linux", "/home/u", map[string]string{"SY_HOME": "/tmp/t", "XDG_CONFIG_HOME": "/x"}),
			want{"/tmp/t/config", "/tmp/t/state", "/tmp/t/data"},
			[3]string{"SY_HOME", "SY_HOME", "SY_HOME"}},
		{"SY_*_DIR beats SY_HOME", env("linux", "/home/u", map[string]string{"SY_HOME": "/tmp/t", "SY_STATE_DIR": "/s/", "SY_DATA_DIR": "/d"}),
			want{"/tmp/t/config", "/s", "/d"},
			[3]string{"SY_HOME", "SY_STATE_DIR", "SY_DATA_DIR"}},
		{"SWITCHYARD_DATA never read", env("linux", "/home/u", map[string]string{"SWITCHYARD_DATA": "/proto"}),
			want{"/home/u/.config/switchyard", "/home/u/.local/state/switchyard", "/home/u/.local/share/switchyard"},
			[3]string{"default", "default", "default"}},
		{"windows LOCALAPPDATA", env("windows", `C:\Users\u`, map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`}),
			want{`C:\Users\u\AppData\Local\Switchyard\config`, `C:\Users\u\AppData\Local\Switchyard\state`, `C:\Users\u\AppData\Local\Switchyard\data`},
			[3]string{"LOCALAPPDATA", "LOCALAPPDATA", "LOCALAPPDATA"}},
		{"windows without LOCALAPPDATA", env("windows", `C:\Users\u`, nil),
			want{`C:\Users\u\AppData\Local\Switchyard\config`, `C:\Users\u\AppData\Local\Switchyard\state`, `C:\Users\u\AppData\Local\Switchyard\data`},
			[3]string{"default", "default", "default"}},
		{"windows ignores XDG", env("windows", `C:\Users\u`, map[string]string{"LOCALAPPDATA": `D:\L`, "XDG_CONFIG_HOME": `C:\x`}),
			want{`D:\L\Switchyard\config`, `D:\L\Switchyard\state`, `D:\L\Switchyard\data`},
			[3]string{"LOCALAPPDATA", "LOCALAPPDATA", "LOCALAPPDATA"}},
		{"windows SY_HOME", env("windows", `C:\Users\u`, map[string]string{"SY_HOME": `D:/sy`}),
			want{`D:\sy\config`, `D:\sy\state`, `D:\sy\data`},
			[3]string{"SY_HOME", "SY_HOME", "SY_HOME"}},
		{"all overridden needs no home", env("linux", "", map[string]string{"SY_CONFIG_DIR": "/a", "SY_STATE_DIR": "/b", "SY_DATA_DIR": "/c"}),
			want{"/a", "/b", "/c"},
			[3]string{"SY_CONFIG_DIR", "SY_STATE_DIR", "SY_DATA_DIR"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Resolve(tc.e)
			if err != nil {
				t.Fatal(err)
			}
			got := want{d.Config.Path, d.State.Path, d.Data.Path}
			if got != tc.want {
				t.Errorf("paths\n got %+v\nwant %+v", got, tc.want)
			}
			src := [3]string{d.Config.Describe(), d.State.Describe(), d.Data.Describe()}
			if src != tc.src {
				t.Errorf("sources got %v want %v", src, tc.src)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name string
		e    Env
	}{
		{"no home", env("linux", "", nil)},
		{"relative SY_HOME", env("linux", "/h", map[string]string{"SY_HOME": "rel"})},
		{"relative SY_STATE_DIR", env("linux", "/h", map[string]string{"SY_STATE_DIR": "./s"})},
		{"windows relative", env("windows", `C:\u`, map[string]string{"SY_DATA_DIR": `d`})},
		{"windows no home no localappdata", env("windows", "", nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve(tc.e); err == nil {
				t.Fatal("want error")
			}
		})
	}
}
