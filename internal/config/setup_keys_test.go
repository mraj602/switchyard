package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The keys `sy setup` writes (setup.md 6.1 config_set): share.*, slots.colours, folder
// names with dots, and SetJSON/Delete.
func TestSetupKeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]string{
		"slots.labels.~/.claude-2":  `"work"`,
		"slots.colours.~/.claude-2": `"#14b8a6"`,
		"share.primary":             `"~/.claude"`,
		"share.mcp":                 `"external"`,
		"share.mcp_exclude":         `["~/.claude-5", "~/.claude-6"]`,
	}
	for k, v := range set {
		if err := f.SetJSON(k, json.RawMessage(v)); err != nil {
			t.Fatalf("SetJSON(%s, %s): %v", k, v, err)
		}
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	f, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Typed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Slots.Labels["~/.claude-2"] != "work" || c.Slots.Colours["~/.claude-2"] != "#14b8a6" {
		t.Fatalf("slots = %+v", c.Slots)
	}
	if c.Share.Primary != "~/.claude" || c.MCPMode() != "external" ||
		!reflect.DeepEqual(c.Share.MCPExclude, []string{"~/.claude-5", "~/.claude-6"}) {
		t.Fatalf("share = %+v (mode %q)", c.Share, c.MCPMode())
	}
	if len(f.Unknown()) != 0 {
		t.Fatalf("setup keys reported unknown: %v", f.Unknown())
	}

	for _, v := range []string{`true`, `false`, `"true"`} {
		if err := f.SetJSON("share.mcp", json.RawMessage(v)); err != nil {
			t.Fatalf("share.mcp=%s: %v", v, err)
		}
	}
	for k, v := range map[string]string{
		"share.mcp":                 `"sometimes"`,
		"slots.colours.~/.claude-2": `"blue"`,
		"share.mcp_exclude":         `[1]`,
	} {
		if err := f.SetJSON(k, json.RawMessage(v)); err == nil {
			t.Errorf("SetJSON(%s, %s) accepted", k, v)
		}
	}

	// null deletes; only slot map keys and share.* can be deleted.
	for _, k := range []string{"slots.labels.~/.claude-2", "slots.colours.~/.claude-2", "share.mcp_exclude", "share.nothing"} {
		if err := f.SetJSON(k, json.RawMessage(`null`)); err != nil {
			t.Fatalf("delete %s: %v", k, err)
		}
	}
	if err := f.Delete("hub.url"); err == nil {
		t.Fatal("hub.url deleted")
	}
	c, _ = f.Typed()
	if _, ok := c.Slots.Labels["~/.claude-2"]; ok {
		t.Fatal("label not deleted")
	}
	if _, ok := c.Slots.Colours["~/.claude-2"]; ok {
		t.Fatal("colour not deleted")
	}
	if len(c.Share.MCPExclude) != 0 {
		t.Fatalf("mcp_exclude = %v", c.Share.MCPExclude)
	}
}

func TestMCPModeDefault(t *testing.T) {
	if m := Defaults().MCPMode(); m != "false" && m != "" {
		t.Fatalf("default MCPMode = %q", m)
	}
}
