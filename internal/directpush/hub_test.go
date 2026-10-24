package directpush

// Tests that the client side agrees with the hub: episode keys (client fold vs hub ingest),
// the rule pick (rules.ResolveDirect + Policy.First vs rules.Match) and quiet hours. Test
// files may import hub packages; the package itself may not.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// runRecords turns a testdata/hooks/runs file into hook records 1 s apart, plus extra
// Stop-with-question and permission records so every trigger state shows up.
func runRecords(t *testing.T, machine, file string) []*record.Record {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var out []*record.Record
	for i := 0; sc.Scan(); i++ {
		var env struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		r, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: "~/.claude", Client: "wy/0.0.0-test",
			Seat: &record.Seat{Account: "acc", Org: "org", Email: "me@example.com"}, Now: t0.Add(time.Duration(i) * time.Second)},
			env.Payload, record.HookOptions{LastMessages: true})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type hubSide struct {
	st  *store.Store
	ing *ingest.Ingester
	dev store.Device
}

func newHubSide(t *testing.T, machine string, now func() time.Time) *hubSide {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c, err := auth.Create(context.Background(), st, auth.CreateRequest{Kind: "machine", Name: "gpu-box", Machine: machine})
	if err != nil {
		t.Fatal(err)
	}
	return &hubSide{st: st, ing: &ingest.Ingester{Store: st, Now: now}, dev: c.Device}
}

func (h *hubSide) ingest(t *testing.T, recs ...*record.Record) {
	t.Helper()
	var raws []json.RawMessage
	for _, r := range recs {
		b, _ := r.Encode()
		raws = append(raws, b)
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "wy/0.0.0-test", "records": raws})
	rep, err := h.ing.Ingest(context.Background(), h.dev, body, nil)
	if err != nil || len(rep.Rejected) > 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// keys is the hub's episode key of every session in a trigger state.
