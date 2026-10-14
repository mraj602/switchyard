package expo_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mraj602/switchyard/internal/hub/push/action"
	"github.com/mraj602/switchyard/internal/hub/push/expo"
	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/rules"
)

var update = flag.Bool("update", false, "rewrite testdata/message-*.json")

// The goldens are also read by the phone app's contract test (phone/test/contract.test.ts),
// which checks every testdata/*.json with its validators.

var (
	gNow   = time.Date(2026, 10, 13, 14, 2, 0, 0, time.UTC)
	gKey   = bytes.Repeat([]byte{7}, 32)
	gToken = "ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]"
	gTgt   = "pt_01JA3B0000000000000000000"
)

func sp(s string) *string { return &s }

func ms(t time.Time) model.Time { return model.Time(t.UnixMilli()) }

func sessionAlert(id string, t model.Trigger, f model.Facts) model.Alert {
	a := model.Alert{AlertBrief: model.AlertBrief{ID: id, Trigger: t, Status: model.StatusFired, FireCount: 1},
		Session: sp("sess_01JA3B7Q"), Seat: sp("seat-work"), Machine: sp("m-gpu"), Facts: f}
	a.SinceAt = ms(gNow.Add(-19 * time.Minute))
	return a
}

func seatAlert(id string, t model.Trigger, f model.Facts) model.Alert {
	return model.Alert{AlertBrief: model.AlertBrief{ID: id, Trigger: t, Status: model.StatusFired, FireCount: 1},
		Seat: sp("seat-client"), WindowKey: sp(f.Window), Facts: f}
}

// alertMessage builds the message the engine builds for one alert (engine/fire.go render).
func alertMessage(a model.Alert, o rules.TextOptions, needs int, tap string) model.Message {
	o.Now = gNow
	txt := rules.Render(a, o)
	p := model.Payload{V: model.PayloadVersion, Type: model.TypeAlert, Alert: a.ID, Alerts: []string{a.ID}, Trigger: a.Trigger,
		Title: txt.Title, Body: txt.Body, Tag: txt.Tag, URL: txt.URL, RemoteURL: a.Facts.RemoteURL, Tap: tap,
		NeedsYou: needs, Private: o.Private, TS: ms(gNow)}
	if a.Session != nil {
		p.Session = *a.Session
	}
	exp := gNow.Add(action.Lifetime)
	fire := uint32(a.FireCount)
	p.Actions = &model.Actions{
		Snooze: action.Encode(gKey, action.Token{Alert: a.ID, Action: action.Snooze, Fire: fire, Target: gTgt, Expires: exp}),
		Seen:   action.Encode(gKey, action.Token{Alert: a.ID, Action: action.Seen, Fire: fire, Target: gTgt, Expires: exp}),
	}
	return model.Message{Payload: p, TTL: rules.TTL(a.Trigger, false, int64(a.Facts.ResetsAt), gNow.UnixMilli()),
		Urgency: rules.Urgency(a.Trigger, false), Priority: rules.Priority(a.Trigger, false), HubURL: "https://hub.example.ts.net"}
}

func summaryMessage(alerts []model.Alert, private bool) model.Message {
	o := rules.TextOptions{Private: private, Now: gNow}
	txt := rules.RenderSummary(alerts, o)
	p := model.Payload{V: model.PayloadVersion, Type: model.TypeSummary, Alert: alerts[0].ID, Title: txt.Title, Body: txt.Body,
		Tag: txt.Tag, URL: txt.URL, Tap: model.TapAuto, NeedsYou: len(alerts), Private: private, TS: ms(gNow)}
	for _, a := range alerts {
		p.Alerts = append(p.Alerts, a.ID)
	}
	return model.Message{Payload: p, TTL: rules.TTL("", true, 0, 0), Urgency: rules.Urgency("", true), Priority: 4}
}

// testMessage is api/push.go's test push.
func testMessage() model.Message {
	return model.Message{Payload: model.Payload{V: model.PayloadVersion, Type: model.TypeTest, Title: "Switchyard",
		Body: "Test push from your hub", Tag: "sy-test", URL: "/#/", Tap: model.TapAuto, TS: ms(gNow)},
		TTL: time.Minute, Urgency: "high", Priority: 3}
}

