package board

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/status"
)

// The words of the board (docs/spec/terminal.md T2 "States and words"): state words, limit
// text with the reset in local time, ages, glyphs and the context level. Every renderer
// shows these strings as they are.

// Age renders d as "40s", "2m", "3h" or "2d" (the same rule as `wy status`).
func Age(d time.Duration) string { return status.Age(d) }

// ClockText is a reset time in the viewer's local time: "17:00" when it is today (or less
// than 18 hours away), else "Tue 17:00".
func ClockText(t time.Time, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	lt, ln := t.In(loc), now.In(loc)
	if sameDay(lt, ln) || (t.After(now) && t.Sub(now) < 18*time.Hour) {
		return lt.Format("15:04")
	}
	return lt.Format("Mon 15:04")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// WindowText is a limit window as shown: "41% · 15:10", or "41%" with no known reset.
// withReset is false for the seven-day window, which shows only its percentage.
func WindowText(pct int, resets *time.Time, now time.Time, loc *time.Location, withReset bool) string {
	s := strconv.Itoa(pct) + "%"
	if withReset && resets != nil {
		s += " · " + ClockText(*resets, now, loc)
	}
	return s
}

// pctInt rounds a percentage to an int in [0, 100].
func pctInt(f float64) int {
	if math.IsNaN(f) {
		return 0
	}
	n := int(math.Round(f))
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

// ContextLevel is "ok" below 80 %, "warn" from 80, "high" from 92.
func ContextLevel(pct int) string {
	switch {
	case pct >= ContextHighPct:
		return ContextHigh
	case pct >= ContextWarnPct:
		return ContextWarn
	}
	return ContextOK
}

// GlyphFor maps a state to the glyph the renderers draw. Needs-you states (asked,
// needs_permission) are "needs"; failed keeps its own glyph whether or not it needs you.
func GlyphFor(st state.State) string {
	switch st {
	case state.Working:
		return GlyphWorking
	case state.Asked, state.NeedsPermission:
		return GlyphNeeds
	case state.Failed:
		return GlyphFailed
	case state.Ended:
		return GlyphEnded
	case state.Gone:
		return GlyphGone
	}
	return GlyphDone
}

// wordIn is what StatusWord needs to know about a session.
type wordIn struct {
	State  state.State
	Detail string
	Since  time.Time
	// Tool is the tool of the last PermissionRequest (needs_permission) or of the
	// AskUserQuestion / ExitPlanMode that asked, when known; "" otherwise.
	Tool string
	// LimitReset is the reset of the seat's 5-hour window, for failed:rate_limit.
	LimitReset *time.Time
}

// StatusWord is Row.Status, the short word shown for a state (T2): "asked",
// "asked · Bash", "perm", "perm · Bash", "limit · 17:00", "done", "new", "2m".
func statusWord(w wordIn, now time.Time, loc *time.Location) string {
	switch w.State {
	case state.Working:
		return Age(now.Sub(w.Since))
	case state.Asked:
		switch {
		case w.Detail == "elicitation":
			return "asked · dialog"
		case w.Detail == "tool" && w.Tool != "":
			return "asked · " + w.Tool
		}
		return "asked"
	case state.NeedsPermission:
		if w.Tool != "" {
			return "perm · " + w.Tool
		}
		return "perm"
	case state.Failed:
		if w.Detail == "rate_limit" {
			if w.LimitReset != nil {
				return "limit · " + ClockText(*w.LimitReset, now, loc)
			}
			return "limit"
		}
		return errorWord(w.Detail)
	case state.Ended:
		return "ended"
	case state.Gone:
		return "gone"
	}
	if w.Detail == "started" {
		return "new"
	}
	return "done"
}

// errorWord is a StopFailure error as a word: "billing_error" -> "billing error".
func errorWord(detail string) string {
	if detail == "" {
		return "failed"
	}
	return strings.ReplaceAll(detail, "_", " ")
}
