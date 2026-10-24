package pull_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/drainproto"
	"github.com/mraj602/watchyard/internal/hub/pull"
	"github.com/mraj602/watchyard/internal/hub/store"
)

func TestDrainHappyPath(t *testing.T) {
	e := newEnv(t)
	ids := e.fill(250)
	ep := e.session(faults{}, nil)
	if ep.remote.Err != nil || ep.hub.Err != nil {
		t.Fatalf("remote %v hub %v", ep.remote.Err, ep.hub.Err)
	}
	if ep.remote.Reason != "drained" || ep.remote.Batches != 3 || ep.hub.Batches != 3 || ep.hub.Outcome != pull.OutcomeOK {
		t.Fatalf("%+v", ep)
	}
	e.assertOnceInOrder(ids)

	// The hello counted as contact: machine row with reach pull, version, backlog.
	var reach, ver string
	var backlog int64
	var lastSeen int64
	if err := e.st.R().QueryRow(`SELECT coalesce(reach,''), coalesce(sy_version,''), coalesce(backlog,-1), coalesce(last_seen,0) FROM machine WHERE id = ?`, machineID).
		Scan(&reach, &ver, &backlog, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if reach != "pull" || ver != "0.5.0" || lastSeen != e.clk.Now().UnixMilli() {
		t.Fatalf("machine: reach %q version %q last_seen %d", reach, ver, lastSeen)
	}
	// Records belong to the pull device.
	var dev string
	_ = e.st.R().QueryRow(`SELECT device_id FROM record WHERE id = ?`, rid(0)).Scan(&dev)
	if dev != e.target.DeviceID {
		t.Fatalf("device %q, want the pull device %q", dev, e.target.DeviceID)
	}
	st := client.ReadStatus(e.ob.Dir)
	if st.Via != client.ViaPull || st.LastContact == "" || st.LastSuccess == "" || !st.OK {
		t.Fatalf("status %+v", st)
	}
	// Second session: nothing to do, drained immediately.
	ep = e.session(faults{}, nil)
	if ep.remote.Reason != "drained" || ep.remote.Batches != 0 {
		t.Fatalf("%+v", ep)
	}
}

// TestDrainCrashEverywhere is machines.md 11: for every crash point, resending yields each
// record stored exactly once and the cursor at the end.
func TestDrainCrashEverywhere(t *testing.T) {
	type point struct {
		name string
		f    func(nth int) faults
	}
	points := []point{
		{"remote crashes after sending the batch (before ack)", func(n int) faults {
			return faults{remote: crashAt(client.StepBatchSent, n, errBoom)}
		}},
		{"remote crashes with the ack read, nothing applied", func(n int) faults {
			return faults{remote: crashAt(client.StepAckReceived, n, errBoom)}
		}},
		{"remote crashes after rejected log, before the cursor write", func(n int) faults {
			return faults{remote: crashAt(client.StepBeforeSave, n, errBoom)}
		}},
		{"remote crashes after the cursor write", func(n int) faults {
			return faults{remote: crashAt(client.StepAfterSave, n, errBoom)}
		}},
		{"hub crashes after the commit, before the ack", func(n int) faults {
			return faults{hub: crashAt(pull.StepAfterIngest, n, errBoom)}
		}},
		{"hub crashes after the ack", func(n int) faults {
			return faults{hub: crashAt(pull.StepAfterAck, n, errBoom)}
		}},
		{"hub crashes after hello", func(n int) faults {
			return faults{hub: crashAt(pull.StepHello, n, errBoom)}
		}},
	}
	for _, p := range points {
		for _, nth := range []int{1, 2} {
			if nth == 2 && strings.Contains(p.name, "after hello") {
				continue // hello happens once per session
			}
			t.Run(p.name+"/"+string(rune('0'+nth)), func(t *testing.T) {
				e := newEnv(t)
				ids := e.fill(150)
				ep := e.session(p.f(nth), nil)
				if ep.remote.Err == nil && ep.hub.Err == nil {
					t.Fatalf("the crash did not happen: %+v", ep)
				}
				// Whatever happened, the cursor never passed a record the hub lacks.
				stored := map[string]bool{}
				for _, id := range e.stored() {
					stored[id] = true
				}
				b, _ := e.ob.Read(e.ob.ReadCursor(), 1000, 0)
				done := 150 - len(b.Items)
				for i := 0; i < done; i++ {
					if !stored[ids[i]] {
						t.Fatalf("cursor is past record %d, which the hub does not have", i)
					}
				}
				// A restart (twice, for good measure) finishes the job.
				e.drainAll()
				e.assertOnceInOrder(ids)
			})
		}
	}
}

func TestDrainHubCommitsBeforeAck(t *testing.T) {
	// The hub crashes after the commit: the records are in the hub, the remote's cursor has
	// not moved; the retry is answered with duplicates and moves the cursor.
	e := newEnv(t)
	ids := e.fill(50)
	ep := e.session(faults{hub: crashAt(pull.StepAfterIngest, 1, errBoom)}, nil)
	if ep.remote.Batches != 0 {
		t.Fatalf("the remote applied something without an ack: %+v", ep.remote)
	}
	if len(e.stored()) != 50 {
		t.Fatalf("hub stored %d, want 50 committed before the ack", len(e.stored()))
	}
	if c := e.ob.ReadCursor(); c.Offset != 0 {
		t.Fatalf("cursor moved without an ack: %+v", c)
	}
	e.drainAll()
	e.assertOnceInOrder(ids)
}

func TestDrainSurvivesCrashOnEveryBatchRepeatedly(t *testing.T) {
	e := newEnv(t)
	ids := e.fill(400)
	steps := []faults{
		{hub: crashAt(pull.StepAfterIngest, 1, errBoom)},
		{remote: crashAt(client.StepBeforeSave, 1, errBoom)},
		{hub: crashAt(pull.StepAfterAck, 2, errBoom)},
		{remote: crashAt(client.StepBatchSent, 1, errBoom)},
		{remote: crashAt(client.StepAfterSave, 2, errBoom)},
	}
	for _, f := range steps {
		e.session(f, nil)
	}
	e.drainAll()
	e.assertOnceInOrder(ids)
}

// scriptedHub plays the hub's side by hand over the pipes: it reads the remote's hello and
// batch and answers with whatever the test wants.
type scriptedHub struct {
	rd *drainproto.Reader
	wr *drainproto.Writer
}

func runScripted(t *testing.T, e *env, mutate func(*client.DrainOptions), hub func(h *scriptedHub)) client.DrainResult {
	t.Helper()
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	dopts := client.DrainOptions{Outbox: e.ob, Machine: machineID, Name: "gpu-box", SyVersion: "0.5.0", Now: e.clk.Now,
		StartTimeout: 2 * time.Second, AckTimeout: 2 * time.Second}
	if mutate != nil {
		mutate(&dopts)
	}
	res := make(chan client.DrainResult, 1)
	go func() {
		r := client.Drain(context.Background(), r2, w1, dopts)
		w1.Close()
		res <- r
	}()
	h := &scriptedHub{rd: drainproto.NewReader(r1), wr: drainproto.NewWriter(w2)}
	hub(h)
	w2.Close()
	go io.Copy(io.Discard, r1) // let the remote finish writing
	select {
	case r := <-res:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("remote did not finish")
		return client.DrainResult{}
	}
}

func (h *scriptedHub) expectHello(t *testing.T) *drainproto.Hello {
	t.Helper()
	f, err := h.rd.Read()
	if err != nil {
		t.Fatal(err)
	}
	hello, ok := f.(*drainproto.Hello)
	if !ok {
		t.Fatalf("got %T, want hello", f)
	}
	return hello
}

func (h *scriptedHub) start(t *testing.T, vmax int) {
	t.Helper()
	if err := h.wr.Write(drainproto.Start{Proto: 1, Hub: "x", RecordVMax: vmax, MaxRecords: 10, MaxBytes: 1 << 20, BudgetS: 30}); err != nil {
		t.Fatal(err)
	}
}

func (h *scriptedHub) expectBatch(t *testing.T) *drainproto.Batch {
	t.Helper()
	f, err := h.rd.Read()
	if err != nil {
		t.Fatal(err)
	}
	b, ok := f.(*drainproto.Batch)
	if !ok {
		t.Fatalf("got %T, want batch", f)
	}
	return b
}

func ackOf(b *drainproto.Batch) drainproto.Ack {
	a := drainproto.Ack{Seq: b.Seq}
	for _, r := range b.Records {
		var e struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(r, &e)
		a.Accepted = append(a.Accepted, e.ID)
	}
	return a
}

func TestDrainForgedAcksChangeNothing(t *testing.T) {
	cases := map[string]func(b *drainproto.Batch) drainproto.Ack{
		"wrong seq": func(b *drainproto.Batch) drainproto.Ack { a := ackOf(b); a.Seq = b.Seq + 1; return a },
		"foreign id": func(b *drainproto.Batch) drainproto.Ack {
			a := ackOf(b)
			a.Accepted = append(a.Accepted, "01JA2XQ8R7Y5V0Z6M3K1TFOREIGN")
			return a
		},
		"foreign id only": func(b *drainproto.Batch) drainproto.Ack {
			return drainproto.Ack{Seq: b.Seq, Accepted: []string{"01JA2XQ8R7Y5V0Z6M3K1TFOREIGN"}}
		},
		"foreign rejected": func(b *drainproto.Batch) drainproto.Ack {
			a := ackOf(b)
			a.Rejected = []drainproto.Rejected{{ID: "01JA2XQ8R7Y5V0Z6M3K1TFOREIGN", Reason: "bad_id"}}
			return a
		},
	}
	for name, forge := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.fill(5)
			res := runScripted(t, e, nil, func(h *scriptedHub) {
				h.expectHello(t)
				h.start(t, 1)
				b := h.expectBatch(t)
				if err := h.wr.Write(forge(b)); err != nil {
					t.Fatal(err)
				}
			})
			if !res.ProtocolErr || res.Err == nil {
				t.Fatalf("a forged ack must be a protocol error (exit 3): %+v", res)
			}
			if c := e.ob.ReadCursor(); c.Offset != 0 || c.File != "" {
				t.Fatalf("cursor moved on a forged ack: %+v", c)
			}
			if _, err := os.Stat(filepath.Join(e.ob.Dir, "rejected.jsonl")); err == nil {
				t.Fatal("rejected.jsonl written on a forged ack")
			}
		})
	}
}