func refreshMessage() model.Message {
	return model.Message{Payload: model.Payload{V: model.PayloadVersion, Type: model.TypeRefresh, NeedsYou: 2, TS: ms(gNow)},
		TTL: expo.RefreshTTL, Urgency: "normal"}
}

func goldenCases() map[string]model.Message {
	work := model.Facts{Seat: "work", Project: "api", Machine: "gpu-box"}
	with := func(f model.Facts, mod func(*model.Facts)) model.Facts { mod(&f); return f }
	resets := ms(time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC))
	room := &rules.Room{Label: "work", Left: 59.4}
	remote := "https://claude.ai/code/session_01JA3B7Q"
	q := sessionAlert("al_01JA3B7Q0001", model.TriggerAsked, with(work, func(f *model.Facts) {
		f.State, f.Detail, f.Question, f.RemoteURL = "asked", "question", "Should I also update the migration?", remote
	}))
	return map[string]model.Message{
		"needs_permission": alertMessage(sessionAlert("al_01JA3B7Q0002", model.TriggerNeedsPermission, with(work, func(f *model.Facts) {
			f.State, f.Tool, f.Message = "needs_permission", "Bash", "Claude needs your permission to use Bash"
		})), rules.TextOptions{}, 1, model.TapAuto),
		"asked_question": alertMessage(q, rules.TextOptions{}, 3, model.TapAuto),
		"asked_plan": alertMessage(sessionAlert("al_01JA3B7Q0003", model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Tool = "asked", "tool", "ExitPlanMode"
		})), rules.TextOptions{}, 1, model.TapSession),
		"asked_elicitation": alertMessage(sessionAlert("al_01JA3B7Q0004", model.TriggerAsked, with(work, func(f *model.Facts) {
			f.State, f.Detail, f.Message = "asked", "elicitation", "The MCP server wants a token"
		})), rules.TextOptions{}, 1, model.TapAuto),
		"failed_rate_limit": alertMessage(sessionAlert("al_01JA3B7Q0005", model.TriggerFailed,
			with(model.Facts{Seat: "client", Project: "data-pipeline", Machine: "gpu-box"},
				func(f *model.Facts) { f.State, f.Detail, f.ResetsAt = "failed", "rate_limit", resets })), rules.TextOptions{Room: room}, 1, model.TapAuto),
		"failed_auth": alertMessage(sessionAlert("al_01JA3B7Q0006", model.TriggerFailed, with(work, func(f *model.Facts) {
			f.State, f.Detail = "failed", "authentication_failed"
		})), rules.TextOptions{}, 1, model.TapAuto),
		"limit_80": alertMessage(seatAlert("al_01JA3B7Q0007", model.TriggerLimit80,
			model.Facts{Seat: "client", Window: "five_hour", Pct: 82.7, ResetsAt: resets}), rules.TextOptions{Room: room}, 0, model.TapAuto),
		"limit_95": alertMessage(seatAlert("al_01JA3B7Q0008", model.TriggerLimit95,
			model.Facts{Seat: "client", Window: "five_hour", Pct: 100, ResetsAt: resets}), rules.TextOptions{}, 0, model.TapAuto),
		"reset": alertMessage(seatAlert("al_01JA3B7Q0009", model.TriggerReset,
			model.Facts{Seat: "client", Window: "five_hour", Peak: 97.2}), rules.TextOptions{}, 0, model.TapAuto),
		"machine_silent": alertMessage(model.Alert{AlertBrief: model.AlertBrief{ID: "al_01JA3B7Q0010", Trigger: model.TriggerMachineSilent,
			FireCount: 1}, Machine: sp("m-old"), Facts: model.Facts{Machine: "old-vps", Sessions: 3,
			LastSeen: ms(gNow.Add(-58 * time.Minute))}}, rules.TextOptions{}, 0, model.TapAuto),
		"repush": func() model.Message {
			a := q
			a.FireCount = 2
			return alertMessage(a, rules.TextOptions{Repush: true}, 3, model.TapAuto)
		}(),
		"tap_claude":      alertMessage(q, rules.TextOptions{}, 3, model.TapClaude),
		"private_session": alertMessage(q, rules.TextOptions{Private: true}, 3, model.TapAuto),
		"private_limit": alertMessage(seatAlert("al_01JA3B7Q0011", model.TriggerLimit95,
			model.Facts{Seat: "client", Window: "five_hour", Pct: 96, ResetsAt: resets}), rules.TextOptions{Private: true}, 0, model.TapAuto),
		"summary":         summaryMessage(summaryAlerts(), false),
		"summary_private": summaryMessage(summaryAlerts(), true),
		"test":            testMessage(),
		"refresh":         refreshMessage(),
	}
}

