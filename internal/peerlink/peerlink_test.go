package peerlink

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/link"
	"github.com/mraj602/switchyard/internal/peers"
)

const stopped = "t_0123456789abcdef"

// resumeSetup is a machine whose threads.json holds a thread the loop guard stopped.
func resumeSetup(t *testing.T, hubMayWiden bool) (*Remote, *bytes.Buffer, func() peers.Thread) {
	t.Helper()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	env := peers.Env{State: t.TempDir(), Now: func() time.Time { return now }}
	paused := now.Add(-time.Minute)
	ts := &peers.ThreadSet{V: 1, Threads: []peers.Thread{{ID: stopped, A: "sess-a", B: "sess-b", OpenedAt: now.Add(-time.Hour),
		LastAt: paused, HumanAt: now.Add(-time.Hour), Turns: 30, PausedAt: &paused, Remote: "sess-b"}}}
	if err := ts.Save(env.ThreadsPath()); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	r := New(env, log.New(logs, "", 0))
	r.HubMayWiden = hubMayWiden
	get := func() peers.Thread {
		t.Helper()
		th := peers.LoadThreads(env.ThreadsPath()).Find(stopped)
		if th == nil {
			t.Fatal("thread gone")
		}
		return *th
	}
	return r, logs, get
}

func resumeFrame(thread string) []byte {
	b, _ := json.Marshal(peers.ThreadResumeBody{V: 1, Thread: thread, By: "dev_owner", At: "2026-10-15T12:00:00Z"})
	return b
}

func auditStatuses(t *testing.T, r *Remote) []string {
	t.Helper()
	b, _ := os.ReadFile(r.Env.AuditPath())
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var v map[string]any
		if json.Unmarshal([]byte(l), &v) == nil {
			out = append(out, v["thread"].(string)+" "+v["status"].(string))
		}
	}
	return out
}

// With hub_may_widen on, a relayed resume resets the thread as a human prompt would: turns 0,
// the pause cleared, human_at unchanged; the next message passes the loop guard.
func TestThreadResumeTaken(t *testing.T) {
	r, _, get := resumeSetup(t, true)
	before := get()
	if err := r.Queued(context.Background(), link.TypePeerThreadResume, 7, resumeFrame(stopped)); err != nil {
		t.Fatal(err)
	}
	th := get()
	if th.Turns != 0 || th.PausedAt != nil || !th.HumanAt.Equal(before.HumanAt) {
		t.Fatalf("thread after resume: %+v", th)
	}
	if peers.LoopGuard(&th, time.Time{}, 30, r.now()) {
		t.Fatal("the loop guard still refuses after the resume")
	}
	if got := auditStatuses(t, r); len(got) != 1 || got[0] != stopped+" "+peers.ResumeResumed {
		t.Fatalf("audit %v", got)
	}
}

// With hub_may_widen off, the hub's signature is not enough: the resume is refused, logged and
// audited, and the thread keeps its turns and its pause.
func TestThreadResumeRefusedWithoutHubMayWiden(t *testing.T) {
	r, logs, get := resumeSetup(t, false)
	if err := r.Queued(context.Background(), link.TypePeerThreadResume, 7, resumeFrame(stopped)); err != nil {
		t.Fatal(err) // refused is final: the frame is acked
	}
	th := get()
	if th.Turns != 30 || th.PausedAt == nil {
		t.Fatalf("a refused resume changed the thread: %+v", th)
	}
	if !peers.LoopGuard(&th, time.Time{}, 30, r.now()) {
		t.Fatal("the loop guard lets the thread go on")
	}
	if !strings.Contains(logs.String(), "hub_may_widen is false") {
		t.Fatalf("log %q", logs.String())
	}
	if got := auditStatuses(t, r); len(got) != 1 || got[0] != stopped+" "+peers.ResumeRefused {
		t.Fatalf("audit %v", got)
	}
}

// The zero Remote refuses: a wiring that forgets the setting never widens.
func TestThreadResumeZeroValueRefuses(t *testing.T) {
	r, _, get := resumeSetup(t, true)
	r2 := New(r.Env, nil)
	if err := r2.Queued(context.Background(), link.TypePeerThreadResume, 1, resumeFrame(stopped)); err != nil {
		t.Fatal(err)
	}
	if th := get(); th.Turns != 30 || th.PausedAt == nil {
		t.Fatalf("the zero value took the resume: %+v", th)
	}
}

// An unknown thread, a closed one and a malformed body change nothing and are acked.
func TestThreadResumeNothingToDo(t *testing.T) {
	r, _, get := resumeSetup(t, true)
	for _, body := range [][]byte{resumeFrame("t_ffffffffffffffff"), resumeFrame("../threads"), []byte("{"), []byte(`{"v":1}`)} {
		if err := r.Queued(context.Background(), link.TypePeerThreadResume, 1, body); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
	}
	if th := get(); th.Turns != 30 || th.PausedAt == nil {
		t.Fatalf("thread changed: %+v", th)
	}
	ts := peers.LoadThreads(r.Env.ThreadsPath())
	c := r.now()
	ts.Threads[0].ClosedAt = &c
	if err := ts.Save(r.Env.ThreadsPath()); err != nil {
		t.Fatal(err)
	}
	if err := r.Queued(context.Background(), link.TypePeerThreadResume, 2, resumeFrame(stopped)); err != nil {
		t.Fatal(err)
	}
	if th := get(); th.Turns != 30 || th.PausedAt == nil {
		t.Fatalf("a closed thread was resumed: %+v", th)
	}
}
