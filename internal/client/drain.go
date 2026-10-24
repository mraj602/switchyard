package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/mraj602/watchyard/internal/drainproto"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/version"
)

// Steps passed to DrainOptions.Hook, for failure injection in tests: a non-nil return
// simulates a crash right there (Drain returns it at once and does nothing more).
const (
	StepBatchSent   = "batch_sent"   // the batch frame was written, no answer read yet
	StepAckReceived = "ack_received" // an ack was read and validated, nothing applied
	StepBeforeSave  = "before_save"  // rejected logged, spills removed, cursor.json not yet written
	StepAfterSave   = "after_save"   // cursor.json written
)

// DrainOptions configures one drain session (machines.md 3.5, the remote rules).
type DrainOptions struct {
	Outbox  *outbox.Outbox
	Machine string // machine id
	Name    string // machine name

	// WyVersion, OS, Arch default to this binary's.
	WyVersion, OS, Arch string
	// RecordV is the highest record v this wy writes; default record MaxVersion (1).
	RecordV int

	// CapBytes and KeepDays are outbox.max_mb and outbox.keep_days: a pulled machine never
	// runs `wy flush` against a hub, so the end of a successful session (flush.lock still
	// held) does the housekeeping the flusher does. 0 skips each.
	CapBytes int64
	KeepDays int

	// StartTimeout and AckTimeout default to the drainproto values.
	StartTimeout, AckTimeout time.Duration

	Now  func() time.Time
	Logf func(format string, args ...any)
	// Hook is a test seam, see the Step constants.
	Hook func(step string) error
}

// DrainResult says how a drain session ended.
type DrainResult struct {
	Busy        bool   // flush.lock was taken: a local flush runs (busy frame sent)
	Refused     string // the hub's refuse code
	Reason      string // the done reason sent, "" when no done was sent
	Batches     int    // batches acknowledged and applied
	Sent        int    // records answered
	Bye         bool   // the hub ended the session on its budget (bye): a clean end, not a failure
	Err         error  // non-nil: how it failed
	ProtocolErr bool   // Err is a protocol error (exit 3); other endings exit 0
}

type drainEvent struct {
	f   drainproto.Frame
	err error
}

