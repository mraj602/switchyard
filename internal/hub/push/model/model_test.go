package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The example of push.md 6.5 (values shortened where the spec elides them).
const payloadExample = `{"v":1,"type":"alert","alert":"al_01JA3B","alerts":["al_01JA3B"],"trigger":"asked",` +
	`"title":"work · api · asked a question","body":"Should I also update the migration?",` +
	`"tag":"wy-s-c41e0000-0000-4000-8000-000000000003",` +
	`"url":"/#/sessions/c41e0000-0000-4000-8000-000000000003?alert=al_01JA3B",` +
	`"remote_url":"https://claude.ai/code/session_","tap":"auto",` +
	`"session":"c41e0000-0000-4000-8000-000000000003","needs_you":3,` +
	`"actions":{"snooze":"a1.x","seen":"a1.y"},"private":false,"ts":"2026-10-12T14:04:01.000Z"}`

func TestPayloadMarshalsToSpecExample(t *testing.T) {
	p := Payload{
		V: PayloadVersion, Type: TypeAlert, Alert: "al_01JA3B", Alerts: []string{"al_01JA3B"},
		Trigger: TriggerAsked, Title: "work · api · asked a question", Body: "Should I also update the migration?",
		Tag:       "wy-s-c41e0000-0000-4000-8000-000000000003",
		URL:       "/#/sessions/c41e0000-0000-4000-8000-000000000003?alert=al_01JA3B",
		RemoteURL: "https://claude.ai/code/session_", Tap: TapAuto,
		Session: "c41e0000-0000-4000-8000-000000000003", NeedsYou: 3,
		Actions: &Actions{Snooze: "a1.x", Seen: "a1.y"},
		TS:      TimeOf(time.Date(2026, 10, 12, 14, 4, 1, 0, time.UTC)),
	}
	got, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payloadExample { // byte for byte: keys and their order are the contract
		t.Fatalf("payload\n got %s\nwant %s", got, payloadExample)
	}
	var back Payload
	if err := json.Unmarshal(got, &back); err != nil || !reflect.DeepEqual(back, p) {
		t.Fatalf("round trip: %v\n%+v\n%+v", err, back, p)
	}
}

func TestPayloadOmitsAbsentFields(t *testing.T) {
	b, _ := json.Marshal(Payload{V: 1, Type: TypeTest, Title: "t", Tag: "wy-test", URL: "/#/", Tap: "auto"})
	for _, k := range []string{`"alert"`, `"alerts"`, `"actions"`, `"remote_url"`, `"session"`} {
		if strings.Contains(string(b), k) {
			t.Errorf("%s present in %s", k, b)
		}
	}
}

func TestSettingsDefaults(t *testing.T) {
	var s Settings
	s.Defaults()
	if s.SnoozeMin != 15 || s.MaxAgeS != 900 || s.BatchS != 2 || s.Flood != (Flood{6, 600}) ||
		s.QuietHours.Start != "23:00" || s.QuietHours.End != "08:00" || s.QuietHours.Enabled || s.Private || s.SkipRemote {
		t.Fatalf("%+v", s)
	}
	if !s.QuietHours.LetCriticalThrough() || len(s.WebPushHosts) != 7 {
		t.Fatalf("%+v", s)
	}
	got, err := ParseSettings([]byte(`{"snooze_min": 30, "batch_s": 0, "flood": {"max": 3, "per_s": 60}}`), "Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	if got.SnoozeMin != 30 || got.BatchS != 0 || got.Flood.Max != 3 || got.MaxAgeS != 900 || got.QuietHours.TZ != "Asia/Kolkata" {
		t.Fatalf("%+v", got)
	}
	if _, err := ParseSettings([]byte(`{"snooze_min": 1}`), ""); err == nil {
		t.Fatal("snooze_min 1 accepted")
	}
	if d, _ := ParseSettings(nil, ""); d.QuietHours.TZ != "UTC" {
		t.Fatalf("tz %q", d.QuietHours.TZ)
	}
	round, _ := json.Marshal(s)
	var again Settings
	if err := json.Unmarshal(round, &again); err != nil || !reflect.DeepEqual(again, s) {
		t.Fatalf("round trip: %v", err)
	}
}

func TestTimeAndAlertJSON(t *testing.T) {
	at := TimeOf(time.Date(2026, 10, 12, 14, 4, 1, 0, time.UTC))
	b, _ := json.Marshal(AlertBrief{ID: "al_1", Trigger: TriggerAsked, Status: StatusPending, DueAt: at})
	want := `{"id":"al_1","trigger":"asked","status":"pending","reason":null,"due_at":"2026-10-12T14:04:01.000Z","fired_at":null,"fire_count":0,"snoozed_until":null,"seen_at":null}`
	if string(b) != want {
		t.Fatalf("%s", b)
	}
	var back AlertBrief
	if err := json.Unmarshal(b, &back); err != nil || back.DueAt != at || back.FiredAt != 0 {
		t.Fatalf("%v %+v", err, back)
	}
	a := Alert{AlertBrief: AlertBrief{ID: "al_1"}, Episode: "e", SeenBy: "d", Facts: Facts{Seat: "x"}}
	ab, _ := json.Marshal(a)
	if strings.Contains(string(ab), "episode") || strings.Contains(string(ab), `"facts"`) || !strings.Contains(string(ab), `"id":"al_1"`) {
		t.Fatalf("%s", ab)
	}
	tg, _ := json.Marshal(Target{ID: "pt_1", Secret: "tk_secret"})
	if strings.Contains(string(tg), "tk_secret") {
		t.Fatal("secret leaked")
	}
}

func TestPrefsHelpers(t *testing.T) {
	p := Prefs{Mute: []Trigger{TriggerLimit80}}
	if !p.Muted(TriggerLimit80) || p.Muted(TriggerAsked) || p.TapMode() != TapAuto {
		t.Fatal("prefs")
	}
	if !(TriggerAsked.Valid() && !Trigger("context_90").Valid()) || !StatusFired.Open() || StatusSeen.Open() {
		t.Fatal("enums")
	}
}
