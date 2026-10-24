package rules

import (
	"slices"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// SameScope reports whether two scopes of trigger t match exactly the same subjects (push.md
// 3.4): the same seats, projects and machines as sets, and for window triggers the same
// windows, an empty list counting as DefaultWindows. Windows of other triggers are ignored.
// Two rules of one trigger with the same scope are duplicates: only one of them could ever
// be picked.
func SameScope(t model.Trigger, a, b model.RuleScope) bool {
	if !sameSet(a.Seats, b.Seats) || !sameSet(a.Projects, b.Projects) || !sameSet(a.Machines, b.Machines) {
		return false
	}
	if !IsWindowTrigger(t) {
		return true
	}
	wa, wb := a.Windows, b.Windows
	if len(wa) == 0 {
		wa = DefaultWindows
	}
	if len(wb) == 0 {
		wb = DefaultWindows
	}
	return sameSet(wa, wb)
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}
