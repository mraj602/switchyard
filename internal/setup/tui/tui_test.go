package tui

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>, after replacing the fixture home with $HOME.
func golden(t *testing.T, name, home, got string) {
	t.Helper()
	got = strings.ReplaceAll(got, home, "$HOME")
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file (go test -update rewrites it)\n--- got ---\n%s", name, got)
	}
}

func laptop(t *testing.T) (*detecttest.Home, model.State) {
	h := detecttest.Laptop(t)
	return h, h.State()
}

func TestRawWalkthroughGolden(t *testing.T) {
	h, st := laptop(t)
	// Main -> Accounts: add "work:blue" -> Sharing: down, toggle -> Hub: Enter -> Done: Enter.
	keys := "\r" + "awork:sky\r" + "\r" + "\x1b[B \r" + "\r" + "\r"
	var out bytes.Buffer
	c, err := Run(st, model.Choices{}, Options{In: strings.NewReader(keys), Out: &out, Raw: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Add) != 1 || c.Add[0].Label != "work" || c.Add[0].Colour != "sky" {
		t.Fatalf("Add = %+v", c.Add)
	}
	if !c.Interactive || c.Login != "ask" {
		t.Fatalf("Interactive/Login = %v/%q", c.Interactive, c.Login)
	}
	if c.Hub.Mode != model.HubHere {
		t.Fatalf("hub = %+v, want here (the default without a hub)", c.Hub)
	}
	if c.Share == nil {
		t.Fatal("toggling a switch left Share at the defaults")
	}
	if !strings.HasPrefix(out.String(), clear) || strings.Contains(out.String(), "\n\n") {
		t.Fatal("raw frames must clear the screen and use \\r\\n")
	}
	golden(t, "raw_walkthrough.golden", h.Root, out.String())
}

func TestLinesWalkthroughGolden(t *testing.T) {
	h, st := laptop(t)
	in := "\n" + "work\n" + "\n" + "b\n" + "\n" + "3\n" + "\n" + "4\n" + "\n"
	var out bytes.Buffer
	c, err := Run(st, model.Choices{}, Options{In: strings.NewReader(in), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Add) != 1 || c.Add[0].Label != "work" {
		t.Fatalf("Add = %+v", c.Add)
	}
	if c.Hub.Mode != model.HubLater {
		t.Fatalf("hub = %+v, want later (option 4)", c.Hub)
	}
	golden(t, "lines_walkthrough.golden", h.Root, out.String())
}

func TestDetectTextGolden(t *testing.T) {
	h, st := laptop(t)
	txt := DetectText(st)
	if strings.Contains(txt, "sk-secret") || strings.Contains(txt, "do-not-read") {
		t.Fatal("detect text shows a secret")
	}
	golden(t, "detect_laptop.golden", h.Root, txt)
}

// q (or end of input) on any screen quits with ErrQuit and nothing on disk changes.
func TestQuitOnEveryScreenWritesNothing(t *testing.T) {
	h, st := laptop(t)
	before := testtree.Take(t, h.Root, testtree.Opts{})
	for n := 0; n < len(Screens); n++ {
		for _, raw := range []bool{true, false} {
			var keys string
			if raw {
				keys = strings.Repeat("\r", n) + "q"
			} else {
				keys = strings.Repeat("\n", n) + "q\n"
			}
			_, err := Run(st, model.Choices{}, Options{In: strings.NewReader(keys), Out: &bytes.Buffer{}, Raw: raw})
			if !errors.Is(err, ErrQuit) {
				t.Fatalf("screen %d raw=%v: err %v, want ErrQuit", n, raw, err)
			}
			_, err = Run(st, model.Choices{}, Options{In: strings.NewReader(strings.Repeat("\r", n)), Out: &bytes.Buffer{}, Raw: raw})
			if !errors.Is(err, ErrQuit) {
				t.Fatalf("EOF on screen %d raw=%v: err %v, want ErrQuit", n, raw, err)
			}
		}
	}
	testtree.Equal(t, before, testtree.Take(t, h.Root, testtree.Opts{}))
}

func TestEscGoesBackAndPluginsFollowSettings(t *testing.T) {
	_, st := laptop(t)
	u := &ui{st: st, share: map[model.Switch]bool{}}
	u.share[model.SwitchSettings] = true
	u.share[model.SwitchPlugins] = true
	u.toggle(model.SwitchPlugins)
	if !u.share[model.SwitchPlugins] {
		t.Fatal("Plugins went off while Settings is on")
	}
	// Esc on Accounts returns to Main; Enter twice more reaches Sharing, then quit.
	var out bytes.Buffer
	_, err := Run(st, model.Choices{}, Options{In: strings.NewReader("\r\x1b\r\rq"), Out: &out, Raw: true})
	if !errors.Is(err, ErrQuit) {
		t.Fatal(err)
	}
	frames := strings.Split(out.String(), clear)
	if len(frames) < 5 {
		t.Fatalf("%d frames", len(frames))
	}
	if frames[1] != frames[3] {
		t.Fatal("Esc did not return to the first screen")
	}
}

func TestConfirm(t *testing.T) {
	for _, tc := range []struct {
		in   string
		raw  bool
		def  bool
		want bool
	}{
		{"\n", false, true, true},
		{"n\n", false, true, false},
		{"yes\n", false, false, true},
		{"", false, true, false},
		{"y", true, false, true},
		{"\r", true, true, true},
	} {
		if got := Confirm(strings.NewReader(tc.in), &bytes.Buffer{}, tc.raw, "Apply?", tc.def); got != tc.want {
			t.Errorf("Confirm(%q raw=%v def=%v) = %v", tc.in, tc.raw, tc.def, got)
		}
	}
}
