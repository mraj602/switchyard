package config

import (
	"encoding/json"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/model"
)

var labelCases = []string{
	"work", "a", "claude-2", "0-x", "abcdefghijklmnopqrstuvwx",
	"", "-x", "Work", "x_y", "x y", "x/../../evil", "x/../../evil\ntouch /tmp/pwned",
	"abcdefghijklmnopqrstuvwxy", "é", "x\r", "x^", "x%y",
}

// Regression: slots.labels.* values reach launcher file names and launcher text, so Set and
// SetJSON refuse anything that is not a valid label and leave the file unchanged.
func TestSetRejectsInvalidLabels(t *testing.T) {
	f, _ := Load(t.TempDir())
	if err := f.Set("slots.labels.~/.claude-2", []string{"work"}); err != nil {
		t.Fatal(err)
	}
	for _, l := range labelCases {
		if model.ValidLabel(l) {
			continue
		}
		if err := f.Set("slots.labels.~/.claude-2", []string{l}); err == nil {
			t.Errorf("Set label %q: want an error", l)
		}
		raw, _ := json.Marshal(l)
		if err := f.SetJSON("slots.labels.~/.claude-3", raw); err == nil {
			t.Errorf("SetJSON label %q: want an error", l)
		}
	}
	if v, _ := f.Get("slots.labels.~/.claude-2"); v != "work" {
		t.Errorf("label changed to %#v", v)
	}
	if _, err := f.Get("slots.labels.~/.claude-3"); err == nil {
		t.Error("an invalid label was stored")
	}
	if err := f.SetJSON("slots.labels.~/.claude-3", json.RawMessage(`"client"`)); err != nil {
		t.Error(err)
	}
}

// config keeps its own copy of the rule (it must not depend on the setup packages); it must
// agree with model.ValidLabel.
func TestValidLabelMatchesModel(t *testing.T) {
	for _, l := range labelCases {
		if ValidLabel(l) != model.ValidLabel(l) {
			t.Errorf("%q: config %v, model %v", l, ValidLabel(l), model.ValidLabel(l))
		}
	}
}
