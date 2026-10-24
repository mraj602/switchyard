package warp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/termenv"
)

// T8's folder table, for each OS layout and channel, through termenv.Env.
func TestFolders(t *testing.T) {
	cases := []struct {
		goos    string
		vars    map[string]string
		stable  string
		preview string
	}{
		{"linux", nil, "/home/u/.local/share/warp-terminal/tab_configs", "/home/u/.local/share/warp-terminal-preview/tab_configs"},
		{"linux", map[string]string{"XDG_DATA_HOME": "/data"}, "/data/warp-terminal/tab_configs", "/data/warp-terminal-preview/tab_configs"},
		{"linux", map[string]string{"XDG_DATA_HOME": "relative"}, "/home/u/.local/share/warp-terminal/tab_configs", "/home/u/.local/share/warp-terminal-preview/tab_configs"},
		{"darwin", nil, "/home/u/.warp/tab_configs", "/home/u/.warp-preview/tab_configs"},
		{"windows", map[string]string{"APPDATA": `C:\Users\u\AppData\Roaming`},
			`C:\Users\u\AppData\Roaming\warp\Warp\data\tab_configs`, `C:\Users\u\AppData\Roaming\warp\WarpPreview\data\tab_configs`},
	}
	for _, c := range cases {
		home := "/home/u"
		if c.goos == "windows" {
			home = `C:\Users\u`
		}
		env := termenv.Fake(home, c.goos, c.vars)
		if runtime.GOOS == "windows" && c.goos != "windows" {
			continue // Unix layouts are checked on Unix runners (filepath would use `\` here)
		}
		if got := Folder(env, Stable); got != c.stable {
			t.Errorf("%s %v stable: %s, want %s", c.goos, c.vars, got, c.stable)
		}
		if got := Folder(env, ""); got != c.stable {
			t.Errorf("%s %v default channel: %s", c.goos, c.vars, got)
		}
		if got := Folder(env, Preview); got != c.preview {
			t.Errorf("%s %v preview: %s, want %s", c.goos, c.vars, got, c.preview)
		}
		if ChannelOf(c.stable) != Stable || ChannelOf(c.preview) != Preview {
			t.Errorf("%s: ChannelOf(%s / %s) wrong", c.goos, c.stable, c.preview)
		}
	}
	// "preview" elsewhere in the path is not Warp Preview's folder.
	for _, f := range []string{"/home/preview/.local/share/warp-terminal/tab_configs", "/Users/Preview/.warp/tab_configs",
		"/srv/previews/data/warp-terminal/tab_configs", `C:\Users\preview\AppData\Roaming\warp\Warp\data\tab_configs`,
		"/home/u/.local/share/warp-terminal-preview-old/tab_configs", ""} {
		if ChannelOf(f) != Stable {
			t.Errorf("ChannelOf(%q) = %s, want stable", f, ChannelOf(f))
		}
	}
	for _, f := range []string{"/home/preview/.local/share/warp-terminal-preview/tab_configs", "/Users/u/.warp-preview",
		`C:\Users\preview\AppData\Roaming\warp\WarpPreview\data\tab_configs`} {
		if ChannelOf(f) != Preview {
			t.Errorf("ChannelOf(%q) = %s, want preview", f, ChannelOf(f))
		}
	}
	if Folder(termenv.Env{GOOS: "windows"}, Stable) != "" || Folder(termenv.Env{GOOS: "darwin"}, Stable) != "" || Folder(termenv.Env{GOOS: "linux"}, Stable) != "" {
		t.Error("no home / APPDATA must give no folder")
	}
}

// The folder of the OS the tests run on, from a temp home (each CI runner checks its own).
func TestFolderRealOS(t *testing.T) {
	home := t.TempDir()
	env := termenv.Fake(home, runtime.GOOS, nil)
	got := Folder(env, Stable)
	var want string
	switch runtime.GOOS {
	case "darwin":
		want = filepath.Join(home, ".warp", "tab_configs")
	case "windows":
		want = filepath.Join(home, "AppData", "Roaming", "warp", "Warp", "data", "tab_configs")
	default:
		want = filepath.Join(home, ".local", "share", "warp-terminal", "tab_configs")
	}
	if got != want {
		t.Fatalf("Folder = %s, want %s", got, want)
	}
}

