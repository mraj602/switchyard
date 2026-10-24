package client

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/outbox"
)

// HubFile is <state>/hub.json: the last known hub version and record_v_max (machines.md 7.2;
// paths.md). It is written by `wy connect`, by every ingest reply that carries `hub` and by a
// /health check (RefreshHub, run by `wy tick` every 6 hours), and read by the flusher.
const HubFile = "hub.json"

// BacklogHeader carries the number of records still queued locally (outbox.md 8).
// BacklogOldestHeader carries the ts (RFC 3339) of the oldest unanswered record, sent with
// it when more than one batch is queued. Legacy compat: each is also sent under its name from
// before the rename, which is all a hub older than the rename reads (rename.md 2.12).
const (
	BacklogHeader             = "X-Wy-Backlog"
	BacklogOldestHeader       = "X-Wy-Backlog-Oldest"
	LegacyBacklogHeader       = "X-Sy-Backlog"        // rename:keep
	LegacyBacklogOldestHeader = "X-Sy-Backlog-Oldest" // rename:keep
)

// HubCache is the content of hub.json.
type HubCache struct {
	URL        string `json:"url"`
	Version    string `json:"version,omitempty"`
	RecordVMax int    `json:"record_v_max"`
	At         string `json:"at"`
	// Frozen is /health's `frozen` at the last check: the hub moved (machines.md 8.3). A 200
	// ingest reply clears it (a frozen hub answers 503); `wy tick` re-reads /health every
	// FrozenRecheck while it is set, so an unfreeze is seen too. `wy doctor` shows it.
	Frozen *HealthFrozen `json:"frozen,omitempty"`
}

// HubRefreshEvery is how often `wy tick` re-reads /health into hub.json (machines.md 7.2);
// FrozenRecheck is how often while the hub is (or looks) frozen.
const (
	HubRefreshEvery = 6 * time.Hour
	FrozenRecheck   = 10 * time.Minute
)

// HubRefreshDue reports whether `wy tick` should call RefreshHub for hub url: no cache for
// it, older than HubRefreshEvery, or older than FrozenRecheck while the cache says frozen or
// the last flush was refused by a frozen hub.
func HubRefreshDue(stateDir, url string, now time.Time) bool {
	c, ok := ReadHubCache(stateDir)
	if !ok || !sameHub(c.URL, url) {
		return true
	}
	at, err := time.Parse(time.RFC3339, c.At)
	if err != nil || at.After(now) {
		return true
	}
	age := now.Sub(at)
	if age >= HubRefreshEvery {
		return true
	}
	if age < FrozenRecheck {
		return false
	}
	if c.Frozen != nil {
		return true
	}
	st := ReadStatus(filepath.Join(stateDir, "outbox"))
	return !st.OK && strings.Contains(st.Error, "frozen")
}

// VMax is the highest record v the hub reads, DefaultRecordVMax when the cache does not say.
func (c HubCache) VMax() int { return (&HubInfo{RecordVMax: c.RecordVMax}).VMax() }

// ReadHubCache loads <stateDir>/hub.json. ok is false when it is missing or unreadable.
func ReadHubCache(stateDir string) (HubCache, bool) {
	var c HubCache
	b, err := os.ReadFile(filepath.Join(stateDir, HubFile))
	if err != nil || json.Unmarshal(b, &c) != nil {
		return HubCache{}, false
	}
	return c, true
}

// WriteHubCache stores hub.json atomically (mode 0600). The state folder must exist.
func WriteHubCache(stateDir string, c HubCache) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(stateDir, HubFile), append(b, '\n'), 0o600)
}

// RefreshHub asks GET /health what the hub reads and caches it in <stateDir>/hub.json
// (`wy connect` and the 6-hourly `wy tick` call this). It returns the health body; the
// caller may count a nil error as contact with the hub.
func RefreshHub(ctx context.Context, c *Client, stateDir string, now time.Time) (Health, error) {
	h, err := c.Health(ctx)
	if err != nil {
		return h, err
	}
	err = WriteHubCache(stateDir, HubCache{URL: strings.TrimRight(c.BaseURL, "/"), Version: h.Version,
		RecordVMax: h.VMax(), At: now.UTC().Format(time.RFC3339), Frozen: h.Frozen})
	return h, err
}

func sameHub(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}

// backlogOldest adds the backlog-oldest headers to the flusher's ingest requests. Client.Ingest
// has no option for it, so the flusher sends through a copy of the client whose transport
// stamps the header (the value is set before each request; requests are sequential).
type backlogOldest struct {
	mu sync.Mutex
	ts string
}

func (b *backlogOldest) get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ts
}

// set records the oldest ts of the batch when more than one batch is queued, else clears it.
func (b *backlogOldest) set(batch *outbox.Batch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ts = ""
	if !batch.More {
		return
	}
	var best time.Time
	for _, it := range batch.Items {
		t, err := time.Parse(time.RFC3339Nano, it.TS)
		if err != nil {
			continue
		}
		if b.ts == "" || t.Before(best) {
			best, b.ts = t, it.TS
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// wrap returns a copy of c that stamps the header on every request it makes.
func (b *backlogOldest) wrap(c *Client) *Client {
	hc := *c.http()
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if v := b.get(); v != "" && r.URL.Path == "/v1/ingest" {
			r = r.Clone(r.Context())
			r.Header.Set(BacklogOldestHeader, v)
			r.Header.Set(LegacyBacklogOldestHeader, v)
		}
		return base.RoundTrip(r)
	})
	nc := *c
	nc.HTTP = &hc
	return &nc
}
