package events

import (
	"testing"
	"time"
)

func TestResumeAndReset(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	b := New(func() time.Time { return now })
	ch, r, cancel := b.Subscribe(0)
	defer cancel()
	if r.Reset != "" || len(r.Missed) != 0 {
		t.Fatalf("fresh subscribe: %+v", r)
	}
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, b.Publish(Event{Type: "session", Data: []byte(`{}`)}).ID)
	}
	if ids[0] <= now.UnixMilli() || ids[1] != ids[0]+1 || ids[2] != ids[1]+1 {
		t.Fatalf("ids %v", ids)
	}
	for i := range ids {
		select {
		case e := <-ch:
			if e.ID != ids[i] {
				t.Fatalf("got %d want %d", e.ID, ids[i])
			}
		case <-time.After(time.Second):
			t.Fatal("not delivered")
		}
	}
	_, r, c2 := b.Subscribe(ids[0])
	c2()
	if r.Reset != "" || len(r.Missed) != 2 || r.Missed[0].ID != ids[1] {
		t.Errorf("resume: %+v", r)
	}
	_, r, c3 := b.Subscribe(ids[2])
	c3()
	if r.Reset != "" || len(r.Missed) != 0 {
		t.Errorf("up to date: %+v", r)
	}
	_, r, c4 := b.Subscribe(1)
	c4()
	if r.Reset != "hub_restarted" {
		t.Errorf("old id: %+v", r)
	}
	// A restarted hub issues higher ids than the one before it.
	b2 := New(func() time.Time { return now.Add(time.Second) })
	if id := b2.Publish(Event{Type: "session"}).ID; id <= ids[2] {
		t.Errorf("restart id %d not above %d", id, ids[2])
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	b := New(time.Now)
	ch, _, cancel := b.Subscribe(0)
	defer cancel()
	for i := 0; i < 1000; i++ {
		b.Publish(Event{Type: "session"})
	}
	n := 0
	for range ch {
		n++
	}
	if n == 0 || n >= 1000 || b.Subscribers() != 0 {
		t.Errorf("slow subscriber got %d events, %d subscribers left", n, b.Subscribers())
	}
}