func TestDetect(t *testing.T) {
	home := t.TempDir()
	env := termenv.Fake(home, runtime.GOOS, nil)
	if in := Detect(env, ""); in.Detected() || in.Channel != Stable {
		t.Fatalf("empty home: %+v", in)
	}
	in := Detect(termenv.Fake(home, runtime.GOOS, map[string]string{"TERM_PROGRAM": "WarpTerminal"}), "")
	if !in.Detected() || in.Present || !in.InWarp {
		t.Fatalf("TERM_PROGRAM only: %+v", in)
	}
	if err := os.MkdirAll(DataFolder(env, Preview), 0o755); err != nil {
		t.Fatal(err)
	}
	if in := Detect(env, ""); in.Channel != Preview || !in.Present || in.FolderExists || in.Both {
		t.Fatalf("preview only: %+v", in)
	}
	if err := os.MkdirAll(Folder(env, Stable), 0o755); err != nil {
		t.Fatal(err)
	}
	if in := Detect(env, ""); in.Channel != Stable || !in.Present || !in.FolderExists || !in.Both {
		t.Fatalf("both: %+v", in)
	}
	if in := Detect(env, Preview); in.Channel != Preview || in.Folder != Folder(env, Preview) || in.FolderExists {
		t.Fatalf("--channel preview: %+v", in)
	}
}

// The setup palette (setup.md 3) maps to these names; black and white never appear.
func TestPaletteColours(t *testing.T) {
	want := map[string]string{
		"teal": "cyan", "amber": "yellow", "violet": "magenta", "rose": "red",
		"sky": "cyan", "lime": "green", "orange": "red", "slate": "blue",
	}
	for _, c := range model.Palette {
		if got := Nearest(c.Hex); got != want[c.Name] {
			t.Errorf("%s %s -> %s, want %s", c.Name, c.Hex, got, want[c.Name])
		}
	}
	for _, c := range Colours {
		if Nearest(c.Hex) != c.Name {
			t.Errorf("%s does not map to itself", c.Name)
		}
	}
	for _, hex := range []string{"#000000", "#ffffff", "#808080"} {
		if n := Nearest(hex); n == "black" || n == "white" || !ValidName(n) {
			t.Errorf("%s -> %q", hex, n)
		}
	}
	if Nearest("teal") != "" || Nearest("#12345") != "" {
		t.Error("invalid hex must give no name")
	}
}

func TestRenderGolden(t *testing.T) {
	s := Spec{Slot: "~/.claude-2", Label: "work", Colour: "yellow", Command: "/home/u/.local/bin/claude-2"}
	if err := Check(s); err != nil {
		t.Fatal(err)
	}
	want := `# sy-warp-tab v1 slot=~/.claude-2 label=work (written by wy term warp on; wy term warp off removes it)
name = "Watchyard · work"
title = "work"
color = "yellow"

[[panes]]
id = "main"
type = "terminal"
commands = ["/home/u/.local/bin/claude-2"]
`
	got := Render(s)
	if string(got) != want {
		t.Fatalf("render:\n%s\nwant:\n%s", got, want)
	}
	info, ok := ParseMarker(got)
	if !ok || info.Slot != "~/.claude-2" || info.Label != "work" {
		t.Fatalf("marker: %+v %v", info, ok)
	}
	win := Render(Spec{Slot: `~/.claude-2`, Label: "work", Command: `C:\Users\u\.local\bin\claude-2.cmd`})
	if !strings.Contains(string(win), `commands = ["C:\\Users\\u\\.local\\bin\\claude-2.cmd"]`) || strings.Contains(string(win), "color") {
		t.Fatalf("windows render:\n%s", win)
	}
	if Hash(got) == Hash(win) || len(Hash(got)) != 64 {
		t.Fatal("hash")
	}
}

