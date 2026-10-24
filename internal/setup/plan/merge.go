package plan

import (
	"encoding/json"
	"fmt"

	"github.com/mraj602/watchyard/internal/setup/model"
)

// MergeTerminal folds a terminal plan (Terminal, made from the same State) into a setup
// plan, for `wy setup --terminal …`: one plan, one backup set, one `wy setup --undo`.
// Actions are put in StepOrder (the setup plan's first within a step), and an action the
// setup plan already does is not done twice: the marketplace write_tree, and a
// `claude plugin` run with the same argv, folder and plugin root (the pane's
// `marketplace add wy-local` when setup adds wy-local itself). Keep lines, warnings and
// needs are joined; the summary and plan_id are computed again.
func MergeTerminal(base, term model.Plan) model.Plan {
	out := base
	key := func(a model.Action) string {
		switch a.Op {
		case model.OpWriteTree:
			return "tree\x00" + a.Path
		case model.OpRun:
			env, _ := json.Marshal(a.Env)
			watch, _ := json.Marshal(a.Watch)
			argv, _ := json.Marshal(a.Argv)
			return "run\x00" + string(argv) + "\x00" + string(env) + "\x00" + string(watch)
		}
		return ""
	}
	seen := map[string]bool{}
	for _, a := range base.Actions {
		if k := key(a); k != "" {
			seen[k] = true
		}
	}
	bySteps := map[model.Step][]model.Action{}
	for _, a := range base.Actions {
		bySteps[a.Step] = append(bySteps[a.Step], a)
	}
	for _, a := range term.Actions {
		if k := key(a); k != "" && seen[k] {
			continue
		}
		bySteps[a.Step] = append(bySteps[a.Step], a)
	}
	out.Actions = []model.Action{}
	for _, step := range model.StepOrder {
		out.Actions = append(out.Actions, bySteps[step]...)
	}
	for i := range out.Actions {
		out.Actions[i].ID = fmt.Sprintf("a%d", i+1)
	}
	out.Keep = append(append([]model.Keep{}, base.Keep...), term.Keep...)
	out.Needs = append(append([]model.Need{}, base.Needs...), term.Needs...)
	warned := map[string]bool{}
	out.Warnings = nil
	for _, w := range append(append([]string{}, base.Warnings...), term.Warnings...) {
		if !warned[w] {
			warned[w] = true
			out.Warnings = append(out.Warnings, w)
		}
	}
	if out.Conflicts == nil {
		out.Conflicts = []model.Conflict{}
	}
	out.Summary = summarize(out.Actions)
	out.PlanID = PlanID(out.Actions, out.Conflicts)
	return out
}

// WritesMarketplace reports whether p writes wy's marketplace at dir (the plugin step of a
// setup plan): a terminal plan merged into it may then count on the marketplace being
// there and current.
func WritesMarketplace(p model.Plan, dir string) bool {
	for _, a := range p.Actions {
		if a.Op == model.OpWriteTree && a.Path == dir {
			return true
		}
	}
	return false
}
