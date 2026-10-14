// Package machineinfo builds and queues this machine's `machine_info` record (record.md 4.8,
// machines.md 6.3): os/arch, version, reach and dial type, outbox health, direct-push state
// and the timer. `sy tick` sends it when its content (counters and times excluded) changed
// since the last one sent, and otherwise every 6 hours; `sy connect`, `sy update` and
// `sy machine rename` send it at once. The last one sent is <state>/machine-info.json.
//
// Phase 5, WP-E. Client package: depends on record, outbox, config, client (flush status),
// directpush (policy cache), identity, atomicfile.
package machineinfo

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/atomicfile"
	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/directpush"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/record"
	"github.com/mraj602/switchyard/internal/version"
)

// File is <state>/machine-info.json: when the last machine_info was sent and what it said.
const File = "machine-info.json"

// Every is the longest time between two machine_info records from `sy tick`.
const Every = 6 * time.Hour

// Above countUpTo unflushed bytes the backlog is estimated at estBytesPerRecord.
const (
	countUpTo         = 8 << 20
	estBytesPerRecord = 600
)

// Timer is the timer state as `sy doctor` sees it (unit files, no service manager asked).
type Timer struct {
	Active bool
	Linger *bool // Linux only
}

// Options is what Build and Send read.
type Options struct {
	StateDir string // <state>
	Config   config.Config
	// Timer reports the timer's state; nil leaves machine_info.timer out.
	Timer func() Timer
	Now   func() time.Time
	// GOOS and GOARCH default to runtime's (tests set them).
	GOOS, GOARCH string
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) outboxDir() string { return filepath.Join(o.StateDir, "outbox") }

// Reach is machine_info.reach for a configuration: hub.reach, or `none` without a hub (a
// pulled machine has no hub.url but reach `pull`).
func Reach(cfg config.Config) string {
	switch cfg.Hub.Reach {
	case config.ReachPull:
		return record.ReachPull
	case config.ReachForward:
		if cfg.Hub.URL != "" {
			return record.ReachForward
		}
	default:
		if cfg.Hub.URL != "" {
			return record.ReachDirect
		}
	}
	return record.ReachNone
}

// Dial is machine_info.dial: the kind of hub.dial, never the address itself.
func Dial(cfg config.Config) string {
	switch {
	case cfg.Hub.Dial == "":
		return ""
	case strings.HasPrefix(cfg.Hub.Dial, config.UnixDialPrefix):
		return "unix"
	}
	return "tcp"
}

// Build makes the payload for this machine now.
func Build(o Options) (record.MachineInfoPayload, error) {
	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	p := record.MachineInfoPayload{OS: goos, Arch: goarch, Sy: version.Version, RecordV: record.MaxVersion,
		Reach: Reach(o.Config), Dial: Dial(o.Config)}

	ob := outbox.New(o.outboxDir())
	if _, err := os.Stat(ob.Dir); err == nil {
		if st, err := ob.Stat(); err == nil {
			p.Outbox.UnflushedBytes = st.UnflushedBytes
			if !st.OldestUnflushed.IsZero() {
				s := record.FormatTime(st.OldestUnflushed)
				p.Outbox.OldestUnflushed = &s
			}
			if st.UnflushedBytes > countUpTo {
				// A long outage: estimate rather than read the whole backlog every minute.
				p.Outbox.Backlog = st.UnflushedBytes / estBytesPerRecord
			} else if n, _, err := ob.Backlog(st.Cursor); err == nil {
				p.Outbox.Backlog = n
			}
		}
		fs := client.ReadStatus(ob.Dir)
		p.Outbox.Held = int64(fs.Held)
		p.Outbox.RejectedTotal = countLines(ob.Dir, "rejected")
		p.Outbox.CorruptTotal = countLines(ob.Dir, "corrupt")
	}

	d := o.Config.Push.Direct
	p.DirectPush.Enabled = d.Enabled
	p.DirectPush.Private = d.Private
	if d.Enabled {
		srv := d.Server
		if srv == "" {
			srv = config.DefaultDirectServer
		}
		p.DirectPush.ServerHost = directpush.ServerHost(srv)
	}
	if pol, ok := directpush.LoadPolicy(o.StateDir); ok && pol.FetchedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, pol.FetchedAt); err == nil {
			s := record.FormatTime(t)
			p.DirectPush.PolicyAt = &s
		}
	}
	if o.Timer != nil {
		t := o.Timer()
		p.Timer = &record.TimerState{Active: t.Active, Linger: t.Linger}
	}
	return p, p.Validate()
}

// countLines counts the lines of an outbox log (<base>.jsonl) and its rotated copy
// (<base>.1.jsonl, outbox appendCapped).
func countLines(dir, base string) int64 {
	var n int64
	for _, f := range []string{base + ".jsonl", base + ".1.jsonl"} {
		fh, err := os.Open(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) > 0 {
				n++
			}
		}
		fh.Close()
	}
	return n
}

// key is the content that decides "changed": the payload without counters and times.
func key(p record.MachineInfoPayload) string {
	p.Outbox = record.OutboxHealth{Held: min(p.Outbox.Held, 1)} // holding or not, not how many
	p.DirectPush.PolicyAt = nil
	if p.Update != nil {
		u := *p.Update
		u.LastCheck = ""
		p.Update = &u
	}
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// last is the content of machine-info.json.
type last struct {
	At      string          `json:"at"`
	Key     string          `json:"key"`
	Record  string          `json:"record"`
	Payload json.RawMessage `json:"payload"`
}

// Due reports whether `sy tick` should send p: no record sent yet, its content changed, or
// the last one is Every old.
func Due(stateDir string, p record.MachineInfoPayload, now time.Time) bool {
	b, err := os.ReadFile(filepath.Join(stateDir, File))
	if err != nil {
		return true
	}
	var l last
	if json.Unmarshal(b, &l) != nil || l.Key != key(p) {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, l.At)
	return err != nil || now.Sub(at) >= Every || at.After(now)
}

// Send builds the record and appends it to the outbox when force is set or Due says so;
// sent reports whether it did. The record goes out with the next flush, or the next drain
// for a pulled machine.
func Send(o Options, force bool) (sent bool, err error) {
	if o.StateDir == "" {
		return false, errors.New("machineinfo: no state folder")
	}
	p, err := Build(o)
	if err != nil {
		return false, err
	}
	now := o.now()
	if !force && !Due(o.StateDir, p, now) {
		return false, nil
	}
	m, err := identity.LoadOrCreateMachine(o.StateDir, identity.MachineOptions{})
	if err != nil {
		return false, err
	}
	r, err := record.NewMachineInfo(record.Meta{Machine: m.ID, Client: "sy/" + version.Version, Now: now}, p)
	if err != nil {
		return false, err
	}
	ob := outbox.New(o.outboxDir())
	ob.NoFsync = !o.Config.Outbox.Fsync
	if _, err := ob.Append(r); err != nil {
		return false, err
	}
	raw, _ := json.Marshal(p)
	b, _ := json.Marshal(last{At: now.UTC().Format(time.RFC3339Nano), Key: key(p), Record: r.ID, Payload: raw})
	if err := atomicfile.Write(filepath.Join(o.StateDir, File), append(b, '\n'), 0o600); err != nil {
		return true, err
	}
	return true, nil
}
