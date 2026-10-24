package tui

import (
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/board"
)

// Filters (the `f` key and --filter): "all", "needs", "here", or "machine:<name>".
const (
	FilterAll   = "all"
	FilterNeeds = "needs"
	FilterHere  = "here"
	filterMach  = "machine:"
)

// model is the whole state of the board screen. It is plain data; Run owns the I/O.
type model struct {
	o      *Options
	v      board.View
	have   bool
	cursor string // selected row id ("" before the first View)
	idx    int    // index of cursor in order(), to land nearby when it disappears

	filter string
	search string
	typing bool // the `/` box is taking runes
	ended  bool
	help   bool
	note   string
	scroll int
	// bodyH is the body height of the last frame, for PgUp/PgDn.
	bodyH int
}

func newModel(o *Options) *model {
	f := o.Filter
	if f == "" {
		f = FilterAll
	}
	return &model{o: o, filter: f}
}

// section is one block of the body: the NEEDS YOU block or one account.
type section struct {
	needs bool
	group *board.Group
	ids   []string
}

func (m *model) rowVisible(r board.Row) bool {
	if !m.ended && (r.Glyph == board.GlyphEnded || r.Glyph == board.GlyphGone) {
		return false
	}
	switch {
	case m.filter == FilterNeeds:
		if !r.NeedsYou {
			return false
		}
	case m.filter == FilterHere:
		if !r.Here {
			return false
		}
	case strings.HasPrefix(m.filter, filterMach):
		name := strings.TrimPrefix(m.filter, filterMach)
		if !strings.EqualFold(r.MachineName, name) && !(r.Here && strings.EqualFold(m.v.Machine.Name, name)) {
			return false
		}
	}
	if m.search != "" {
		q := strings.ToLower(m.search)
		if !strings.Contains(strings.ToLower(r.Project), q) &&
			!strings.Contains(strings.ToLower(r.Title), q) &&
			!strings.Contains(strings.ToLower(r.Account), q) {
			return false
		}
	}
	return true
}

