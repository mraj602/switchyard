package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The ledger's terminal block has the T10 keys and round-trips; an old ledger without it
// still reads, and an empty block is reported empty.
func TestLedgerTerminalRoundTrip(t *testing.T) {
	l := Ledger{V: 1, Terminal: &LedgerTerminal{
		Pane: &LedgerTermTargets{Targets: []string{"/home/u/.claude/settings.json"}},
		WarpPlugin: &LedgerWarpPlugin{Roots: []string{"/home/u/.claude/plugins"},
			Targets: []string{"/home/u/.claude/settings.json"}, Ref: "main",
			Commit: "53ab8d63ef74d7653002444762812d2fb5639eee"},
		Titles: &LedgerTermTargets{Targets: []string{"/home/u/.claude/settings.json"}},
		Tabs: []LedgerTab{{Path: "/home/u/.local/share/warp-terminal/tab_configs/switchyard-work.toml",
			SHA256: "eeee", Label: "work"}},
		Warp: &LedgerWarp{Channel: WarpStable, Folder: "/home/u/.local/share/warp-terminal/tab_configs"},
	}}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"terminal":{`, `"pane":{"targets"`, `"warp_plugin":{"roots"`, `"ref":"main"`,
		`"commit":"53ab`, `"titles":{"targets"`, `"tabs":[{"path"`, `"sha256":"eeee"`, `"label":"work"`,
		`"warp":{"channel":"stable","folder"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
	var back Ledger
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, l) {
		t.Fatalf("round trip: %v\n%+v", err, back)
	}
	if back.Terminal.Empty() || !(&LedgerTerminal{}).Empty() || !(*LedgerTerminal)(nil).Empty() {
		t.Error("Empty() is wrong")
	}
	var old Ledger
	if err := json.Unmarshal([]byte(`{"v":1,"timer":true}`), &old); err != nil || old.Terminal != nil {
		t.Fatalf("old ledger: %v %+v", err, old)
	}
	if b, _ := json.Marshal(Ledger{V: 1}); strings.Contains(string(b), "terminal") {
		t.Errorf("an empty ledger writes a terminal key: %s", b)
	}
}

func TestTermChoices(t *testing.T) {
	if (TermChoices{}).Any() || (TermChoices{Off: true, Channel: WarpPreview}).Any() {
		t.Error("no piece chosen, Any() true")
	}
	for _, c := range []TermChoices{{Pane: true}, {WarpPlugin: true}, {Titles: true}, {WarpTabs: true}} {
		if !c.Any() {
			t.Errorf("%+v: Any() false", c)
		}
	}
	b, _ := json.Marshal(TermChoices{Pane: true, WarpTabs: true, Channel: WarpPreview})
	if string(b) != `{"pane":true,"warp_tabs":true,"channel":"preview"}` {
		t.Errorf("json = %s", b)
	}
}
