package pull_test

// Phase 5 wave 2: the hub drain path goes through the ingester like an upload (Contact on
// hello, IngestWith with more/reach), and a freeze in the middle of a session refuses.

import (
	"context"
	"sync"
	"testing"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/drainproto"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/pull"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// changeLog collects the ingester's change feed (what the engine and the stream get).
type changeLog struct {
	mu  sync.Mutex
	chs []ingest.Changes
}

func (c *changeLog) add(ch ingest.Changes) { c.mu.Lock(); c.chs = append(c.chs, ch); c.mu.Unlock() }
func (c *changeLog) all() []ingest.Changes {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ingest.Changes(nil), c.chs...)
}

func TestSessionHelloIsContactThroughTheIngester(t *testing.T) {
	e := newEnv(t)
	var log changeLog
	e.ing.OnChange = log.add
	// No records: a hello alone must reach the engine (machine_silent recovers, push.md 3.1).
	ep := e.session(faults{}, nil)
	if ep.hub.Outcome != pull.OutcomeOK {
		t.Fatalf("session: %+v / %+v", ep.hub, ep.remote)
	}
	chs := log.all()
	if len(chs) == 0 || !chs[0].Machines[machineID] {
		t.Fatalf("hello did not reach the change feed: %+v", chs)
	}
	var reach string
	var lastSeen int64
	if err := e.st.R().QueryRowContext(context.Background(), `SELECT coalesce(reach, ''), coalesce(last_seen, 0) FROM machine WHERE id = ?`, machineID).Scan(&reach, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if reach != "pull" || lastSeen == 0 {
		t.Errorf("machine after hello: reach %q last_seen %d", reach, lastSeen)
	}
}

func TestSessionBatchesCarryMoreAndReach(t *testing.T) {
	e := newEnv(t)
	var log changeLog
	e.ing.OnChange = log.add
	ids := e.fill(250) // MaxRecords 100: three batches, the first two with more
	ep := e.session(faults{}, nil)
	if ep.hub.Outcome != pull.OutcomeOK || ep.hub.Batches != 3 {
		t.Fatalf("session: %+v", ep.hub)
	}
	e.assertOnceInOrder(ids)
	var mores []bool
	for _, ch := range log.all() {
		if m, ok := ch.More[machineID]; ok {
			mores = append(mores, m)
		}
	}
	if len(mores) != 3 || !mores[0] || !mores[1] || mores[2] {
		t.Errorf("catch-up hold input (Changes.More per batch) = %v, want [true true false]", mores)
	}
	var backlog int64
	var oldest *int64
	if err := e.st.R().QueryRowContext(context.Background(), `SELECT coalesce(backlog, -1), backlog_oldest FROM machine WHERE id = ?`, machineID).Scan(&backlog, &oldest); err != nil {
		t.Fatal(err)
	}
	if backlog != 0 || oldest != nil {
		t.Errorf("after the last batch: backlog %d (want 0: the last batch is answered), backlog_oldest %v (want none)", backlog, oldest)
	}
}

func TestSessionFreezeMidSessionRefusesFrozen(t *testing.T) {
	e := newEnv(t)
	ids := e.fill(250)
	freezeAfterFirst := func(step string) error {
		if step == pull.StepAfterAck {
			// The freeze commits between two batches of a running session.
			_ = e.st.Write(context.Background(), func(tx *store.Tx) error {
				return tx.PutAlertSetting(pull.SettingFrozen, []byte(`{"since":1,"moved_to":"https://new.example"}`), 1)
			})
		}
		return nil
	}
	ep := e.session(faults{hub: freezeAfterFirst}, nil)
	if ep.hub.Outcome != pull.OutcomeRefused || ep.hub.Code != drainproto.RefuseFrozen {
		t.Fatalf("hub: %+v, want refused/frozen at the next batch", ep.hub)
	}
	got := e.stored()
	if len(got) != 100 {
		t.Fatalf("stored %d records, want only the first batch (100)", len(got))
	}
	st := client.ReadStatus(e.ob.Dir)
	if st.PullRefused != drainproto.RefuseFrozen {
		t.Errorf("remote flush.status pull_refused = %q", st.PullRefused)
	}
	// Unfreeze: the next session takes the rest, nothing twice.
	if _, err := e.st.Unfreeze(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.drainAll()
	e.assertOnceInOrder(ids)
}
