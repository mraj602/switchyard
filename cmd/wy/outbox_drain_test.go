package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/drainproto"
)

const drainRec = `{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T00001","kind":"hook_event","ts":"2026-10-12T10:00:00.000Z","machine":"m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K","payload":{}}`

func drainEnv(t *testing.T) (state, outboxDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WY_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	state = filepath.Join(home, "state")
	t.Setenv("WY_STATE_DIR", state)
	outboxDir = filepath.Join(state, "outbox")
	if err := os.MkdirAll(outboxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outboxDir, "2026-10-12.jsonl"), []byte(drainRec+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return state, outboxDir
}

func TestOutboxDrainPeekChangesNothing(t *testing.T) {
	_, dir := drainEnv(t)
	var out, errb bytes.Buffer
	if err := runOutboxIn(nil, []string{"drain", "--peek", "--dir", dir}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	if out.String() != drainRec+"\n" {
		t.Fatalf("peek: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "cursor.json")); err == nil {
		t.Fatal("peek wrote a cursor")
	}
	if _, err := os.Stat(filepath.Join(dir, "flush.status.json")); err == nil {
		t.Fatal("peek wrote a status")
	}
}

func TestOutboxDrainRequiresExactlyOneMode(t *testing.T) {
	drainEnv(t)
	for _, args := range [][]string{{"drain"}, {"drain", "--stdio", "--peek"}, {"drain", "extra"}} {
		var out, errb bytes.Buffer
		if err := runOutboxIn(nil, args, &out, &errb); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestOutboxDrainUnderForcedCommandAcceptsOnlyStdio(t *testing.T) {
	_, dir := drainEnv(t)
	t.Setenv("SSH_ORIGINAL_COMMAND", "sh -c 'cat /etc/passwd'")
	for _, args := range [][]string{
		{"drain", "--stdio", "--dir", dir},
		{"drain", "--peek"},
		{"drain", "--stdio", "extra"},
		{"drain"},
	} {
		var out, errb bytes.Buffer
		err := runOutboxIn(strings.NewReader(""), args, &out, &errb)
		var ee *exitError
		if err == nil || !errors.As(err, &ee) || ee.code != 3 {
			t.Errorf("%v: %v", args, err)
		}
		if out.Len() != 0 {
			t.Errorf("%v wrote to stdout: %q", args, out.String())
		}
	}
	// --stdio alone is fine (and ignores the original command): the hub refuses, we exit 0.
	pr, pw := io.Pipe()
	var out, errb bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runOutboxIn(pr, []string{"drain", "--stdio"}, &out, &errb) }()
	_ = pw.Close() // the hub vanished
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EOF from the hub must exit 0: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("drain did not return")
	}
	f, err := drainproto.NewReader(&out).Read()
	if err != nil {
		t.Fatalf("first frame: %v (%q)", err, out.String())
	}
	if h, ok := f.(*drainproto.Hello); !ok || h.Backlog != 1 || h.Proto != 1 || !strings.HasPrefix(h.Machine, "m_") {
		t.Fatalf("hello %+v", f)
	}
}

func TestOutboxDrainStdioProtocolEndToEnd(t *testing.T) {
	_, dir := drainEnv(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var errb bytes.Buffer
	done := make(chan error, 1)
	go func() {
		err := runOutboxIn(inR, []string{"drain", "--stdio", "--dir", dir}, outW, &errb)
		outW.Close()
		done <- err
	}()
	rd := drainproto.NewReader(outR)
	wr := drainproto.NewWriter(inW)
	f, err := rd.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.(*drainproto.Hello); !ok {
		t.Fatalf("%T", f)
	}
	_ = wr.Write(drainproto.Start{Proto: 1, Hub: "x", RecordVMax: 1, MaxRecords: 10, MaxBytes: 1 << 20, BudgetS: 30})
	f, _ = rd.Read()
	b, ok := f.(*drainproto.Batch)
	if !ok || len(b.Records) != 1 || string(b.Records[0]) != drainRec {
		t.Fatalf("%T %+v", f, f)
	}
	_ = wr.Write(drainproto.Ack{Seq: 1, Accepted: []string{"01JA2XQ8R7Y5V0Z6M3K1T00001"}})
	f, _ = rd.Read()
	if d, ok := f.(*drainproto.Done); !ok || d.Reason != "drained" {
		t.Fatalf("%T %+v", f, f)
	}
	if err := <-done; err != nil {
		t.Fatal(err, errb.String())
	}
	if c, _ := os.ReadFile(filepath.Join(dir, "cursor.json")); !strings.Contains(string(c), `"offset":`) {
		t.Fatalf("cursor %q", c)
	}
	// A protocol error from the hub exits 3.
	inR2, inW2 := io.Pipe()
	var out2 bytes.Buffer
	go func() { _, _ = inW2.Write([]byte("{\"t\":\"nonsense\"}\n")) }()
	err = runOutboxIn(inR2, []string{"drain", "--stdio", "--dir", dir}, &out2, &errb)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 3 {
		t.Fatalf("protocol error exit: %v", err)
	}
}
