package board

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/mraj602/switchyard/internal/hub/state"
	"github.com/mraj602/switchyard/internal/warp"
)

// sortRows orders rows the way `sy status` does (status.rank): needs-you first (the one
// waiting longest first), then working, done, transient failures, ended, gone; inside a
// class the newest first; ties by id. The hub source keeps the hub's own order instead.
func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		if !a.Since.Equal(b.Since) {
			if a.NeedsYou {
				return a.Since.Before(b.Since)
			}
			return a.Since.After(b.Since)
		}
		return a.ID < b.ID
	})
}

func rank(r Row) int {
	switch {
	case r.NeedsYou:
		return 0
	case r.State == string(state.Working):
		return 1
	case r.State == string(state.Done):
		return 2
	case r.State == string(state.Failed):
		return 3
	case r.State == string(state.Ended):
		return 4
	}
	return 5
}

// assemble makes the View: the rows by id, each row listed in its seat's group (rows with
// no known seat go to a last "Unattributed" group), needs_you, counts, and the caps of T2.
// rows must already be in display order; needs is the preferred order of needs-you ids
// (nil: oldest waiting first); groups come in display order with empty Rows.
func (c ctx) assemble(source string, rows []Row, groups []Group, needs []string, machines int) View {
	rows = dedupe(rows)
	var needRows []Row
	for _, r := range rows {
		if r.NeedsYou {
			needRows = append(needRows, r)
		}
	}
	total, totalNeeds := len(rows), len(needRows)

	keep := keepIDs(rows, MaxRows)
	var v View
	for {
		v = c.view(source, filterRows(rows, keep), groups, needs, machines)
		v.Counts.Sessions, v.Counts.NeedsYou = total, totalNeeds
		v.Truncated = total - len(v.Rows)
		b, err := json.Marshal(v)
		if err != nil || len(b) <= MaxBytes {
			return v
		}
		// Too big: drop the oldest tenth of what is kept (never a needs-you row).
		n := keepCount(keep, rows) / 10
		if n < 1 {
			n = 1
		}
		next := keepIDs(rows, len(keep)-n)
		if len(next) >= len(keep) {
			return v // only needs-you rows are left
		}
		keep = next
	}
}

