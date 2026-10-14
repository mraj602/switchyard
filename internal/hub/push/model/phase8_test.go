package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Phase 8 (push.md 12): the expo kind, the refresh type, Result.Ref, prefs.widgets and
// expo_refresh_min.
func TestPhase8Fields(t *testing.T) {
	if KindExpo != "expo" || TypeRefresh != "refresh" {
		t.Fatal("constants")
	}
	var s Settings
	s.Defaults()
	if s.ExpoRefreshMin != 15 {
		t.Fatalf("expo_refresh_min default %d, want 15", s.ExpoRefreshMin)
	}
	for _, c := range []struct {
		raw string
		ok  bool
	}{
		{`{}`, true}, {`{"expo_refresh_min": 10}`, true}, {`{"expo_refresh_min": 240}`, true},
		{`{"expo_refresh_min": 9}`, false}, {`{"expo_refresh_min": 241}`, false}, {`{"expo_refresh_min": 0}`, false},
	} {
		got, err := ParseSettings([]byte(c.raw), "UTC")
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v, want ok=%v", c.raw, err, c.ok)
		}
		if c.raw == `{}` && got.ExpoRefreshMin != 15 {
			t.Errorf("absent key gave %d", got.ExpoRefreshMin)
		}
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"expo_refresh_min":15`) {
		t.Fatalf("%s", b)
	}

	// Prefs: widgets is omitted when nil, so prefs written before phase 8 encode unchanged.
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

	// Target meta is expo only and never carries a secret.
	tg, _ := json.Marshal(Target{ID: "pt_1", Kind: KindExpo, Endpoint: "ExponentPushToken[x]",
		Meta: &TargetMeta{Platform: PlatformIOS, AppVersion: "0.8.0", AppBuild: "12"}})
	if !strings.Contains(string(tg), `"meta":{"platform":"ios","app_version":"0.8.0","app_build":"12"}`) {
		t.Fatalf("%s", tg)
	}
	if wb, _ := json.Marshal(Target{ID: "pt_2", Kind: KindWebPush}); strings.Contains(string(wb), "meta") {
		t.Fatalf("%s", wb)
	}

	if (Result{Outcome: OutcomeSent, Ref: "t1"}).Ref != "t1" {
		t.Fatal("ref")
	}
}
