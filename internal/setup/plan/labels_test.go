package plan_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/plan"
)

const evilLabel = "x/../../evil\ntouch /tmp/pwned"

// Regression (review finding): a label that reached State unvalidated (hand-edited
// config.toml) must not become a launcher path outside the launcher dir or launcher text.
// The planner ignores it and uses the folder's default label.
func TestInvalidStateLabelIgnored(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	found := false
	for i := range st.Folders {
		if st.Folders[i].Folder == "~/.claude-4" {
			st.Folders[i].Label, st.Folders[i].Launcher = evilLabel, nil
			found = true
		}
	}
	if !found {
		t.Fatal("no ~/.claude-4 in the laptop fixture")
	}
	var keep []model.Launcher
	for _, l := range st.Launchers {
		if l.Name != "claude-4" {
			keep = append(keep, l)
		}
	}
	st.Launchers = keep
	p := mustPlan(t, st, model.Choices{NamedLaunchers: true})
	bin := h.Abs(".local/bin")
	launchers := 0
	for _, a := range p.Actions {
		if a.Op == model.OpWriteFile && strings.Contains(a.Path, "claude-") {
			launchers++
			if filepath.Dir(a.Path) != bin {
				t.Errorf("launcher outside %s: %q", bin, a.Path)
			}
		}
		if strings.Contains(a.Path, "evil") || strings.Contains(a.Note, "evil") || strings.Contains(string(a.Value), "evil") {
			t.Errorf("invalid label leaked into %+v", a)
		}
		if a.Op == model.OpConfigSet {
			var cfg map[string]any
			_ = json.Unmarshal(a.Value, &cfg)
			if v, ok := cfg["slots.labels.~/.claude-4"]; ok && v != "claude-4" {
				t.Errorf("label set to %#v", v)
			}
		}
	}
	if launchers == 0 {
		t.Errorf("no launcher planned for ~/.claude-4:\n%s", dump(p))
	}
}

// Explicit labels from --labels / --add / choices are refused, not ignored.
func TestInvalidChoiceLabelsRefused(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	for name, c := range map[string]model.Choices{
		"labels":   {Labels: map[string]model.LabelColour{"~/.claude-2": {Label: evilLabel}}},
		"add":      {Add: []model.LabelColour{{Label: evilLabel}}},
		"accounts": {Accounts: []string{"a", "b", "c", "d", "e", "f", "g", evilLabel}},
		"caret":    {Add: []model.LabelColour{{Label: "w^"}}},
	} {
		if _, err := plan.PlanWith(st, c, opt); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A Windows folder that cannot be written into a .cmd launcher (non-ASCII: the file is
// UTF-8, cmd.exe reads the OEM code page) gets no launcher and a warning, not a broken one.
func TestWindowsNonASCIIFolderNoLauncher(t *testing.T) {
	h := detecttest.Laptop(t)
	st := h.State()
	st.OS = "windows"
	for i := range st.Folders {
		if st.Folders[i].Folder == "~/.claude-4" {
			st.Folders[i].Folder, st.Folders[i].Launcher = "D:/Bénédicte/.claude-4", nil
		}
	}
	var keep []model.Launcher
	for _, l := range st.Launchers {
		if l.Name != "claude-4" {
			keep = append(keep, l)
		}
	}
	st.Launchers = keep
	p := mustPlan(t, st, model.Choices{})
	for _, a := range p.Actions {
		if a.Op == model.OpWriteFile && strings.Contains(a.Path, "claude-4") {
			t.Errorf("launcher planned for a non-ASCII folder: %+v", a)
		}
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "not ASCII") {
		t.Errorf("no warning: %v", p.Warnings)
	}
}