func TestCheckAndMarker(t *testing.T) {
	good := Spec{Slot: "~/.claude", Label: "me", Command: "/bin/claude"}
	for name, s := range map[string]Spec{
		"label":   {Slot: good.Slot, Label: "Me!", Command: good.Command},
		"colour":  {Slot: good.Slot, Label: "me", Colour: "black", Command: good.Command},
		"command": {Slot: good.Slot, Label: "me"},
		"newline": {Slot: "~/.claude\nx", Label: "me", Command: good.Command},
		"marker":  {Slot: "~/a label=x", Label: "me", Command: good.Command},
		// Warp runs commands in the pane's shell: a path that needs quoting is refused.
		"space":     {Slot: good.Slot, Label: "me", Command: "/home/u/My Apps/claude"},
		"winspace":  {Slot: good.Slot, Label: "me", Command: `C:\Users\Jane Doe\.local\bin\claude-2.cmd`},
		"dollar":    {Slot: good.Slot, Label: "me", Command: "/home/u/$x/claude"},
		"quote":     {Slot: good.Slot, Label: "me", Command: "/home/o'brien/claude"},
		"comma":     {Slot: good.Slot, Label: "me", Command: `C:\a,b\claude.cmd`},
		"backslash": {Slot: good.Slot, Label: "me", Command: `/home/u/a\b/claude`},
	} {
		if Check(s) == nil {
			t.Errorf("%s: Check accepted %+v", name, s)
		}
	}
	for _, c := range []string{"/home/u/.local/bin/claude-2", `C:\Users\u\.local\bin\claude-2.cmd`, `C:\PROGRA~1\claude.exe`, "/opt/claude_code-2.1/bin/claude"} {
		if err := Check(Spec{Slot: good.Slot, Label: "me", Command: c}); err != nil {
			t.Errorf("%s refused: %v", c, err)
		}
	}
	for _, b := range []string{"", "name = \"x\"\n", "# sy-warp-tab v2 slot=~ label=x (\n", "x\n# sy-warp-tab v1 slot=~ label=x (\n"} {
		if _, ok := ParseMarker([]byte(b)); ok {
			t.Errorf("marker found in %q", b)
		}
	}
	if s := tomlString("a\"b\\c\x01"); s != `"a\"b\\c\u0001"` {
		t.Errorf("tomlString = %s", s)
	}
}

func TestNamesAndURIs(t *testing.T) {
	if FileName("work") != "watchyard-work.toml" || Stem("work") != "watchyard-work" {
		t.Fatal("names")
	}
	for p, want := range map[string]string{
		"/x/tab_configs/watchyard-work.toml": "work", `C:\x\tab_configs\watchyard-a-1.toml`: "a-1",
		"/x/watchyard-.toml": "", "/x/watchyard-Work.toml": "", "/x/other.toml": "", "watchyard-x.json": "",
	} {
		if got := LabelOf(p); got != want {
			t.Errorf("LabelOf(%s) = %q", p, got)
		}
	}
	if !IsTabPath("/x/tab_configs/watchyard-work.toml") || !IsTabPath(`C:\a\tab_configs\watchyard-w.toml`) || IsTabPath("/x/other/watchyard-work.toml") {
		t.Error("IsTabPath")
	}
	if TabPath(`C:\a\tab_configs`, "w") != `C:\a\tab_configs\watchyard-w.toml` || TabPath("/a/tab_configs", "w") != "/a/tab_configs/watchyard-w.toml" {
		t.Error("TabPath")
	}
	if u := TabURI(Stable, "work"); u != "warp://tab_config/watchyard-work" || !ValidTabURI(u) {
		t.Errorf("stable uri %s", u)
	}
	if u := TabURI(Preview, "work"); u != "warppreview://tab_config/watchyard-work" || !ValidTabURI(u) {
		t.Errorf("preview uri %s", u)
	}
	for _, u := range []string{"warp://tab_config/other", "warp://tab_config/watchyard-work ", "https://tab_config/watchyard-w",
		"warp://tab_config/watchyard-../x", "warp://session/0123"} {
		if ValidTabURI(u) {
			t.Errorf("ValidTabURI(%q)", u)
		}
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	if got, err := Scan(filepath.Join(dir, "missing")); err != nil || got != nil {
		t.Fatalf("missing folder: %v %v", got, err)
	}
	ours := Render(Spec{Slot: "~/.claude", Label: "me", Command: "/bin/claude"})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "watchyard-me.toml"), ours, 0o644))
	must(os.WriteFile(filepath.Join(dir, "watchyard-x.toml"), []byte("name = \"mine\"\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "work.toml"), []byte("x"), 0o644))
	got, err := Scan(dir)
	must(err)
	if len(got) != 2 || got[0].Label != "me" || !got[0].Marker || got[0].SHA256 != Hash(ours) || got[0].Slot != "~/.claude" ||
		got[1].Label != "x" || got[1].Marker {
		t.Fatalf("scan: %+v", got)
	}
	if runtime.GOOS != "windows" {
		must(os.Symlink(filepath.Join(dir, "watchyard-me.toml"), filepath.Join(dir, "watchyard-ln.toml")))
		got, _ = Scan(dir)
		for _, f := range got {
			if f.Label == "ln" && (f.Marker || f.SHA256 != "") {
				t.Fatalf("a link is never ours: %+v", f)
			}
		}
	}
}
