package peers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// Rates of peers.md 9.2, as enforced by the sender's side. (The per-hub daily limit is the
// hub's.)
const (
	SessionPerMinute = 10
	SessionBurst     = 20
	PairPerHour      = 60
)

// RateState is what the rate checks remember between `wy peers send` runs.
type RateState struct {
	V        int                    `json:"v"`
	Sessions map[string]SessionRate `json:"sessions"`
	Pairs    map[string][]int64     `json:"pairs"` // pair key -> send times, unix ms, last hour
}

// SessionRate is one token bucket.
type SessionRate struct {
	Tokens float64 `json:"tokens"`
	AtMS   int64   `json:"at_ms"`
}

// NewRateState is an empty state.
func NewRateState() *RateState {
	return &RateState{V: 1, Sessions: map[string]SessionRate{}, Pairs: map[string][]int64{}}
}

// PairKey names an unordered-by-direction session pair as peers.md 9.2 counts it: the two
// ids sorted, so a reply and a start share one budget.
func PairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// CheckRate is the pure rate check: it takes one token from the sending session's bucket
// (10 a minute, burst 20) and one slot from the pair's hour (60 an hour). When either is
// empty it changes nothing and returns how long until a retry could succeed.
func CheckRate(st *RateState, now time.Time, session, pair string) (retryAfter time.Duration, ok bool) {
	if st.Sessions == nil {
		st.Sessions = map[string]SessionRate{}
	}
	if st.Pairs == nil {
		st.Pairs = map[string][]int64{}
	}
	nowMS := now.UnixMilli()
	const refillPerMS = float64(SessionPerMinute) / 60000
	b, seen := st.Sessions[session]
	if !seen {
		b = SessionRate{Tokens: SessionBurst, AtMS: nowMS}
	}
	if d := nowMS - b.AtMS; d > 0 {
		b.Tokens += float64(d) * refillPerMS
	}
	if b.Tokens > SessionBurst {
		b.Tokens = SessionBurst
	}
	b.AtMS = nowMS

	hourAgo := nowMS - time.Hour.Milliseconds()
	var recent []int64
	for _, t := range st.Pairs[pair] {
		if t > hourAgo {
			recent = append(recent, t)
		}
	}
	switch {
	case b.Tokens < 1:
		wait := time.Duration((1 - b.Tokens) / refillPerMS * float64(time.Millisecond))
		return wait.Round(time.Second) + time.Second, false
	case len(recent) >= PairPerHour:
		return time.Duration(recent[0]-hourAgo)*time.Millisecond + time.Second, false
	}
	b.Tokens--
	st.Sessions[session] = b
	st.Pairs[pair] = append(recent, nowMS)
	return 0, true
}

// Prune drops idle buckets and old pair entries so the file stays small.
func (st *RateState) Prune(now time.Time) {
	nowMS := now.UnixMilli()
	for k, b := range st.Sessions {
		if nowMS-b.AtMS > time.Hour.Milliseconds() {
			delete(st.Sessions, k)
		}
	}
	for k, v := range st.Pairs {
		var keep []int64
		for _, t := range v {
			if t > nowMS-time.Hour.Milliseconds() {
				keep = append(keep, t)
			}
		}
		if len(keep) == 0 {
			delete(st.Pairs, k)
		} else {
			st.Pairs[k] = keep
		}
	}
}

// LoadRates reads the state file; a missing or unreadable file is an empty state (a lost
// budget only means a fresh one, never a refusal).
func LoadRates(path string) *RateState {
	st := NewRateState()
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, st) != nil {
		return NewRateState()
	}
	return st
}

// Save writes the state atomically (0600).
func (st *RateState) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}
