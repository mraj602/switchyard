package devserver

import (
	"net/http"
	"time"
)

// F1 keys mock (keys.md 3.3, 4.2; api.yaml tag keys): the hub key, one owner key proposed 30
// minutes ago by the laptop and still pending, and two devices with certificates, one signed by
// the owner key once it is active and one not. GET /v1/keys shows `pending` only to an owner and
// `devices` only to owner and write callers; confirming is for owner callers and activates the
// proposal. The documents are not signed here.

const (
	mockHubFP   = "hhhhhhhhhhhhhhhhhhhhhhhh"
	mockOwnerFP = "k7q2m4xapz3cufh62wbeq5no"
)

type keysState struct {
	active      bool
	proposedAt  time.Time
	confirmedAt time.Time
}

func (s *Server) keysRoutes() {
	s.mux.HandleFunc("GET /v1/keys", s.guard(s.getKeys, "owner", "read", "push", "write"))
	s.mux.HandleFunc("POST /v1/keys/owner/confirm", s.guard(s.confirmKey, "owner"))
}

// keysSt returns the state, made on first use. Call with s.mu held.
func (s *Server) keysSt() *keysState {
	if s.keyState == nil {
		s.keyState = &keysState{proposedAt: s.opts.Now().Add(-30 * time.Minute)}
	}
	return s.keyState
}

func (k *keysState) ownerKey() M {
	o := M{"fp": mockOwnerFP, "pub": "AAAA", "state": "pending", "proposed_by": "dev_laptop", "created_at": ts(k.proposedAt), "confirmed_at": nil}
	if k.active {
		o["state"] = "active"
		o["confirmed_at"] = ts(k.confirmedAt)
	}
	return o
}

func (s *Server) getKeys(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.keysSt()
	out := M{"hub_key": M{"alg": "ed25519", "pub": "AAAA", "fp": mockHubFP}, "owner_key": nil, "revocations_version": 0}
	if k.active {
		out["owner_key"] = k.ownerKey()
	}
	if c.has("owner") {
		pend := []M{}
		if !k.active {
			pend = append(pend, k.ownerKey())
		}
		out["pending"] = pend
	}
	if c.has("owner", "write") {
		out["devices"] = []M{
			{"id": "dev_laptop", "kind": "machine", "name": "laptop", "keys": M{"sign_fp": "aaaaaaaaaaaaaaaaaaaaaaaa", "box_fp": "bbbbbbbbbbbbbbbbbbbbbbbb", "reg": "local", "owner_signed": k.active, "certified_at": nil}},
			{"id": "dev_gpu", "kind": "machine", "name": "gpu-box", "keys": M{"sign_fp": "cccccccccccccccccccccccc", "box_fp": "dddddddddddddddddddddddd", "reg": "ssh", "owner_signed": false, "certified_at": nil}},
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) confirmKey(w http.ResponseWriter, r *http.Request, _ *caller) {
	var body struct {
		FP string `json:"fp"`
	}
	if !decode(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.keysSt()
	if k.active || body.FP != mockOwnerFP {
		writeErr(w, 404, "not_found", "no pending owner key with that fingerprint")
		return
	}
	k.active = true
	k.confirmedAt = s.opts.Now()
	writeJSON(w, 200, k.ownerKey())
}
