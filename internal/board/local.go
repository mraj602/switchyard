package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/hub/state"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/record"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/status"
	"github.com/mraj602/switchyard/internal/termenv"
)

// The local source (T3 "Local"): this machine's outbox folded with the same code as
// `sy status` (status.Load, status.Adapt, state.Fold), so the two agree on ids, states,
// order and questions. It reads only; it never writes to the outbox.

// Defaults of LocalSource's timing (T3).
const (
	DefaultPoll      = time.Second
	DefaultDebounce  = 300 * time.Millisecond
	DefaultHeartbeat = 30 * time.Second // ages move, so the view is refolded at least this often
)

// LocalOptions configures BuildLocal and LocalSource.
type LocalOptions struct {
	Options
	// Env gives the home folder (for "~" in a session's cwd). Nothing else is read from it.
	Env termenv.Env
	// OutboxDir is <state>/outbox.
	OutboxDir string
	// Labels and Colours are config slots.labels and slots.colours, keyed by slot
	// ("~/.claude-2").
	Labels, Colours map[string]string

	// PollEvery, Debounce and Heartbeat default to the constants above.
	PollEvery, Debounce, Heartbeat time.Duration
}

var launcherSlot = regexp.MustCompile(`^~/\.claude-(\d{1,3})$`)

// slotMeta is what the newest limit_sample of a slot said.
type slotMeta struct {
	at                    time.Time
	fivePct, sevenPct     *float64
	fiveReset, sevenReset *time.Time
}

// sessMeta is what the records said about a session beyond its state.
type sessMeta struct {
	machine    string
	machineAt  time.Time
	termHost   string
	focusURL   string
	termAt     time.Time
	remoteURL  string
	remoteAt   time.Time
	title      string
	titleAt    time.Time
	lastSlot   string
	lastSlotAt time.Time
}

// BuildLocal folds the outbox into a View with source "local" at lo.Now.
func BuildLocal(lo LocalOptions) (View, error) {
	c := newCtx(lo.Options)
	recs, err := loadRecords(lo.OutboxDir, c.now.Add(-status.Window))
	if err != nil {
		return View{}, err
	}
	sessions := status.Adapt(recs)
	slots, metas := scanMeta(recs)

	type built struct {
		row  Row
		slot string
		seat *record.Seat
	}
	var items []built
	for _, s := range sessions {
		if len(s.Inputs) == 0 {
			continue
		}
		f := state.Fold(s.Inputs)
		if f.State == "" {
			continue
		}
		if (f.State == state.Ended || f.State == state.Gone) && !c.keepEnded(f.Since) {
			continue
		}
		m := metas[s.ID]
		if m == nil {
			m = &sessMeta{}
		}
		cwd := s.Cwd
		if lo.Env.Home != "" && cwd != "" {
			cwd = identity.Tilde(lo.Env.Home, cwd)
		}
		key, label, colourHex := seatKey(s, lo)
		in := rowIn{
			ID: s.ID, State: f.State, Detail: f.Detail, NeedsYou: f.NeedsYou(), Since: f.Since,
			Tool: toolOf(s.Inputs, f.State), Question: f.Question,
			Project: project(cwd), Title: m.title, Seat: key, Account: label, Colour: colourHex,
			Machine: m.machine, MachineName: c.machineName(m.machine), TermHost: m.termHost,
			ContextPct: s.ContextPct, FocusURL: m.focusURL, Resume: resumeFor(s.Slot, s.ID), RemoteURL: m.remoteURL,
		}
		if f.LastMessage != "" {
			lm := f.LastMessage
			in.LastMessage = &lm
		}
		if f.State == state.Working && f.LastListedAt.IsZero() && !f.LastEventAt.IsZero() &&
			c.now.Sub(f.LastEventAt) > 12*time.Hour {
			in.Stale = true
		}
		if sm := slots[s.Slot]; sm != nil && sm.fiveReset != nil {
			in.LimitReset = sm.fiveReset
		}
		items = append(items, built{c.rowFrom(in), s.Slot, s.Seat})
	}
	rows := make([]Row, len(items))
	for i, it := range items {
		rows[i] = it.row
	}
	sortRows(rows)

	// Groups: one per seat key, ordered by slot; labelled slots show even with no rows.
	type gslot struct{ key, slot string }
	seen := map[string]bool{}
	var gs []gslot
	for _, it := range items {
		if it.row.Seat != "" && !seen[it.row.Seat] {
			seen[it.row.Seat] = true
			gs = append(gs, gslot{it.row.Seat, it.slot})
		}
	}
	withRows := map[string]bool{}
	for _, it := range items {
		withRows[it.slot] = true
	}
	for slot := range lo.Labels {
		k := "slot:" + slot
		if !seen[k] && !withRows[slot] {
			seen[k] = true
			gs = append(gs, gslot{k, slot})
		}
	}
	sort.SliceStable(gs, func(i, j int) bool { return slotLess(gs[i].slot, gs[j].slot) })
	var groups []Group
	for i, g := range gs {
		label := lo.Labels[g.slot]
		if label == "" {
			label = firstLabel(rows, g.key)
		}
		colour := lo.Colours[g.slot]
		if !hexColour.MatchString(colour) {
			colour = model.Palette[i%len(model.Palette)].Hex
		}
		hs := HubSeat{ID: g.key, Label: &label, Colour: &colour}
		if sm := slots[g.slot]; sm != nil {
			hs.Windows = sm.windows(c.now)
		}
		groups = append(groups, c.groupFrom(hs, i))
	}
	// Row colours follow their group's colour.
	for i := range rows {
		for _, g := range groups {
			if g.Seat == rows[i].Seat {
				rows[i].Colour = g.Colour
			}
		}
	}
	return c.assemble(SourceLocal, rows, groups, nil, 1), nil
}

