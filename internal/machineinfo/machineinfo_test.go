package machineinfo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/record"
)

func cfgWith(url, reach, dial string) config.Config {
	c := config.Defaults()
	c.Hub.URL, c.Hub.Reach, c.Hub.Dial = url, reach, dial
	return c
}

func TestReachAndDial(t *testing.T) {
	for _, tc := range []struct {
		url, reach, dial string
		wantReach        string
		wantDial         string
	}{
		{"https://hub.example", config.ReachDirect, "", record.ReachDirect, ""},
		{"https://hub.example", config.ReachForward, "127.0.0.1:47830", record.ReachForward, "tcp"},
		{"https://hub.example", config.ReachForward, "unix:/run/user/1000/sy.sock", record.ReachForward, "unix"},
		{"", config.ReachPull, "", record.ReachPull, ""},
		{"", config.ReachDirect, "", record.ReachNone, ""},
	} {
		c := cfgWith(tc.url, tc.reach, tc.dial)
		if got := Reach(c); got != tc.wantReach {
			t.Errorf("Reach(%+v) = %q, want %q", tc, got, tc.wantReach)
		}
		if got := Dial(c); got != tc.wantDial {
			t.Errorf("Dial(%+v) = %q, want %q", tc, got, tc.wantDial)
		}
	}
}

// outboxRecords returns the machine_info payloads in the outbox, in order.
func outboxRecords(t *testing.T, state string) []record.MachineInfoPayload {
	t.Helper()
	b, err := outbox.New(filepath.Join(state, "outbox")).Read(outbox.Cursor{}, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []record.MachineInfoPayload
	for _, it := range b.Items {
		var r record.Record
		if err := json.Unmarshal(it.Raw, &r); err != nil {
			t.Fatal(err)
		}
		if r.Kind != record.KindMachineInfo {
			continue
		}
		if r.Slot != "" || r.Seat != nil {
			t.Errorf("machine_info has a slot or seat: %s", it.Raw)
		}
		var p record.MachineInfoPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestSendOnChangeAndEverySixHours(t *testing.T) {
	state := t.TempDir()
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	o := Options{StateDir: state, Config: cfgWith("https://hub.example", config.ReachForward, "unix:/tmp/sy.sock"),
		Now: func() time.Time { return now }, GOOS: "linux", GOARCH: "arm64",
		Timer: func() Timer { l := true; return Timer{Active: true, Linger: &l} }}

	if sent, err := Send(o, false); err != nil || !sent {
		t.Fatalf("first send: %v %v", sent, err)
	}
	now = now.Add(time.Minute)
	if sent, _ := Send(o, false); sent {
		t.Fatal("unchanged content sent again within 6 h")
	}
	// A counter (a new record waiting) is not a change.
	ob := outbox.New(filepath.Join(state, "outbox"))
	if _, err := ob.AppendLine([]byte(`{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T00001","kind":"hook_event","ts":"2026-10-13T09:00:30.000Z","machine":"m_x","payload":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if sent, _ := Send(o, false); sent {
		t.Fatal("a backlog change alone sent machine_info")
	}
	// Reach changed: sent at once.
	o.Config = cfgWith("", config.ReachPull, "")
	if sent, _ := Send(o, false); !sent {
		t.Fatal("reach change not sent")
	}
	// Six hours later, unchanged: sent again.
	now = now.Add(Every)
	if sent, _ := Send(o, false); !sent {
		t.Fatal("not resent after 6 h")
	}
	// Force (connect, rename, update) always sends.
	if sent, _ := Send(o, true); !sent {
		t.Fatal("forced send skipped")
	}

	ps := outboxRecords(t, state)
	if len(ps) != 4 {
		t.Fatalf("%d machine_info records, want 4", len(ps))
	}
	first := ps[0]
	if first.Reach != record.ReachForward || first.Dial != "unix" || first.OS != "linux" || first.Arch != "arm64" ||
		first.RecordV != record.MaxVersion || first.Timer == nil || !first.Timer.Active || first.Timer.Linger == nil {
		t.Errorf("first machine_info: %+v", first)
	}
	if ps[1].Reach != record.ReachPull || ps[1].Dial != "" || ps[1].Outbox.Backlog < 1 {
		t.Errorf("second machine_info: %+v", ps[1])
	}
	raw, _ := os.ReadFile(filepath.Join(state, File))
	if !strings.Contains(string(raw), `"key"`) {
		t.Errorf("%s: %s", File, raw)
	}
}

func TestBuildNeverLeaksTheDialAddressOrTopic(t *testing.T) {
	state := t.TempDir()
	c := cfgWith("https://hub.example", config.ReachForward, "unix:/home/u/.secret-path/sy.sock")
	c.Push.Direct.Enabled = true
	c.Push.Direct.Server = "https://ntfy.example.org"
	p, err := Build(Options{StateDir: state, Config: c})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "secret-path") {
		t.Errorf("payload has the dial address: %s", b)
	}
	if !p.DirectPush.Enabled || p.DirectPush.ServerHost != "ntfy.example.org" {
		t.Errorf("direct_push: %+v", p.DirectPush)
	}
}
