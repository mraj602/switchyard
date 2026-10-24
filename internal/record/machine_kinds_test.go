package record

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func fixtureRecord(t *testing.T, name string) (*Record, []byte) {
	t.Helper()
	in := readFixture(t, "records/"+name)
	var r Record
	if err := json.Unmarshal(in, &r); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	return &r, in
}

func samePayload(t *testing.T, in []byte, got json.RawMessage) {
	t.Helper()
	var a struct{ Payload json.RawMessage }
	if err := json.Unmarshal(in, &a); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeAny(t, a.Payload), decodeAny(t, got)) {
		t.Fatalf("payload changed:\n%s\n%s", a.Payload, got)
	}
}

func TestMachineInfoFixtureRoundTrip(t *testing.T) {
	r, in := fixtureRecord(t, "machine_info.json")
	var p MachineInfoPayload
	if err := r.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Reach != ReachPull || p.Outbox.Backlog != 1240 || p.Timer == nil || p.Timer.Linger == nil || !*p.Timer.Linger {
		t.Fatalf("decoded: %+v", p)
	}
	if p.Extra["future_field"] == nil || p.Outbox.Extra["future_counter"] == nil {
		t.Fatalf("unknown fields dropped: %v %v", p.Extra, p.Outbox.Extra)
	}
	if p.DirectPush.PolicyAt != nil {
		t.Fatal("policy_at null must stay nil")
	}
	r2 := New(KindMachineInfo, Meta{})
	if err := r2.SetPayload(p); err != nil {
		t.Fatal(err)
	}
	samePayload(t, in, r2.Payload)
	roundTrip(t, r)
}

func TestDirectPushFixtureRoundTrip(t *testing.T) {
	r, in := fixtureRecord(t, "direct_push.json")
	var p DirectPushPayload
	if err := r.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.OK || p.Status != 503 || p.Extra["future_field"] == nil {
		t.Fatalf("decoded: %+v", p)
	}
	r2 := New(KindDirectPush, Meta{})
	if err := r2.SetPayload(p); err != nil {
		t.Fatal(err)
	}
	samePayload(t, in, r2.Payload)
	roundTrip(t, r)
}

func validInfo() MachineInfoPayload {
	return MachineInfoPayload{OS: "linux", Arch: "amd64", Wy: "0.5.0", RecordV: 1, Reach: ReachDirect}
}

func TestNewMachineInfo(t *testing.T) {
	r, err := NewMachineInfo(testMeta, validInfo())
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindMachineInfo || r.Slot != "" || r.Seat != nil || r.V != 1 {
		t.Fatalf("envelope: %+v", r)
	}
	if !strings.Contains(string(r.Payload), `"oldest_unflushed":null`) {
		t.Fatalf("null oldest_unflushed missing: %s", r.Payload)
	}
	roundTrip(t, r)
	bad := map[string]func(*MachineInfoPayload){
		"os":       func(p *MachineInfoPayload) { p.OS = "" },
		"record_v": func(p *MachineInfoPayload) { p.RecordV = 0 },
		"reach":    func(p *MachineInfoPayload) { p.Reach = "ssh" },
		"dial":     func(p *MachineInfoPayload) { p.Dial = "127.0.0.1:1" },
		"negative": func(p *MachineInfoPayload) { p.Outbox.Held = -1 },
		"oldest":   func(p *MachineInfoPayload) { x := "yesterday"; p.Outbox.OldestUnflushed = &x },
	}
	for name, mut := range bad {
		p := validInfo()
		mut(&p)
		if _, err := NewMachineInfo(testMeta, p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func validDirect() DirectPushPayload {
	return DirectPushPayload{
		Episode: "s:abc-123:needs_permission:1760191402418", Trigger: DirectTriggerNeedsPermission,
		SessionID: "abc-123", Since: "2026-10-11T14:03:22.418Z", Record: "01JA2W5N0G8H3Q7S2V6X9Y1Z4J",
		SentAt: "2026-10-11T14:04:07.000Z", Channel: DirectChannelNtfy, ServerHost: "ntfy.sh", OK: true,
	}
}

func TestNewDirectPush(t *testing.T) {
	r, err := NewDirectPush(testMeta, validDirect())
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindDirectPush || r.Seat != nil || r.Slot != testMeta.Slot {
		t.Fatalf("envelope: %+v", r)
	}
	roundTrip(t, r)
	bad := map[string]func(*DirectPushPayload){
		"trigger":    func(p *DirectPushPayload) { p.Trigger = "limit_80" },
		"session":    func(p *DirectPushPayload) { p.SessionID = "" },
		"episode":    func(p *DirectPushPayload) { p.Episode = "s:other:needs_permission:1" },
		"episode ms": func(p *DirectPushPayload) { p.Episode = "s:abc-123:needs_permission:x" },
		"since":      func(p *DirectPushPayload) { p.Since = "" },
		"record":     func(p *DirectPushPayload) { p.Record = "short" },
		"channel":    func(p *DirectPushPayload) { p.Channel = "webpush" },
		"host":       func(p *DirectPushPayload) { p.ServerHost = "" },
		"ok+error":   func(p *DirectPushPayload) { p.Error = "timeout" },
	}
	for name, mut := range bad {
		p := validDirect()
		mut(&p)
		if _, err := NewDirectPush(testMeta, p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMaxVersionMatchesWriter(t *testing.T) {
	if MaxVersion != Version {
		t.Fatalf("MaxVersion %d, Version %d: bump both together", MaxVersion, Version)
	}
}
