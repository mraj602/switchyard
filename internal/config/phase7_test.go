package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalKeysDefaults(t *testing.T) {
	f, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Typed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Terminal.Titles || !c.Terminal.Band || !c.Terminal.PaneAutoOpen || c.Terminal.Source != "auto" || c.Terminal.ASCII {
		t.Fatalf("defaults: %+v", c.Terminal)
	}
	for k, want := range map[string]any{"terminal.titles": false, "terminal.band": true,
		"terminal.pane_auto_open": true, "terminal.source": "auto", "terminal.ascii": false} {
		if got, err := f.Get(k); err != nil || got != want {
			t.Errorf("Get %s = %v (%v), want %v", k, got, err, want)
		}
	}
}

func TestTerminalKeysSetAndValidate(t *testing.T) {
	f, _ := Load(t.TempDir())
	for k, args := range map[string][]string{
		"terminal.band": {"false"}, "terminal.pane_auto_open": {"off"},
		"terminal.source": {"local"}, "terminal.ascii": {"true"},
	} {
		if err := f.Set(k, args); err != nil {
			t.Errorf("Set %s: %v", k, err)
		}
	}
	c, err := f.Typed()
	if err != nil || c.Terminal.Band || c.Terminal.PaneAutoOpen || c.Terminal.Source != "local" || !c.Terminal.ASCII {
		t.Fatalf("after set: %+v (%v)", c.Terminal, err)
	}
	if err := f.Set("terminal.source", []string{"remote"}); err == nil || !strings.Contains(err.Error(), "auto, hub or local") {
		t.Errorf("bad source accepted: %v", err)
	}
	if err := f.Set("terminal.band", []string{"maybe"}); err == nil {
		t.Error("bad bool accepted")
	}
}

// terminal.titles is written only by `sy term titles on|off`: `sy config set` refuses it,
// the plan path (SetJSON, config_set) accepts it.
func TestTerminalTitlesIsManaged(t *testing.T) {
	f, _ := Load(t.TempDir())
	err := f.Set("terminal.titles", []string{"true"})
	if err == nil || !strings.Contains(err.Error(), "sy term titles on|off") {
		t.Fatalf("sy config set terminal.titles: %v", err)
	}
	if err := f.SetJSON("terminal.titles", json.RawMessage(`true`)); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}
	if c, err := f.Typed(); err != nil || !c.Terminal.Titles {
		t.Fatalf("titles not set: %+v %v", c.Terminal, err)
	}
	if err := f.SetJSON("terminal.titles", json.RawMessage(`false`)); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.Typed(); c.Terminal.Titles {
		t.Fatal("titles not cleared")
	}
}

// A [terminal] table with a key this version does not know survives a rewrite, and only
// that key is reported unknown.
func TestTerminalUnknownKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := "version = 1\n\n[terminal]\nband = false\nsource = \"hub\"\nfuture_key = \"kept\"\n\n[terminal.later]\nx = 1\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u := f.Unknown(); strings.Join(u, ",") != "terminal.future_key,terminal.later" {
		t.Fatalf("Unknown = %v", u)
	}
	if err := f.Set("terminal.ascii", []string{"true"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := back.Typed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Terminal.Band || c.Terminal.Source != "hub" || !c.Terminal.ASCII || !c.Terminal.PaneAutoOpen {
		t.Fatalf("after round trip: %+v", c.Terminal)
	}
	if v, err := back.Get("terminal.future_key"); err != nil || v != "kept" {
		t.Fatalf("unknown key lost: %v %v", v, err)
	}
	if v, err := back.Get("terminal.later"); err != nil || v.(map[string]any)["x"] != int64(1) {
		t.Fatalf("unknown table lost: %v %v", v, err)
	}
}
