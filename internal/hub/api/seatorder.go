package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// PUT /v1/seats/order (api.yaml putSeatOrder): the owner's drag-and-drop order of the Accounts
// page. The listed seats come first in that order, the rest follow as they were. It publishes
// a `seats` stream event with every seat id in the new order. The owner scope is enforced by
// the route table.
func init() {
	Register("PUT", "/v1/seats/order", func(s *Server) http.Handler { return http.HandlerFunc(s.putSeatOrder) })
}

func (s *Server) putSeatOrder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		IDs []string `json:"ids"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.IDs == nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", `the body must be {"ids": [seat ids]}`)
		return
	}
	var order []string
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		var e error
		order, e = tx.ReorderSeats(body.IDs)
		return e
	})
	switch {
	case errors.Is(err, store.ErrBadSeatOrder):
		auth.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	data, _ := json.Marshal(map[string]any{"order": order})
	s.Broker.Publish(events.Event{Type: "seats", Data: data})
	all, err := s.seats(r, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": all})
}