func summaryAlerts() []model.Alert {
	mk := func(id, seat, project string, t model.Trigger, state string) model.Alert {
		a := sessionAlert(id, t, model.Facts{Seat: seat, Project: project, Machine: "gpu-box", State: state, Detail: "question"})
		a.Session = sp("sess_" + id[len(id)-4:])
		return a
	}
	return []model.Alert{
		mk("al_01JA3B7Q0101", "work", "api", model.TriggerAsked, "asked"),
		mk("al_01JA3B7Q0102", "work", "web", model.TriggerNeedsPermission, "needs_permission"),
		mk("al_01JA3B7Q0103", "client", "data-pipeline", model.TriggerAsked, "asked"),
	}
}

func indent(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		t.Fatal(err)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func TestMessageGoldens(t *testing.T) {
	cases := goldenCases()
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			wire, err := expo.Marshal(expo.Build(gToken, m))
			if err != nil {
				t.Fatal(err)
			}
			if len(wire) > expo.MaxMessageBytes {
				t.Fatalf("%d bytes", len(wire))
			}
			got := indent(t, wire)
			path := filepath.Join("testdata", "message-"+strings.ReplaceAll(name, "_", "-")+".json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs:\n%s\nwant:\n%s", path, got, want)
			}
		})
	}
	// Every golden file belongs to a case: the phone's contract test reads them all.
	files, _ := filepath.Glob(filepath.Join("testdata", "*.json"))
	if len(files) != len(cases) {
		t.Errorf("%d files in testdata, %d cases: %v", len(files), len(cases), files)
	}
}

