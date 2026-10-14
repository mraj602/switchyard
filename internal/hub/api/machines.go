package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/events"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// Machine operations of phase 5 (machines.md 6.2). The server wraps them with the scopes of
// phase5Routes (owner), so the handlers do not check scopes themselves.
func init() {
	Register("PATCH", "/v1/machines/{id}", func(s *Server) http.Handler { return http.HandlerFunc(s.machinePatch) })
	Register("DELETE", "/v1/machines/{id}", func(s *Server) http.Handler { return http.HandlerFunc(s.machineDelete) })
	Register("POST", "/v1/machines/{id}/pull", func(s *Server) http.Handler { return http.HandlerFunc(s.machinePull) })
}

// machineName cleans a MachinePatch name: control characters dropped, trimmed, 1-60 runes.
func machineName(raw string) (string, bool) {
	n := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	return n, n != "" && len([]rune(n)) <= 60
}

func (s *Server) machinePatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name *string `json:"name"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<10+1))
	if err != nil || len(raw) > 16<<10 {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body too large")
		return
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body must be a MachinePatch object")
		return
	}
	if body.Name != nil {
		n, ok := machineName(*body.Name)
		if !ok {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "name must be 1 to 60 characters")
			return
		}
		err := s.store.Write(r.Context(), func(tx *store.Tx) error { return tx.MachineRename(id, n) })
		if errors.Is(err, store.ErrNotFound) {
			auth.WriteError(w, http.StatusNotFound, "not_found", "no such machine")
			return
		} else if err != nil {
			s.fail(w, err)
			return
		}
	}
	m, err := s.store.MachineByID(r.Context(), id, s.now())
	if errors.Is(err, store.ErrNotFound) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such machine")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	if body.Name != nil {
		s.publishMachine(m)
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) machineDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	caller, _ := auth.DeviceFrom(r.Context())
	own := caller.MachineID
	if parent, ok := strings.CutPrefix(caller.CreatedBy, "login:"); ok && own == "" {
		// A board session opened from a device: removing that device's machine would
		// revoke the session too (RevokeDeviceCascade).
		if d, err := s.store.DeviceByID(r.Context(), parent); err == nil {
			own = d.MachineID
		}
	}
	if own != "" && own == id {
		auth.WriteError(w, http.StatusConflict, "conflict", "this is the machine your own device is bound to")
		return
	}
	var rm store.MachineRemoval
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		var err error
		rm, err = tx.MachineRemove(id, s.now().UnixMilli())
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such machine")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.logf("machine removed id=%s devices=%d pull=%d alerts=%d", id, len(rm.Devices), len(rm.Pull), len(rm.Alerts))
	if m, err := s.store.MachineByID(r.Context(), id, s.now()); err == nil {
		s.publishMachine(m)
	}
	if s.cfg.Engine != nil {
		s.cfg.Engine.Notify(ingest.Changes{Machines: map[string]bool{id: true}})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) machinePull(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Puller == nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "this hub does not run pulls")
		return
	}
	st, err := s.cfg.Puller.RunNow(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, ErrNoPullTarget), errors.Is(err, store.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "this machine has no pull target")
	case errors.Is(err, ErrPullBlocked):
		auth.WriteError(w, http.StatusConflict, "conflict", "the pull target needs fixing first (host key or refused)")
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusAccepted, st)
	}
}

// publishMachine sends a stream `machine` event (api.yaml StreamMachine).
func (s *Server) publishMachine(m store.MachineView) {
	if s.Broker == nil {
		return
	}
	data, _ := json.Marshal(map[string]any{"machine": m})
	s.Broker.Publish(events.Event{Type: "machine", Data: data})
}

// logf writes one line to the request log, when there is one.
func (s *Server) logf(format string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log.Printf(format, args...)
	}
}