func firstLabel(rows []Row, key string) string {
	for _, r := range rows {
		if r.Seat == key && r.Account != "" {
			return r.Account
		}
	}
	return "account"
}

func slotLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func (m *slotMeta) windows(now time.Time) []HubWindow {
	var ws []HubWindow
	add := func(key string, pct *float64, reset *time.Time) {
		if pct == nil {
			return
		}
		if reset != nil && !reset.After(now) && *pct < 100 {
			return // that window has rolled over since the sample
		}
		ws = append(ws, HubWindow{Key: key, UsedPct: pct, ResetsAt: reset})
	}
	add("five_hour", m.fivePct, m.fiveReset)
	add("seven_day", m.sevenPct, m.sevenReset)
	return ws
}

func (c ctx) machineName(id string) string {
	if id != "" && id == c.opts.Machine.ID {
		return c.opts.Machine.Name
	}
	return ""
}

// seatKey names a session's seat: the account uuid when a record named one, else the slot.
// It also gives the label and colour the slot has in config; the colour is "" when unset.
func seatKey(s *status.Session, lo LocalOptions) (key, label, colour string) {
	label, colour = lo.Labels[s.Slot], lo.Colours[s.Slot]
	switch {
	case s.Seat != nil && s.Seat.Account != "":
		key = s.Seat.Account
		if label == "" {
			label = s.Seat.Email
		}
		if label == "" {
			label = s.Seat.Account[:min(8, len(s.Seat.Account))]
		}
	case s.Slot != "":
		key = "slot:" + s.Slot
		if label == "" {
			label = strings.TrimPrefix(path.Base(s.Slot), ".")
		}
	}
	return key, label, colour
}

func project(cwd string) string {
	if cwd == "" {
		return "-"
	}
	if cwd == "~" || cwd == "/" {
		return cwd
	}
	return path.Base(strings.ReplaceAll(cwd, `\`, "/"))
}

// toolOf is the tool a permission prompt or ask names, from the newest matching hook.
func toolOf(inputs []state.Input, st state.State) string {
	for i := len(inputs) - 1; i >= 0; i-- {
		h := inputs[i].Hook
		if h == nil {
			continue
		}
		switch {
		case st == state.NeedsPermission && h.Name == "PermissionRequest":
			return h.ToolName
		case st == state.Asked && h.Name == "PreToolUse" && h.ToolName != "":
			return h.ToolName
		}
	}
	return ""
}

// resumeFor is the command that resumes a session of slot: "claude --resume <id>" for the
// default folder, "claude-2 --resume <id>" for ~/.claude-2 (the launcher setup.md 8.3
// writes), else the folder spelled out. Session ids are checked by the hub at ingest
// (record.md 5), but a local record is not, so an id that is not a plain word gives "".
func resumeFor(slot, id string) string {
	if !safeID.MatchString(id) {
		return ""
	}
	switch {
	case slot == "~/.claude":
		return "claude --resume " + id
	case launcherSlot.MatchString(slot):
		return "claude-" + launcherSlot.FindStringSubmatch(slot)[1] + " --resume " + id
	case safeSlot.MatchString(slot):
		return "CLAUDE_CONFIG_DIR=" + slot + " claude --resume " + id
	}
	return ""
}

var (
	safeID   = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,63}$`)
	safeSlot = regexp.MustCompile(`^(~/)?[A-Za-z0-9._/@%+=:,-]+$`)
)

// scanMeta reads what Adapt does not keep: the newest limit sample of each slot, and per
// session the machine, terminal, remote link and name of the newest records that carry them.
func scanMeta(recs []*record.Record) (map[string]*slotMeta, map[string]*sessMeta) {
	slots := map[string]*slotMeta{}
	metas := map[string]*sessMeta{}
	get := func(id string) *sessMeta {
		m := metas[id]
		if m == nil {
			m = &sessMeta{}
			metas[id] = m
		}
		return m
	}
	for _, r := range recs {
		ts, err := r.Time()
		if err != nil {
			continue
		}
		switch r.Kind {
		case record.KindHookEvent:
			var p struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(r.Payload, &p) != nil || p.SessionID == "" {
				continue
			}
			m := get(p.SessionID)
			if r.Machine != "" && ts.After(m.machineAt) {
				m.machine, m.machineAt = r.Machine, ts
			}
			if r.Term != nil && ts.After(m.termAt) && (r.Term.FocusURL != "" || r.Term.Host != "") {
				m.termHost, m.focusURL, m.termAt = r.Term.Host, r.Term.FocusURL, ts
			}
			if raw, ok := r.Derived["remote_url"]; ok && ts.After(m.remoteAt) {
				var u string
				if json.Unmarshal(raw, &u) == nil && u != "" {
					m.remoteURL, m.remoteAt = u, ts
				}
			}
		case record.KindLimitSample:
			var p struct {
				SessionID   string `json:"session_id"`
				SessionName string `json:"session_name"`
			}
			if json.Unmarshal(r.Payload, &p) != nil {
				continue
			}
			if p.SessionID != "" && p.SessionName != "" {
				if m := get(p.SessionID); ts.After(m.titleAt) {
					m.title, m.titleAt = p.SessionName, ts
				}
			}
			if r.Slot == "" {
				continue
			}
			sm := slots[r.Slot]
			if sm == nil {
				sm = &slotMeta{}
				slots[r.Slot] = sm
			}
			if ts.After(sm.at) {
				sm.at = ts
				sm.fivePct, sm.sevenPct = derivedPct(r, "five_hour_pct"), derivedPct(r, "seven_day_pct")
				sm.fiveReset, sm.sevenReset = derivedTime(r, "five_hour_resets_at"), derivedTime(r, "seven_day_resets_at")
			}
		}
	}
	return slots, metas
}

func derivedPct(r *record.Record, key string) *float64 {
	raw, ok := r.Derived[key]
	if !ok {
		return nil
	}
	var f *float64
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f
}

func derivedTime(r *record.Record, key string) *time.Time {
	raw, ok := r.Derived[key]
	if !ok {
		return nil
	}
	var s *string
	if json.Unmarshal(raw, &s) != nil || s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return nil
	}
	return &t
}

