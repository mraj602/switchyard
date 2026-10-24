// Package directpush is direct ntfy from `wy` (machines.md 5): when the hub cannot be reached,
// this machine pushes its own sessions' needs_permission / asked / failed episodes straight to
// an ntfy topic, then tells the hub with a direct_push record so the hub does not push again.
//
// It is a client package: from the hub side it imports only the pure state fold
// (internal/hub/state, through internal/status), per ARCHITECTURE.md. The rule pick and the
// quiet-hours check mirror push/rules; tests check they agree.
package directpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/client"
)

// Triggers a machine computes itself (machines.md 5.1).
const (
	TriggerNeedsPermission = "needs_permission"
	TriggerAsked           = "asked"
	TriggerFailed          = "failed"
)

// Seat is a login as the machine knows it.
type Seat struct {
	Account string `json:"account"`
	Org     string `json:"org"`
}

// Rule is one api.yaml DirectPolicyRule.
type Rule struct {
	Trigger     string   `json:"trigger"`
	Enabled     bool     `json:"enabled"`
	DelayS      int      `json:"delay_s"`
	BypassQuiet bool     `json:"bypass_quiet"`
	Seats       []Seat   `json:"seats"`
	Projects    []string `json:"projects"`
}

// QuietHours is api.yaml QuietHours: Start > End spans midnight; Start == End is off;
// Critical nil means bypass_quiet rules pass.
type QuietHours struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start"`
	End      string `json:"end"`
	TZ       string `json:"tz,omitempty"`
	Critical *bool  `json:"critical,omitempty"`
}

// Settings is DirectPolicy.settings.
type Settings struct {
	QuietHours QuietHours `json:"quiet_hours"`
	SkipRemote bool       `json:"skip_remote"`
	// SkipPeers is push.settings.skip_peers (push.md 2); absent (a policy from a hub that
	// predates it, or no policy) means the hub default, true.
	SkipPeers *bool `json:"skip_peers,omitempty"`
	MaxAgeS   int   `json:"max_age_s"`
}

// SkipsPeers is SkipPeers with its default.
func (s Settings) SkipsPeers() bool { return s.SkipPeers == nil || *s.SkipPeers }

// Policy is api.yaml DirectPolicy, as cached in <state>/push/policy.json. FetchedAt is
// added by the cache (RFC 3339).
type Policy struct {
	V         int      `json:"v"`
	Hub       string   `json:"hub,omitempty"`
	PublicURL *string  `json:"public_url"`
	Machine   string   `json:"machine"`
	Settings  Settings `json:"settings"`
	Rules     []Rule   `json:"rules"`
	FetchedAt string   `json:"fetched_at,omitempty"`
}

// DefaultMaxAgeS is the hub's stale guard default (push.md 3.3).
const DefaultMaxAgeS = 900

// PolicyEvery is how often `wy tick` refreshes the cached policy (machines.md 5.3).
const PolicyEvery = time.Hour

// DefaultPolicy is what applies with no policy (machines.md 5.3): push.md 3.1's base rules,
// no quiet hours, max_age_s 900.
func DefaultPolicy() Policy {
	return Policy{V: 1, Settings: Settings{MaxAgeS: DefaultMaxAgeS}, Rules: []Rule{
		{Trigger: TriggerNeedsPermission, Enabled: true, DelayS: 30, Seats: []Seat{}, Projects: []string{}},
		{Trigger: TriggerAsked, Enabled: true, DelayS: 60, Seats: []Seat{}, Projects: []string{}},
		{Trigger: TriggerFailed, Enabled: true, DelayS: 0, BypassQuiet: true, Seats: []Seat{}, Projects: []string{}},
	}}
}

// First is the client's pick (machines.md 5.3): the first rule of the trigger whose seats
// and projects lists are empty or contain the session's seat and project.
func (p Policy) First(trigger string, seat Seat, project string) (Rule, bool) {
	if i := p.firstIndex(trigger, seat, project); i >= 0 {
		return p.Rules[i], true
	}
	return Rule{}, false
}

func (p Policy) firstIndex(trigger string, seat Seat, project string) int {
	for i, r := range p.Rules {
		if r.Trigger != trigger {
			continue
		}
		if len(r.Seats) > 0 && !slices.Contains(r.Seats, seat) {
			continue
		}
		if len(r.Projects) > 0 && !slices.Contains(r.Projects, project) {
			continue
		}
		return i
	}
	return -1
}

// Validate checks a policy read from the hub or a file.
func (p Policy) Validate() error {
	if p.V != 1 {
		return fmt.Errorf("direct-push policy: v %d, want 1", p.V)
	}
	for _, r := range p.Rules {
		switch r.Trigger {
		case TriggerNeedsPermission, TriggerAsked, TriggerFailed:
		default:
			return fmt.Errorf("direct-push policy: bad trigger %q", r.Trigger)
		}
		if r.DelayS < 0 {
			return fmt.Errorf("direct-push policy: negative delay_s")
		}
	}
	if p.Settings.MaxAgeS < 0 {
		return fmt.Errorf("direct-push policy: negative max_age_s")
	}
	return nil
}

