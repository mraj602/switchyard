package directpush

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/status"
)

// Look-back windows: sessions with records in the last Recent are checked (machines.md 5.4);
// the fold reads day files back FoldBack so a state entered before Recent folds right.
const (
	Recent   = 24 * time.Hour
	FoldBack = 48 * time.Hour
)

var (
	dayFileRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)
	spillFileRe = regexp.MustCompile(`^spill-\d+-[0-9A-Za-z]+\.jsonl$`)
)

// loc is where a record lives in the outbox.
type loc struct {
	file  string
	end   int64 // offset just past its newline
	spill bool
	side  bool // from the answered sidecar: the hub has answered it
}

// answered reports whether the hub has answered the record (outbox.md 5): it is before the
// cursor in a day file. Spill files are never covered.
func (l loc) answered(c outbox.Cursor) bool {
	if l.side {
		return true
	}
	if l.spill || c.File == "" {
		return false
	}
	return l.file < c.File || (l.file == c.File && l.end <= c.Offset)
}

// Episode is one session of this machine in a trigger state, as the hub would key it.
type Episode struct {
	Key       string    // s:<session>:<state>:<since ms> (push.md 3.2)
	Trigger   string    // needs_permission, asked, failed
	Session   string    // session id
	Since     time.Time // ts of the record that entered the state
	Record    string    // that record's id
	Answered  bool      // the hub has answered that record (condition 3 fails)
	Slot      string
	Seat      Seat
	Email     string
	Project   string // base name of cwd, as the hub derives it
	RemoteURL string
	Detail    string // state detail (failed:<detail>, asked:question|tool|elicitation)
	Peers     string // the turn is sessions coordinating: "message" or "wait" (states.md 3.1)
	Question  string
	LastMsg   string
	Notice    string // the last Notification message, for needs_permission
	ToolName  string
	LastAt    time.Time // newest record of the session
}

// EpisodeKey is push.md 3.2's session episode key.
func EpisodeKey(session, trigger string, since time.Time) string {
	return "s:" + session + ":" + trigger + ":" + strconv.FormatInt(since.UnixMilli(), 10)
}

// triggerOf maps a folded state to its trigger (rules.SessionTrigger).
func triggerOf(s *state.Session) (string, bool) {
	switch s.State {
	case state.NeedsPermission:
		return TriggerNeedsPermission, true
	case state.Asked:
		return TriggerAsked, true
	case state.Failed:
		if s.NeedsYou() {
			return TriggerFailed, true
		}
	}
	return "", false
}

// projectOf is the hub's project(): the base name of cwd ("" for none).
func projectOf(cwd string) string {
	if cwd == "" {
		return ""
	}
	cwd = strings.ReplaceAll(cwd, "\\", "/")
	return path.Base(strings.TrimRight(cwd, "/"))
}

// Scan folds the outbox and returns the sessions of machine that are in a trigger state
// and had a record in the last Recent before now, ordered by Since. machine "" keeps every
// record.
func Scan(outboxDir, machine string, now time.Time) ([]Episode, error) {
	recs, where, err := readOutbox(outboxDir, machine, now.Add(-FoldBack))
	if err != nil {
		return nil, err
	}
	cur := outbox.New(outboxDir).ReadCursor()
	sessions := status.Adapt(recs)
	byID := make(map[string]*record.Record, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	extra := hookExtras(recs)
	var out []Episode
	for id, s := range sessions {
		var f state.Session
		var enter string
		var last time.Time
		for _, in := range s.Inputs {
			before, beforeSince := f.State, f.Since
			f.Apply(in)
			if f.State != before || !f.Since.Equal(beforeSince) {
				enter = in.ID
			}
			if in.TS.After(last) {
				last = in.TS
			}
		}
		trig, ok := triggerOf(&f)
		if !ok || enter == "" || now.Sub(last) > Recent {
			continue
		}
		e := Episode{Key: EpisodeKey(id, string(f.State), f.Since), Trigger: trig, Session: id, Since: f.Since, Record: enter,
			Slot: s.Slot, Project: projectOf(s.Cwd), Detail: f.Detail, Peers: f.Peers, Question: f.Question, LastMsg: f.LastMessage, LastAt: last}
		if l, ok := where[enter]; ok {
			e.Answered = l.answered(cur)
		}
		if s.Seat != nil {
			e.Seat = Seat{Account: s.Seat.Account, Org: s.Seat.Org}
			e.Email = s.Seat.Email
		}
		if x, ok := extra[id]; ok {
			e.RemoteURL, e.Notice, e.ToolName = x.remote, x.notice, x.tool
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].Session < out[j].Session
	})
	return out, nil
}

