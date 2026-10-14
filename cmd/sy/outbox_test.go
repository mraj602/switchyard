package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOutboxAppendAndStat(t *testing.T) {
	dir := t.TempDir() + "/ob"
	in := `{"kind":"web_usage","machine":"m_x","payload":{"a":1}}
{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T9B4CD","kind":"hook_event","ts":"2026-10-11T14:03:22.418Z","machine":"m_x","extra":true,"payload":{}}
`
	var out, errb bytes.Buffer
	if err := runOutboxIn(strings.NewReader(in), []string{"append", "--dir", dir}, &out, &errb); err != nil {
		t.Fatalf("append: %v: %s", err, errb.String())
	}
	lines := strings.Fields(out.String())
	if len(lines) != 2 || lines[1] != "01JA2XQ8R7Y5V0Z6M3K1T9B4CD" || len(lines[0]) != 26 {
		t.Fatalf("ids = %q", out.String())
	}
	out.Reset()
	if err := runOutboxIn(nil, []string{"stat", "--dir", dir}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 day, 0 spill") || !strings.Contains(out.String(), "oldest unflushed:") {
		t.Fatalf("stat:\n%s", out.String())
	}
	// A record without a kind is rejected, the rest still written, exit status non-nil.
	out.Reset()
	err := runOutboxIn(strings.NewReader(`{"payload":{}}`+"\n"+`{"kind":"x_y","machine":"m","payload":{}}`), []string{"append", "--dir", dir}, &out, &errb)
	if err == nil || len(strings.Fields(out.String())) != 1 {
		t.Fatalf("err=%v out=%q", err, out.String())
	}
}