func (p Policy) maxAge() time.Duration {
	if p.Settings.MaxAgeS <= 0 {
		return DefaultMaxAgeS * time.Second
	}
	return time.Duration(p.Settings.MaxAgeS) * time.Second
}

func (p Policy) publicURL() string {
	if p.PublicURL == nil {
		return ""
	}
	return strings.TrimRight(*p.PublicURL, "/")
}

// PushDir is <state>/push.
func PushDir(stateDir string) string { return filepath.Join(stateDir, "push") }

// PolicyPath is <state>/push/policy.json.
func PolicyPath(stateDir string) string { return filepath.Join(PushDir(stateDir), "policy.json") }

// LoadPolicy reads the cached policy. ok is false when there is none (or it does not
// parse): the result is then DefaultPolicy.
func LoadPolicy(stateDir string) (p Policy, ok bool) {
	b, err := os.ReadFile(PolicyPath(stateDir))
	if err != nil || json.Unmarshal(b, &p) != nil || p.Validate() != nil {
		return DefaultPolicy(), false
	}
	return p, true
}

// SavePolicy writes the cache (0600, atomic), stamping FetchedAt with at.
func SavePolicy(stateDir string, p Policy, at time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	p.FetchedAt = at.UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(PolicyPath(stateDir), append(b, '\n'))
}

// FetchPolicy is GET /v1/push/direct-policy with the machine's token.
func FetchPolicy(ctx context.Context, cl *client.Client) (Policy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cl.BaseURL, "/")+"/v1/push/direct-policy", nil)
	if err != nil {
		return Policy{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cl.Token)
	resp, err := cl.HTTPClient().Do(req)
	if err != nil {
		return Policy{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Policy{}, fmt.Errorf("direct-push policy: hub answered %d", resp.StatusCode)
	}
	var p Policy
	if err := json.Unmarshal(body, &p); err != nil {
		return Policy{}, fmt.Errorf("direct-push policy: %w", err)
	}
	return p, p.Validate()
}

// RefreshPolicy fetches and caches the policy when the cache is older than PolicyEvery (or
// force). fetched reports whether it asked the hub.
func RefreshPolicy(ctx context.Context, stateDir string, cl *client.Client, now time.Time, force bool) (p Policy, fetched bool, err error) {
	cur, ok := LoadPolicy(stateDir)
	if ok && !force {
		if at, err := time.Parse(time.RFC3339, cur.FetchedAt); err == nil && now.Sub(at) < PolicyEvery && !at.After(now) {
			return cur, false, nil
		}
	}
	p, err = FetchPolicy(ctx, cl)
	if err != nil {
		return cur, true, err
	}
	if err := SavePolicy(stateDir, p, now); err != nil {
		return cur, true, err
	}
	p.FetchedAt = now.UTC().Format(time.RFC3339)
	return p, true, nil
}

// Secret is <state>/push/direct.json: the ntfy topic (the secret on ntfy.sh) and an
// optional access token. It never goes into config.toml or argv.
type Secret struct {
	Topic string `json:"topic"`
	Token string `json:"token,omitempty"`
}

// SecretPath is <state>/push/direct.json.
func SecretPath(stateDir string) string { return filepath.Join(PushDir(stateDir), "direct.json") }

// ErrNoSecret: direct push has no topic yet (`wy direct-push on --secret-stdin`).
var ErrNoSecret = errors.New("direct push has no ntfy topic (run `wy direct-push on --secret-stdin`)")

// LoadSecret reads direct.json.
func LoadSecret(stateDir string) (Secret, error) {
	b, err := os.ReadFile(SecretPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return Secret{}, ErrNoSecret
	} else if err != nil {
		return Secret{}, err
	}
	var s Secret
	if err := json.Unmarshal(b, &s); err != nil {
		return Secret{}, fmt.Errorf("%s: %w", SecretPath(stateDir), err)
	}
	if err := s.Validate(); err != nil {
		return Secret{}, err
	}
	return s, nil
}

// Validate checks the topic (ntfy: 1-64 of A-Z a-z 0-9 _ -) and the token's form.
func (s Secret) Validate() error {
	if n := len(s.Topic); n < 1 || n > 64 {
		return errors.New("direct push: the topic must be 1 to 64 characters")
	}
	for _, c := range s.Topic {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return errors.New("direct push: the topic may hold only letters, digits, _ and -")
		}
	}
	if s.Token != "" && (strings.ContainsAny(s.Token, " \t\r\n") || len(s.Token) > 200) {
		return errors.New("direct push: bad token")
	}
	return nil
}

// SaveSecret writes direct.json with mode 0600.
func SaveSecret(stateDir string, s Secret) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return writeAtomic(SecretPath(stateDir), append(b, '\n'))
}

// writeAtomic writes data to path via a temp file and rename, mode 0600, folder 0700.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(0o600); err != nil && !isWindows() {
		f.Close()
		os.Remove(tmp)
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