type extras struct {
	remote, notice, tool string
	remoteAt             time.Time
}

// hookExtras collects, per session, the newest Remote Control link (derived.remote_url), the
// last Notification message and the last tool name: text for the push.
func hookExtras(recs []*record.Record) map[string]extras {
	out := map[string]extras{}
	type stamped struct {
		r  *record.Record
		ts time.Time
	}
	var list []stamped
	for _, r := range recs {
		if r.Kind != record.KindHookEvent {
			continue
		}
		if ts, err := r.Time(); err == nil {
			list = append(list, stamped{r, ts})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].ts.Before(list[j].ts) })
	for _, it := range list {
		var p struct {
			SessionID string `json:"session_id"`
			Event     string `json:"hook_event_name"`
			Message   string `json:"message"`
			ToolName  string `json:"tool_name"`
		}
		if json.Unmarshal(it.r.Payload, &p) != nil || p.SessionID == "" {
			continue
		}
		x := out[p.SessionID]
		if raw, ok := it.r.Derived["remote_url"]; ok {
			var u string
			if json.Unmarshal(raw, &u) == nil && strings.HasPrefix(u, "https://claude.ai/") && !strings.ContainsAny(u, " \t\r\n\"'<>") {
				x.remote, x.remoteAt = u, it.ts
			}
		}
		if p.Event == "Notification" && p.Message != "" {
			x.notice = p.Message
		}
		if p.ToolName != "" {
			x.tool = p.ToolName
		}
		out[p.SessionID] = x
	}
	return out
}

// readOutbox parses the day files from the one holding since onward and every spill file;
// records of other machines (when machine is set), unparsable lines and partial last lines
// are skipped. It never writes.
func readOutbox(dir, machine string, since time.Time) ([]*record.Record, map[string]loc, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, map[string]loc{}, nil
	} else if err != nil {
		return nil, nil, err
	}
	first := since.UTC().Format("2006-01-02") + ".jsonl"
	var days, spills []string
	for _, e := range ents {
		switch n := e.Name(); {
		case e.IsDir():
		case dayFileRe.MatchString(n):
			if n >= first {
				days = append(days, n)
			}
		case spillFileRe.MatchString(n):
			spills = append(spills, n)
		}
	}
	sort.Strings(days)
	sort.Strings(spills)
	var recs []*record.Record
	where := map[string]loc{}
	read := func(name string, spill, side bool) error {
		f, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		defer f.Close()
		br := bufio.NewReaderSize(f, 64<<10)
		var pos int64
		for {
			raw, err := br.ReadBytes('\n')
			if err == io.EOF {
				return nil // a partial last line belongs to a write in progress
			} else if err != nil {
				return err
			}
			pos += int64(len(raw))
			var r record.Record
			if json.Unmarshal(raw, &r) != nil || r.ID == "" {
				continue
			}
			if machine != "" && r.Machine != machine {
				continue
			}
			if _, dup := where[r.ID]; dup {
				continue
			}
			if side {
				// Only records still inside the look-back window matter.
				if t, err := r.Time(); err != nil || t.Before(since) {
					continue
				}
			}
			where[r.ID] = loc{file: name, end: pos, spill: spill, side: side}
			recs = append(recs, &r)
		}
	}
	// The answered sidecar first: a record in it was answered, and wins over a copy that a
	// crash left in a spill file not yet deleted.
	for _, n := range []string{outbox.AnsweredBase + ".1.jsonl", outbox.AnsweredBase + ".jsonl"} {
		if err := read(n, false, true); err != nil {
			return nil, nil, err
		}
	}
	for _, n := range days {
		if err := read(n, false, false); err != nil {
			return nil, nil, err
		}
	}
	for _, n := range spills {
		if err := read(n, true, false); err != nil {
			return nil, nil, err
		}
	}
	return recs, where, nil
}
