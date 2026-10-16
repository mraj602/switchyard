// Package events is the hub's in-process broadcaster for /v1/stream: every change the
// ingest makes is published here once, kept in a ring buffer (10 minutes or 5000 events,
// api.yaml) for Last-Event-ID resume, and fanned out to subscribers.
//
// Event ids are hub-wide and increasing across restarts: the first id of a run is the
// start time in milliseconds times 1000, so an id from an earlier run is always lower than
// any id of this one and can be told apart (reset reason hub_restarted).
//
// Phase 2. Depends on: nothing inside the module.
package events

import (
	"sync"
	"time"
)

// Event is one server-sent event.
type Event struct {
	ID   int64
	Type string // hello, reset, ping, session, usage, machine, alert, seats
	Data []byte // one JSON object
	// Focus, when set, is an alternative Data that includes focus_url, for desktop devices
	// on FocusMachine only (record.md 3.3).
	FocusData    []byte
	FocusMachine string
	At           time.Time
}

// Defaults from api.yaml.
const (
	BufferAge    = 10 * time.Minute
	BufferEvents = 5000
)

// Broker fans events out to subscribers and keeps recent ones.
type Broker struct {
	mu     sync.Mutex
	first  int64 // first id of this run
	next   int64
	buf    []Event // oldest first
	subs   map[chan Event]struct{}
	now    func() time.Time
	maxAge time.Duration
	maxLen int
}

// New makes a broker. now may be nil.
func New(now func() time.Time) *Broker {
	if now == nil {
		now = time.Now
	}
	first := now().UnixMilli() * 1000
	return &Broker{first: first, next: first, subs: map[chan Event]struct{}{}, now: now, maxAge: BufferAge, maxLen: BufferEvents}
}

// LastID is the id of the newest published event (first-1 if none yet).
func (b *Broker) LastID() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.next - 1
}

// Publish assigns an id, buffers and delivers the event. Slow subscribers whose queue is
// full are dropped (their channel is closed); the client reconnects and resumes.
func (b *Broker) Publish(e Event) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	e.ID = b.next
	b.next++
	e.At = b.now()
	b.buf = append(b.buf, e)
	b.trim()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			delete(b.subs, ch)
			close(ch)
		}
	}
	return e
}

func (b *Broker) trim() {
	cut := b.now().Add(-b.maxAge)
	i := 0
	for i < len(b.buf) && (len(b.buf)-i > b.maxLen || b.buf[i].At.Before(cut)) {
		i++
	}
	if i > 0 {
		b.buf = append([]Event(nil), b.buf[i:]...)
	}
}

// Resume says what a reconnecting client gets.
type Resume struct {
	Missed []Event
	Reset  string // "", "buffer_expired" or "hub_restarted"
}

// Subscribe registers a subscriber. With lastID > 0 the events after it are returned in
// Missed when still buffered; otherwise Reset says why the client must refetch. The
// returned cancel function must be called when the client goes away.
func (b *Broker) Subscribe(lastID int64) (<-chan Event, Resume, func()) {
	return b.SubscribeFrom(lastID, lastID > 0)
}

// SubscribeFrom is Subscribe where resume says lastID is meaningful even when 0 (a client
// that saw stream_id 0: the hub had published nothing yet, so every buffered event is new).
func (b *Broker) SubscribeFrom(lastID int64, resume bool) (<-chan Event, Resume, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trim()
	var r Resume
	if resume {
		switch {
		case lastID < b.first-1 || lastID >= b.next:
			r.Reset = "hub_restarted"
		case lastID == b.next-1:
			// up to date
		case len(b.buf) == 0 || b.buf[0].ID > lastID+1:
			r.Reset = "buffer_expired"
		default:
			for _, e := range b.buf {
				if e.ID > lastID {
					r.Missed = append(r.Missed, e)
				}
			}
		}
	}
	ch := make(chan Event, 256)
	b.subs[ch] = struct{}{}
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
	return ch, r, cancel
}

// Subscribers is the number of connected subscribers.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
