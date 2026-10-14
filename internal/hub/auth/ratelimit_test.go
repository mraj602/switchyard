package auth

import (
	"testing"
	"time"
)

func TestFailedLoginsAreLimitedPerAddressAndHubWide(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	a := &Authenticator{Now: func() time.Time { return now }, touched: map[string]time.Time{}}
	for range maxFailsPerIP {
		a.fail("1.1.1.1")
	}
	if !a.tooManyFails("1.1.1.1") {
		t.Error("one address may fail only a few times a minute")
	}
	if a.tooManyFails("2.2.2.2") {
		t.Error("another address was blocked by the first one's failures")
	}
	now = now.Add(61 * time.Second)
	if a.tooManyFails("1.1.1.1") {
		t.Error("the per-address bucket did not empty after a minute")
	}
	// Many addresses together still hit the hub-wide limit.
	for i := range maxFailsTotal {
		a.fail("10.0.0." + string(rune('a'+i)))
	}
	if !a.tooManyFails("9.9.9.9") {
		t.Error("hub-wide limit not applied")
	}
}
