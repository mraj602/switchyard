package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

// RefoldReport is what Refold changed (or, dry, would change).
type RefoldReport struct {
	Sessions int // sessions looked at
	Live     int // refolded from their hook events and session lists
	History  int // known only from transcripts: ended:history
	// Changed counts sessions whose (state, detail, since, ended_at) moved; Started those
	// whose started_at moved; Project those that got a project.
	Changed, Started, Project int
	// ByState counts the sessions per state after the refold.
	ByState map[string]int
}

var errDryRun = errors.New("dry run")

// Refold rebuilds every session's fold and its times from the stored records, with the
// current engine (states.md 5.1, `wy hub refold`): sessions with a hook event or a
// session_list that listed them are refolded from those records; the rest are history,
// ended at their newest transcript activity. started_at becomes the earliest of the first
// hook event, the first transcript row and (for listed sessions) the stored start; for a
// history session it is its first transcript row, or unknown. With dry, nothing is
// written. It runs in one write transaction, so a running hub's ingest just waits.
func Refold(ctx context.Context, st *store.Store, dry bool) (RefoldReport, error) {
	rep := RefoldReport{ByState: map[string]int{}}
	err := st.Write(ctx, func(tx *store.Tx) error {
		rep = RefoldReport{ByState: map[string]int{}}
		ext := map[string]extent{}
		err := tx.EachRecordOfKind(record.KindTokenRows, func(r store.RecordRef) error {
			var b struct {
				Payload record.TokenRowsPayload `json:"payload"`
			}
			if json.Unmarshal(r.Body, &b) != nil {
				return nil // a record that never typed stays as it is
			}
			for id, e := range transcriptExtents(b.Payload.Rows) {
				e.rec = r.ID
				old, ok := ext[id]
				if !ok {
					ext[id] = e
					continue
				}
				if e.last > old.last {
					old.rec = r.ID
				}
				if e.first < old.first {
					old.first = e.first
					if e.cwd != "" {
						old.cwd = e.cwd
					}
				}
				if old.cwd == "" {
					old.cwd = e.cwd
				}
				old.last = max(old.last, e.last)
				ext[id] = old
			}
			return nil
		})
		if err != nil {
			return err
		}
		ids, err := tx.SessionIDs()
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := refoldOne(tx, id, ext[id], &rep); err != nil {
				return fmt.Errorf("refold %s: %w", id, err)
			}
		}
		if dry {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	return rep, err
}

func refoldOne(tx *store.Tx, id string, e extent, rep *RefoldReport) error {
	rep.Sessions++
	sess, _, err := tx.GetSession(id)
	if err != nil {
		return err
	}
	before, hadFold, err := tx.GetFold(id)
	if err != nil {
		return err
	}
	inputs, err := loadInputs(tx, id)
	if err != nil {
		return err
	}
	live := false
	first := int64(0)
	for _, in := range inputs {
		if in.Hook != nil {
			live = true
			if t := ms(in.TS); first == 0 || t < first {
				first = t
			}
		}
		if in.List != nil && in.List.Listed {
			live = true
		}
	}
	start := sess.StartedAt
	if live {
		rep.Live++
		for _, t := range []int64{first, e.first} {
			if t != 0 && (start == 0 || t < start) {
				start = t
			}
		}
	} else {
		rep.History++
		start = e.first
		// Only the transcript: the fold is ended:history at its newest activity.
		inputs = []state.Input{{ID: e.rec, TS: tm(e.last), Transcript: &state.TranscriptObs{}}}
	}
	if start != sess.StartedAt {
		rep.Started++
		if err := tx.SetSessionStart(id, start); err != nil {
			return err
		}
	}
	if sess.Project == "" && project(e.cwd) != "" {
		rep.Project++
		if err := tx.PatchSession(id, store.SessionInfo{Project: project(e.cwd)}); err != nil {
			return err
		}
	}
	if err := persistFold(tx, id, inputs); err != nil {
		return err
	}
	after, ok, err := tx.GetFold(id)
	if err != nil {
		return err
	}
	if ok {
		rep.ByState[after.State]++
	}
	if ok != hadFold || before.State != after.State || before.Detail != after.Detail || before.Since != after.Since ||
		before.EndedAt != after.EndedAt {
		rep.Changed++
	}
	return nil
}
