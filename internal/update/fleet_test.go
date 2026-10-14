package update

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRemote struct {
	mu       sync.Mutex
	order    []string
	runs     map[string][][]string
	pushed   map[string][]string
	noNet    map[string]bool // exit 4 for a plain update
	down     map[string]bool
	inflight int32
	maxSeen  int32
}

func (f *fakeRemote) Run(_ context.Context, m FleetMachine, argv []string) (string, int, error) {
	n := atomic.AddInt32(&f.inflight, 1)
	defer atomic.AddInt32(&f.inflight, -1)
	for {
		old := atomic.LoadInt32(&f.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(&f.maxSeen, old, n) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, m.Name)
	f.runs[m.Name] = append(f.runs[m.Name], argv)
	if f.down[m.Name] {
		return "", 0, errors.New("ssh: connection refused")
	}
	fromFile := strings.Contains(strings.Join(argv, " "), "--from-file")
	if f.noNet[m.Name] && !fromFile {
		return `{"status":"failed","error":"download failed"}`, 4, nil
	}
	if m.Name == "bad" {
		return `{"status":"failed","error":"checksum mismatch"}`, 3, nil
	}
	return `{"status":"updated","from":"0.4.2","to":"0.5.0"}`, 0, nil
}

func (f *fakeRemote) Platform(context.Context, FleetMachine) (string, string, error) {
	return "linux", "amd64", nil
}

func (f *fakeRemote) Push(_ context.Context, m FleetMachine, local, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushed[m.Name] = append(f.pushed[m.Name], name)
	return "/state/update/" + name, nil
}

func TestFleetOrderParallelAndPush(t *testing.T) {
	r := relFiles{}
	r.add(t, "0.5.0", 4, false)
	s := server(t, r)
	f := &fakeRemote{runs: map[string][][]string{}, pushed: map[string][]string{}, noNet: map[string]bool{"offline": true}, down: map[string]bool{"gone": true}}
	var ms []FleetMachine
	ms = append(ms, FleetMachine{Name: "a"}, FleetMachine{Name: "b"}, FleetMachine{Name: "offline"}, FleetMachine{Name: "gone"},
		FleetMachine{Name: "bad"}, FleetMachine{Name: "c"}, FleetMachine{Name: "d"}, FleetMachine{Name: "hub", Hub: true})
	res, failed := UpdateFleet(context.Background(), FleetOptions{Machines: ms, Version: "0.5.0", Source: Source{BaseURL: s.URL},
		Remote: f, CacheDir: t.TempDir()})
	if !failed {
		t.Error("failures must be reported")
	}
	if f.order[0] != "hub" {
		t.Errorf("hub host must go first: %v", f.order)
	}
	if f.maxSeen > 4 {
		t.Errorf("more than 4 at a time: %d", f.maxSeen)
	}
	by := map[string]FleetResult{}
	for _, x := range res {
		by[x.Name] = x
	}
	if res[0].Name != "hub" || len(res) != 8 {
		t.Errorf("table order: %+v", res)
	}
	if by["a"].Outcome != "updated" || by["a"].From != "0.4.2" {
		t.Errorf("a: %+v", by["a"])
	}
	if by["offline"].Outcome != "pushed" || len(f.pushed["offline"]) != 2 {
		t.Errorf("offline: %+v %v", by["offline"], f.pushed)
	}
	last := f.runs["offline"][1]
	if got := strings.Join(last, " "); !strings.Contains(got, "--from-file /state/update/sy_0.5.0_linux_amd64.tar.gz --sums-file /state/update/SHA256SUMS") {
		t.Errorf("push argv: %s", got)
	}
	if by["gone"].Outcome != "failed" || !strings.Contains(by["gone"].Error, "refused") {
		t.Errorf("gone: %+v", by["gone"])
	}
	if by["bad"].Outcome != "failed" || !strings.Contains(by["bad"].Error, "exit 3") {
		t.Errorf("bad: %+v", by["bad"])
	}
	if by["c"].Outcome != "updated" || by["d"].Outcome != "updated" {
		t.Error("one failure stopped the others")
	}
	if got := strings.Join(f.runs["a"][0], " "); got != ".local/bin/sy update --yes --json --version 0.5.0" {
		t.Errorf("argv %q", got)
	}
}

func TestSelectMachines(t *testing.T) {
	reg := []FleetMachine{{Name: "a"}, {Name: "b"}}
	if got, err := SelectMachines(reg, "all"); err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if got, err := SelectMachines(reg, "b"); err != nil || got[0].Name != "b" {
		t.Fatal(got, err)
	}
	if _, err := SelectMachines(reg, "zz"); err == nil || !strings.Contains(err.Error(), "known: a, b") {
		t.Fatal(err)
	}
}

func TestFleetPushNeedsListedPlatform(t *testing.T) {
	r := relFiles{}
	r.add(t, "0.5.0", 4, false)
	s := server(t, r)
	f := &fakeRemote{runs: map[string][][]string{}, pushed: map[string][]string{}, noNet: map[string]bool{"m": true}}
	res, failed := UpdateFleet(context.Background(), FleetOptions{Machines: []FleetMachine{{Name: "m", OS: "linux", Arch: "arm64"}}, Version: "0.5.0",
		Source: Source{BaseURL: s.URL}, Remote: f, CacheDir: t.TempDir()})
	if !failed || res[0].Outcome != "failed" || !strings.Contains(res[0].Error, "not listed") {
		t.Fatalf("%+v", res)
	}
}
