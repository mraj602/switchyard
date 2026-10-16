package devserver

import "net/http"

// PUT /v1/seats/order mock (api.yaml putSeatOrder): the listed seats first, the rest after
// them as they were, every seat's `order` renumbered, and a `seats` stream event.
func (s *Server) putSeatOrder(w http.ResponseWriter, r *http.Request, _ *caller) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.IDs == nil || len(body.IDs) > 500 {
		writeErr(w, 400, "bad_request", `the body must be {"ids": [seat ids]}`)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := map[string]M{}
	for _, x := range s.data.Seats {
		byID[x["id"].(string)] = x
	}
	used := map[string]bool{}
	next := make([]M, 0, len(s.data.Seats))
	for _, id := range body.IDs {
		seat, ok := byID[id]
		if !ok {
			writeErr(w, 400, "bad_request", "invalid seat order: unknown seat "+id)
			return
		}
		if used[id] {
			writeErr(w, 400, "bad_request", "invalid seat order: duplicate seat "+id)
			return
		}
		used[id] = true
		next = append(next, seat)
	}
	for _, x := range s.data.Seats {
		if !used[x["id"].(string)] {
			next = append(next, x)
		}
	}
	order := make([]string, len(next))
	for i, x := range next {
		x["order"] = int64(i)
		order[i] = x["id"].(string)
	}
	s.data.Seats = next
	s.emitLocked("seats", M{"order": order})
	writeJSON(w, 200, M{"accounts": s.data.Seats})
}
