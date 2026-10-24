package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/store"
)

// The overview names the hub's time zone and where it came from, so the
// board can label "hub time" (push.md 2). The hub.tz setting wins; else the machine's zone.
func TestOverviewHubZone(t *testing.T) {
	t.Setenv("TZ", "Asia/Kolkata") // the machine's zone, as SystemZone reads it
	h := startHub(t, "")
	phone := h.token("phone")
	get := func() (string, string) {
		t.Helper()
		code, body := h.do("GET", "/v1/overview", phone, nil)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var o struct {
			TZ       string `json:"tz"`
			TZSource string `json:"tz_source"`
		}
		if err := json.Unmarshal(body, &o); err != nil {
			t.Fatal(err)
		}
		return o.TZ, o.TZSource
	}
	put := func(v string) {
		t.Helper()
		raw, _ := json.Marshal(v)
		if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
			return tx.PutAlertSetting(store.AlertKeyHubTZ, raw, 1)
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		setting       string // "" = not set
		want, wantSrc string
	}{
		{"", "Asia/Kolkata", "system"},
		{"Europe/Berlin", "Europe/Berlin", "setting"},
		{"Not/AZone", "Asia/Kolkata", "system"}, // a zone the hub cannot load is ignored
	} {
		if tc.setting != "" {
			put(tc.setting)
		}
		if z, src := get(); z != tc.want || src != tc.wantSrc {
			t.Errorf("hub.tz %q: tz %q (%s), want %q (%s)", tc.setting, z, src, tc.want, tc.wantSrc)
		}
	}
}
