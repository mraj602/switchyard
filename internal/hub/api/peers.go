package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	"github.com/mraj602/switchyard/internal/keys"
)

// PE1 peers endpoints (peers.md 5.4, 10, 11; api.yaml tag peers): the matrix, the kill
// switch and the audit log. The audit holds metadata only; no message text ever reaches it.
// Like the key endpoints they answer 503 when the hub has no signing key.

func init() {
	reg := func(method, path string, h func(*Server, http.ResponseWriter, *http.Request)) {
		Register(method, path, func(s *Server) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if s.cfg.Keys == nil {
					auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "this hub has no signing key; restart `sy hub serve`")
					return
				}
				h(s, w, r)
			})
		})
	}
	reg("GET", "/v1/peers/matrix", (*Server).matrixGet)
	reg("PUT", "/v1/peers/matrix", (*Server).matrixPut)
	reg("POST", "/v1/peers/pause", (*Server).peersPause)
	reg("GET", "/v1/peers/audit", (*Server).peersAudit)
}

// matrixInfo is api.yaml MatrixInfo.
func matrixInfo(rec *hubkeys.MatrixRecord) map[string]any {
	return map[string]any{
		"doc": rec.Signed.Doc, "matrix": rec.Signed.Body, "signer": rec.Signed.Signer,
		"widens": rec.Widens, "made_by": rec.MadeBy, "created_at": msTime(rec.CreatedAt),
	}
}

func (s *Server) matrixGet(w http.ResponseWriter, r *http.Request) {
	rec, err := s.cfg.Keys.MatrixCurrent(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, matrixInfo(rec))
}

// matrixPut takes `doc` (an owner-signed document, any caller with the route's scopes) or
// `edit` (the hub signs; widening needs the owner scope).
func (s *Server) matrixPut(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	var body struct {
		Doc  *keys.Doc           `json:"doc"`
		Edit *hubkeys.MatrixEdit `json:"edit"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	if (body.Doc == nil) == (body.Edit == nil) {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "send exactly one of doc and edit")
		return
	}
	var rec *hubkeys.MatrixRecord
	var err error
	if body.Doc != nil {
		rec, err = s.cfg.Keys.PutOwnerMatrix(r.Context(), *body.Doc, caller.ID)
	} else {
		rec, err = s.cfg.Keys.EditMatrix(r.Context(), *body.Edit, caller.ID, caller.HasScope("owner"))
	}
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, matrixInfo(rec))
}

// peersPause is the kill switch (peers.md 11): a matrix edit of `paused`. Resuming widens, so
// it needs the owner scope (an owner session); the owner key signs a resume at a terminal
// with PUT /v1/peers/matrix instead.
func (s *Server) peersPause(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	var body struct {
		Paused *bool `json:"paused"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	if body.Paused == nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "paused is required")
		return
	}
	cur, err := s.cfg.Keys.MatrixCurrent(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	if cur.Signed.Body.Paused == *body.Paused { // already so: nothing to sign
		writeJSON(w, http.StatusOK, matrixInfo(cur))
		return
	}
	rec, err := s.cfg.Keys.EditMatrix(r.Context(), hubkeys.MatrixEdit{Paused: body.Paused}, caller.ID, caller.HasScope("owner"))
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, matrixInfo(rec))
}

func (s *Server) peersAudit(w http.ResponseWriter, r *http.Request) {
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "since is an RFC 3339 time")
			return
		}
		since = t.UnixMilli()
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "limit is 1 to 500")
			return
		}
		limit = n
	}
	rows, err := s.Store().PeerAudit(r.Context(), since, limit)
	if err != nil {
		keysFail(w, err)
		return
	}
	entries := []map[string]any{}
	for _, row := range rows {
		e := map[string]any{"id": row.ID, "at": msTime(row.At), "actor": row.Actor, "event": row.Event, "thread": nil, "msg": nil,
			"detail": json.RawMessage(row.Detail)}
		if row.Thread != "" {
			e["thread"] = row.Thread
		}
		if row.Msg != "" {
			e["msg"] = row.Msg
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
