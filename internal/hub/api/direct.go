package api

import (
	"net/http"
	"strings"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/version"
)

// GET /v1/push/direct-policy (machines.md 5.3, 6.2): the hub's rules resolved for the
// machine the calling token is bound to. Scope write, checked by the server.
func init() {
	Register("GET", "/v1/push/direct-policy", func(s *Server) http.Handler { return http.HandlerFunc(s.directPolicy) })
}

// DirectPolicy is api.yaml DirectPolicy.
type DirectPolicy struct {
	V         int                `json:"v"`
	Hub       string             `json:"hub"`
	PublicURL *string            `json:"public_url"`
	Machine   string             `json:"machine"`
	Settings  DirectSettings     `json:"settings"`
	Rules     []rules.DirectRule `json:"rules"`
}

// DirectSettings is DirectPolicy.settings: the hub-wide settings direct push follows.
type DirectSettings struct {
	QuietHours model.QuietHours `json:"quiet_hours"`
	SkipRemote bool             `json:"skip_remote"`
	SkipPeers  bool             `json:"skip_peers"`
	MaxAgeS    int              `json:"max_age_s"`
}

func (s *Server) directPolicy(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	if caller.MachineID == "" {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "this token is not bound to a machine")
		return
	}
	rs, err := s.store.RuleList(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	st, err := s.ruleSettings(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	pairs, err := s.store.SeatPairs(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	seats := make(map[string]rules.DirectSeat, len(pairs))
	for id, p := range pairs {
		seats[id] = rules.DirectSeat{Account: p.Account, Org: p.Org}
	}
	qh := st.QuietHours
	if qh.TZ == "" {
		qh.TZ = s.alertHubTZ(r)
	}
	out := DirectPolicy{V: 1, Hub: version.Version, Machine: caller.MachineID,
		Settings: DirectSettings{QuietHours: qh, SkipRemote: st.SkipRemote, SkipPeers: st.SkipPeers, MaxAgeS: st.MaxAgeS},
		Rules:    rules.ResolveDirect(rs, caller.MachineID, seats)}
	if u := strings.TrimRight(hubPublicURL(r.Context(), s), "/"); u != "" {
		out.PublicURL = &u
	}
	writeJSON(w, http.StatusOK, out)
}
