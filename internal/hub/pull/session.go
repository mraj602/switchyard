package pull

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mraj602/switchyard/internal/drainproto"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

// SettingFrozen is the hub.frozen setting (machines.md 8.1): while it exists the hub
// refuses pulls with `frozen` and the scheduler pauses.
const SettingFrozen = "hub.frozen"

// Steps passed to Session.Hook for failure injection in tests; a non-nil return simulates a
// crash of the hub right there (Serve returns it and sends nothing more).
const (
	StepHello       = "hello"        // hello accepted and counted, start not yet sent
	StepAfterIngest = "after_ingest" // the batch is committed, the ack is not sent
	StepAfterAck    = "after_ack"    // the ack was written
)

// Session is the hub side of one drain session over a pipe pair. It holds no state between
// sessions; the scheduler makes one per run, tests make their own.
type Session struct {
	Store    *store.Store
	Ingester *ingest.Ingester
	// HubVersion goes in `start`; default "dev".
	HubVersion string
	Now        func() time.Time

	// Budget and MaxBatches end a session with `bye` (defaults drainproto.Budget, MaxBatches).
	// HelloTimeout and AckTimeout (the wait for the next batch) default to the drainproto values.
	Budget       time.Duration
	MaxBatches   int
	HelloTimeout time.Duration
	BatchTimeout time.Duration
	// MaxRecords and MaxBytes lower the limits sent in `start` (never raise them).
	MaxRecords, MaxBytes int

	// ByeGrace is how long the session keeps reading after bye (default 2 s).
	ByeGrace time.Duration

	// Hook is a test seam, see the Step constants.
	Hook func(step string) error
}

// Outcome classifies how a session ended, for the scheduler.
type Outcome int

const (
	OutcomeOK      Outcome = iota // hello and a normal end (done drained/held/budget)
	OutcomeBusy                   // the remote answered `busy`
	OutcomeRefused                // we refused (Result.Code) or every record was machine_mismatch
	OutcomeNack                   // the store could not take a batch
	OutcomeFailed                 // protocol error, timeout, EOF before done, ...
)

// Result is what a session did.
type Result struct {
	Outcome Outcome
	Code    string // refuse code for OutcomeRefused
	Msg     string // the refusal's message, for the target's last_error
	More    bool   // records remain on the remote (budget used up with more)
	Batches int
	Records int    // records ingested (accepted, includes duplicates)
	Machine string // the remote's machine id once known
	Err     error
}

type event struct {
	f   drainproto.Frame
	err error
}