func dedupe(rows []Row) []Row {
	seen := make(map[string]bool, len(rows))
	out := rows[:0:0]
	for _, r := range rows {
		if r.ID == "" || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	return out
}

func keepCount(keep map[string]bool, rows []Row) int {
	n := 0
	for _, r := range rows {
		if keep[r.ID] && !r.NeedsYou {
			n++
		}
	}
	return n
}

// keepIDs chooses at most max rows: every needs-you row (oldest waiting first when there
// are more than max), then the newest others.
func keepIDs(rows []Row, max int) map[string]bool {
	keep := make(map[string]bool, len(rows))
	if max < 0 {
		max = 0
	}
	if len(rows) <= max {
		for _, r := range rows {
			keep[r.ID] = true
		}
		return keep
	}
	var needs, others []Row
	for _, r := range rows {
		if r.NeedsYou {
			needs = append(needs, r)
		} else {
			others = append(others, r)
		}
	}
	sort.SliceStable(needs, func(i, j int) bool {
		if !needs[i].Since.Equal(needs[j].Since) {
			return needs[i].Since.Before(needs[j].Since)
		}
		return needs[i].ID < needs[j].ID
	})
	if len(needs) > max {
		needs = needs[:max]
	}
	for _, r := range needs {
		keep[r.ID] = true
	}
	sort.SliceStable(others, func(i, j int) bool {
		if !others[i].Since.Equal(others[j].Since) {
			return others[i].Since.After(others[j].Since)
		}
		return others[i].ID < others[j].ID
	})
	for _, r := range others {
		if len(keep) >= max {
			break
		}
		keep[r.ID] = true
	}
	return keep
}

func filterRows(rows []Row, keep map[string]bool) []Row {
	out := make([]Row, 0, len(keep))
	for _, r := range rows {
		if keep[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// view lays the kept rows out in groups.
func (c ctx) view(source string, rows []Row, groups []Group, needs []string, machines int) View {
	v := View{
		V: Version, GeneratedAt: c.now.UTC(), Source: source, Hub: c.opts.Hub,
		Machine:  Machine{ID: Line(c.opts.Machine.ID), Name: field(c.opts.Machine.Name, maxMachine)},
		Counts:   Counts{Sessions: len(rows), Machines: machines},
		NeedsYou: []string{}, Groups: make([]Group, 0, len(groups)+1), Rows: make(map[string]Row, len(rows)),
	}
	if v.Hub.State == "" {
		v.Hub.State = HubNone
		if source != SourceLocal {
			v.Hub.State = HubLive
		}
	}
	v.Hub.URL = Line(v.Hub.URL)
	idx := map[string]int{}
	for _, g := range groups {
		g.Rows = []string{}
		idx[g.Seat] = len(v.Groups)
		v.Groups = append(v.Groups, g)
	}
	un := -1
	for _, r := range rows {
		v.Rows[r.ID] = r
		i, ok := idx[r.Seat]
		if !ok || r.Seat == "" {
			if un < 0 {
				un = len(v.Groups)
				v.Groups = append(v.Groups, Group{Label: UnattributedLabel, Colour: neutralColour,
					WarpColour: c.warp(neutralColour), Rows: []string{}})
			}
			i = un
		}
		v.Groups[i].Rows = append(v.Groups[i].Rows, r.ID)
	}
	listed := map[string]bool{}
	for _, id := range needs {
		if r, ok := v.Rows[id]; ok && r.NeedsYou && !listed[id] {
			v.NeedsYou = append(v.NeedsYou, id)
			listed[id] = true
		}
	}
	var rest []Row
	for _, r := range rows {
		if r.NeedsYou && !listed[r.ID] {
			rest = append(rest, r)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if !rest[i].Since.Equal(rest[j].Since) {
			return rest[i].Since.Before(rest[j].Since)
		}
		return rest[i].ID < rest[j].ID
	})
	for _, r := range rest {
		v.NeedsYou = append(v.NeedsYou, r.ID)
	}
	v.Counts.NeedsYou = len(v.NeedsYou)
	return v
}

// MergeOffline is the view while the hub is down (T3 "Offline"): last is the last hub View
// (other machines' rows keep their hub values, greyed), local is the live local View (this
// machine's rows replace the hub's). The result has source "mixed", hub state offline since
// `since`, the hub's groups (with the limit windows as last known), and rows in display
// order. Neither argument is modified.
func MergeOffline(last, local View, since time.Time, o Options) View {
	c := newCtx(o)
	me := o.Machine.ID
	// A local seat with the same label as a hub seat is that seat: its rows join the hub's
	// group. Other local seats keep their own group.
	byLabel := map[string]string{}
	for _, g := range last.Groups {
		if g.Seat != "" {
			byLabel[g.Label] = g.Seat
		}
	}
	remap := map[string]string{}
	groups := append([]Group(nil), last.Groups...)
	have := map[string]bool{}
	for _, g := range groups {
		have[g.Seat] = true
	}
	for _, g := range local.Groups {
		if g.Seat == "" {
			continue
		}
		if hubSeat, ok := byLabel[g.Label]; ok {
			remap[g.Seat] = hubSeat
		} else if !have[g.Seat] {
			groups = append(groups, g)
		}
	}
	var rows []Row
	mine := map[string]bool{}
	for _, r := range local.Rows {
		mine[r.ID] = true
		if s, ok := remap[r.Seat]; ok {
			r.Seat = s
		}
		rows = append(rows, r)
	}
	for _, r := range last.Rows {
		if mine[r.ID] || (me != "" && r.Machine == me) {
			continue
		}
		r.Greyed = true
		rows = append(rows, r)
	}
	sortRows(rows)
	var keepGroups []Group
	for _, g := range groups {
		if g.Seat == "" && g.Label == UnattributedLabel {
			continue // rebuilt from the rows
		}
		keepGroups = append(keepGroups, g)
	}
	machines := last.Counts.Machines
	if machines < 1 {
		machines = 1
	}
	t := since.UTC()
	c.opts.Hub = HubInfo{State: HubOffline, URL: last.Hub.URL, OfflineSince: &t}
	return c.assemble(SourceMixed, rows, keepGroups, nil, machines)
}

// ---------------------------------------------------------------------------------------
// Warp colour names

// WarpName is the nearest of Warp's six chromatic tab colours to hex ("#rrggbb"), by OKLab
// distance (T8). It is internal/warp's table, the one the tab configs use, so the board and
// the tab colour cannot differ. Anything that is not a colour gives "blue".
func WarpName(hex string) string {
	if n := warp.Nearest(hex); n != "" {
		return n
	}
	return "blue"
}
