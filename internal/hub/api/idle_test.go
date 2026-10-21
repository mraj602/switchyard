package api_test

import (
	"encoding/json"
	"testing"
	"time"
)

// idleUsage is a claude.ai reading of an idle account (record.md 4.3): the 5-hour window has
// not started (0 %, resets_at null), seven_day is null, limits[] has the session and a
// model-scoped week.
const idleUsage = `{"five_hour":{"utilization":0,"resets_at":null,"limit_dollars":null,"used_dollars":null},
	"seven_day":null,"seven_day_opus":null,"seven_day_sonnet":null,"iguana_necktie":null,
	"extra_usage":{"is_enabled":false,"monthly_limit":null,"used_credits":null},
	"limits":[{"kind":"session","group":"session","percent":0,"resets_at":null,"is_active":true,"scope":null},
	  {"kind":"weekly_scoped","group":"weekly","percent":0,"resets_at":"2030-10-27T04:00:00+00:00","is_active":false,
	   "scope":{"model":{"id":null,"display_name":"Fable"},"surface":null}}],
	"spend":{"percent":0}}`

type winJSON struct {
	Key       string   `json:"key"`
	UsedPct   *float64 `json:"used_pct"`
	ResetsAt  *string  `json:"resets_at"`
	StartedAt *string  `json:"started_at"`
	UpdatedAt *string  `json:"updated_at"`
	Pace      any      `json:"pace"`
	State     string   `json:"state"`
	Source    string   `json:"source"`
}

func (h *hub) seatWindows(owner, path string) map[string]winJSON {
	h.t.Helper()
	var body struct {
		Accounts []struct {
			Windows []winJSON `json:"windows"`
		} `json:"accounts"`
		Seats []struct {
			Windows []winJSON `json:"windows"`
		} `json:"seats"`
	}
	h.get(path, owner, &body)
	list := body.Accounts
	if path == "/v1/overview" {
		list = body.Seats
	}
	if len(list) != 1 {
		h.t.Fatalf("%s: %d seats", path, len(list))
	}
	out := map[string]winJSON{}
	for _, w := range list[0].Windows {
		out[w.Key] = w
	}
	return out
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// A window whose reset has passed reads 0 % with state "reset" and keeps its reset time; a
// newer idle reading replaces it with an idle window (0 %, no reset, updated_at of the
// reading) in /v1/accounts and /v1/overview; history keeps the real window.
func TestIdleReadingReplacesStaleWindow(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	five := t0.Add(2 * time.Hour)
	busy := json.RawMessage(`{"five_hour":{"utilization":30,"resets_at":"` + five.Format(time.RFC3339) + `"},
		"seven_day":{"utilization":10,"resets_at":"2030-10-20T09:30:00Z"}}`)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, busy, nil))

	ws := h.seatWindows(owner, "/v1/accounts")
	f := ws["five_hour"]
	if f.State != "reset" || f.UsedPct == nil || *f.UsedPct != 0 || f.ResetsAt == nil || *f.ResetsAt != iso(five) {
		t.Fatalf("passed window: %+v", f)
	}
	if s := ws["seven_day"]; s.State != "current" || s.UsedPct == nil || *s.UsedPct != 10 {
		t.Fatalf("seven_day: %+v", s)
	}

	later := t0.Add(24 * time.Hour)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", later, json.RawMessage(idleUsage), nil))
	for _, path := range []string{"/v1/accounts", "/v1/overview"} {
		ws = h.seatWindows(owner, path)
		f = ws["five_hour"]
		if f.State != "idle" || f.UsedPct == nil || *f.UsedPct != 0 || f.ResetsAt != nil || f.StartedAt != nil || f.Pace != nil ||
			f.UpdatedAt == nil || *f.UpdatedAt != iso(later) || f.Source != "web" {
			t.Fatalf("%s idle five_hour: %+v", path, f)
		}
		if s := ws["seven_day"]; s.State != "current" || *s.UsedPct != 10 {
			t.Fatalf("%s seven_day kept: %+v", path, s)
		}
		if wf := ws["weekly_fable"]; wf.State != "current" || wf.ResetsAt == nil {
			t.Fatalf("%s weekly_fable: %+v", path, wf)
		}
	}

	// History is the real windows: the 30 % five_hour, never an idle one.
	seat := h.onlySeat(owner).ID
	var hist struct {
		Windows []winJSON `json:"windows"`
	}
	h.get("/v1/accounts/"+seat+"/windows?key=five_hour", owner, &hist)
	if len(hist.Windows) != 1 || *hist.Windows[0].UsedPct != 30 || hist.Windows[0].State != "reset" {
		t.Fatalf("history: %+v", hist.Windows)
	}

	// A newer reading with a reset time ends the idle state.
	start := t0.Add(48 * time.Hour)
	again := json.RawMessage(`{"five_hour":{"utilization":5,"resets_at":"` + start.Add(5*time.Hour).Format(time.RFC3339) + `"}}`)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", start, again, nil))
	if f := h.seatWindows(owner, "/v1/accounts")["five_hour"]; f.State != "reset" || f.ResetsAt == nil {
		t.Fatalf("after a new window: %+v", f)
	}
}

// A seat whose only 5-hour readings are idle (claude-2 on the real hub) still has a five_hour
// window: idle, 0 %.
func TestIdleOnlySeatHasFiveHour(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, json.RawMessage(idleUsage), nil))
	ws := h.seatWindows(owner, "/v1/accounts")
	if f, ok := ws["five_hour"]; !ok || f.State != "idle" || *f.UsedPct != 0 || f.ResetsAt != nil {
		t.Fatalf("%+v", ws)
	}
	if _, ok := ws["seven_day"]; ok {
		t.Fatalf("seven_day invented: %+v", ws)
	}
}
