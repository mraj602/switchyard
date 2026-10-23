package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestFailedLoginsAreLimitedPerAddress(t *testing.T) {
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
	// Many addresses failing together must not lock out an address that has not failed.
	for i := range 200 {
		for range maxFailsPerIP {
			a.fail(fmt.Sprintf("10.0.%d.%d", i/250, i%250))
		}
	}
	if a.tooManyFails("9.9.9.9") {
		t.Error("failures from other addresses blocked a clean address")
	}
}