// decode reads a built message back as a generic object.
func decode(t *testing.T, wire []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(wire, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMessageRules(t *testing.T) {
	cases := goldenCases()
	type want struct {
		channel, priority, category string
		ttl                         float64
		badge                       any
	}
	for name, w := range map[string]want{
		"needs_permission":  {"sy_needs_you", "high", "sy_alert", 21600, float64(1)},
		"asked_question":    {"sy_needs_you", "high", "sy_alert", 21600, float64(3)},
		"failed_rate_limit": {"sy_needs_you", "high", "sy_alert", 21600, float64(1)},
		"limit_80":          {"sy_limits", "normal", "sy_alert", 21600, float64(0)},
		"limit_95":          {"sy_limits", "high", "sy_alert", 10680, float64(0)},
		"reset":             {"sy_limits", "normal", "sy_alert", 3600, float64(0)},
		"machine_silent":    {"sy_machines", "normal", "sy_alert", 21600, float64(0)},
		"summary":           {"sy_needs_you", "high", "", 21600, float64(3)},
		"test":              {"sy_test", "high", "", 60, nil},
	} {
		wire, err := expo.Marshal(expo.Build(gToken, cases[name]))
		if err != nil {
			t.Fatal(err)
		}
		m := decode(t, wire)
		cat, _ := m["categoryId"].(string)
		if m["channelId"] != w.channel || m["priority"] != w.priority || cat != w.category || m["ttl"] != w.ttl ||
			m["badge"] != w.badge || m["sound"] != "default" || m["to"] != gToken {
			t.Errorf("%s: %s", name, wire)
		}
		data := m["data"].(map[string]any)
		if m["title"] != data["title"] || m["body"] != data["body"] {
			t.Errorf("%s: title/body differ from data", name)
		}
	}
	// Private: no remote_url in data; non-private keeps it (the app's tap rule needs it).
	pub := decode(t, mustMarshal(t, cases["asked_question"]))["data"].(map[string]any)
	priv := decode(t, mustMarshal(t, cases["private_session"]))["data"].(map[string]any)
	if pub["remote_url"] == nil || priv["remote_url"] != nil || priv["private"] != true || priv["title"] != "Switchyard" {
		t.Fatalf("public %v\nprivate %v", pub, priv)
	}
	// The engine's payload is not changed by the builder.
	if cases["private_session"].Payload.RemoteURL == "" {
		t.Fatal("Build modified the caller's payload")
	}
	// Refresh: silent.
	r := decode(t, mustMarshal(t, cases["refresh"]))
	if r["contentAvailable"] != true || r["priority"] != "normal" || r["ttl"] != float64(900) || r["title"] != nil ||
		r["sound"] != nil || r["badge"] != nil || r["categoryId"] != nil || r["channelId"] != nil {
		t.Fatalf("%v", r)
	}
	if d := r["data"].(map[string]any); len(d) != 4 || d["type"] != "refresh" || d["needs_you"] != float64(2) || d["ts"] != "2026-10-13T14:02:00.000Z" {
		t.Fatalf("%v", d)
	}
}

func mustMarshal(t *testing.T, m model.Message) []byte {
	t.Helper()
	b, err := expo.Marshal(expo.Build(gToken, m))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMessageCutTo4096(t *testing.T) {
	base := goldenCases()["asked_question"]
	actions := *base.Payload.Actions
	ids := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, "al_01JA3B7Q"+strings.Repeat("x", 200)+string(rune('a'+i)))
		}
		return out
	}
	for name, c := range map[string]struct {
		title, body string
		alerts      int
		wantBody    bool // body survives (non-empty)
		wantAlerts  int  // -1: any
	}{
		"body cut":            {"t", strings.Repeat("é<&", 2000), 1, true, 1},
		"body then alerts":    {"t", strings.Repeat("b", 3000), 20, false, -1},
		"body, alerts, title": {strings.Repeat("ü", 3000), strings.Repeat("b", 3000), 20, false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			m := base
			p := m.Payload
			p.Title, p.Body, p.Alerts = c.title, c.body, ids(c.alerts)
			m.Payload = p
			wire, err := expo.Marshal(expo.Build(gToken, m))
			if err != nil {
				t.Fatal(err)
			}
			if len(wire) > expo.MaxMessageBytes || len(wire) < expo.MaxMessageBytes-300 {
				t.Fatalf("%d bytes", len(wire))
			}
			if !utf8.Valid(wire) {
				t.Fatal("cut inside a rune")
			}
			msg := decode(t, wire)
			data := msg["data"].(map[string]any)
			acts := data["actions"].(map[string]any)
			if acts["snooze"] != actions.Snooze || acts["seen"] != actions.Seen {
				t.Fatal("actions were cut")
			}
			body, _ := data["body"].(string)
			if (body != "") != c.wantBody || (body != "" && !strings.HasSuffix(body, "…")) {
				t.Fatalf("body %q", body)
			}
			if msg["body"] != nil && msg["body"] != body {
				t.Fatal("body differs")
			}
			n := 0
			if a, ok := data["alerts"].([]any); ok {
				n = len(a)
			}
			if c.wantAlerts >= 0 && n != c.wantAlerts {
				t.Fatalf("%d alerts", n)
			}
			if title, _ := data["title"].(string); c.wantAlerts == 0 && !strings.HasSuffix(title, "…") {
				t.Fatalf("title %q", title)
			}
		})
	}
	// The caller's payload is untouched.
	if len(base.Payload.Body) == 0 || base.Payload.Actions == nil {
		t.Fatal("payload changed")
	}
}

func TestValidToken(t *testing.T) {
	for tok, ok := range map[string]bool{
		"ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]":           true,
		"ExpoPushToken[abcdEFGH_-12]":                         true,
		"ExponentPushToken[abc]":                              false,
		"ExponentPushToken[xxxxxxxx":                          false,
		"ExponentPushToken[xxxx xxxx]":                        false,
		"https://exp.host/x":                                  false,
		"ExponentPushToken[" + strings.Repeat("a", 201) + "]": false,
	} {
		if expo.ValidToken(tok) != ok {
			t.Errorf("%q: %v", tok, !ok)
		}
	}
}
