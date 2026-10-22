package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	// Line endings are compared separately, so a checkout that converts the golden's
	// CRLF does not fail the test.
	norm := func(b []byte) string { return strings.ReplaceAll(string(b), "\r\n", "\n") }
	if norm(want) != norm(got) {
		t.Errorf("%s differs:\n--- want\n%q\n--- got\n%q", name, want, got)
	}
	if strings.HasSuffix(name, ".cmd.golden") != strings.Contains(string(got), "\r\n") {
		t.Errorf("%s: wrong line endings in %q", name, got)
	}
}

func TestGolden(t *testing.T) {
	s := Spec{Slot: "~/.claude-2", Label: "work"}
	golden(t, "claude-2.sh.golden", Render("linux", s))
	golden(t, "claude-2.sh.golden", Render("darwin", s))
	golden(t, "claude-2.cmd.golden", Render("windows", s))
}

func TestFileName(t *testing.T) {
	if FileName("linux", "claude-2") != "claude-2" || FileName("windows", "claude-2") != "claude-2.cmd" {
		t.Error("FileName")
	}
	if Name("~/.claude-2") != "claude-2" || Name("~/.claude") != "claude" || Name("/x/y/.claude_z") != "claude_z" {
		t.Error("Name")
	}
}

func TestParseMarkerAndHash(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		b := Render(goos, Spec{Slot: "~/.claude-3", Label: "client"})
		info, ok := ParseMarker(b)
		if !ok || info.Slot != "~/.claude-3" || info.Label != "client" {
			t.Errorf("%s: %+v %v", goos, info, ok)
		}
		h := Hash(b)
		if !StillOurs(b, h) || StillOurs(append(b, '#'), h) || StillOurs(b, "") {
			t.Errorf("%s: StillOurs", goos)
		}
	}
	if info, ok := ParseMarker(Render("linux", Spec{Slot: "~/.claude-4"})); !ok || info.Label != "" {
		t.Errorf("no label: %+v %v", info, ok)
	}
	for _, bad := range []string{"", "#!/bin/sh\n", "#!/bin/sh\n# something else\n", "# sy-launcher v1 slot=x\n#!/bin/sh\n", "#!/bin/sh\n# sy-launcher v10 slot=x\n"} {
		if _, ok := ParseMarker([]byte(bad)); ok {
			t.Errorf("ParseMarker(%q) ok", bad)
		}
	}
}

func TestQuoting(t *testing.T) {
	b := string(Render("linux", Spec{Slot: "/opt/my dir/it's"}))
	if !strings.Contains(b, `CLAUDE_CONFIG_DIR='/opt/my dir/it'\''s'`) {
		t.Errorf("abs quoting:\n%s", b)
	}
	b = string(Render("linux", Spec{Slot: "~/.claude-$x"}))
	if !strings.Contains(b, `CLAUDE_CONFIG_DIR="$HOME/.claude-\$x"`) {
		t.Errorf("dollar:\n%s", b)
	}
}

