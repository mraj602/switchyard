package rules

import (
	"testing"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

func TestResolveDirect(t *testing.T) {
	rs := []model.Rule{
		{ID: "rule_asked", Trigger: model.TriggerAsked, Base: true, Enabled: true, DelayS: 60},
		{ID: "rule_failed", Trigger: model.TriggerFailed, Base: true, Enabled: true, BypassQuiet: true},
		{ID: "rule_limit_80", Trigger: model.TriggerLimit80, Base: true, Enabled: true},
		{ID: "o_other", Trigger: model.TriggerAsked, Enabled: false, Scope: model.RuleScope{Machines: []string{"m_other"}}},
		{ID: "o_here", Trigger: model.TriggerAsked, Enabled: true, DelayS: 10, Scope: model.RuleScope{Machines: []string{"m_here"}}, UpdatedAt: 5},
		{ID: "o_seat", Trigger: model.TriggerAsked, Enabled: true, DelayS: 300, UpdatedAt: 1,
			Scope: model.RuleScope{Seats: []string{"seat_a", "seat_gone"}, Projects: []string{"infra"}}},
		{ID: "o_unmapped", Trigger: model.TriggerFailed, Enabled: false, Scope: model.RuleScope{Seats: []string{"seat_gone"}}},
	}
	got := ResolveDirect(rs, "m_here", map[string]DirectSeat{"seat_a": {Account: "acc", Org: "org"}})
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	want := []string{"o_seat", "o_here", "rule_asked", "rule_failed"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
	if s := got[0].Seats; len(s) != 1 || s[0] != (DirectSeat{Account: "acc", Org: "org"}) || got[0].Projects[0] != "infra" {
		t.Fatalf("%+v", got[0])
	}
	if got[1].Seats == nil || got[1].Projects == nil {
		t.Fatal("empty lists must encode as [] not null")
	}
}