// Drain runs the remote end of `wy outbox drain --stdio`: frames go to out, the hub's frames
// come from in. It takes flush.lock (a busy lock sends `busy`), says hello, reads batches
// from the cursor with outbox.ReadV, sends one at a time (stop-and-wait) and applies each
// ack with outbox.Apply, the function `wy flush` uses. The cursor moves only on an ack that
// carries the outstanding seq and names only ids of that batch; a nack, bye, timeout, EOF or
// protocol error stops without applying anything more.
func Drain(ctx context.Context, in io.Reader, out io.Writer, o DrainOptions) DrainResult {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.WyVersion == "" {
		o.WyVersion = version.Version
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.RecordV == 0 {
		o.RecordV = 1
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = drainproto.StartTimeout
	}
	if o.AckTimeout <= 0 {
		o.AckTimeout = drainproto.AckTimeout
	}
	d := &drainer{DrainOptions: o, w: drainproto.NewWriter(out)}
	lock, ok, err := o.Outbox.TryFlushLock()
	if err != nil {
		d.res.Err = err
		return d.res
	}
	if !ok {
		d.res.Busy = true
		d.res.Err = d.w.Write(drainproto.Busy{})
		return d.res
	}
	defer lock.Release()

	done := make(chan struct{})
	defer close(done)
	ch := make(chan drainEvent)
	rd := drainproto.NewReader(in)
	go func() {
		for {
			f, err := rd.Read()
			select {
			case ch <- drainEvent{f, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	d.ch = ch
	d.run(ctx)
	if !d.crashed {
		if d.res.Err == nil && d.success {
			d.housekeeping()
		}
		d.writeStatus()
	}
	return d.res
}

// housekeeping is the end-of-session cap and prune, under flush.lock (Drain holds it).
func (d *drainer) housekeeping() {
	if d.CapBytes > 0 {
		if r, err := d.Outbox.EnforceCap(d.CapBytes); err != nil {
			d.Logf("size cap: %v", err)
		} else if len(r.Dropped) > 0 {
			d.Logf("outbox over its cap: dropped %v", r.Dropped)
		}
	}
	if d.KeepDays > 0 {
		if _, err := d.Outbox.Prune(d.KeepDays); err != nil {
			d.Logf("prune: %v", err)
		}
	}
}

// byeEnd records the hub's bye. The hub sends it only after it acked a batch and its budget
// (30 s or 20 batches) ran out, so with a batch applied it is the clean end of the session;
// a bye before that is the hub giving up.
func (d *drainer) byeEnd() {
	if d.res.Batches > 0 {
		d.res.Bye = true
		return
	}
	d.res.Err = errors.New("hub ended the session")
}

// pendingBye reports whether the hub's bye (its budget used up) is already waiting, without
// blocking. The hub stops reading after it sends ack+bye, so the next batch must not go.
func (d *drainer) pendingBye() bool {
	select {
	case ev := <-d.ch:
		switch {
		case ev.err != nil:
			d.fail(ev.err)
		case ev.f.Type() == drainproto.TBye:
			d.byeEnd()
		default:
			d.fail(fmt.Errorf("%w: unexpected %s frame", drainproto.ErrProtocol, ev.f.Type()))
		}
		return true
	default:
		return false
	}
}

type drainer struct {
	DrainOptions
	w  *drainproto.Writer
	ch chan drainEvent

	res     DrainResult
	cur     outbox.Cursor
	crashed bool // a Hook simulated a crash: write nothing more

	contact, success bool
	refused          string
	held, heldNeeds  int
	hubVersion       string
	vmax             int
}

func (d *drainer) hook(step string) bool {
	if d.Hook == nil {
		return false
	}
	if err := d.Hook(step); err != nil {
		d.crashed = true
		d.res.Err = err
		return true
	}
	return false
}

// next waits for the next hub frame for up to timeout.
func (d *drainer) next(ctx context.Context, timeout time.Duration) (drainproto.Frame, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case ev := <-d.ch:
		return ev.f, ev.err
	case <-t.C:
		return nil, errTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var errTimeout = errors.New("timed out waiting for the hub")

// fail records how the session ended: EOF, nack, bye and timeouts exit 0; protocol errors 3.
func (d *drainer) fail(err error) {
	d.res.Err = err
	d.res.ProtocolErr = errors.Is(err, drainproto.ErrProtocol)
}

func (d *drainer) run(ctx context.Context) {
	d.cur = d.Outbox.ReadCursor()
	n, oldest, err := d.Outbox.Backlog(d.cur)
	if err != nil {
		d.res.Err = err
		return
	}
	hello := drainproto.Hello{Proto: drainproto.Proto, Machine: d.Machine, Name: d.Name, Wy: d.WyVersion,
		OS: d.OS, Arch: d.Arch, RecordV: d.RecordV, Backlog: n}
	if oldest != "" {
		hello.Oldest = &oldest
	}
	if err := d.w.Write(hello); err != nil {
		d.res.Err = err
		return
	}
	f, err := d.next(ctx, d.StartTimeout)
	if err != nil {
		d.fail(err)
		return
	}
	var start *drainproto.Start
	switch v := f.(type) {
	case *drainproto.Refuse:
		d.refused = v.Code
		d.res.Refused = v.Code
		d.res.Err = fmt.Errorf("hub refused the pull: %s: %s", v.Code, v.Message)
		return
	case *drainproto.Start:
		start = v
	case *drainproto.Bye:
		d.res.Err = errors.New("hub ended the session")
		return
	default:
		d.fail(fmt.Errorf("%w: expected start or refuse, got %s", drainproto.ErrProtocol, f.Type()))
		return
	}
	if start.Proto != drainproto.Proto {
		d.fail(fmt.Errorf("%w: start proto %d", drainproto.ErrProtocol, start.Proto))
		return
	}
	d.contact = true
	d.hubVersion, d.vmax = start.Hub, start.RecordVMax
	deadline := d.Now().Add(time.Duration(start.BudgetS) * time.Second)

	seq := 0
	for {
		if err := ctx.Err(); err != nil {
			d.res.Err = err
			return
		}
		if d.pendingBye() {
			return
		}
		b, err := d.Outbox.ReadV(d.cur, start.MaxRecords, start.MaxBytes, start.RecordVMax)
		if err != nil {
			d.res.Err = err
			return
		}
		d.held, d.heldNeeds = b.Held, b.HeldNeeds
		if len(b.Items) == 0 {
			if b.Next.File != d.cur.File || b.Next.Offset != d.cur.Offset {
				d.cur.File, d.cur.Offset = b.Next.File, b.Next.Offset
				if err := d.save(); err != nil {
					d.res.Err = err
					return
				}
			}
			reason := drainproto.DoneDrained
			if b.Held > 0 {
				reason = drainproto.DoneHeld
			} else {
				d.success = true
			}
			d.sendDone(reason, b)
			return
		}
		seq++
		ids := make(map[string]bool, len(b.Items))
		batch := drainproto.Batch{Seq: seq, More: b.More}
		for _, it := range b.Items {
			ids[it.ID] = true
			batch.Records = append(batch.Records, it.Raw)
		}
		if err := d.w.Write(batch); err != nil {
			d.res.Err = err
			return
		}
		if d.hook(StepBatchSent) {
			return
		}
		f, err := d.next(ctx, d.AckTimeout)
		if err != nil {
			d.fail(err)
			return
		}
		switch v := f.(type) {
		case *drainproto.Ack:
			if err := checkAck(v, seq, ids); err != nil {
				d.fail(err)
				return
			}
			if d.hook(StepAckReceived) {
				return
			}
			if d.apply(b, v) {
				return
			}
		case *drainproto.Nack:
			d.res.Err = fmt.Errorf("hub could not store the batch (%s)", v.Code)
			return
		case *drainproto.Refuse:
			// A hub frozen for a move refuses at the next batch (machines.md 8.1): nothing
			// of the batch was stored, so nothing is applied, as for a nack.
			d.refused = v.Code
			d.res.Refused = v.Code
			d.res.Err = fmt.Errorf("hub refused the pull: %s: %s", v.Code, v.Message)
			return
		case *drainproto.Bye:
			// The hub's budget ended the session while this batch was in flight: it stopped
			// reading, so the batch was not stored and nothing is applied. Not a failure.
			d.byeEnd()
			return
		default:
			d.fail(fmt.Errorf("%w: expected ack, got %s", drainproto.ErrProtocol, f.Type()))
			return
		}
		if !d.Now().Before(deadline) {
			d.sendDone(drainproto.DoneBudget, b)
			return
		}
	}
}

// checkAck is the validation of machines.md 3.5 remote rule 4: the ack carries the
// outstanding seq and names only ids of the batch.
func checkAck(a *drainproto.Ack, seq int, ids map[string]bool) error {
	if a.Seq != seq {
		return fmt.Errorf("%w: ack seq %d, outstanding %d", drainproto.ErrProtocol, a.Seq, seq)
	}
	for _, id := range a.IDs() {
		if !ids[id] {
			return fmt.Errorf("%w: ack names %q, which is not in the batch", drainproto.ErrProtocol, id)
		}
	}
	return nil
}

// apply applies a validated ack. It returns true when the session must end.
func (d *drainer) apply(b *outbox.Batch, a *drainproto.Ack) bool {
	rej := make([]outbox.Rejected, len(a.Rejected))
	for i, r := range a.Rejected {
		rej[i] = outbox.Rejected{ID: r.ID, Reason: r.Reason, Detail: r.Detail}
	}
	ap := d.Outbox.Apply(&d.cur, b, outbox.Answer{Accepted: a.Accepted, Duplicates: a.Duplicates, Rejected: rej}, d.Logf)
	if d.hook(StepBeforeSave) {
		return true
	}
	if err := d.save(); err != nil {
		d.res.Err = err
		return true
	}
	if d.hook(StepAfterSave) {
		return true
	}
	d.res.Batches++
	d.res.Sent += len(b.Items) - ap.Missing
	d.contact, d.success = true, true
	if ap.Missing > 0 {
		d.Logf("hub ack left out %d of %d ids", ap.Missing, len(b.Items))
		d.res.Err = fmt.Errorf("hub ack left out %d of %d records", ap.Missing, len(b.Items))
		d.sendDone(drainproto.DoneError, b)
		return true
	}
	return false
}

func (d *drainer) save() error {
	c := d.cur
	c.Updated = ""
	return d.Outbox.WriteCursor(c)
}

func (d *drainer) sendDone(reason string, b *outbox.Batch) {
	backlog, _, _ := d.Outbox.Backlog(d.cur)
	d.res.Reason = reason
	if err := d.w.Write(drainproto.Done{Reason: reason, Backlog: backlog, Held: int64(b.Held)}); err != nil && d.res.Err == nil {
		d.res.Err = err
	}
}

func (d *drainer) writeStatus() {
	prev := ReadStatus(d.Outbox.Dir)
	now := d.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	st := Status{Time: now, TokenFP: prev.TokenFP, LastSuccess: prev.LastSuccess, LastContact: prev.LastContact, Via: ViaPull}
	if d.contact {
		st.LastContact = now
	}
	if d.success {
		st.LastSuccess = now
	}
	st.OK = d.res.Err == nil
	if d.res.Err != nil {
		st.Error = d.res.Err.Error()
	}
	st.PullRefused = d.refused
	if d.held > 0 {
		st.Held = d.held
		st.HubTooOld = &HubTooOld{Hub: d.hubVersion, RecordVMax: d.vmax, Needs: d.heldNeeds}
	}
	writeStatusFile(d.Outbox.Dir, st)
}
