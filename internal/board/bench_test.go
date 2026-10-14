package board

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bigOutbox writes about size bytes of outbox: token_rows lines (most of a real outbox,
// skipped without decoding) and hook events of many sessions.
func bigOutbox(tb testing.TB, size int) string {
	tb.Helper()
	dir := tb.TempDir()
	var buf bytes.Buffer
	pad := bytes.Repeat([]byte("x"), 1800)
	n := 0
	for buf.Len() < size {
		n++
		// one session per 6 hook events; ids run to a few thousand
		sess := fmt.Sprintf("sess-%05d", (n/12)%3000)
		sec := n % 80000
		ts := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second).Format("2006-01-02T15:04:05.000Z")
		switch n % 12 {
		case 0:
			fmt.Fprintf(&buf, `{"v":1,"id":"01JA2XRB3T0M2N4P6Q8R%07d","kind":"token_rows","ts":"%s","machine":"m_1","slot":"~/.claude","payload":{"rows":"%s"}}`+"\n", n, ts, pad)
		case 1, 2, 3, 7, 8, 9, 10:
			fmt.Fprintf(&buf, `{"v":1,"id":"01JA2XRB3T0M2N4P6Q8R%07d","kind":"token_rows","ts":"%s","machine":"m_1","slot":"~/.claude","payload":{"rows":"%s"}}`+"\n", n, ts, pad[:600])
		case 4, 11:
			fmt.Fprintf(&buf, `{"v":1,"id":"01JA2XRB3T0M2N4P6Q8R%07d","kind":"hook_event","ts":"%s","machine":"m_1","slot":"~/.claude","seat":{"account":"a1","org":"o"},"derived":{"last_message":"Should I go on?"},"payload":{"hook_event_name":"Stop","session_id":"%s","cwd":"/home/u/p%d"}}`+"\n", n, ts, sess, n%50)
		case 5:
			fmt.Fprintf(&buf, `{"v":1,"id":"01JA2XRB3T0M2N4P6Q8R%07d","kind":"hook_event","ts":"%s","machine":"m_1","slot":"~/.claude","seat":{"account":"a1","org":"o"},"payload":{"hook_event_name":"UserPromptSubmit","session_id":"%s","cwd":"/home/u/p%d"}}`+"\n", n, ts, sess, n%50)
		case 6:
			fmt.Fprintf(&buf, `{"v":1,"id":"01JA2XRB3T0M2N4P6Q8R%07d","kind":"limit_sample","ts":"%s","machine":"m_1","slot":"~/.claude","derived":{"five_hour_pct":40,"five_hour_resets_at":"2026-10-12T15:00:00.000Z","context_pct":30},"payload":{"session_id":"%s"}}`+"\n", n, ts, sess)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-10-12.jsonl"), buf.Bytes(), 0o644); err != nil {
		tb.Fatal(err)
	}
	return dir
}

func BenchmarkLocalFold20MiB(b *testing.B) {
	dir := bigOutbox(b, 20<<20)
	lo := LocalOptions{Options: Options{Now: fxNow, Loc: time.UTC, Machine: Machine{ID: "m_1"}}, Env: fakeEnv(b), OutboxDir: dir}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, err := BuildLocal(lo)
		if err != nil {
			b.Fatal(err)
		}
		if len(v.Rows) == 0 {
			b.Fatal("no rows")
		}
	}
}
