package pair

import (
	"sync"
	"time"
)

// Limits of pairing.md 3.3.
const (
	// MaxRequestsPerMinute is the requests to /v1/pair/claim and /v1/session/owner one
	// client IP may make a minute.
	MaxRequestsPerMinute = 20
	// TypedFailsPerIP failed typed-code claims in FailWindow lock that IP out of typed claims.
	TypedFailsPerIP = 5
	// TypedFailsHub failed typed-code claims in FailWindow lock typed claims for everyone. It
	// is a last-resort ceiling, far above what one address can cause (TypedFailsPerIP, then
	// that address alone is locked): reaching it takes TypedFailsHub/TypedFailsPerIP
	// addresses at once. QR secret claims are never blocked by it.
	TypedFailsHub = 100
	// SecretFailsPerIP failed secret claims in FailWindow lock that IP out of secret claims
	// (only to keep logs quiet: a 128-bit secret cannot be guessed).
	SecretFailsPerIP = 20
	// FailWindow is the window failures are counted in; Lockout is how long a lock lasts.
	FailWindow = 10 * time.Minute
	Lockout    = 10 * time.Minute
)

// Limiter is the in-memory rate limiter of pairing claims. A restart clears it, which an
// outside attacker cannot cause. The hub-wide lockout applies to typed codes only: a QR
// secret is never blocked by it, so a lockout cannot keep the owner from pairing.
type Limiter struct {
	now func() time.Time

	mu        sync.Mutex
	reqs      map[string][]time.Time
	typedFail map[string][]time.Time
	typedLock map[string]time.Time
	hubFail   []time.Time
	hubLock   time.Time
	// inflight are typed attempts that passed Allow and are not settled yet. They count as
	// failures until Settle, so concurrent guesses cannot all slip in before the first one
	// is recorded.
	inflight    map[string]int
	hubInflight int
	secFail     map[string][]time.Time
	secLock     map[string]time.Time
	pruned      time.Time
}

// NewLimiter returns a limiter on the given clock (nil = time.Now).
func NewLimiter(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{now: now, reqs: map[string][]time.Time{}, typedFail: map[string][]time.Time{},
		typedLock: map[string]time.Time{}, inflight: map[string]int{}, secFail: map[string][]time.Time{}, secLock: map[string]time.Time{}}
}

func after(ts []time.Time, cut time.Time) []time.Time {
	keep := ts[:0]
	for _, t := range ts {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	return keep
}

// prune drops stale entries, at most once a minute, so the maps stay small.
func (l *Limiter) prune(now time.Time) {
	if now.Sub(l.pruned) < time.Minute {
		return
	}
	l.pruned = now
	for k, ts := range l.reqs {
		if ts = after(ts, now.Add(-time.Minute)); len(ts) == 0 {
			delete(l.reqs, k)
		} else {
			l.reqs[k] = ts
		}
	}
	for _, m := range []map[string][]time.Time{l.typedFail, l.secFail} {
		for k, ts := range m {
			if ts = after(ts, now.Add(-FailWindow)); len(ts) == 0 {
				delete(m, k)
			} else {
				m[k] = ts
			}
		}
	}
	for _, m := range []map[string]time.Time{l.typedLock, l.secLock} {
		for k, until := range m {
			if !until.After(now) {
				delete(m, k)
			}
		}
	}
}

// Allow counts one request from ip and says whether it may proceed. A typed attempt that
// may proceed reserves a failure slot (per IP and hub-wide) until Settle. typed is true for a
// typed-code claim, false for a secret claim or owner sign-in. When it may not, retry is
// how long until it could.
func (l *Limiter) Allow(ip string, typed bool) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	reqs := after(l.reqs[ip], now.Add(-time.Minute))
	if len(reqs) >= MaxRequestsPerMinute {
		l.reqs[ip] = reqs
		return false, max(reqs[0].Add(time.Minute).Sub(now), time.Second)
	}
	var until time.Time
	if typed {
		until = l.typedLock[ip]
		if l.hubLock.After(until) {
			until = l.hubLock
		}
	} else {
		until = l.secLock[ip]
	}
	if until.After(now) {
		l.reqs[ip] = reqs
		return false, until.Sub(now)
	}
	if typed {
		cut := now.Add(-FailWindow)
		if len(after(l.typedFail[ip], cut))+l.inflight[ip] >= TypedFailsPerIP ||
			len(after(l.hubFail, cut))+l.hubInflight >= TypedFailsHub {
			l.reqs[ip] = reqs
			return false, time.Second // attempts in flight may still fail: try again shortly
		}
	}
	l.reqs[ip] = append(reqs, now)
	if typed {
		l.inflight[ip]++
		l.hubInflight++
	}
	return true, 0
}

// Settle ends a typed attempt that Allow let through: its reservation is released and, when
// failed, the failure is recorded (and the lockouts applied). Every typed Allow that
// returns true must be settled exactly once.
func (l *Limiter) Settle(ip string, failed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Releasing the reservation and recording the failure happen under one lock hold:
	// otherwise a concurrent Allow could see neither and admit a guess past the limits.
	if l.inflight[ip] > 1 {
		l.inflight[ip]--
	} else {
		delete(l.inflight, ip)
	}
	if l.hubInflight > 0 {
		l.hubInflight--
	}
	if failed {
		l.failLocked(ip, true)
	}
}

// Fail records a failed claim from ip and applies the lockouts.
func (l *Limiter) Fail(ip string, typed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failLocked(ip, typed)
}

func (l *Limiter) failLocked(ip string, typed bool) {
	now := l.now()
	cut := now.Add(-FailWindow)
	if typed {
		f := append(after(l.typedFail[ip], cut), now)
		if len(f) >= TypedFailsPerIP {
			l.typedLock[ip] = now.Add(Lockout)
			f = nil
		}
		l.typedFail[ip] = f
		l.hubFail = append(after(l.hubFail, cut), now)
		if len(l.hubFail) >= TypedFailsHub {
			l.hubLock = now.Add(Lockout)
			l.hubFail = nil
		}
		return
	}
	f := append(after(l.secFail[ip], cut), now)
	if len(f) >= SecretFailsPerIP {
		l.secLock[ip] = now.Add(Lockout)
		f = nil
	}
	l.secFail[ip] = f
}
