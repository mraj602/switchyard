package rules

import (
	"slices"
	"sort"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

// DirectSeat is a seat as a machine knows it: the login's account and org uuids, not the
// hub seat id (machines.md 5.3).
type DirectSeat struct {
	Account string `json:"account"`
	Org     string `json:"org"`
}

// DirectRule is one rule of GET /v1/push/direct-policy (api.yaml DirectPolicyRule). Empty
// Seats or Projects match anything. ID is kept for tests and logs only.
type DirectRule struct {
	ID          string        `json:"-"`
	Trigger     model.Trigger `json:"trigger"`
	Enabled     bool          `json:"enabled"`
	DelayS      int           `json:"delay_s"`
	BypassQuiet bool          `json:"bypass_quiet"`
	Seats       []DirectSeat  `json:"seats"`
	Projects    []string      `json:"projects"`
}

// DirectTriggers are the triggers a machine can compute on its own (machines.md 5.1).
var DirectTriggers = []model.Trigger{model.TriggerNeedsPermission, model.TriggerAsked, model.TriggerFailed}

// ResolveDirect resolves the hub's rules for one machine (machines.md 5.3): only the session
// triggers, rules whose machines list excludes the machine dropped, the machines list
// resolved away, seat ids mapped through seats (a rule none of whose seats maps is dropped:
// no session the machine sees can match it), ordered by push.md 3.4's tie-breaks. The client
// takes the first rule whose trigger matches and whose seats and projects lists are empty or
// contain the session's, which is the rule Match picks on the hub.
func ResolveDirect(rules []model.Rule, machine string, seats map[string]DirectSeat) []DirectRule {
	var kept []model.Rule
	for _, r := range rules {
		if !slices.Contains(DirectTriggers, r.Trigger) || !listMatches(r.Scope.Machines, machine) {
			continue
		}
		kept = append(kept, r)
	}
	sort.SliceStable(kept, func(i, j int) bool { return better(kept[i], kept[j]) })
	out := make([]DirectRule, 0, len(kept))
	for _, r := range kept {
		d := DirectRule{ID: r.ID, Trigger: r.Trigger, Enabled: r.Enabled, DelayS: r.DelayS, BypassQuiet: r.BypassQuiet,
			Seats: []DirectSeat{}, Projects: append([]string{}, r.Scope.Projects...)}
		for _, id := range r.Scope.Seats {
			if p, ok := seats[id]; ok && !slices.Contains(d.Seats, p) {
				d.Seats = append(d.Seats, p)
			}
		}
		if len(r.Scope.Seats) > 0 && len(d.Seats) == 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}
