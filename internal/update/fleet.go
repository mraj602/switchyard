package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fleet update (`wy update --machines`, machines.md 7.4). The operator runs it with the
// person's own SSH. internal/sshx and the machine registry reader are not imported here:
// this package codes against the two small interfaces below and cmd/wy adapts them. Nothing in this file starts anything on the hub: the operator
// pushes.

// FleetMachine is one registry entry as the fleet update needs it.
type FleetMachine struct {
	Name string
	// Target is what the Remote implementation connects to (an ssh destination as typed).
	Target string
	// Hub marks the hub host (updated first).
	Hub bool
	// OS and Arch are the remote's platform ("linux", "arm64"), used for the push path; empty
	// means ask Remote.Platform.
	OS, Arch string
	// RemoteWy is the remote's wy path relative to its home ("" = ".local/bin/wy").
	RemoteWy string
}

func (m FleetMachine) wy() string {
	if m.RemoteWy == "" {
		return ".local/bin/wy"
	}
	return m.RemoteWy
}

// Registry lists the machines the operator added.
type Registry interface {
	Machines() ([]FleetMachine, error)
}

// Remote runs things on a machine over SSH.
type Remote interface {
	// Run runs argv there and returns its standard output and exit status. A transport
	// failure (cannot connect) is an error; a nonzero exit is not.
	Run(ctx context.Context, m FleetMachine, argv []string) (stdout string, exit int, err error)
	// Platform returns the remote's goos and goarch.
	Platform(ctx context.Context, m FleetMachine) (goos, goarch string, err error)
	// Push uploads a local file into the remote's update folder under name, checking its
	// SHA-256 on the remote after the transfer (machines.md 4.2 step 3), and returns the
	// path to use in remote commands.
	Push(ctx context.Context, m FleetMachine, localPath, name string) (remotePath string, err error)
}

// Time limits for the SSH steps of a fleet update, so one hung machine cannot stall the run.
// A remote `wy update` downloads and runs the post-update steps, hence the longer limit.
const (
	remoteRunTimeout = 10 * time.Minute
	pushTimeout      = 10 * time.Minute
	platformTimeout  = time.Minute
)

// FleetOptions configures UpdateFleet.
type FleetOptions struct {
	Machines []FleetMachine
	// Version is the version to bring everyone to (the operator's).
	Version string
	Source  Source
	Fetcher Fetcher
	Remote  Remote
	// Attestation is the operator's policy. On the push path the operator checks the archive
	// itself (the remote has no internet to do it); when it is off, the remotes are told not to
	// check either.
	Attestation Attestation
	// CacheDir holds archives fetched for the push path.
	CacheDir string
	// Parallel is how many machines at once after the hub host (default 4).
	Parallel int
	// Progress is called as each machine finishes (may be nil).
	Progress func(FleetResult)
}

// FleetResult is one row of the result table.
type FleetResult struct {
	Name    string `json:"name"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Outcome string `json:"outcome"` // updated, current, pushed, failed
	Error   string `json:"error,omitempty"`
}

// remoteReply is the last JSON line of a remote `wy update --json`.
type remoteReply struct {
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to"`
	Error  string `json:"error"`
}

func parseReply(out string) remoteReply {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r remoteReply
		if json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &r) == nil && (r.Status != "" || r.Error != "") {
			return r
		}
	}
	return remoteReply{}
}

// OrderFleet puts the hub host first. The rest keep their order.
func OrderFleet(ms []FleetMachine) (hub []FleetMachine, rest []FleetMachine) {
	for _, m := range ms {
		if m.Hub {
			hub = append(hub, m)
		} else {
			rest = append(rest, m)
		}
	}
	return
}

// SelectMachines resolves `--machines all|a,b` against the registry.
func SelectMachines(reg []FleetMachine, spec string) ([]FleetMachine, error) {
	if spec == "all" {
		return reg, nil
	}
	by := map[string]FleetMachine{}
	for _, m := range reg {
		by[m.Name] = m
	}
	var out []FleetMachine
	for _, n := range strings.Split(spec, ",") {
		n = strings.TrimSpace(n)
		m, ok := by[n]
		if !ok {
			names := make([]string, 0, len(by))
			for k := range by {
				names = append(names, k)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("no added machine named %q (known: %s)", n, strings.Join(names, ", "))
		}
		out = append(out, m)
	}
	return out, nil
}

// UpdateFleet brings every machine to o.Version: the hub host first, then the rest o.Parallel
// at a time. One failure does not stop the others. The returned bool is true when any failed.
func UpdateFleet(ctx context.Context, o FleetOptions) ([]FleetResult, bool) {
	if o.Parallel <= 0 {
		o.Parallel = 4
	}
	if o.Fetcher == nil {
		o.Fetcher = Real{}
	}
	hub, rest := OrderFleet(o.Machines)
	var results []FleetResult
	var mu sync.Mutex
	record := func(r FleetResult) {
		mu.Lock()
		results = append(results, r)
		if o.Progress != nil {
			o.Progress(r)
		}
		mu.Unlock()
	}
	for _, m := range hub {
		record(updateOne(ctx, o, m))
	}
	sem := make(chan struct{}, o.Parallel)
	var wg sync.WaitGroup
	for _, m := range rest {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			record(updateOne(ctx, o, m))
		}()
	}
	wg.Wait()
	// result order: the order of o.Machines, hub first
	order := map[string]int{}
	for i, m := range append(append([]FleetMachine{}, hub...), rest...) {
		order[m.Name] = i
	}
	sort.SliceStable(results, func(i, j int) bool { return order[results[i].Name] < order[results[j].Name] })
	failed := false
	for _, r := range results {
		if r.Outcome == "failed" {
			failed = true
		}
	}
	return results, failed
}