// A rendered launcher run under sh with a fake claude on PATH gives claude CLAUDE_CONFIG_DIR
// and every argument unchanged.
func TestRunUnderSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{home, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := "#!/bin/sh\nprintf 'dir=%s\\n' \"$CLAUDE_CONFIG_DIR\"\nfor a in \"$@\"; do printf 'arg=[%s]\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	l := filepath.Join(bin, "claude-2")
	if err := os.WriteFile(l, Render("linux", Spec{Slot: "~/.claude-2", Label: "work"}), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, l, "--resume", "a b", "", "$HOME", "it's", "-p", "x*")
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "CLAUDE_CONFIG_DIR=/wrong"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "dir=" + home + "/.claude-2\narg=[--resume]\narg=[a b]\narg=[]\narg=[$HOME]\narg=[it's]\narg=[-p]\narg=[x*]\n"
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

// Regression: a label or folder with a newline must not end the marker/comment line and
// inject a command (a label from a hand-edited config.toml).
func TestInterpolatedValuesStayOnOneLine(t *testing.T) {
	// A folder with a newline keeps it inside the quoted sh value (that is the folder's real
	// name; Check refuses it anyway), but never in a comment line.
	slotNL := Spec{Slot: "~/.claude-2\r\ntouch /tmp/pwned-slot", Label: "work"}
	if m := markerLine(slotNL); strings.ContainsAny(m, "\r\n") {
		t.Errorf("marker line %q", m)
	}
	for _, goos := range []string{"linux", "windows"} {
		for _, l := range strings.Split(strings.ReplaceAll(string(Render(goos, slotNL)), "\r\n", "\n"), "\n") {
			if strings.HasPrefix(l, "touch") && goos == "windows" {
				t.Errorf("%s: injected line %q", goos, l)
			}
		}
		if info, ok := ParseMarker(Render(goos, slotNL)); !ok || info.Label != "work" {
			t.Errorf("%s: marker %+v %v", goos, info, ok)
		}
	}

	s := Spec{Slot: "~/.claude-2", Label: "x/../../evil\ntouch /tmp/pwned\u0085y"}
	for goos, lines := range map[string]int{"linux": 6, "windows": 5} {
		b := string(Render(goos, s))
		got := strings.Split(strings.TrimSuffix(strings.ReplaceAll(b, "\r\n", "\n"), "\n"), "\n")
		if len(got) != lines {
			t.Errorf("%s: %d lines, want %d:\n%q", goos, len(got), lines, b)
		}
		for _, l := range got {
			if strings.HasPrefix(strings.TrimSpace(l), "touch") {
				t.Errorf("%s: injected line %q", goos, l)
			}
		}
		if goos == "linux" && strings.Contains(b, "\r") {
			t.Errorf("linux: CR in %q", b)
		}
	}
}

func TestCheck(t *testing.T) {
	ok := []struct{ goos, slot, label string }{
		{"linux", "~/.claude-2", "work"},
		{"linux", "/opt/é dir/x", ""},
		{"windows", "~/.claude-2", "work"},
		{"windows", "C:/Users/a b/100%/x&y", "a-1"},
	}
	for _, c := range ok {
		if err := Check(c.goos, Spec{Slot: c.slot, Label: c.label}); err != nil {
			t.Errorf("Check(%s, %q, %q) = %v", c.goos, c.slot, c.label, err)
		}
	}
	bad := []struct{ goos, slot, label string }{
		{"linux", "~/.claude-2", "x/../../evil\ntouch /tmp/pwned"},
		{"linux", "~/.claude-2", "Work"},
		{"linux", "~/.claude-2\nx", ""},
		{"windows", "~/.claude-2", "work^"},
		{"windows", "C:/Users/é/x", ""},
		{"windows", `C:/a"b`, ""},
		{"windows", "C:/a^", ""},
		{"windows", "C:/a\tb", ""},
	}
	for _, c := range bad {
		if err := Check(c.goos, Spec{Slot: c.slot, Label: c.label}); err == nil {
			t.Errorf("Check(%s, %q, %q) = nil, want an error", c.goos, c.slot, c.label)
		}
	}
}

// `%` starts an expansion in a .cmd file (even in a rem line or inside quotes): it is
// doubled everywhere, for ~/ and absolute folders, and the marker still parses back.
func TestWindowsPercentEscaping(t *testing.T) {
	for slot, set := range map[string]string{
		"C:/Users/a%PATH%b/.claude-x": `@set "CLAUDE_CONFIG_DIR=C:\Users\a%%PATH%%b\.claude-x"`,
		"~/.claude-%~x%":              `@set "CLAUDE_CONFIG_DIR=%USERPROFILE%\.claude-%%~x%%"`,
	} {
		b := string(Render("windows", Spec{Slot: slot, Label: "work"}))
		if !strings.Contains(b, set+"\r\n") {
			t.Errorf("%s: want %q in:\n%s", slot, set, b)
		}
		for _, l := range strings.Split(b, "\r\n") {
			if strings.HasPrefix(l, "rem ") && strings.Contains(strings.ReplaceAll(l, "%%", ""), "%") {
				t.Errorf("%s: single %% in %q", slot, l)
			}
		}
		info, ok := ParseMarker([]byte(b))
		if !ok || info.Slot != slot || info.Label != "work" {
			t.Errorf("%s: ParseMarker = %+v %v", slot, info, ok)
		}
	}
	// Only the folder's own % are doubled: %USERPROFILE% and %* stay as they are.
	b := string(Render("windows", Spec{Slot: "~/.claude-2"}))
	if !strings.Contains(b, `=%USERPROFILE%\.claude-2"`) || !strings.Contains(b, "@claude %*\r\n") {
		t.Errorf("plain launcher changed:\n%s", b)
	}
}