// sections lists what is on screen, in order: NEEDS YOU, then each account's other rows.
func (m *model) sections() []section {
	var out []section
	seen := map[string]bool{}
	var needs []string
	for _, id := range m.v.NeedsYou {
		if r, ok := m.v.Rows[id]; ok && !seen[id] && m.rowVisible(r) {
			seen[id] = true
			needs = append(needs, id)
		}
	}
	if len(needs) > 0 {
		out = append(out, section{needs: true, ids: needs})
	}
	for i := range m.v.Groups {
		g := &m.v.Groups[i]
		var ids []string
		for _, id := range g.Rows {
			r, ok := m.v.Rows[id]
			if !ok || seen[id] || !m.rowVisible(r) {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			out = append(out, section{group: g, ids: ids})
		}
	}
	return out
}

// order is every selectable row id, top to bottom.
func (m *model) order() []string {
	var ids []string
	for _, s := range m.sections() {
		ids = append(ids, s.ids...)
	}
	return ids
}

// settle keeps the cursor on a visible row: the same id if it still shows, else the row at
// the same position (clamped), else none.
func (m *model) settle() {
	ids := m.order()
	if len(ids) == 0 {
		m.cursor, m.idx = "", 0
		return
	}
	for i, id := range ids {
		if id == m.cursor {
			m.idx = i
			return
		}
	}
	if m.idx >= len(ids) {
		m.idx = len(ids) - 1
	}
	if m.idx < 0 {
		m.idx = 0
	}
	m.cursor = ids[m.idx]
}

func (m *model) move(d int) {
	ids := m.order()
	if len(ids) == 0 {
		return
	}
	m.settle()
	i := m.idx + d
	if i < 0 {
		i = 0
	}
	if i >= len(ids) {
		i = len(ids) - 1
	}
	m.idx, m.cursor = i, ids[i]
}

func (m *model) selected() (board.Row, bool) {
	r, ok := m.v.Rows[m.cursor]
	return r, ok && m.cursor != ""
}

// filterChoices is the `f` cycle: All, Needs you, This machine, then each other machine.
func (m *model) filterChoices() []string {
	c := []string{FilterAll, FilterNeeds, FilterHere}
	names := map[string]bool{}
	for _, r := range m.v.Rows {
		if !r.Here && r.MachineName != "" {
			names[r.MachineName] = true
		}
	}
	var ns []string
	for n := range names {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	for _, n := range ns {
		c = append(c, filterMach+n)
	}
	return c
}

func (m *model) cycleFilter() {
	c := m.filterChoices()
	next := c[0]
	for i, f := range c {
		if strings.EqualFold(f, m.filter) {
			next = c[(i+1)%len(c)]
			break
		}
	}
	m.filter = next
	m.settle()
}

func (m *model) filterLabel() string {
	switch {
	case m.filter == FilterAll:
		return "all"
	case m.filter == FilterNeeds:
		return "needs you"
	case m.filter == FilterHere:
		return "this machine"
	case strings.HasPrefix(m.filter, filterMach):
		return "machine " + strings.TrimPrefix(m.filter, filterMach)
	}
	return m.filter
}

// effect is what a key asks Run to do beyond redrawing.
type effect struct {
	quit    bool
	refresh bool
	focus   string // row id: run Options.Focus
	open    string // URL: run Options.OpenURL
	copy    string // text for OSC 52
}

// press applies one key and returns the side effect it asks for.
func (m *model) press(k key) effect {
	m.note = ""
	if k.kind == kCtrlC {
		return effect{quit: true}
	}
	if m.help {
		m.help = false
		return effect{}
	}
	if m.typing {
		switch k.kind {
		case kEnter:
			m.typing = false
		case kEsc:
			m.typing, m.search = false, ""
		case kBackspace:
			if rs := []rune(m.search); len(rs) > 0 {
				m.search = string(rs[:len(rs)-1])
			}
		case kRune:
			if len([]rune(m.search)) < 80 {
				m.search += string(k.r)
			}
		case kUp:
			m.move(-1)
			return effect{}
		case kDown:
			m.move(1)
			return effect{}
		}
		m.settle()
		return effect{}
	}
	switch k.kind {
	case kUp:
		m.move(-1)
	case kDown:
		m.move(1)
	case kPgUp:
		m.move(-max(1, m.bodyH/2))
	case kPgDn:
		m.move(max(1, m.bodyH/2))
	case kHome:
		m.move(-1 << 20)
	case kEnd:
		m.move(1 << 20)
	case kEsc:
		if m.search != "" {
			m.search = ""
			m.settle()
		}
	case kEnter:
		return m.enter()
	case kRune:
		return m.rune(k.r)
	}
	return effect{}
}

func (m *model) enter() effect {
	r, ok := m.selected()
	if !ok {
		return effect{}
	}
	if !r.CanFocus {
		m.note = "no tab to focus for this session (no Warp focus link recorded here)"
		return effect{}
	}
	m.note = "focusing " + shortName(r) + "…"
	return effect{focus: r.ID}
}

func shortName(r board.Row) string {
	if r.Title != "" {
		return r.Project + " · " + r.Title
	}
	return r.Project
}

func (m *model) rune(r rune) effect {
	switch r {
	case 'j':
		m.move(1)
	case 'k':
		m.move(-1)
	case 'g':
		m.move(-1 << 20)
	case 'G':
		m.move(1 << 20)
	case 'q':
		return effect{quit: true}
	case 'r':
		m.note = "refreshing…"
		return effect{refresh: true}
	case '?':
		m.help = true
	case 'e':
		m.ended = !m.ended
		m.settle()
		if m.ended {
			m.note = "showing ended sessions"
		} else {
			m.note = "hiding ended sessions"
		}
	case 'f':
		m.cycleFilter()
	case '/':
		m.typing = true
	case 'c':
		row, ok := m.selected()
		if !ok || row.Resume == "" {
			m.note = "nothing to copy"
			return effect{}
		}
		m.note = "copied: " + row.Resume
		return effect{copy: row.Resume}
	case 'o':
		row, ok := m.selected()
		if !ok {
			return effect{}
		}
		if !strings.HasPrefix(row.RemoteURL, "https://claude.ai/") {
			m.note = "no claude.ai link for this session"
			return effect{}
		}
		m.note = "opening " + row.RemoteURL
		return effect{open: row.RemoteURL}
	}
	return effect{}
}
