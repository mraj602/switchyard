package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/record"
)

// AgentsTimeout bounds one `claude agents --json` run.
const AgentsTimeout = 20 * time.Second

// AgentsOptions configures Agents, the `sy agents` command.
type AgentsOptions struct {
	Slots   []identity.Slot
	Environ []string        // the environment `claude` runs in (os.Environ())
	Run     identity.Runner // nil: the real command
	FS      identity.FS     // nil: identity.OSFS (refuses *.key and .credentials.json)
	Outbox  *outbox.Outbox
	Machine string
	Client  string
	SeatOf  func(slot identity.Slot) *record.Seat
	Now     func() time.Time
}

// SlotList is the session list of one slot and what was done with it.
type SlotList struct {
	Slot     string
	Complete bool
	Source   string // claude_agents or sessions_dir
	Sessions int
	Error    string
	RecordID string
}

// Agents writes one session_list record per slot (record.md 4.5): the output of
// `claude agents --json` run with CLAUDE_CONFIG_DIR set to the slot, or, when that fails,
// the files <slot>/sessions/*.json.
func Agents(ctx context.Context, o AgentsOptions) ([]SlotList, error) {
	if o.Run == nil {
		o.Run = identity.ExecRunner
	}
	if o.FS == nil {
		o.FS = identity.OSFS{}
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	lists := make([]CollectedList, len(o.Slots))
	started := make([]time.Time, len(o.Slots)) // when each slot's `claude agents` run began
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, s := range o.Slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			started[i] = now()
			lists[i] = CollectList(ctx, s, o.Environ, o.Run, o.FS)
		}()
	}
	wg.Wait()

	var out []SlotList
	var firstErr error
	for i, s := range o.Slots {
		l := lists[i]
		m := record.Meta{Machine: o.Machine, Slot: s.Name, Client: o.Client, Now: started[i]}
		if o.SeatOf != nil {
			m.Seat = o.SeatOf(s)
		}
		r, err := record.NewSessionList(m, record.SessionListPayload{
			Complete: l.Complete, Source: l.Source, Sessions: l.Sessions, Error: l.Error,
		})
		if err == nil {
			_, err = o.Outbox.Append(r)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", s.Name, err)
			}
			continue
		}
		out = append(out, SlotList{Slot: s.Name, Complete: l.Complete, Source: l.Source,
			Sessions: len(l.Sessions), Error: l.Error, RecordID: r.ID})
	}
	return out, firstErr
}

// CollectedList is the raw result for one slot, before it becomes a record.
type CollectedList struct {
	Complete bool
	Source   string
	Sessions []json.RawMessage
	Error    string
}

// CollectList lists the live sessions of one slot. It never opens *.key files.
//
// ~/.claude is special: `claude` reads it both when CLAUDE_CONFIG_DIR is unset and when it
// is set to ~/.claude, and the two views differ (a background session started with the
// variable set shows only in the second), so for the default slot both are run and the
// results are merged by session id. The list is complete only when every run succeeded.
func CollectList(ctx context.Context, slot identity.Slot, environ []string, run identity.Runner, fsys identity.FS) CollectedList {
	envs := [][]string{identity.AuthEnv(environ, slot)}
	if slot.Explicit {
		// Called with the explicit view of ~/.claude: add the unset view as well.
		envs = [][]string{identity.AuthEnv(environ, identity.Slot{Dir: slot.Dir, Default: true}), identity.AuthEnv(environ, slot)}
	}
	if slot.Default {
		explicit := identity.AuthEnv(environ, identity.Slot{Dir: slot.Dir}) // sets CLAUDE_CONFIG_DIR=<slot.Dir>
		envs = append(envs, explicit)
	}
	var merged []json.RawMessage
	seen := map[string]bool{}
	var firstErr error
	for _, env := range envs {
		items, err := runAgents(ctx, env, run)
		if err != nil {
			firstErr = err
			break
		}
		for _, it := range items {
			if k := itemKey(it); k != "" {
				if seen[k] {
					continue
				}
				seen[k] = true
			}
			merged = append(merged, it)
		}
	}
	if firstErr == nil {
		return CollectedList{Complete: true, Source: "claude_agents", Sessions: merged}
	}
	// Fallback: the sessions folder. It has no background sessions and may hold files of
	// processes that died without cleaning up, so it never counts as complete.
	items, err := readSessionsDir(fsys, slot)
	res := CollectedList{Complete: false, Source: "sessions_dir", Sessions: items,
		Error: "claude agents failed: " + firstErr.Error()}
	if err != nil {
		res.Error += "; sessions folder: " + err.Error()
	}
	return res
}

func runAgents(ctx context.Context, env []string, run identity.Runner) ([]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, AgentsTimeout)
	defer cancel()
	out, err := run(ctx, env, "claude", "agents", "--json")
	if err != nil {
		return nil, fmt.Errorf("%s", firstLine(err.Error()))
	}
	var items []json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(out), &items); err != nil {
		return nil, fmt.Errorf("output is not a JSON array")
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	return items, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// itemKey identifies a session across the two views of the default slot.
func itemKey(raw json.RawMessage) string {
	var x struct {
		SessionID string `json:"sessionId"`
		ID        string `json:"id"`
		PID       int    `json:"pid"`
	}
	if json.Unmarshal(raw, &x) != nil {
		return ""
	}
	switch {
	case x.SessionID != "":
		return "s:" + x.SessionID
	case x.ID != "":
		return "i:" + x.ID
	case x.PID != 0:
		return fmt.Sprintf("p:%d", x.PID)
	}
	return ""
}

// readSessionsDir reads <slot>/sessions/*.json. Only names ending in ".json" are opened, so
// the *.key files next to them are never touched (and identity.OSFS refuses them anyway).
func readSessionsDir(fsys identity.FS, slot identity.Slot) ([]json.RawMessage, error) {
	dir := filepath.Join(slot.Dir, "sessions")
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return []json.RawMessage{}, err
	}
	items := []json.RawMessage{}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		f, err := fsys.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if err != nil || !json.Valid(b) || len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
			continue
		}
		items = append(items, json.RawMessage(bytes.TrimSpace(b)))
	}
	return items, nil
}
