package expo

import (
	"context"
	"io"
	"log"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/clock"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// Receipt timing of push.md 12.4.
const (
	ReceiptsEvery    = 5 * time.Minute
	ReceiptsAfter    = 15 * time.Minute // Expo has the receipt about 15 minutes after the send
	ReceiptsKeep     = 24 * time.Hour   // Expo clears receipts after 24 hours
	ReceiptsPerAsk   = 1000             // ids per getReceipts request
	receiptsPerPoll  = 10 * ReceiptsPerAsk
	receiptsPollTime = 2 * time.Minute
)

// ReceiptsOptions configure a Receipts poller. Store and Sender are required.
type ReceiptsOptions struct {
	Store  *store.Store
	Sender *Sender
	// Clock is the time source; nil is clock.Real{}.
	Clock clock.Clock
	// Every is the poll interval; 0 is ReceiptsEvery.
	Every time.Duration
	// Log gets one line per poll that read receipts (counts only). Nil discards.
	Log *log.Logger
}

// Receipts polls Expo's push receipts (push.md 12.4): one goroutine, started by `sy hub
// serve` and stopped on shutdown.
type Receipts struct {
	o ReceiptsOptions

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
}

// ReceiptStats counts what one poll did.
type ReceiptStats struct {
	Asked, OK, Gone, Errors, Expired, Missing int
}

// NewReceipts builds a poller.
func NewReceipts(o ReceiptsOptions) *Receipts {
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Every <= 0 {
		o.Every = ReceiptsEvery
	}
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	return &Receipts{o: o, stop: make(chan struct{}), done: make(chan struct{})}
}

// Start runs the poll loop until Stop. The first poll is one interval after the start.
func (r *Receipts) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return
	}
	r.started = true
	go r.loop()
}

// Stop ends the loop and waits for a running poll to finish.
func (r *Receipts) Stop() {
	r.mu.Lock()
	started := r.started
	r.started = false
	r.mu.Unlock()
	if !started {
		return
	}
	close(r.stop)
	<-r.done
}

func (r *Receipts) loop() {
	defer close(r.done)
	t := r.o.Clock.NewTimer(r.o.Every)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C():
		}
		ctx, cancel := context.WithTimeout(context.Background(), receiptsPollTime)
		go func() { // a Stop cancels a poll in progress
			select {
			case <-r.stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		if _, err := r.Poll(ctx); err != nil {
			r.o.Log.Printf("expo receipts: %v", err)
		}
		cancel()
		t.Reset(r.o.Every)
	}
}

// Poll runs one pass (push.md 12.4): tickets sent at least 15 minutes ago are looked up,
// up to 1000 per request; ok closes the ticket, DeviceNotRegistered makes the target gone,
// any other error goes to the target's last_error; a ticket older than 24 hours is closed
// without asking (Expo no longer has it). A ticket missing from a fresher answer is left
// open for the next poll. A failed request ends the pass; what was read is kept.
func (r *Receipts) Poll(ctx context.Context) (ReceiptStats, error) {
	var st ReceiptStats
	now := r.o.Clock.Now()
	nowMS := now.UnixMilli()
	open, err := r.o.Store.ExpoOpenTickets(ctx, now.Add(-ReceiptsAfter).UnixMilli(), receiptsPerPoll)
	if err != nil {
		return st, err
	}
	var marks []store.ExpoReceipt
	var ask []store.ExpoTicket
	for _, t := range open {
		if t.SentAt <= now.Add(-ReceiptsKeep).UnixMilli() {
			marks = append(marks, store.ExpoReceipt{DeliveryID: t.DeliveryID, TargetID: t.TargetID})
			st.Expired++
			continue
		}
		ask = append(ask, t)
	}
	var askErr error
	for len(ask) > 0 && askErr == nil {
		chunk := ask[:min(len(ask), ReceiptsPerAsk)]
		ask = ask[len(chunk):]
		ids := make([]string, len(chunk))
		for i, t := range chunk {
			ids[i] = t.Ticket
		}
		got, err := r.o.Sender.Receipts(ctx, ids)
		if err != nil {
			askErr = err
			break
		}
		st.Asked += len(chunk)
		for _, t := range chunk {
			rc, ok := got[t.Ticket]
			switch {
			case !ok:
				st.Missing++
				continue
			case rc.Status == "ok":
				st.OK++
				marks = append(marks, store.ExpoReceipt{DeliveryID: t.DeliveryID, TargetID: t.TargetID})
			default:
				code := rc.ErrorCode()
				if code == "" {
					code = "unknown"
				}
				if code == store.ReceiptDeviceNotRegistered {
					st.Gone++
				} else {
					st.Errors++
					if code != "unknown" {
						code = ErrorText(code) // the send path's texts, so the doctor's hints match
					}
				}
				marks = append(marks, store.ExpoReceipt{DeliveryID: t.DeliveryID, TargetID: t.TargetID, Error: code})
			}
		}
	}
	if err := r.o.Store.MarkReceipts(context.WithoutCancel(ctx), marks, nowMS); err != nil {
		return st, err
	}
	if len(open) > 0 {
		r.o.Log.Printf("expo receipts: %d open, %d asked, %d ok, %d gone, %d errors, %d expired, %d not ready",
			len(open), st.Asked, st.OK, st.Gone, st.Errors, st.Expired, st.Missing)
	}
	if askErr != nil {
		return st, askErr
	}
	return st, nil
}
