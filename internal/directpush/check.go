package directpush

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

// Timing of machines.md 5.4.
const (
	// Grace lets a hub that is only slow win (condition 2).
	Grace = 15 * time.Second
	// FloodMax pushes per FloodPer (condition 8, push.md's flood default).
	FloodMax = 6
	FloodPer = 10 * time.Minute
	// MaxTries: a failed POST is retried by later checks, at least RetryAfter apart.
	MaxTries   = 3
	RetryAfter = time.Minute
	// MarkerKeep: markers older than this are deleted by Check.
	MarkerKeep = 7 * 24 * time.Hour
)

// Options is one machine's direct-push setup.
type Options struct {
	StateDir  string // <state>
	OutboxDir string // <state>/outbox
	MachineID string
	Machine   string // machine name, for the text
	Config    config.Config
	Client    string // record client, e.g. "wy/0.5.0"
	Now       func() time.Time
	HTTP      *http.Client // ntfy client; nil = HTTPClient()
	Logf      func(format string, args ...any)
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// After is condition 4's threshold: push.direct.after_s, 180 s in pull mode when it is
// left at the push default (machines.md 5.4).
func (o Options) After() time.Duration {
	s := o.Config.Push.Direct.AfterS
	if s <= 0 || (o.Config.Hub.Reach == config.ReachPull && s == config.DefaultDirectAfterS) {
		if o.Config.Hub.Reach == config.ReachPull {
			s = config.DefaultDirectAfterPullS
		} else {
			s = config.DefaultDirectAfterS
		}
	}
	return time.Duration(s) * time.Second
}

// LastContact is flush.status.json's last_contact (or last_success, which implies contact,
// for a flusher that does not write last_contact); zero when there is none.
func LastContact(outboxDir string) time.Time {
	var st struct {
		LastContact string `json:"last_contact"`
		LastSuccess string `json:"last_success"`
	}
	b, err := os.ReadFile(filepath.Join(outboxDir, "flush.status.json"))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return time.Time{}
	}
	var best time.Time
	for _, s := range []string{st.LastContact, st.LastSuccess} {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil && t.After(best) {
			best = t
		}
	}
	return best
}

// Verdict is one episode checked against the eight conditions.
type Verdict struct {
	Episode
	Rule    Rule
	DueAt   time.Time // since + delay + grace
	Expires time.Time // since + delay + max_age
	Blocked string    // the first failing condition, "" when it may be pushed now
}

// Blocked reasons (Verdict.Blocked).
const (
	BlockNoRule     = "no_rule"     // 1: no rule, or the rule is off
	BlockRemote     = "remote"      // 1: skip_remote and a Remote Control link
	BlockPeers      = "peers"       // 1: skip_peers and a question that only concerns peers
	BlockNotDue     = "not_due"     // 2
	BlockAnswered   = "answered"    // 3: the hub has the record
	BlockReachable  = "reachable"   // 4
	BlockStale      = "stale"       // 5
	BlockQuiet      = "quiet"       // 6
	BlockMarked     = "marked"      // 7
	BlockFlood      = "flood"       // 8
	BlockRetryLater = "retry_later" // 7: a failed attempt, retried later
	BlockGaveUp     = "gave_up"     // 7: MaxTries failed attempts
)

// Evaluate checks conditions 1-7 for the episodes (8, the flood cap, is applied by Check).
func Evaluate(eps []Episode, pol Policy, now, lastContact time.Time, after time.Duration, markers map[string]Marker) []Verdict {
	out := make([]Verdict, 0, len(eps))
	for _, e := range eps {
		v := Verdict{Episode: e}
		r, ok := pol.First(e.Trigger, e.Seat, e.Project)
		v.Rule = r
		v.DueAt = e.Since.Add(time.Duration(r.DelayS)*time.Second + Grace)
		v.Expires = e.Since.Add(time.Duration(r.DelayS)*time.Second + pol.maxAge())
		mk, marked := markers[e.Key]
		switch {
		case !ok || !r.Enabled:
			v.Blocked = BlockNoRule
		case pol.Settings.SkipRemote && e.RemoteURL != "" && e.Trigger != TriggerFailed:
			v.Blocked = BlockRemote
		case pol.Settings.SkipsPeers() && e.Trigger == TriggerAsked && e.Detail == "question" && e.Peers != "":
			v.Blocked = BlockPeers
		case e.Answered:
			v.Blocked = BlockAnswered
		case now.After(v.Expires):
			v.Blocked = BlockStale
		case marked && mk.Outcome != OutcomeFailed:
			v.Blocked = BlockMarked
		case marked && mk.Tries >= MaxTries:
			v.Blocked = BlockGaveUp
		case now.Before(v.DueAt):
			v.Blocked = BlockNotDue
		case !lastContact.IsZero() && !lastContact.Before(e.Since) && now.Sub(lastContact) <= after:
			v.Blocked = BlockReachable
		case quietHolds(pol.Settings.QuietHours, r.BypassQuiet, now):
			v.Blocked = BlockQuiet
		case marked && now.Before(mk.NextAt()):
			v.Blocked = BlockRetryLater
		}
		out = append(out, v)
	}
	return out
}

