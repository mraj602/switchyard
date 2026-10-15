package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Widget refresh (push.md 13.5): the refresh type, prefs.widgets and refresh_min.
func TestRefreshFields(t *testing.T) {
	if KindLink != "link" || TypeRefresh != "refresh" {
		t.Fatal("constants")
	}
	var s Settings
	s.Defaults()
	if s.RefreshMin != 15 {
		t.Fatalf("refresh_min default %d, want 15", s.RefreshMin)
	}
	for _, c := range []struct {
		raw string
		ok  bool
	}{
		{`{}`, true}, {`{"refresh_min": 10}`, true}, {`{"refresh_min": 240}`, true},
		{`{"refresh_min": 9}`, false}, {`{"refresh_min": 241}`, false}, {`{"refresh_min": 0}`, false},
	} {
		got, err := ParseSettings([]byte(c.raw), "UTC")
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v, want ok=%v", c.raw, err, c.ok)
		}
		if c.raw == `{}` && got.RefreshMin != 15 {
			t.Errorf("absent key gave %d", got.RefreshMin)
		}
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"refresh_min":15`) {
		t.Fatalf("%s", b)
	}

	// Prefs: widgets is omitted when nil, so prefs without it encode unchanged.
	old := Prefs{Mute: []Trigger{}, Tap: TapAuto}
	ob, _ := json.Marshal(old)
	if string(ob) != `{"mute":[],"quiet_hours":null,"private":null,"tap":"auto","tz":null}` {
		t.Fatalf("%s", ob)
	}
	if old.WidgetsOn() {
		t.Fatal("nil widgets is on")
	}
	var p Prefs
	if err := json.Unmarshal([]byte(`{"widgets": true}`), &p); err != nil || !p.WidgetsOn() {
		t.Fatalf("%v %+v", err, p)
	}
	if pb, _ := json.Marshal(p); !strings.HasSuffix(string(pb), `"widgets":true}`) {
		t.Fatalf("%s", pb)
	}
}