func TestDrainNackKeepsCursor(t *testing.T) {
	e := newEnv(t)
	e.fill(5)
	res := runScripted(t, e, nil, func(h *scriptedHub) {
		h.expectHello(t)
		h.start(t, 1)
		b := h.expectBatch(t)
		_ = h.wr.Write(drainproto.Nack{Seq: b.Seq, Code: drainproto.NackUnavailable, RetryAfterS: 5})
	})
	if res.ProtocolErr || res.Err == nil || res.Batches != 0 {
		t.Fatalf("nack: %+v", res)
	}
	if c := e.ob.ReadCursor(); c.Offset != 0 {
		t.Fatalf("cursor moved on nack: %+v", c)
	}
}

func TestDrainByeAndEOFStopWithoutApplying(t *testing.T) {
	e := newEnv(t)
	e.fill(5)
	res := runScripted(t, e, nil, func(h *scriptedHub) {
		h.expectHello(t)
		h.start(t, 1)
		h.expectBatch(t)
		_ = h.wr.Write(drainproto.Bye{})
	})
	if res.ProtocolErr || res.Err == nil || e.ob.ReadCursor().Offset != 0 {
		t.Fatalf("bye: %+v", res)
	}
	// Dropped stdin: the hub vanishes after the batch.
	res = runScripted(t, e, nil, func(h *scriptedHub) {
		h.expectHello(t)
		h.start(t, 1)
		h.expectBatch(t)
	})
	if res.ProtocolErr || res.Err == nil || e.ob.ReadCursor().Offset != 0 {
		t.Fatalf("EOF: %+v", res)
	}
}