func updateOne(ctx context.Context, o FleetOptions, m FleetMachine) FleetResult {
	res := FleetResult{Name: m.Name, To: o.Version}
	argv := []string{m.wy(), "update", "--yes", "--json", "--version", o.Version}
	if !o.Attestation.Auto && !o.Attestation.Required {
		argv = append(argv, "--no-attestation")
	}
	rctx, rcancel := context.WithTimeout(ctx, remoteRunTimeout)
	defer rcancel()
	out, exit, err := o.Remote.Run(rctx, m, argv)
	if err != nil {
		res.Outcome, res.Error = "failed", err.Error()
		return res
	}
	r := parseReply(out)
	res.From = r.From
	switch exit {
	case 0:
		res.Outcome = "updated"
		if r.Status == "current" {
			res.Outcome = "current"
		}
		if r.To != "" {
			res.To = r.To
		}
		return res
	case ExitDownload:
		return pushUpdate(ctx, o, m, res)
	}
	res.Outcome, res.Error = "failed", failText(r, exit)
	return res
}

func failText(r remoteReply, exit int) string {
	if r.Error != "" {
		return fmt.Sprintf("exit %d: %s", exit, r.Error)
	}
	return fmt.Sprintf("exit %d", exit)
}

// pushUpdate is 7.4 step 3: the remote has no internet, so the operator downloads that
// platform's archive and SHA256SUMS, uploads both and runs the update from files; the remote
// verifies again before replacing.
func pushUpdate(ctx context.Context, o FleetOptions, m FleetMachine, res FleetResult) FleetResult {
	failWith := func(format string, a ...any) FleetResult {
		res.Outcome, res.Error = "failed", fmt.Sprintf(format, a...)
		return res
	}
	goos, goarch := m.OS, m.Arch
	if goos == "" || goarch == "" {
		var err error
		pc, cancel := context.WithTimeout(ctx, platformTimeout)
		defer cancel()
		if goos, goarch, err = o.Remote.Platform(pc, m); err != nil {
			return failWith("push path: cannot tell the remote's platform: %v", err)
		}
	}
	rel, err := o.Fetcher.Resolve(ctx, o.Source, o.Version)
	if err != nil {
		return failWith("push path: %v", err)
	}
	cache := filepath.Join(o.CacheDir, goos+"_"+goarch)
	arch, err := o.Fetcher.Fetch(ctx, rel, goos, goarch, cache)
	if err != nil {
		return failWith("push path: %v", err)
	}
	if _, err := VerifyAttestation(ctx, o.Attestation, arch.Path); err != nil {
		return failWith("push path: %v", err)
	}
	// A per-call temp file: machines of one platform share cache, and pushes run in parallel.
	sf, err := os.CreateTemp(cache, "SHA256SUMS."+rel.Version+".*")
	if err != nil {
		return failWith("push path: %v", err)
	}
	sums := sf.Name()
	defer os.Remove(sums)
	if _, err := sf.Write(rel.SumsRaw); err != nil {
		sf.Close()
		return failWith("push path: %v", err)
	}
	if err := sf.Close(); err != nil {
		return failWith("push path: %v", err)
	}
	pctx, pcancel := context.WithTimeout(ctx, pushTimeout)
	defer pcancel()
	rArch, err := o.Remote.Push(pctx, m, arch.Path, arch.Name)
	if err != nil {
		return failWith("push path: uploading %s: %v", arch.Name, err)
	}
	rSums, err := o.Remote.Push(pctx, m, sums, "SHA256SUMS")
	if err != nil {
		return failWith("push path: uploading SHA256SUMS: %v", err)
	}
	argv := []string{m.wy(), "update", "--yes", "--json", "--version", o.Version, "--from-file", rArch, "--sums-file", rSums,
		"--no-attestation"} // the operator checked it above; the remote cannot reach GitHub
	rctx, rcancel := context.WithTimeout(ctx, remoteRunTimeout)
	defer rcancel()
	out, exit, err := o.Remote.Run(rctx, m, argv)
	if err != nil {
		return failWith("push path: %v", err)
	}
	r := parseReply(out)
	if exit != 0 {
		return failWith("push path: %s", failText(r, exit))
	}
	res.Outcome = "pushed"
	if r.Status == "current" {
		res.Outcome = "current"
	}
	if r.From != "" {
		res.From = r.From
	}
	if r.To != "" {
		res.To = r.To
	}
	return res
}