// Outcome values of a marker.
const (
	OutcomeClaimed = "claimed" // being sent (or the sender died)
	OutcomeSent    = "sent"
	OutcomeFailed  = "failed"
	OutcomeFlood   = "flood" // skipped by the flood cap
)

// Marker is <state>/push/direct/<sha256(episode)[:16]>.json.
type Marker struct {
	Episode string `json:"episode"`
	Session string `json:"session"`
	Trigger string `json:"trigger"`
	Outcome string `json:"outcome"`
	Tries   int    `json:"tries"`
	At      string `json:"at"` // last attempt (RFC 3339)
	Status  int    `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}

// NextAt is when a failed attempt may be retried.
func (m Marker) NextAt() time.Time {
	t, err := time.Parse(time.RFC3339Nano, m.At)
	if err != nil {
		return time.Time{}
	}
	return t.Add(RetryAfter)
}

// MarkerDir is <state>/push/direct.
func MarkerDir(stateDir string) string { return filepath.Join(PushDir(stateDir), "direct") }

func markerName(episode string) string {
	h := sha256.Sum256([]byte(episode))
	return hex.EncodeToString(h[:])[:16]
}

// Markers reads every marker by episode.
func Markers(stateDir string) map[string]Marker {
	out := map[string]Marker{}
	ents, err := os.ReadDir(MarkerDir(stateDir))
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(MarkerDir(stateDir), e.Name()))
		if err != nil {
			continue
		}
		var m Marker
		if json.Unmarshal(b, &m) == nil && m.Episode != "" {
			out[m.Episode] = m
		}
	}
	return out
}

// claim takes attempt n of an episode with O_EXCL, so two processes never both send it:
// attempt 1 creates the marker itself, a retry creates <name>.try<n>.
func claim(stateDir, episode string, n int, m Marker) (bool, error) {
	dir := MarkerDir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	name := markerName(episode) + ".json"
	if n > 1 {
		name = markerName(episode) + ".try" + strconv.Itoa(n)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if n == 1 {
		b, _ := json.Marshal(m)
		_, err = f.Write(append(b, '\n'))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err == nil, err
}

func saveMarker(stateDir string, m Marker) error {
	b, _ := json.Marshal(m)
	return writeAtomic(filepath.Join(MarkerDir(stateDir), markerName(m.Episode)+".json"), append(b, '\n'))
}

// pruneMarkers deletes markers (and retry claims) older than MarkerKeep.
func pruneMarkers(stateDir string, now time.Time) {
	dir := MarkerDir(stateDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > MarkerKeep {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// recentSends counts attempts (sent or failed) within FloodPer before now.
func recentSends(markers map[string]Marker, now time.Time) int {
	n := 0
	for _, m := range markers {
		if m.Outcome != OutcomeSent && m.Outcome != OutcomeFailed && m.Outcome != OutcomeClaimed {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, m.At); err == nil && now.Sub(t) < FloodPer && !t.After(now) {
			n++
		}
	}
	return n
}

// Result is what Check did for one episode it acted on.
type Result struct {
	Episode string
	Session string
	Trigger string
	Outcome string // sent, failed, flood
	Status  int
	Error   string
}

// Ready reports whether direct push is on and has a topic.
func Ready(o Options) (Secret, bool) {
	if !o.Config.Push.Direct.Enabled {
		return Secret{}, false
	}
	s, err := LoadSecret(o.StateDir)
	return s, err == nil
}

func directServer(c config.Config) string {
	if c.Push.Direct.Server == "" {
		return config.DefaultDirectServer
	}
	return c.Push.Direct.Server
}

// Check runs the direct-push decision once (machines.md 5.4): for every episode that passes
// conditions 1-7, under the flood cap, it claims the marker, POSTs the ntfy message, records
// the outcome and appends a direct_push record to the outbox. Off or without a topic it
// does nothing.
func Check(ctx context.Context, o Options) ([]Result, error) {
	sec, ok := Ready(o)
	if !ok {
		return nil, nil
	}
	if err := CheckServer(directServer(o.Config)); err != nil {
		return nil, err
	}
	now := o.now()
	pruneMarkers(o.StateDir, now)
	eps, err := Scan(o.OutboxDir, o.MachineID, now)
	if err != nil {
		return nil, err
	}
	if len(eps) == 0 {
		return nil, nil
	}
	pol, _ := LoadPolicy(o.StateDir)
	markers := Markers(o.StateDir)
	verdicts := Evaluate(eps, pol, now, LastContact(o.OutboxDir), o.After(), markers)
	server := o.Config.Push.Direct.Server
	if server == "" {
		server = config.DefaultDirectServer
	}
	sent := recentSends(markers, now)
	var out []Result
	for _, v := range verdicts {
		if v.Blocked != "" {
			continue
		}
		prev, had := markers[v.Key]
		m := Marker{Episode: v.Key, Session: v.Session, Trigger: v.Trigger, Outcome: OutcomeClaimed, Tries: prev.Tries + 1,
			At: now.UTC().Format(time.RFC3339Nano)}
		if sent >= FloodMax {
			if had {
				continue // a retry waits for room
			}
			m.Outcome, m.Tries = OutcomeFlood, 0
			if won, err := claim(o.StateDir, v.Key, 1, m); err != nil || !won {
				continue
			}
			o.logf("direct push skipped (flood cap) session=%s episode=%s", v.Session, v.Key)
			out = append(out, Result{Episode: v.Key, Session: v.Session, Trigger: v.Trigger, Outcome: OutcomeFlood})
			continue
		}
		won, err := claim(o.StateDir, v.Key, m.Tries, m)
		if err != nil {
			return out, err
		}
		if !won {
			continue
		}
		sent++
		msg := Build(v.Episode, MessageOptions{Topic: sec.Topic, Machine: o.Machine, Labels: o.Config.Slots.Labels,
			PublicURL: pol.publicURL(), Private: o.Config.Push.Direct.Private})
		hc := o.HTTP
		if hc == nil {
			hc = HTTPClient()
		}
		res := Send(ctx, hc, server, sec.Token, msg)
		sentAt := o.now()
		m.At = sentAt.UTC().Format(time.RFC3339Nano)
		if res.OK {
			m.Outcome = OutcomeSent
		} else {
			m.Outcome, m.Status, m.Error = OutcomeFailed, res.Status, res.Error
		}
		if err := saveMarker(o.StateDir, m); err != nil {
			o.logf("direct push: marker: %v", err)
		}
		if err := appendRecord(o, v, server, res, sentAt); err != nil {
			o.logf("direct push: direct_push record: %v", err)
		}
		r := Result{Episode: v.Key, Session: v.Session, Trigger: v.Trigger, Outcome: m.Outcome, Status: res.Status, Error: res.Error}
		o.logf("direct push %s session=%s episode=%s %s", r.Outcome, r.Session, r.Episode, r.Error)
		out = append(out, r)
	}
	return out, nil
}

// appendRecord writes the direct_push record (record.md 4.9) to the outbox.
func appendRecord(o Options, v Verdict, server string, res SendResult, sentAt time.Time) error {
	priv := o.Config.Push.Direct.Private
	p := record.DirectPushPayload{Episode: v.Key, Trigger: v.Trigger, SessionID: v.Session,
		Since: record.FormatTime(v.Since), Record: v.Record, SentAt: record.FormatTime(sentAt),
		Channel: record.DirectChannelNtfy, ServerHost: ServerHost(server), OK: res.OK, Private: &priv}
	if !res.OK {
		p.Status, p.Error = res.Status, res.Error
		if p.Error == "" {
			p.Error = "failed"
		}
	}
	r, err := record.NewDirectPush(record.Meta{Machine: o.MachineID, Slot: v.Slot, Client: o.Client, Now: sentAt}, p)
	if err != nil {
		return err
	}
	ob := outbox.New(o.OutboxDir)
	ob.Now = o.Now
	_, err = ob.Append(r)
	return err
}

// NextDue is the earliest time an episode that passes every other condition becomes due
// (condition 2), for the hook-started flush that waits up to 90 s (machines.md 5.4). ok is
// false when there is none, or direct push is off.
func NextDue(o Options) (time.Time, bool, error) {
	if _, ok := Ready(o); !ok {
		return time.Time{}, false, nil
	}
	now := o.now()
	eps, err := Scan(o.OutboxDir, o.MachineID, now)
	if err != nil {
		return time.Time{}, false, err
	}
	pol, _ := LoadPolicy(o.StateDir)
	var best time.Time
	for _, v := range Evaluate(eps, pol, now, LastContact(o.OutboxDir), o.After(), Markers(o.StateDir)) {
		if v.Blocked != "" && v.Blocked != BlockNotDue {
			continue
		}
		at := v.DueAt
		if at.Before(now) {
			at = now
		}
		if best.IsZero() || at.Before(best) {
			best = at
		}
	}
	return best, !best.IsZero(), nil
}

// SendTest sends `wy direct-push test`'s message. It needs a topic, not Enabled.
func SendTest(ctx context.Context, o Options) (SendResult, error) {
	sec, err := LoadSecret(o.StateDir)
	if err != nil {
		return SendResult{}, err
	}
	server := directServer(o.Config)
	if err := CheckServer(server); err != nil {
		return SendResult{}, err
	}
	hc := o.HTTP
	if hc == nil {
		hc = HTTPClient()
	}
	name := o.Machine
	if name == "" {
		name = "this machine"
	}
	res := Send(ctx, hc, server, sec.Token, TestMessage(sec.Topic, name))
	if !res.OK {
		return res, fmt.Errorf("direct push test failed: %s", res.Error)
	}
	return res, nil
}
