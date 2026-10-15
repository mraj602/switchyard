package rules

import (
	"testing"

	"github.com/mraj602/switchyard/internal/hub/push/model"
)

func TestSameScope(t *testing.T) {
	sc := func(seats, projects, machines, windows []string) model.RuleScope {
		return model.RuleScope{Seats: seats, Projects: projects, Machines: machines, Windows: windows}
	}
	l := func(v ...string) []string { return v }
	for _, tc := range []struct {
		name string
		t    model.Trigger
		a, b model.RuleScope
		want bool
	}{
		{"both empty", model.TriggerAsked, sc(nil, nil, nil, nil), sc(nil, nil, nil, nil), true},
		{"order and repeats", model.TriggerAsked, sc(l("s1", "s2"), nil, nil, nil), sc(l("s2", "s1", "s2"), nil, nil, nil), true},
		{"different project", model.TriggerAsked, sc(nil, l("api"), nil, nil), sc(nil, l("web"), nil, nil), false},
		{"subset is not same", model.TriggerAsked, sc(l("s1"), nil, nil, nil), sc(l("s1", "s2"), nil, nil, nil), false},
		{"project vs any", model.TriggerAsked, sc(nil, l("api"), nil, nil), sc(nil, nil, nil, nil), false},
		{"windows ignored off window triggers", model.TriggerAsked, sc(nil, nil, nil, l("five_hour")), sc(nil, nil, nil, nil), true},
		{"empty windows are the defaults", model.TriggerLimit80, sc(nil, nil, nil, nil), sc(nil, nil, nil, l("seven_day", "five_hour")), true},
		{"different windows", model.TriggerLimit80, sc(nil, nil, nil, l("seven_day_opus")), sc(nil, nil, nil, nil), false},
		{"project case matters", model.TriggerFailed, sc(nil, l("API"), nil, nil), sc(nil, l("api"), nil, nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameScope(tc.t, tc.a, tc.b); got != tc.want {
				t.Fatalf("SameScope = %v, want %v", got, tc.want)
			}
		})
	}
}
