package devserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Scenarios are extra data for checking the board's edge cases by eye and in the browser
// smoke: POST /_dev/scenario {"names": ["history", "long-names", ...]} changes the current
// data in place; POST /_dev/reset brings the default fixtures back. The default fixtures stay
// as they are, so the existing tests keep their numbers.
var scenarios = map[string]func(d *Data, now time.Time){
	// Sessions imported from old Claude Code history: no project, folder, title or text,
	// done or ended, hours to days old. The board folds them into one line.
	"history": func(d *Data, now time.Time) {
		for i := 0; i < 24; i++ {
			at := now.Add(-time.Duration(13+i*3) * time.Hour)
			state := "done"
			if i%3 == 0 {
				state = "ended"
			}
			d.Sessions = append(d.Sessions, M{
				"id": fmt.Sprintf("c41e0000-0000-4000-8000-1000000000%02x", i), "state": state, "detail": nil, "needs_you": false,
				"state_since": ts(at), "question": nil, "last_message": nil,
				"seat": "seat_personal", "seat_label": "personal", "seat_colour": "#0E7C76",
				"machine": "m_laptop", "machine_name": "laptop", "slot": "~/.claude", "project": nil, "cwd": nil,
				"name": nil, "title": nil, "kind": "interactive", "model": nil, "context_pct": nil,
				"started_at": ts(at.Add(-time.Hour)), "last_event_at": ts(at), "ended_at": nil,
				"remote_url": nil, "resume": nil, "focus_url": nil, "stale": false, "machine_stale": false, "alert": nil,
			})
		}
		// And a finished session left for a day: not open any more.
		d.Sessions = append(d.Sessions, M{
			"id": "c41e0000-0000-4000-8000-1000000000ff", "state": "done", "detail": nil, "needs_you": false,
			"state_since": ts(now.Add(-26 * time.Hour)), "question": nil, "last_message": "Merged the branch.",
			"seat": "seat_work", "seat_label": "work", "seat_colour": "#6A4FD6",
			"machine": "m_gpu", "machine_name": "gpu-box", "slot": "~/.claude", "project": "api", "cwd": "/home/me/api",
			"name": nil, "title": "old-merge", "kind": "interactive", "model": "claude-opus-4-7", "context_pct": 40.0,
			"started_at": ts(now.Add(-28 * time.Hour)), "last_event_at": ts(now.Add(-26 * time.Hour)), "ended_at": nil,
			"remote_url": nil, "resume": "claude --resume c41e0000-0000-4000-8000-1000000000ff", "focus_url": nil,
			// The hub marks only a working session stale; an idle done one is dormant (R11).
			"stale": false, "machine_stale": false, "dormant": true, "alert": nil,
		})
	},
	// Long account and machine names: nothing may wrap to many lines or scroll sideways.
	"long-names": func(d *Data, now time.Time) {
		long := map[string]string{
			"seat_personal": "personal-account-with-a-rather-long-name",
			"seat_work":     "work-team-subscription-for-the-platform-group",
		}
		for _, s := range d.Seats {
			if l, ok := long[s["id"].(string)]; ok {
				s["label"] = l
			}
		}
		name := "build-server-with-a-very-long-name.internal.example.com"
		for _, m := range d.Machines {
			if m["id"] == "m_gpu" {
				m["name"] = name
			}
		}
		for _, s := range d.Sessions {
			if l, ok := long[fmt.Sprint(s["seat"])]; ok {
				s["seat_label"] = l
			}
			if s["machine"] == "m_gpu" {
				s["machine_name"] = name
			}
		}
	},
	// No device can receive a notification.
	"no-push": func(d *Data, now time.Time) {
		d.Targets = []M{}
	},
	// Two accounts whose emails start the same, with no labels: the board must tell them apart.
	"collide": func(d *Data, now time.Time) {
		emails := map[string]string{"seat_personal": "you@work.example", "seat_work": "you@example.com", "seat_client": "mraj602@gmail.com"}
		for _, s := range d.Seats {
			if e, ok := emails[s["id"].(string)]; ok {
				s["email"], s["label"] = e, nil
			}
		}
		for _, s := range d.Sessions {
			if _, ok := emails[fmt.Sprint(s["seat"])]; ok {
				s["seat_label"] = nil
			}
		}
	},
}

func scenarioNames() []string {
	out := make([]string, 0, len(scenarios))
	for k := range scenarios {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// devScenario is POST /_dev/scenario {"names": [...]}: applies each named scenario to the
// current data and tells the board to reload.
func (s *Server) devScenario(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Names) == 0 {
		writeErr(w, 400, "bad_request", fmt.Sprintf("body must be {\"names\": [...]}, one or more of %v", scenarioNames()))
		return
	}
	for _, n := range b.Names {
		if scenarios[n] == nil {
			writeErr(w, 400, "bad_request", fmt.Sprintf("unknown scenario %q; known: %v", n, scenarioNames()))
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	for _, n := range b.Names {
		scenarios[n](s.data, now)
	}
	writeJSON(w, 200, M{"applied": b.Names})
	s.emitLocked("reset", M{})
}
