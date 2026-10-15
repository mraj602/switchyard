package localtime

import (
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	old := Zone
	Zone = ist
	t.Cleanup(func() { Zone = old })

	at := time.Date(2026, 10, 12, 8, 32, 7, 123456789, time.UTC)
	for _, c := range []struct{ got, want string }{
		{Format(at), "2026-10-12 14:02 IST"},
		{Format(time.Time{}), "-"},
		{MS(at.UnixMilli()), "2026-10-12 14:02 IST"},
		{MS(0), "-"},
		{String("2026-10-12T08:32:07Z"), "2026-10-12 14:02 IST"},
		{String("2026-10-12T08:32:07.123456789Z"), "2026-10-12 14:02 IST"},
		{String("2026-10-12T14:02:07+05:30"), "2026-10-12 14:02 IST"},
		{String(""), "-"},
		{String("soon"), "soon"},
		{Ptr(nil), "-"},
		{StringIn("2026-10-12T08:32:07Z", time.UTC), "2026-10-12 08:32 UTC"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
