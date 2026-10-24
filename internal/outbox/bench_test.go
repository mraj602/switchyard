package outbox

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

// BenchmarkAppend measures one Append with fsync on and a typical Stop-event sized record
// (about 1.5 KiB). It reports the p50/p95/p99 latency per append; the budget is p95 under
// 5 ms on an SSD (outbox.md section 3). Run it with TMPDIR on a real disk, since many
// systems mount /tmp as tmpfs, where fsync is free:
//
//	TMPDIR=$HOME/.cache go test -run '^$' -bench Append ./internal/outbox
func BenchmarkAppend(b *testing.B) {
	o := New(b.TempDir())
	r := record.New("hook_event", record.Meta{Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", Slot: "~/.claude-2", Client: "wy/0.1.0"})
	r.Payload = json.RawMessage(`{"hook_event_name":"Stop","pad":"` + strings.Repeat("x", 1400) + `"}`)
	lat := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.ID = record.NewID(time.Now())
		t := time.Now()
		if _, err := o.Append(r); err != nil {
			b.Fatal(err)
		}
		lat = append(lat, time.Since(t))
	}
	b.StopTimer()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) float64 {
		return float64(lat[int(float64(len(lat)-1)*p)]) / float64(time.Millisecond)
	}
	b.ReportMetric(pct(0.50), "p50-ms")
	b.ReportMetric(pct(0.95), "p95-ms")
	b.ReportMetric(pct(0.99), "p99-ms")
}
