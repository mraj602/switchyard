package ingest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

// Phase 5 kinds (record.md 4.8, 4.9; machines.md 5.5, 6.3).

// machineInfo copies a machine_info record into the machine row when it is newer than the
// stored one (only a newer info_at wins).
func (t *typer) machineInfo(c *ctx) error {
	var p record.MachineInfoPayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("machine_info payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	var oldest int64
	if o := p.Outbox.OldestUnflushed; o != nil {
		if ts, err := time.Parse(time.RFC3339Nano, *o); err == nil {
			oldest = ts.UnixMilli()
		}
	}
	changed, err := t.tx.MachineInfo(c.r.Machine, store.MachineInfoUpdate{
		OS: p.OS, Arch: p.Arch, Reach: p.Reach, RecordV: p.RecordV, Held: p.Outbox.Held,
		BacklogOldest: oldest, Info: c.r.Payload, At: c.at, Name: cleanMachineName(p.Name),
	})
	if err != nil {
		return err
	}
	if changed {
		t.ch.Machines[c.r.Machine] = true
	}
	return nil
}

// cleanMachineName is machine_info.name when it is a valid machine name (identity.md 2:
// 1-60 characters, no control characters), else "" (ignored; the record still applies).
func cleanMachineName(n string) string {
	n = strings.TrimSpace(n)
	if n == "" || !utf8.ValidString(n) || utf8.RuneCountInString(n) > 60 {
		return ""
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return n
}

// directPush applies a direct_push record (machines.md 5.5) to the episode's alert. A failed
// push changes nothing (the hub may still push), and a push for a session that is not on the
// record's machine is stored and ignored. The session goes into the batch's changes, so the
// engine reconciles at once (an alert created fired for an episode that is already over is
// closed by that reconcile).
func (t *typer) directPush(c *ctx) error {
	var p record.DirectPushPayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("direct_push payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if !p.OK {
		return nil
	}
	s, ok, err := t.tx.GetSession(p.SessionID)
	if err != nil {
		return err
	}
	if !ok || s.MachineID != c.r.Machine {
		t.logs = append(t.logs, fmt.Sprintf("direct_push ignored record=%s session=%s: not a session of machine %s",
			c.r.ID, p.SessionID, c.r.Machine))
		return nil
	}
	since, _ := strconv.ParseInt(p.Episode[strings.LastIndexByte(p.Episode, ':')+1:], 10, 64)
	sent, err := time.Parse(time.RFC3339Nano, p.SentAt)
	if err != nil {
		return fmt.Errorf("direct_push: bad sent_at %q", p.SentAt)
	}
	outcome, a, err := t.tx.AlertDirectPush(store.DirectPush{
		Episode: p.Episode, Trigger: p.Trigger, Session: p.SessionID, Seat: s.SeatID, Machine: c.r.Machine,
		SinceMS: since, SentMS: sent.UnixMilli(), Project: s.Project,
	}, t.now)
	if err != nil {
		return err
	}
	if outcome == store.DirectDuplicate {
		t.logs = append(t.logs, fmt.Sprintf("direct_push duplicate alert=%s session=%s episode=%s status=%s",
			a.ID, p.SessionID, p.Episode, a.Status))
	}
	return t.touch(p.SessionID, false)
}