func (h *hubSide) keys(t *testing.T, ids []string) []string {
	t.Helper()
	var out []string
	err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		for _, id := range ids {
			f, ok, err := tx.GetFold(id)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if _, ok := rules.SessionTrigger(f.State, f.NeedsYou); ok {
				out = append(out, rules.SessionEpisode(id, f.State, f.Since))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func sessionsOf(recs []*record.Record) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range recs {
		var p struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(r.Payload, &p) == nil && p.SessionID != "" && !seen[p.SessionID] {
			seen[p.SessionID] = true
			out = append(out, p.SessionID)
		}
	}
	return out
}

// The same records give the same episode key through the client fold (Scan over the
// outbox) and the hub fold (ingest), after every record of every fixture run, whatever
// order the hub receives them in.
func TestEpisodeKeyMatchesHub(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/hooks/runs/*.jsonl")
	if len(files) == 0 {
		t.Fatal("no fixture runs")
	}
	machine := "m_" + record.NewID(t0)
	triggers := 0
	for _, file := range files {
		recs := runRecords(t, machine, file)
		ids := sessionsOf(recs)
		for _, order := range []string{"in-order", "reversed"} {
			t.Run(filepath.Base(file)+"/"+order, func(t *testing.T) {
				for k := 1; k <= len(recs); k++ {
					now := t0.Add(time.Duration(k) * time.Second)
					clock := func() time.Time { return now }
					dir := t.TempDir()
					ob := outbox.New(dir)
					ob.Now, ob.NoFsync = clock, true
					for _, r := range recs[:k] {
						if _, err := ob.Append(r); err != nil {
							t.Fatal(err)
						}
					}
					eps, err := Scan(dir, machine, now)
					if err != nil {
						t.Fatal(err)
					}
					var client []string
					for _, e := range eps {
						client = append(client, e.Key)
					}
					sort.Strings(client)
					h := newHubSide(t, machine, clock)
					batch := append([]*record.Record(nil), recs[:k]...)
					if order == "reversed" {
						for i, j := 0, len(batch)-1; i < j; i, j = i+1, j-1 {
							batch[i], batch[j] = batch[j], batch[i]
						}
					}
					h.ingest(t, batch...)
					hub := h.keys(t, ids)
					if fmt.Sprint(client) != fmt.Sprint(hub) {
						t.Fatalf("after %d records: client %v, hub %v", k, client, hub)
					}
					triggers += len(hub)
				}
			})
		}
	}
	if triggers == 0 {
		t.Fatal("no fixture reached a trigger state: the test compares nothing")
	}
}

// Random rule sets: rules.Match on the hub and Policy.First on rules.ResolveDirect's output
// pick the same rule for random sessions (machines.md 5.3).
func TestFirstMatchesHub(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	seatIDs := []string{"seat_a", "seat_b", "seat_c"}
	pairs := map[string]rules.DirectSeat{"seat_a": {Account: "a", Org: "o1"}, "seat_b": {Account: "b", Org: "o1"}, "seat_c": {Account: "c", Org: "o2"}}
	projects := []string{"api", "infra", "web"}
	machines := []string{"m_here", "m_other", "m_third"}
	triggers := []model.Trigger{model.TriggerNeedsPermission, model.TriggerAsked, model.TriggerFailed, model.TriggerLimit80}
	pick := func(from []string) []string {
		var out []string
		for _, v := range from {
			if rng.Intn(3) == 0 {
				out = append(out, v)
			}
		}
		return out
	}
	for iter := 0; iter < 3000; iter++ {
		var rs []model.Rule
		for i, n := 0, rng.Intn(8); i < n; i++ {
			rs = append(rs, model.Rule{ID: fmt.Sprintf("r%02d", rng.Intn(40)), Trigger: triggers[rng.Intn(len(triggers))],
				Base: rng.Intn(4) == 0, Enabled: rng.Intn(3) > 0, DelayS: rng.Intn(300), BypassQuiet: rng.Intn(2) == 0,
				UpdatedAt: model.Time(rng.Intn(5)),
				Scope:     model.RuleScope{Seats: pick(seatIDs), Projects: pick(projects), Machines: pick(machines)}})
		}
		resolved := rules.ResolveDirect(rs, "m_here", pairs)
		b, _ := json.Marshal(resolved)
		var pol Policy
		if err := json.Unmarshal(b, &pol.Rules); err != nil {
			t.Fatal(err)
		}
		for s := 0; s < 10; s++ {
			trig := triggers[rng.Intn(3)]
			seat := ""
			if i := rng.Intn(4); i < 3 {
				seat = seatIDs[i]
			}
			project := projects[rng.Intn(3)]
			want, wok := rules.Match(rs, trig, rules.Subject{Seat: seat, Project: project, Machine: "m_here"})
			p := pairs[seat]
			i := pol.firstIndex(string(trig), Seat{Account: p.Account, Org: p.Org}, project)
			if wok != (i >= 0) || (wok && resolved[i].ID != want.ID) {
				got := "none"
				if i >= 0 {
					got = resolved[i].ID
				}
				t.Fatalf("iter %d: %s seat %q project %q: hub %v (%v), client %s\nrules %+v", iter, trig, seat, project, want.ID, wok, got, rs)
			}
			if wok {
				r := pol.Rules[i]
				if r.Enabled != want.Enabled || r.DelayS != want.DelayS || r.BypassQuiet != want.BypassQuiet {
					t.Fatalf("rule %s fields differ: %+v vs %+v", want.ID, r, want)
				}
			}
		}
	}
}

// The copied quiet-hours check agrees with push/rules.Holds.
func TestQuietMatchesHub(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	zones := []string{"", "UTC", "Europe/Berlin", "America/New_York", "Australia/Lord_Howe", "Asia/Kolkata"}
	f := false
	for i := 0; i < 20000; i++ {
		q := QuietHours{Enabled: rng.Intn(5) > 0, Start: fmt.Sprintf("%02d:%02d", rng.Intn(24), rng.Intn(4)*15),
			End: fmt.Sprintf("%02d:%02d", rng.Intn(24), rng.Intn(4)*15), TZ: zones[rng.Intn(len(zones))]}
		if rng.Intn(3) == 0 {
			q.Critical = &f
		}
		at := t0.Add(time.Duration(rng.Int63n(int64(400 * 24 * time.Hour))))
		bypass := rng.Intn(2) == 0
		hq := model.QuietHours{Enabled: q.Enabled, Start: q.Start, End: q.End, TZ: q.TZ, Critical: q.Critical}
		want, _ := rules.Holds(hq, bypass, at)
		if got := quietHolds(q, bypass, at); got != want {
			t.Fatalf("%+v bypass %v at %v: client %v, hub %v", q, bypass, at, got, want)
		}
	}
}
