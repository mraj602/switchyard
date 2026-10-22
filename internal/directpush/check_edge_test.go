package directpush

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/outbox"
)

// A record that entered the state sits in a spill file the hub then answers: Apply deletes
// the file, and the episode key must not move to a later record.
func TestEpisodeKeyStableAfterAnsweredSpillDeleted(t *testing.T) {
	r := newRig(t)
	r.ask(sid1, 0)
	day := filepath.Join(r.o.OutboxDir, "2026-10-12.jsonl")
	b, err := os.ReadFile(day)
	if err != nil {
		t.Fatal(err)
	}
	spill := filepath.Join(r.o.OutboxDir, "spill-1-AAAA.jsonl")
	if err := os.WriteFile(spill, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(day); err != nil {
		t.Fatal(err)
	}
	r.ask(sid1, time.Minute) // a later record of the same state, in the day file
	before, err := Scan(r.o.OutboxDir, r.o.MachineID, t0.Add(2*time.Minute))
	if err != nil || len(before) != 1 {
		t.Fatalf("%v %v", before, err)
	}

	cur := r.ob.ReadCursor()
	batch, err := r.ob.Read(cur, 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var ans outbox.Answer
	for _, it := range batch.Items {
		ans.Accepted = append(ans.Accepted, it.ID)
	}
	r.ob.Apply(&cur, batch, ans, nil)
	if err := r.ob.WriteCursor(cur); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spill); err == nil {
		t.Fatal("answered spill file not deleted")
	}

	after, err := Scan(r.o.OutboxDir, r.o.MachineID, t0.Add(2*time.Minute))
	if err != nil || len(after) != 1 {
		t.Fatalf("%v %v", after, err)
	}
	if after[0].Key != before[0].Key {
		t.Fatalf("episode key moved: %s -> %s", before[0].Key, after[0].Key)
	}
}

func TestDirectPushNotRepeatedAfterSpillAnswered(t *testing.T) {
	r := newRig(t)
	r.ask(sid1, 0)
	day := filepath.Join(r.o.OutboxDir, "2026-10-12.jsonl")
	b, _ := os.ReadFile(day)
	spill := filepath.Join(r.o.OutboxDir, "spill-1-AAAA.jsonl")
	os.WriteFile(spill, b, 0o600)
	os.Remove(day)
	r.ask(sid1, time.Minute)
	r.now = t0.Add(10 * time.Minute) // past the quiet period
	if res := r.check(); len(res) != 1 || res[0].Outcome != OutcomeSent {
		t.Fatalf("first check: %+v", res)
	}
	cur := r.ob.ReadCursor()
	batch, _ := r.ob.Read(cur, 100, 1<<20)
	var ans outbox.Answer
	for _, it := range batch.Items {
		ans.Accepted = append(ans.Accepted, it.ID)
	}
	r.ob.Apply(&cur, batch, ans, nil)
	r.ob.WriteCursor(cur)
	r.now = t0.Add(20 * time.Minute)
	r.check()
	if n := len(r.ntfy.msgs()); n != 1 {
		t.Fatalf("pushed %d times", n)
	}
}

func TestPlainHTTPPublicServerRefused(t *testing.T) {
	r := newRig(t)
	r.o.Config.Push.Direct.Server = "http://ntfy.example.com"
	r.ask(sid1, 0)
	r.now = t0.Add(10 * time.Minute)
	if _, err := Check(context.Background(), r.o); err == nil || !strings.Contains(err.Error(), "http is allowed only") {
		t.Fatalf("Check: %v", err)
	}
	if _, err := SendTest(context.Background(), r.o); err == nil {
		t.Fatal("SendTest sent over public http")
	}
	if res := Send(context.Background(), nil, "http://ntfy.example.com", "tk", TestMessage("t", "m")); res.OK || res.Error == "" {
		t.Fatalf("Send: %+v", res)
	}
}

func TestConfigRejectsPlainHTTPDirectServer(t *testing.T) {
	f := &config.File{}
	_ = f
	if err := config.CheckDirectServer("http://ntfy.example.com"); err == nil {
		t.Fatal("public http accepted")
	}
	if err := config.CheckDirectServer("http://10.0.0.5:8080"); err != nil {
		t.Fatal(err)
	}
}