// ---------------------------------------------------------------------------------------

// LocalSource is the board.Source of the local fold.
type LocalSource struct {
	lo      LocalOptions
	refresh chan struct{}
	once    sync.Once
}

// NewLocalSource returns a source over lo.OutboxDir.
func NewLocalSource(lo LocalOptions) *LocalSource {
	return &LocalSource{lo: lo, refresh: make(chan struct{}, 1)}
}

var _ Source = (*LocalSource)(nil)

// Refresh refolds now. It never blocks.
func (s *LocalSource) Refresh() {
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

// Start folds once (returning its error), then sends the first View and a new one whenever
// the outbox changes (polled, debounced) or the heartbeat passes with the view changed.
// The channel holds at most one View: a slow reader gets the newest, never a queue. It is
// closed when ctx ends. Start may be called once.
func (s *LocalSource) Start(ctx context.Context) (<-chan View, error) {
	var started bool
	s.once.Do(func() { started = true })
	if !started {
		return nil, errors.New("board: local source already started")
	}
	lo := s.lo
	if lo.PollEvery <= 0 {
		lo.PollEvery = DefaultPoll
	}
	if lo.Debounce <= 0 {
		lo.Debounce = DefaultDebounce
	}
	if lo.Heartbeat <= 0 {
		lo.Heartbeat = DefaultHeartbeat
	}
	fold := func() (View, error) {
		o := lo
		if o.Options.Now.IsZero() {
			o.Options.Now = time.Now()
		}
		return BuildLocal(o)
	}
	fp := fingerprint(lo.OutboxDir) // before the fold, so a change during it is seen
	first, err := fold()
	if err != nil {
		return nil, err
	}
	out := make(chan View, 1)
	out <- first
	go func() {
		defer close(out)
		lastKey := viewKey(first)

		lastFold := time.Now()
		tick := time.NewTicker(lo.PollEvery)
		defer tick.Stop()
		var debounce <-chan time.Time
		emit := func() {
			v, err := fold()
			lastFold = time.Now()
			if err != nil {
				return
			}
			if k := viewKey(v); k != lastKey {
				lastKey = k
				// latest wins: drop an unread older View
				select {
				case <-out:
				default:
				}
				out <- v
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.refresh:
				fp = fingerprint(lo.OutboxDir)
				debounce = nil
				emit()
			case <-tick.C:
				if n := fingerprint(lo.OutboxDir); n != fp {
					fp = n
					debounce = time.After(lo.Debounce)
				} else if debounce == nil && time.Since(lastFold) >= lo.Heartbeat {
					emit()
				}
			case <-debounce:
				debounce = nil
				emit()
			}
		}
	}()
	return out, nil
}

// viewKey identifies a View's content, ignoring when it was generated.
func viewKey(v View) string {
	v.GeneratedAt = time.Time{}
	b, _ := json.Marshal(v)
	return string(b)
}

// fingerprint changes when an outbox file is added, removed or grows.
func fingerprint(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "none"
		}
		return "err"
	}
	var b strings.Builder
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", filepath.Base(e.Name()), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}