func (s *Session) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Serve runs one session against target t: in is what the remote writes (its stdout), out is
// what it reads (its stdin). It sends `bye` or a final frame before returning where the
// protocol allows, and never closes in or out.
func (s *Session) Serve(ctx context.Context, t store.PullTarget, in io.Reader, out io.Writer) Result {
	budget, maxBatches := s.Budget, s.MaxBatches
	if budget <= 0 {
		budget = drainproto.Budget
	}
	if maxBatches <= 0 {
		maxBatches = drainproto.MaxBatches
	}
	helloT, batchT := s.HelloTimeout, s.BatchTimeout
	if helloT <= 0 {
		helloT = drainproto.HelloTimeout
	}
	if batchT <= 0 {
		batchT = drainproto.AckTimeout
	}
	done := make(chan struct{})
	defer close(done)
	ch := make(chan event)
	rd := drainproto.NewReader(in)
	go func() {
		for {
			f, err := rd.Read()
			select {
			case ch <- event{f, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	next := func(timeout time.Duration) (drainproto.Frame, error) {
		tm := time.NewTimer(timeout)
		defer tm.Stop()
		select {
		case ev := <-ch:
			return ev.f, ev.err
		case <-tm.C:
			return nil, errors.New("timed out waiting for the remote")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	w := drainproto.NewWriter(out)
	var res Result
	fail := func(err error) Result {
		res.Outcome, res.Err = OutcomeFailed, err
		return res
	}

	// 1. hello (or busy).
	f, err := next(helloT)
	if err != nil {
		return fail(fmt.Errorf("no hello: %w", err))
	}
	var hello *drainproto.Hello
	switch v := f.(type) {
	case *drainproto.Busy:
		res.Outcome = OutcomeBusy
		return res
	case *drainproto.Hello:
		hello = v
	default:
		return fail(fmt.Errorf("%w: expected hello, got %s", drainproto.ErrProtocol, f.Type()))
	}
	res.Machine = hello.Machine

	refuse := func(code, msg string) Result {
		if err := w.Write(drainproto.Refuse{Code: code, Message: msg}); err != nil {
			return fail(err)
		}
		res.Outcome, res.Code, res.Msg = OutcomeRefused, code, msg
		return res
	}
	// 2. refusals.
	if hello.Proto != drainproto.Proto {
		return refuse(drainproto.RefuseProto, fmt.Sprintf("this hub speaks drain proto %d", drainproto.Proto))
	}
	if _, frozen, _ := s.Store.AlertSetting(ctx, SettingFrozen); frozen {
		return refuse(drainproto.RefuseFrozen, "the hub is frozen for a move")
	}
	// Refuse as disabled only for a target that is gone, disabled or revoked; any other
	// store error is transient and backs off like a failed run.
	cur, err := s.Store.PullTargetByID(ctx, t.ID)
	if errors.Is(err, store.ErrNotFound) {
		return refuse(drainproto.RefuseDisabled, "the pull target no longer exists")
	}
	if err != nil {
		return fail(err)
	}
	if !cur.Enabled {
		return refuse(drainproto.RefuseDisabled, "the pull target is disabled")
	}
	if cur.MachineID != "" && cur.MachineID != hello.Machine {
		return refuse(drainproto.RefuseMachineMismatch, "this target is bound to another machine")
	}
	if cur.MachineID == "" || cur.DeviceID == "" {
		// Trust on the verified host (ssh already checked the pinned key), not on the claim
		// alone: first hello binds the target and makes the pull device. A machine that
		// uploads directly right now is not claimed by a hello: the host could be anyone.
		if direct, err := s.Store.MachineRecentDirect(ctx, hello.Machine, DirectContactWindow); err != nil {
			return fail(err)
		} else if direct {
			return refuse(drainproto.RefuseMachineMismatch, "that machine uploads to the hub directly; it cannot be bound to a pull target (remove its token first, or register the target with --machine)")
		}
		if _, err := s.Store.BindPullTarget(ctx, cur.ID, hello.Machine, hello.Name); err != nil {
			if errors.Is(err, store.ErrPullExists) {
				return refuse(drainproto.RefuseMachineMismatch, "that machine is pulled through another target")
			}
			if errors.Is(err, store.ErrFrozen) {
				return refuse(drainproto.RefuseFrozen, "the hub is frozen for a move")
			}
			return fail(err)
		}
		if cur, err = s.Store.PullTargetByID(ctx, t.ID); err != nil {
			return fail(err)
		}
	}
	dev, err := s.Store.DeviceByID(ctx, cur.DeviceID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fail(err)
	}
	if err != nil || dev.RevokedAt != 0 {
		return refuse(drainproto.RefuseDisabled, "the pull device is revoked")
	}

	// 3. the hello counts as contact.
	var oldestMS int64
	if hello.Oldest != nil {
		if ts, err := time.Parse(time.RFC3339Nano, *hello.Oldest); err == nil {
			oldestMS = ts.UnixMilli()
		}
	}
	// Through the ingester, so the engine hears of it (a machine_silent alert recovers on a
	// hello alone, push.md 3.1) and a freeze committed since the check above still refuses.
	bkHello := hello.Backlog
	if err := s.Ingester.Contact(ctx, dev, ingest.Contact{Backlog: &bkHello, BacklogOldest: oldestMS,
		Reach: ReachPull, SyVersion: hello.Sy, RecordV: hello.RecordV}); err != nil {
		if errors.Is(err, ingest.ErrFrozen) {
			return refuse(drainproto.RefuseFrozen, "the hub is frozen for a move")
		}
		return fail(err)
	}
	if s.hook(StepHello) != nil {
		return fail(errCrash)
	}

	ver := s.HubVersion
	if ver == "" {
		ver = "dev"
	}
	start := drainproto.Start{Proto: drainproto.Proto, Hub: ver, RecordVMax: record.MaxVersion,
		MaxRecords: orMax(s.MaxRecords, drainproto.MaxRecords), MaxBytes: orMax(s.MaxBytes, drainproto.MaxBytes),
		BudgetS: max(1, int(budget/time.Second))}
	if start.BudgetS > int(drainproto.Budget/time.Second) {
		start.BudgetS = int(drainproto.Budget / time.Second)
	}
	if err := w.Write(start); err != nil {
		return fail(err)
	}

	// 4. batches, stop-and-wait.
	deadline := s.now().Add(budget)
	remaining := hello.Backlog
	seq := 0
	allMismatch := false
	for {
		f, err := next(batchT)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fail(errors.New("the remote ended without done"))
			}
			return fail(err)
		}
		switch v := f.(type) {
		case *drainproto.Done:
			_ = s.Store.SetMachineHeld(ctx, hello.Machine, v.Held)
			if v.Reason == drainproto.DoneError {
				return fail(errors.New("the remote reported an error"))
			}
			res.Outcome = OutcomeOK
			res.More = v.Reason == drainproto.DoneBudget
			if allMismatch {
				res.Outcome, res.Code = OutcomeRefused, drainproto.RefuseMachineMismatch
			}
			return res
		case *drainproto.Batch:
			seq++
			if v.Seq != seq {
				return fail(fmt.Errorf("%w: batch seq %d, expected %d", drainproto.ErrProtocol, v.Seq, seq))
			}
			if len(v.Records) > start.MaxRecords || v.RecordBytes() > start.MaxBytes {
				return fail(fmt.Errorf("%w: batch over the limits in start", drainproto.ErrProtocol))
			}
			body := buildBody(hello.Sy, v.Records)
			// Hub rule 3: backlog is the remote's more-adjusted count (this batch included,
			// as X-Sy-Backlog), its age the oldest ts of the batch while more is queued.
			bk := max(remaining, int64(len(v.Records)))
			opt := ingest.Options{Backlog: &bk, More: v.More, Reach: ReachPull}
			if v.More {
				opt.BacklogOldest = oldestTS(v.Records)
			}
			reply, err := s.Ingester.IngestWith(ctx, dev, body, opt)
			if errors.Is(err, ingest.ErrFrozen) {
				// machines.md 8.1: a running drain gets refuse/frozen at its next batch;
				// nothing was stored, so the remote keeps its cursor.
				return refuse(drainproto.RefuseFrozen, "the hub is frozen for a move")
			}
			if err != nil {
				// Nothing was committed (the ingest transaction is atomic): tell the remote
				// to keep its cursor, and end the session so the scheduler backs off.
				_ = w.Write(drainproto.Nack{Seq: v.Seq, Code: drainproto.NackUnavailable, RetryAfterS: 5})
				res.Outcome, res.Err = OutcomeNack, err
				return res
			}
			if s.hook(StepAfterIngest) != nil { // committed, ack not sent
				return fail(errCrash)
			}
			ack := drainproto.Ack{Seq: v.Seq, Accepted: reply.Accepted, Duplicates: reply.Duplicates}
			for _, r := range reply.Rejected {
				ack.Rejected = append(ack.Rejected, drainproto.Rejected{ID: r.ID, Reason: r.Reason, Detail: r.Detail})
			}
			if err := w.Write(ack); err != nil {
				return fail(err)
			}
			if s.hook(StepAfterAck) != nil {
				return fail(errCrash)
			}
			res.Batches++
			res.Records += len(reply.Accepted)
			remaining -= int64(len(v.Records))
			allMismatch = len(reply.Accepted) == 0 && len(reply.Rejected) > 0 && onlyMismatch(reply.Rejected)
			if allMismatch {
				// The remote's state folder changed: it is not the machine this target was
				// bound to. End the session; the target goes to `refused`.
				_ = w.Write(drainproto.Bye{})
				res.Outcome, res.Code = OutcomeRefused, drainproto.RefuseMachineMismatch
				return res
			}
			if res.Batches >= maxBatches || !s.now().Before(deadline) {
				_ = w.Write(drainproto.Bye{})
				res.Outcome, res.More = OutcomeOK, v.More
				// We read no more frames now; let the remote's last writes (a batch sent
				// before it saw the bye, a done) drain to EOF so it never blocks on a full pipe.
				s.drainRemote(ctx, ch)
				return res
			}
		case *drainproto.Busy:
			res.Outcome = OutcomeBusy
			return res
		default:
			return fail(fmt.Errorf("%w: unexpected %s frame", drainproto.ErrProtocol, f.Type()))
		}
	}
}

// byeGrace is how long the hub keeps reading after it sent bye.
const byeGrace = 2 * time.Second

// drainRemote discards frames until the remote closes its stdout, an error, ctx ends or the
// grace runs out.
func (s *Session) drainRemote(ctx context.Context, ch <-chan event) {
	grace := s.ByeGrace
	if grace <= 0 {
		grace = byeGrace
	}
	tm := time.NewTimer(grace)
	defer tm.Stop()
	for {
		select {
		case ev := <-ch:
			if ev.err != nil {
				return
			}
		case <-tm.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// DirectContactWindow is how recent a machine's direct upload must be to refuse binding a
// pull target to it on a hello.
const DirectContactWindow = 10 * time.Minute

// ReachPull is the machine reach the hub's own pull reports (machines.md 3.5 hub rule 2).
const ReachPull = "pull"

// oldestTS is the oldest parsable `ts` of the records, client ms; 0 when none has one.
func oldestTS(recs []json.RawMessage) int64 {
	var best int64
	for _, r := range recs {
		var env struct {
			TS string `json:"ts"`
		}
		if json.Unmarshal(r, &env) != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, env.TS); err == nil && (best == 0 || t.UnixMilli() < best) {
			best = t.UnixMilli()
		}
	}
	return best
}

var errCrash = errors.New("simulated crash")

func (s *Session) hook(step string) error {
	if s.Hook == nil {
		return nil
	}
	return s.Hook(step)
}

func orMax(v, limit int) int {
	if v <= 0 || v > limit {
		return limit
	}
	return v
}

func onlyMismatch(rs []ingest.Rejected) bool {
	for _, r := range rs {
		if r.Reason != ingest.MachineMismatch {
			return false
		}
	}
	return true
}

// buildBody makes the ingest batch for the records, copying every record byte for byte.
func buildBody(syVersion string, recs []json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteString(`{"v":1,"client":`)
	cl, _ := json.Marshal("sy/" + syVersion)
	b.Write(cl)
	b.WriteString(`,"records":[`)
	for i, r := range recs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(r)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}
