package devserver

import (
	"net/http"
	"strings"
	"time"
)

// The machine endpoints (docs/spec/machines.md 6.2): GET /v1/machines?all=true and
// the three owner operations.

func (s *Server) machineRoutes() {
	g := s.guard
	m := s.mux.HandleFunc
	m("PATCH /v1/machines/{id}", g(s.patchMachine, "owner"))
	m("DELETE /v1/machines/{id}", g(s.deleteMachine, "owner"))
	m("POST /v1/machines/{id}/pull", g(s.pullMachine, "owner"))
}

func (s *Server) machine(id string) M {
	for _, m := range s.data.Machines {
		if m["id"] == id {
			return m
		}
	}
	return nil
}

func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Query().Get("all") == "true" {
		writeJSON(w, 200, M{"machines": s.data.Machines})
		return
	}
	writeJSON(w, 200, M{"machines": s.data.LiveMachines()})
}

func (s *Server) patchMachine(w http.ResponseWriter, r *http.Request, _ *caller) {
	var b struct {
		Name *string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machine(r.PathValue("id"))
	if m == nil {
		writeErr(w, 404, "not_found", "no such machine")
		return
	}
	if b.Name != nil {
		n := strings.TrimSpace(*b.Name)
		if len([]rune(n)) < 1 || len([]rune(n)) > 60 {
			writeErr(w, 400, "bad_request", "name must be 1 to 60 characters")
			return
		}
		m["name"] = n
		if p, ok := m["pull"].(M); ok {
			p["name"] = n
		}
	}
	s.emitLocked("machine", M{"machine": m})
	writeJSON(w, 200, m)
}

func (s *Server) deleteMachine(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machine(r.PathValue("id"))
	if m == nil {
		writeErr(w, 404, "not_found", "no such machine")
		return
	}
	if m["id"] == s.data.SelfMachine {
		writeErr(w, 409, "conflict", "this is the machine your own device is bound to")
		return
	}
	m["removed_at"] = ts(s.opts.Now())
	m["silent"], m["alert"], m["live_sessions"] = false, nil, 0
	if p, ok := m["pull"].(M); ok {
		p["state"], p["next_at"] = "disabled", nil
	}
	s.emitLocked("machine", M{"machine": m})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pullMachine(w http.ResponseWriter, r *http.Request, _ *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machine(r.PathValue("id"))
	if m == nil {
		writeErr(w, 404, "not_found", "no such machine")
		return
	}
	p, ok := m["pull"].(M)
	if !ok {
		writeErr(w, 404, "not_found", "this machine has no pull target")
		return
	}
	if st := p["state"]; st == "host_key" || st == "refused" {
		writeErr(w, 409, "conflict", "the pull target needs you first ("+st.(string)+"): fix it with sy hub pull")
		return
	}
	p["next_at"] = ts(s.opts.Now())
	s.emitLocked("machine", M{"machine": m})
	writeJSON(w, 202, p)
}

// devPullStates (dev only) adds one machine per pull state, so every state can be seen.
func (s *Server) devPullStates(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	have := map[any]bool{}
	for _, m := range s.data.Machines {
		have[m["id"]] = true
	}
	for _, m := range s.data.StateMachines {
		if !have[m["id"]] {
			s.data.Machines = append(s.data.Machines, m)
			s.emitLocked("machine", M{"machine": m})
		}
	}
	writeJSON(w, 200, M{"added": len(s.data.StateMachines), "at": ts(time.Now())})
}