func TestDrainAckTimeout(t *testing.T) {
	e := newEnv(t)
	e.fill(5)
	res := runScripted(t, e, func(o *client.DrainOptions) { o.AckTimeout = 100 * time.Millisecond }, func(h *scriptedHub) {
		h.expectHello(t)
		h.start(t, 1)
		h.expectBatch(t)
		time.Sleep(400 * time.Millisecond) // no answer
	})
	if res.Err == nil || e.ob.ReadCursor().Offset != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestDrainRefuseCodes(t *testing.T) {
	for _, code := range []string{drainproto.RefuseProto, drainproto.RefuseMachineMismatch, drainproto.RefuseDisabled, drainproto.RefuseFrozen} {
		t.Run(code, func(t *testing.T) {
			e := newEnv(t)
			e.fill(3)
			res := runScripted(t, e, nil, func(h *scriptedHub) {
				h.expectHello(t)
				_ = h.wr.Write(drainproto.Refuse{Code: code, Message: "no"})
			})
			if res.Refused != code || res.ProtocolErr {
				t.Fatalf("%+v", res)
			}
			st := client.ReadStatus(e.ob.Dir)
			if st.PullRefused != code || st.OK || st.Error == "" || st.Via != client.ViaPull {
				t.Fatalf("status %+v", st)
			}
			if st.LastContact != "" {
				t.Fatalf("a refusal is not contact: %+v", st)
			}
			if e.ob.ReadCursor().Offset != 0 {
				t.Fatal("cursor moved")
			}
		})
	}
}

func TestDrainBusyWhenFlushLockTaken(t *testing.T) {
	e := newEnv(t)
	e.fill(3)
	lock, ok, err := e.ob.TryFlushLock()
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	ep := e.session(faults{}, nil)
	lock.Release()
	if !ep.remote.Busy || ep.hub.Outcome != pull.OutcomeBusy {
		t.Fatalf("%+v", ep)
	}
	if len(e.stored()) != 0 {
		t.Fatal("a busy drain stored records")
	}
	// After the lock is free the drain works.
	e.drainAll()
}

func TestDrainHeldAtTooNewRecordAndResumes(t *testing.T) {
	e := newEnv(t)
	ids := e.fillV(6, map[int]int{3: 2})
	var hello *drainproto.Hello
	res := runScripted(t, e, nil, func(h *scriptedHub) {
		hello = h.expectHello(t)
		h.start(t, 1)
		b := h.expectBatch(t)
		if len(b.Records) != 3 || b.More {
			t.Errorf("batch of %d, more %v: must stop before the v2 record", len(b.Records), b.More)
		}
		_ = h.wr.Write(ackOf(b))
		f, _ := h.rd.Read()
		d, ok := f.(*drainproto.Done)
		if !ok || d.Reason != drainproto.DoneHeld || d.Held != 3 {
			t.Errorf("done %+v", f)
		}
	})
	if res.Err != nil || res.Reason != drainproto.DoneHeld {
		t.Fatalf("%+v", res)
	}
	if hello.Backlog != 6 || hello.Oldest == nil {
		t.Fatalf("hello %+v", hello)
	}
	st := client.ReadStatus(e.ob.Dir)
	if st.Held != 3 || st.HubTooOld == nil || st.HubTooOld.Needs != 2 || st.HubTooOld.RecordVMax != 1 {
		t.Fatalf("status %+v %+v", st, st.HubTooOld)
	}
	// The hub now reads v2: the rest goes.
	res = runScripted(t, e, nil, func(h *scriptedHub) {
		h.expectHello(t)
		h.start(t, 2)
		b := h.expectBatch(t)
		if len(b.Records) != 3 {
			t.Errorf("resumed batch of %d", len(b.Records))
		}
		_ = h.wr.Write(ackOf(b))
		_, _ = h.rd.Read()
	})
	if res.Err != nil || res.Sent != 3 {
		t.Fatalf("%+v", res)
	}
	if st := client.ReadStatus(e.ob.Dir); st.Held != 0 || st.HubTooOld != nil {
		t.Fatalf("status %+v", st)
	}
	_ = ids
}

func TestDrainRejectedRecordsAreLogged(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.ob.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Record 1 has a bad envelope (machine is not m_...), 0 and 2 are fine.
	lines := []string{recLine(0, 1, machineID),
		`{"v":1,"id":"` + rid(1) + `","kind":"hook_event","ts":"2026-10-12T10:00:01.000Z","machine":"nope","payload":{}}`,
		recLine(2, 1, machineID)}
	if err := os.WriteFile(filepath.Join(e.ob.Dir, "2026-10-12.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.drainAll()
	if got := e.stored(); len(got) != 2 || got[0] != rid(0) || got[1] != rid(2) {
		t.Fatalf("stored %v", got)
	}
	rej, err := os.ReadFile(filepath.Join(e.ob.Dir, "rejected.jsonl"))
	if err != nil || !strings.Contains(string(rej), rid(1)) || !strings.Contains(string(rej), "bad_envelope") || strings.Count(string(rej), "\n") != 1 {
		t.Fatalf("rejected.jsonl: %q %v", rej, err)
	}
	if st, _ := e.ob.Stat(); st.UnflushedBytes != 0 {
		t.Fatal("cursor not at the end")
	}
}

func TestSessionRecordsAreStoredByteForByte(t *testing.T) {
	e := newEnv(t)
	odd := `{"payload":{"session_id":"s1",   "hook_event_name":"Stop"},"machine":"` + machineID + `",  "ts":"2026-10-12T10:00:00.000Z","kind":"hook_event","id":"` + rid(0) + `","v":1}`
	if err := os.MkdirAll(e.ob.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.ob.Dir, "2026-10-12.jsonl"), []byte(odd+"\n"), 0o600)
	e.drainAll()
	var body string
	if err := e.st.R().QueryRow(`SELECT body FROM record WHERE id = ?`, rid(0)).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != odd {
		t.Fatalf("stored %q\nwant   %q", body, odd)
	}
}

func TestSessionRefusals(t *testing.T) {
	ctx := context.Background()
	refused := func(t *testing.T, e *env, mutate func(*client.DrainOptions)) endpoint {
		t.Helper()
		return e.session(faults{}, func(_ *pull.Session, d *client.DrainOptions) {
			if mutate != nil {
				mutate(d)
			}
		})
	}
	t.Run("machine mismatch", func(t *testing.T) {
		e := newEnv(t)
		e.fill(2)
		ep := refused(t, e, func(d *client.DrainOptions) { d.Machine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4Z" })
		if ep.hub.Outcome != pull.OutcomeRefused || ep.hub.Code != drainproto.RefuseMachineMismatch || ep.remote.Refused != drainproto.RefuseMachineMismatch {
			t.Fatalf("%+v", ep)
		}
		if len(e.stored()) != 0 {
			t.Fatal("stored records of a foreign machine")
		}
	})
	t.Run("proto", func(t *testing.T) {
		// A remote speaking another proto is refused. The Hello frame is validated by the
		// writer only for proto >= 1, so proto 2 can be sent by a scripted remote.
		e := newEnv(t)
		r1, w1 := io.Pipe()
		r2, w2 := io.Pipe()
		sess := &pull.Session{Store: e.st, Ingester: e.ing, HelloTimeout: time.Second}
		done := make(chan pull.Result, 1)
		go func() { done <- sess.Serve(ctx, e.target, r1, w2) }()
		wr := drainproto.NewWriter(w1)
		go func() { _ = wr.Write(drainproto.Hello{Proto: 2, Machine: machineID, Name: "x", Sy: "9"}) }()
		f, err := drainproto.NewReader(r2).Read()
		if err != nil {
			t.Fatal(err)
		}
		if rf, ok := f.(*drainproto.Refuse); !ok || rf.Code != drainproto.RefuseProto {
			t.Fatalf("%T %+v", f, f)
		}
		if r := <-done; r.Outcome != pull.OutcomeRefused {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		e := newEnv(t)
		e.fill(2)
		if err := e.st.SetPullEnabled(ctx, e.target.ID, false); err != nil {
			t.Fatal(err)
		}
		ep := refused(t, e, nil)
		if ep.hub.Code != drainproto.RefuseDisabled || len(e.stored()) != 0 {
			t.Fatalf("%+v", ep)
		}
	})
	t.Run("revoked device", func(t *testing.T) {
		e := newEnv(t)
		e.fill(2)
		if err := e.st.Write(ctx, func(tx *store.Tx) error { return tx.RevokeDeviceCascade(e.target.DeviceID) }); err != nil {
			t.Fatal(err)
		}
		ep := refused(t, e, nil)
		if ep.hub.Code != drainproto.RefuseDisabled || len(e.stored()) != 0 {
			t.Fatalf("%+v", ep)
		}
	})
	t.Run("frozen", func(t *testing.T) {
		e := newEnv(t)
		e.fill(2)
		_ = e.st.Write(ctx, func(tx *store.Tx) error {
			return tx.PutAlertSetting(pull.SettingFrozen, []byte(`{"since":1,"moved_to":null}`), 1)
		})
		ep := refused(t, e, nil)
		if ep.hub.Code != drainproto.RefuseFrozen || len(e.stored()) != 0 {
			t.Fatalf("%+v", ep)
		}
	})
	t.Run("every record machine_mismatch", func(t *testing.T) {
		// The hello claims the bound machine but the records are for another one: the
		// remote's state folder changed. Acked like any other, then the target is refused.
		e := newEnv(t)
		if err := os.MkdirAll(e.ob.Dir, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(e.ob.Dir, "2026-10-12.jsonl"), []byte(recLine(0, 1, "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4Z")+"\n"), 0o600)
		ep := e.session(faults{}, nil)
		if ep.hub.Outcome != pull.OutcomeRefused || ep.hub.Code != drainproto.RefuseMachineMismatch {
			t.Fatalf("%+v", ep)
		}
		rej, _ := os.ReadFile(filepath.Join(e.ob.Dir, "rejected.jsonl"))
		if !strings.Contains(string(rej), "machine_mismatch") {
			t.Fatalf("the remote must log the rejection: %q", rej)
		}
	})
}

func TestSessionBindsUnboundTargetOnFirstHello(t *testing.T) {
	e := newEnv(t)
	tg, err := e.st.AddPullTarget(context.Background(), store.PullTarget{Name: "later", Host: "later.example", User: "dev", HostKeys: []string{hostKeyA}})
	if err != nil {
		t.Fatal(err)
	}
	if tg.MachineID != "" || tg.DeviceID != "" {
		t.Fatalf("%+v", tg)
	}
	other := "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4Z"
	e.target = tg
	e.ob = outboxAt(t, "other")
	writeRecs(t, e.ob, other, 4)
	ep := e.session(faults{}, func(_ *pull.Session, d *client.DrainOptions) { d.Machine = other; d.Name = "later-box" })
	if ep.hub.Err != nil || ep.remote.Err != nil || len(e.stored()) != 4 {
		t.Fatalf("%+v stored %d", ep, len(e.stored()))
	}
	bound, _ := e.st.PullTargetByID(context.Background(), tg.ID)
	if bound.MachineID != other || bound.DeviceID == "" {
		t.Fatalf("not bound: %+v", bound)
	}
	d, err := e.st.DeviceByID(context.Background(), bound.DeviceID)
	if err != nil || d.Kind != "machine" || d.CreatedBy != "pull:"+tg.ID || d.MachineID != other {
		t.Fatalf("device %+v %v", d, err)
	}
	// A different machine saying hello on the bound target is refused.
	ep = e.session(faults{}, func(_ *pull.Session, d *client.DrainOptions) { d.Machine = machineID })
	if ep.hub.Code != drainproto.RefuseMachineMismatch {
		t.Fatalf("%+v", ep)
	}
}

func TestSessionBudgetEndsWithByeAndMore(t *testing.T) {
	e := newEnv(t)
	ids := e.fill(180)
	ep := e.session(faults{}, func(s *pull.Session, _ *client.DrainOptions) { s.MaxBatches = 2; s.MaxRecords = 50 })
	if ep.hub.Outcome != pull.OutcomeOK || !ep.hub.More || ep.hub.Batches != 2 {
		t.Fatalf("%+v", ep.hub)
	}
	// The hub's budget bye is a clean end for the remote, not a failure.
	if ep.remote.Err != nil || ep.remote.ProtocolErr || !ep.remote.Bye || ep.remote.Batches != 2 {
		t.Fatalf("remote %+v", ep.remote)
	}
	if ep.remote.Reason == "drained" {
		t.Fatal("drained after a budget stop")
	}
	if len(e.stored()) != 100 {
		t.Fatalf("stored %d", len(e.stored()))
	}
	e.drainAll()
	e.assertOnceInOrder(ids)
}
