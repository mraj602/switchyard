package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/events"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/version"
)

// publish turns a committed batch's changes into stream events (api.yaml StreamSession,
// StreamUsage, StreamMachine). It reads the new rows from the store, after the commit.
func (s *Server) publish(ch ingest.Changes) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := s.now()
	ids := make([]string, 0, len(ch.Sessions))
	for id := range ch.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := ch.Sessions[id]
		v, err := s.store.Session(ctx, id, now)
		if err != nil {
			continue
		}
		var prev *string
		if c.Previous != "" {
			p := c.Previous
			prev = &p
		}
		data, _ := json.Marshal(map[string]any{"session": v, "change": c.Change, "previous_state": prev})
		e := events.Event{Type: "session", Data: data}
		if v.FocusLink != "" {
			link := v.FocusLink
			v.FocusURL = &link
			e.FocusData, _ = json.Marshal(map[string]any{"session": v, "change": c.Change, "previous_state": prev})
			e.FocusMachine = v.FocusTarget
		}
		s.Broker.Publish(e)
	}
	seats := make([]string, 0, len(ch.Seats))
	for id := range ch.Seats {
		seats = append(seats, id)
	}
	sort.Strings(seats)
	for _, id := range seats {
		ws, err := s.store.Windows(ctx, id, nil, time.Time{}, true)
		if err != nil {
			continue
		}
		data, _ := json.Marshal(map[string]any{"seat": id, "windows": windowsOf(ws, now)})
		s.Broker.Publish(events.Event{Type: "usage", Data: data})
	}
	mids := make([]string, 0, len(ch.Machines))
	for id := range ch.Machines {
		mids = append(mids, id)
	}
	if len(mids) > 0 {
		ms, err := s.store.Machines(ctx, mids, now)
		if err == nil {
			for _, m := range ms {
				data, _ := json.Marshal(map[string]any{"machine": m})
				s.Broker.Publish(events.Event{Type: "machine", Data: data})
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, id int64, typ string, data []byte) error {
	var b strings.Builder
	if id > 0 {
		fmt.Fprintf(&b, "id: %d\n", id)
	}
	fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", typ, data)
	_, err := w.Write([]byte(b.String()))
	return err
}

// streamWriteTimeout bounds one write to a stream client.
const streamWriteTimeout = 30 * time.Second

// stream is GET /v1/stream (server-sent events).
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	dev, _ := auth.DeviceFrom(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok {
		auth.WriteError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	var types map[string]bool
	if t := listParam(r, "types"); len(t) > 0 {
		types = map[string]bool{}
		for _, x := range t {
			types[x] = true
		}
	}
	want := func(t string) bool { return types == nil || types[t] }
	// Last-Event-ID is what EventSource sends on reconnect; ?last_event_id= is for the first
	// connect, which cannot set a header (the board passes overview.stream_id so nothing
	// between the overview and the subscription is missed).
	var last int64
	resumeFrom := false
	h0 := r.Header.Get("Last-Event-ID")
	if h0 == "" {
		h0 = r.URL.Query().Get("last_event_id")
	}
	if h0 != "" {
		var err error
		last, err = strconv.ParseInt(strings.TrimSpace(h0), 10, 64)
		resumeFrom = err == nil && last >= 0
	}
	ch, resume, cancel := s.Broker.SubscribeFrom(last, resumeFrom)
	defer cancel()
	kicked := s.streams.add(dev.ID, auth.ViaOf(dev))
	defer s.streams.remove(dev.ID, kicked)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// A stuck client must not hold the goroutine: every write gets a deadline.
	rc := http.NewResponseController(w)
	arm := func() { _ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout)) }
	arm()
	hello, _ := json.Marshal(map[string]any{"version": version.Version, "schema": store.LatestVersion(), "device": dev.ID})
	if writeEvent(w, 0, "hello", hello) != nil {
		return
	}
	if resume.Reset != "" {
		data, _ := json.Marshal(map[string]string{"reason": resume.Reset})
		if writeEvent(w, 0, "reset", data) != nil {
			return
		}
	}
	send := func(e events.Event) error {
		if !want(e.Type) {
			return nil
		}
		arm()
		data := e.Data
		if e.FocusData != nil && dev.Kind == "desktop" && dev.MachineID != "" && dev.MachineID == e.FocusMachine {
			data = e.FocusData
		}
		return writeEvent(w, e.ID, e.Type, data)
	}
	for _, e := range resume.Missed {
		if send(e) != nil {
			return
		}
	}
	flusher.Flush()

	ping := time.NewTicker(s.cfg.PingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-kicked:
			return // the device was revoked
		case e, ok := <-ch:
			if !ok {
				return // too slow; the client reconnects with Last-Event-ID
			}
			if send(e) != nil {
				return
			}
			flusher.Flush()
		case t := <-ping.C:
			if !s.Auth.Alive(r.Context(), dev) {
				return // revoked (or the board session expired): end the stream
			}
			arm()
			data, _ := json.Marshal(map[string]any{"time": store.Time(t.UnixMilli())})
			if !want("ping") {
				data = nil
				_, err := w.Write([]byte(": ping\n\n"))
				if err != nil {
					return
				}
			} else if writeEvent(w, 0, "ping", data) != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// streamSet is the open event streams per device id. Each stream also remembers the device
// its board session was opened from (auth.ViaOf): revoking a device revokes those sessions
// too (store RevokeDeviceCascade), so kicking it ends their streams as well.
type streamSet struct {
	mu sync.Mutex
	m  map[string]map[chan struct{}]string // device -> stream -> via
}

// add registers a stream of the device; the channel closes when the device, or the device
// via it was opened from, is kicked.
func (t *streamSet) add(device, via string) chan struct{} {
	c := make(chan struct{})
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]map[chan struct{}]string{}
	}
	if t.m[device] == nil {
		t.m[device] = map[chan struct{}]string{}
	}
	t.m[device][c] = via
	return c
}

func (t *streamSet) remove(device string, c chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m[device], c)
	if len(t.m[device]) == 0 {
		delete(t.m, device)
	}
}

// kick ends every open stream of the devices and of the sessions opened from them.
func (t *streamSet) kick(devices ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	gone := make(map[string]bool, len(devices))
	for _, d := range devices {
		gone[d] = true
	}
	for d, streams := range t.m {
		for c, via := range streams {
			if gone[d] || (via != "" && gone[via]) {
				close(c)
				delete(streams, c)
			}
		}
		if len(streams) == 0 {
			delete(t.m, d)
		}
	}
}
