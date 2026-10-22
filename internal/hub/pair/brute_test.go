package pair

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Concurrent typed guesses must not outrun the failure counters.
// 20 addresses x 20 simultaneous wrong guesses: at most 5 per IP and 20 hub-wide may reach
// the database.
func TestConcurrentTypedGuessesRespectLockouts(t *testing.T) {
	f := newFixture(t)
	c := f.code("phone")
	var invalid, limited atomic.Int32
	perIP := make([]atomic.Int32, 20)
	var wg sync.WaitGroup
	start := make(chan struct{})
	n := 0
	for ip := 0; ip < 20; ip++ {
		for k := 0; k < 20; k++ {
			n++
			guess := fmt.Sprintf("%06d", (n*7919)%1000000)
			if guess == c.Digits {
				guess = "000000"
			}
			wg.Add(1)
			go func(ip int, g string) {
				defer wg.Done()
				<-start
				_, err := f.svc.Claim(context.Background(), fmt.Sprintf("10.0.0.%d", ip), ClaimRequest{Code: g})
				var le *LimitError
				switch {
				case errors.Is(err, ErrInvalid):
					invalid.Add(1)
					perIP[ip].Add(1)
				case errors.As(err, &le):
					limited.Add(1)
				default:
					t.Errorf("unexpected: %v", err)
				}
			}(ip, guess)
		}
	}
	close(start)
	wg.Wait()
	if got := invalid.Load(); got > TypedFailsHub {
		t.Errorf("%d guesses reached the database, hub-wide limit is %d", got, TypedFailsHub)
	}
	for i := range perIP {
		if got := perIP[i].Load(); got > TypedFailsPerIP {
			t.Errorf("ip %d: %d guesses reached the database, limit is %d", i, got, TypedFailsPerIP)
		}
	}
	if invalid.Load()+limited.Load() != 400 {
		t.Errorf("invalid %d + limited %d != 400", invalid.Load(), limited.Load())
	}
	// The real code still works for a secret claim (the hub lockout is typed-only).
	if _, err := f.svc.Claim(context.Background(), "10.9.9.9", ClaimRequest{Secret: c.Secret}); err != nil {
		t.Errorf("secret claim after the flood: %v", err)
	}
}

// A successful typed claim releases its reservation: a legitimate user is never locked out
// by their own good attempts.
func TestSettleReleasesReservation(t *testing.T) {
	l := NewLimiter(nil)
	for i := 0; i < 50; i++ {
		if ok, _ := l.Allow("1.1.1.1", true); !ok {
			t.Fatalf("attempt %d blocked", i)
		}
		l.Settle("1.1.1.1", false)
		l.reqs["1.1.1.1"] = nil // keep the request-rate limit out of this test
	}
	for i := 0; i < TypedFailsPerIP; i++ { // reservations alone fill the slots
		if ok, _ := l.Allow("2.2.2.2", true); !ok {
			t.Fatalf("reservation %d blocked", i)
		}
	}
	if ok, _ := l.Allow("2.2.2.2", true); ok {
		t.Fatal("a sixth in-flight guess was let through")
	}
}
