// Package machineadd is `wy machine add|remove|repoint|list|probe|authorize-pull`
// (machines.md 4): from the operator, with the person's own SSH, copy the right wy to a
// remote (release for its os/arch, SHA-256 checked there before the atomic move), join the
// hub with a one-time pairing secret passed on stdin, or register it for pull, write the
// forward block for forward mode, and log accounts in with the person's own terminal. The
// remote sides (probe, authorize-pull) are here too.
//
// It never imports internal/hub: everything the hub host must do goes through the admin
// channel (Admin: `wy hub …` in process, over a command prefix, or a pasted code).
//
// Depends on: sshx, update, client, config, identity, setup/{apply,backup,model},
// atomicfile, version.
package machineadd

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/sshx"
	"github.com/mraj602/watchyard/internal/update"
)

// Exit codes of `wy machine add` (4.2).
const (
	ExitFailed      = 1
	ExitUsage       = 2
	ExitNeedsPerson = 3
	ExitVerify      = 6
	ExitNoClaude    = 7
)

// ExitError carries an exit code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func exitf(code int, format string, a ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, a...)}
}

// Reach choices of --reach.
const (
	ReachAuto    = "auto"
	ReachDirect  = config.ReachDirect
	ReachForward = config.ReachForward
	ReachPull    = config.ReachPull
)

// Event is one step line (text) or one --json object.
type Event struct {
	Step   string         `json:"step"`
	Status string         `json:"status"` // ok, skip, warn, fail, plan
	Text   string         `json:"text"`
	Data   map[string]any `json:"data,omitempty"`
}

// Options are the flags of `wy machine add` (4.1).
type Options struct {
	Host          string
	Name          string
	Reach         string
	HubURL        string
	InsecureLAN   bool
	Port          int
	ForwardSocket bool
	PullFrom      string
	// Code is a machine pairing code (digits) read from stdin by --code-stdin.
	Code        string
	CodeStdin   bool
	Version     string
	Binary      string
	Accounts    string
	Share       string
	Login       string // ask, never
	DirectPush  bool
	Yes, DryRun bool
}

// Env is the operator side.
type Env struct {
	SSH      *sshx.Runner
	Admin    Admin // nil: no channel (paste a code)
	StateDir string
	Home     string
	// OperatorHub is the operator's own hub.url (and whether the hub runs here, merged).
	OperatorHub string
	HubLocal    bool
	Fetcher     update.Fetcher
	Source      update.Source
	SelfExe     string
	SelfVersion string
	Interactive bool // a person is at the terminal
	// Ask asks a yes/no question (default def); Choose picks one of options (first is the
	// default); ReadSecret reads a pasted code with echo off.
	Ask        func(q string, def bool) bool
	Choose     func(q string, options []string) string
	ReadSecret func(prompt string) (string, error)
	// DirectPush returns the operator's direct-push settings for --direct-push: server,
	// private, and the {"topic","token"} JSON for stdin.
	DirectPush func() (server string, private bool, secret []byte, err error)
	Events     func(Event)
	Now        func() time.Time
	// HostKeyDir is the remote folder of the public host keys (default /etc/ssh; tests).
	HostKeyDir string
	// Command is the command line recorded in backup sets.
	Command string
	// HubHealth reads the operator's own hub /health (nil: a plain GET of OperatorHub);
	// used to check who holds a forward port that is already taken.
	HubHealth func(ctx context.Context) (client.Health, error)
	// HubPin and OwnerPin are this machine's pins (keys.md 3.5; nil func or nil pin: none):
	// the hub key the admin channel answers with must be the pinned one, and the owner key
	// is pinned on the remote over SSH.
	HubPin, OwnerPin func() *keys.Pin
	// Certify owner-signs the new machine's certificate in the same run (keys.md 4.2 step 2):
	// the signing guard, the terminal, the passphrase. It returns the owner fingerprint and
	// the signature; an error leaves `wy keys certify` as the next step. Nil: never.
	Certify func(ctx context.Context, name string, c keys.Cert) (fp, sig string, err error)
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Env) emit(step, status, text string, data map[string]any) {
	if e.Events != nil {
		e.Events(Event{Step: step, Status: status, Text: text, Data: data})
	}
}

// Summary is the result of Add.
type Summary struct {
	Name      string   `json:"name"`
	Machine   string   `json:"machine,omitempty"`
	Host      string   `json:"host"`
	Reach     string   `json:"reach"`
	HubURL    string   `json:"hub_url,omitempty"`
	Port      int      `json:"port,omitempty"`
	OS        string   `json:"os"`
	Arch      string   `json:"arch"`
	Sy        string   `json:"sy"`
	DevBuild  bool     `json:"dev_build,omitempty"`
	Logins    []string `json:"logins,omitempty"` // commands left to run
	Verified  bool     `json:"verified"`
	DryRun    bool     `json:"dry_run,omitempty"`
	Next      string   `json:"next,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	PullName  string   `json:"pull_target_name,omitempty"`
	PullID    string   `json:"pull_target,omitempty"`
	ExitCode  int      `json:"exit_code"`
	ChangedFw bool     `json:"forward_written,omitempty"`
	// Keys is the link keys step (nil when it did not run: pull, no admin channel, a failure).
	Keys *KeysSummary `json:"keys,omitempty"`
}

// setupReply is the part of the remote `wy setup --json` report (setup.md 6.3) machine add
// reads.
type setupReply struct {
	Mode string `json:"mode"`
	Plan struct {
		Needs []struct {
			Kind    string `json:"kind"`
			Folder  string `json:"folder"`
			Command string `json:"command"`
		} `json:"needs"`
	} `json:"plan"`
	Hub *struct {
		URL     string `json:"url"`
		Healthy bool   `json:"healthy"`
		Error   string `json:"error"`
	} `json:"hub"`
}

type adder struct {
	o   Options
	e   *Env
	r   remote
	sum Summary
	reg *Registry
	old Entry
	cfg sshx.Config
	pre PreProbe
	pr  *ProbeReport
	fw  *Forward
	// fwArgs are the -R/-o arguments for this run's connections in forward mode.
	fwArgs []string
}

// Add runs the steps of machines.md 4.2.
func Add(ctx context.Context, o Options, e *Env) (Summary, error) {
	a := &adder{o: o, e: e, r: remote{ssh: e.SSH, host: o.Host}}
	a.sum = Summary{Host: o.Host, DryRun: o.DryRun}
	err := a.run(ctx)
	if err != nil {
		var ee *ExitError
		a.sum.ExitCode = ExitFailed
		if errors.As(err, &ee) {
			a.sum.ExitCode = ee.Code
		}
	}
	return a.sum, err
}

func (a *adder) run(ctx context.Context) error {
	o := a.o
	if err := sshx.CheckHost(o.Host); err != nil {
		return exitf(ExitUsage, "%v", err)
	}
	switch o.Reach {
	case "", ReachAuto, ReachDirect, ReachForward, ReachPull:
	default:
		return exitf(ExitUsage, "--reach must be auto, direct, forward or pull")
	}
	if o.Reach == "" {
		a.o.Reach = ReachAuto
	}
	if o.CodeStdin && o.Reach == ReachPull {
		return exitf(ExitUsage, "--code-stdin is for direct and forward; pull registers the machine through the admin channel")
	}
	if o.Reach == ReachPull && a.e.Admin == nil {
		return exitf(ExitUsage, "pull mode needs the hub host: pass --hub-admin local or a command prefix such as \"ssh hub-host wy\"")
	}
	if o.Version != "" && o.Binary != "" {
		return exitf(ExitUsage, "use --version or --binary, not both")
	}
	if o.Port != 0 && (o.Port < FirstForwardPort || o.Port > LastForwardPort) {
		return exitf(ExitUsage, "--port must be in %d-%d", FirstForwardPort, LastForwardPort)
	}
	reg, err := LoadRegistry(a.e.StateDir)
	if err != nil {
		return err
	}
	a.reg = reg
	if old, ok := reg.Find(o.Host); ok {
		a.old = old
	}
	if a.o.Name != "" {
		if old, ok := reg.Find(a.o.Name); ok {
			a.old = old
		}
	}

	if err := a.connect(ctx); err != nil {
		return err
	}
	if err := a.probe(ctx); err != nil {
		return err
	}
	if o.DryRun {
		a.dryRun()
		return nil
	}
	if err := a.binary(ctx); err != nil {
		return err
	}
	if err := a.probeNew(ctx); err != nil {
		return err
	}
	if err := a.decideReach(); err != nil {
		return err
	}
	cred, err := a.hubAdmin(ctx)
	if err != nil {
		return err
	}
	if a.sum.Reach == ReachForward {
		if err := a.forward(ctx); err != nil {
			return err
		}
	}
	reply, err := a.setup(ctx, cred)
	if err != nil {
		_ = a.saveRegistry(ctx, false) // the binary and the hub side are in place; a rerun redoes the rest
		return err
	}
	verr := a.verify(ctx)
	a.logins(ctx, reply)
	if err := a.directPush(ctx); err != nil {
		a.warn("direct_push", err.Error())
	}
	kerr := a.linkKeys(ctx)
	if err := a.saveRegistry(ctx, true); err != nil {
		return err
	}
	a.summaryLine()
	if verr != nil {
		return verr
	}
	return kerr
}

func (a *adder) warn(step, text string) {
	a.sum.Warnings = append(a.sum.Warnings, text)
	a.e.emit(step, "warn", text, nil)
}

// --- 1. connect -----------------------------------------------------------------------

func (a *adder) connect(ctx context.Context) error {
	cfg, err := a.e.SSH.G(ctx, a.o.Host)
	if err != nil {
		return exitf(ExitFailed, "%v", err)
	}
	a.cfg = cfg
	a.e.SSH.ApplyDefaults(cfg)
	if cfg.HostKeyCheckingOff() {
		a.warn("connect", fmt.Sprintf("your ssh config turns host key checking off for %s; wy does not override it, but you should", a.o.Host))
	}
	a.e.SSH.Batch = !a.e.Interactive
	res, err := a.e.SSH.Run(ctx, sshx.Call{Host: a.o.Host, Remote: []string{"uname", "-sm"}, Interactive: a.e.Interactive})
	if err != nil {
		var se *sshx.Error
		if errors.As(err, &se) && se.Class == sshx.ClassUnknownHostKey && !a.e.Interactive {
			return exitf(ExitFailed, "ssh %s once by hand to check and accept its host key, then run this again", a.o.Host)
		}
		return exitf(ExitFailed, "%v", err)
	}
	if res.Exit != 0 {
		return exitf(ExitFailed, "`uname -sm` failed on %s (exit %d): %s", a.o.Host, res.Exit, firstLine(res.Stderr))
	}
	goos, goarch, err := Platform(string(res.Stdout))
	if errors.Is(err, ErrWindows) {
		return exitf(ExitUsage, "%s runs Windows: use install.ps1 on that machine", a.o.Host)
	}
	if err != nil {
		return exitf(ExitUsage, "%v", err)
	}
	a.sum.OS, a.sum.Arch = goos, goarch
	a.e.emit("connect", "ok", fmt.Sprintf("connected to %s: %s/%s", a.o.Host, goos, goarch), map[string]any{"os": goos, "arch": goarch})
	return nil
}

// --- 2. probe -------------------------------------------------------------------------

func (a *adder) probe(ctx context.Context) error {
	dir := a.e.HostKeyDir
	if dir == "" {
		dir = "/etc/ssh"
	}
	res, err := a.r.script(ctx, preProbeScript, nil, dir)
	if err != nil {
		return exitf(ExitFailed, "%v", err)
	}
	p, err := parsePreProbe(res.Stdout)
	if err != nil {
		return exitf(ExitFailed, "%v", err)
	}
	a.pre = p
	if p.Rosetta && a.sum.OS == "darwin" {
		a.sum.Arch = "arm64"
	}
	state := "wy not installed"
	if p.Sy != "" {
		state = "wy " + p.Sy
	}
	claude := "claude not found on the login PATH"
	if p.Claude != "" {
		claude = "claude " + strings.TrimSpace(strings.TrimSuffix(p.ClaudeVersion, "(Claude Code)"))
	}
	a.e.emit("probe", "ok", fmt.Sprintf("probed %s: %s/%s, %s, %s", a.o.Host, a.sum.OS, a.sum.Arch, state, claude),
		map[string]any{"sy": p.Sy, "claude": p.Claude != "", "host_keys": len(p.HostKeys)})
	return nil
}

// probeNew runs `wy machine probe --json` with the installed wy (step 2, second half).
func (a *adder) probeNew(ctx context.Context) error {
	words := []string{RemoteSy, "machine", "probe", "--json", "--ports", fmt.Sprintf("%d-%d", FirstForwardPort, LastForwardPort)}
	if u := a.candidateURL(); u != "" {
		if err := sshx.CheckWord(u); err != nil {
			return exitf(ExitUsage, "--hub-url: %v", err)
		}
		words = append(words, "--hub-url", u)
	}
	res, err := a.r.run(ctx, words, nil)
	if err != nil {
		return exitf(ExitFailed, "%v", err)
	}
	if res.Exit != 0 {
		return exitf(ExitFailed, "`wy machine probe` failed on %s (exit %d): %s", a.o.Host, res.Exit, firstLine(res.Stderr))
	}
	var pr ProbeReport
	if err := json.Unmarshal(firstJSON(res.Stdout), &pr); err != nil || pr.Machine == "" {
		return exitf(ExitFailed, "unreadable answer from wy machine probe on %s", a.o.Host)
	}
	a.pr = &pr
	a.sum.Machine = pr.Machine
	// The registry entry is the one with this machine id; an entry found by name or host
	// word that belongs to another machine is not this machine's (3.1).
	if a.old.Machine != pr.Machine {
		a.old = Entry{}
		if e, ok := a.reg.FindMachine(pr.Machine); ok {
			a.old = e
		}
	}
	if a.sum.Name == "" {
		a.sum.Name = a.o.Name
	}
	if a.sum.Name == "" {
		if a.old.Name != "" {
			a.sum.Name = a.old.Name
		} else {
			a.sum.Name = a.reg.UniqueName(pr.Name, pr.Machine)
		}
	}
	if a.sum.Name != a.old.Name && a.reg.NameTaken(a.sum.Name, pr.Machine) {
		return exitf(ExitUsage, "the name %q belongs to another machine in the registry: pass a different --name (wy machine list)", a.sum.Name)
	}
	hub := ""
	if pr.Hub != nil {
		if pr.Hub.Reachable {
			hub = ", reaches the hub (" + pr.Hub.Version + ")"
		} else {
			hub = ", cannot reach " + pr.Hub.URL + ": " + pr.Hub.Error
		}
	}
	a.e.emit("probe", "ok", fmt.Sprintf("%s is machine %s (%s)%s", a.o.Host, pr.Name, pr.Machine, hub),
		map[string]any{"machine": pr.Machine, "name": pr.Name, "free_ports": len(pr.Free)})
	return nil
}

// candidateURL is the hub address the remote would use directly (3.2): --hub-url, else the
// operator's hub.url when it is not loopback. A loopback URL is never offered to a remote.
func (a *adder) candidateURL() string {
	if a.o.HubURL != "" {
		return a.o.HubURL
	}
	if a.e.OperatorHub != "" && !isLoopbackURL(a.e.OperatorHub) {
		return a.e.OperatorHub
	}
	return ""
}

func isLoopbackURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := p.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (a *adder) dryRun() {
	reach := a.o.Reach
	steps := []string{
		fmt.Sprintf("install wy %s for %s/%s into ~/.local/bin/wy (SHA-256 checked on the remote)", a.targetVersion(), a.sum.OS, a.sum.Arch),
	}
	switch reach {
	case ReachPull:
		steps = append(steps, "register a pull target on the hub ("+a.adminName()+")", "wy setup --hub pull, then authorize the hub's key in ~/.ssh/authorized_keys")
	case ReachForward:
		steps = append(steps, "mint a machine pairing code ("+a.adminName()+")", "write the forward block in ~/.ssh/watchyard.conf", "wy setup --hub join:<url> --code-stdin --reach forward")
	default:
		steps = append(steps, "mint a machine pairing code ("+a.adminName()+")", "wy setup --hub join:<url> --code-stdin (reach "+reach+")")
	}
	steps = append(steps, "verify on the hub", "offer logins")
	if reach != ReachPull {
		steps = append(steps, "register the link keys over SSH (reg ssh), pin the hub and owner keys there, offer the owner signature")
	}
	steps = append(steps, "save the registry entry")
	for _, s := range steps {
		a.e.emit("plan", "plan", s, nil)
	}
}

func (a *adder) adminName() string {
	if a.e.Admin == nil {
		return "pasted code"
	}
	return a.e.Admin.Describe()
}

// --- 3. binary ------------------------------------------------------------------------

func (a *adder) targetVersion() string {
	if a.o.Version != "" {
		return strings.TrimPrefix(a.o.Version, "v")
	}
	return a.e.SelfVersion
}

func isRelease(v string) bool {
	_, ok := update.CompareVersions(v, v)
	return ok && !strings.Contains(v, "dev")
}

func (a *adder) binary(ctx context.Context) error {
	want := a.targetVersion()
	var bin, sum string
	switch {
	case a.o.Binary != "":
		goos, goarch, err := binPlatform(a.o.Binary)
		if err == nil && (goos != a.sum.OS || goarch != a.sum.Arch) {
			return exitf(ExitUsage, "--binary is for %s/%s, but %s is %s/%s", goos, goarch, a.o.Host, a.sum.OS, a.sum.Arch)
		}
		bin = a.o.Binary
		a.sum.DevBuild = true
	case a.o.Version == "" && !isRelease(want) && a.sum.OS == runtime.GOOS && a.sum.Arch == runtime.GOARCH && a.e.SelfExe != "":
		bin = a.e.SelfExe
		a.sum.DevBuild = true
	default:
		if !isRelease(want) {
			return exitf(ExitUsage, "this wy is %q, not a release, and %s is %s/%s: pass --version <release> or --binary <file>", want, a.o.Host, a.sum.OS, a.sum.Arch)
		}
		if a.pre.Sy == want {
			a.sum.Sy = want
			a.e.emit("binary", "skip", fmt.Sprintf("%s already runs wy %s", a.o.Host, want), nil)
			return nil
		}
		f := a.e.Fetcher
		if f == nil {
			f = update.Real{}
		}
		rel, err := f.Resolve(ctx, a.e.Source, want)
		if err != nil {
			return &ExitError{Code: ExitFailed, Err: err}
		}
		arch, err := f.Fetch(ctx, rel, a.sum.OS, a.sum.Arch, filepath.Join(a.e.StateDir, "update", "cache", a.sum.OS+"_"+a.sum.Arch))
		if err != nil {
			return &ExitError{Code: ExitFailed, Err: err}
		}
		if bin, sum, err = f.Extract(arch); err != nil {
			return &ExitError{Code: ExitFailed, Err: err}
		}
	}
	if sum == "" {
		var err error
		if sum, err = fileSHA256(bin); err != nil {
			return err
		}
	}
	if a.pre.SySHA256 != "" && a.pre.SySHA256 == sum {
		a.sum.Sy = a.pre.Sy
		a.e.emit("binary", "skip", fmt.Sprintf("%s already has this wy (%s)", a.o.Host, short(sum)), nil)
		return nil
	}
	wantV := ""
	if isRelease(want) && !a.sum.DevBuild {
		wantV = want
	}
	p, err := a.r.push(ctx, bin, "bin", "wy", wantV)
	if err != nil {
		return exitf(ExitFailed, "%v", err)
	}
	a.sum.Sy = want
	note := "checksum matched on the remote"
	if !p.Verified {
		note = "no hash tool on the remote: checked the version instead"
		a.warn("binary", fmt.Sprintf("%s has no SHA-256 tool, so the copy was checked by its version only", a.o.Host))
	}
	if a.sum.DevBuild {
		note += "; not a release build"
	}
	a.e.emit("binary", "ok", fmt.Sprintf("installed wy %s on %s (%s)", want, a.o.Host, note), map[string]any{"sha256": sum})
	return nil
}

// binPlatform reads GOOS/GOARCH from a Go binary's build info.
func binPlatform(p string) (string, string, error) {
	bi, err := buildinfo.ReadFile(p)
	if err != nil {
		return "", "", err
	}
	var goos, goarch string
	for _, s := range bi.Settings {
		switch s.Key {
		case "GOOS":
			goos = s.Value
		case "GOARCH":
			goarch = s.Value
		}
	}
	if goos == "" || goarch == "" {
		return "", "", errors.New("no platform in the build info")
	}
	return goos, goarch, nil
}

// --- reach ----------------------------------------------------------------------------

func (a *adder) decideReach() error {
	reach := a.o.Reach
	if reach == ReachAuto && a.old.Reach != "" && a.o.HubURL == "" {
		reach = a.old.Reach // a rerun keeps what worked
	}
	if reach == ReachAuto {
		switch {
		case a.pr.Hub != nil && a.pr.Hub.Reachable:
			reach = ReachDirect
		case a.e.Interactive && !a.o.Yes:
			opts := []string{ReachPull, ReachForward}
			if !a.e.HubLocal {
				opts = []string{ReachForward, ReachPull}
			}
			reach = a.e.Choose(fmt.Sprintf("%s cannot reach the hub. How should its records get there?", a.o.Host), opts)
		default:
			return exitf(ExitUsage, "the remote cannot reach the hub; pass --reach forward or --reach pull")
		}
	}
	if reach == ReachPull && a.e.Admin == nil {
		return exitf(ExitUsage, "pull mode needs the hub host: pass --hub-admin local or a command prefix such as \"ssh hub-host wy\"")
	}
	a.sum.Reach = reach
	return nil
}

// --- 4. hub admin ---------------------------------------------------------------------

// credential is what the remote setup gets on stdin: the pairing secret (or pasted digits),
// or the hub's pull public key. keep means the remote is already connected as asked.
type credential struct {
	secret string
	keep   bool
	pubKey string
}

// remoteURL is the hub address as the remote will use it.
func (a *adder) remoteURL(port int) (string, error) {
	switch a.sum.Reach {
	case ReachForward:
		if a.e.OperatorHub == "" {
			return "", exitf(ExitUsage, "forward mode needs this machine's hub.url (wy connect … here first)")
		}
		if isLoopbackURL(a.e.OperatorHub) {
			return "http://127.0.0.1:" + strconv.Itoa(port), nil
		}
		return a.e.OperatorHub, nil
	case ReachDirect:
		return a.candidateURL(), nil
	}
	return "", nil
}

// forwardTarget is the right side of the RemoteForward: the hub as the operator reaches it.
func (a *adder) forwardTarget() (string, error) {
	u, err := url.Parse(a.e.OperatorHub)
	if err != nil || u.Host == "" {
		return "", exitf(ExitUsage, "forward mode needs this machine's hub.url (wy connect … here first)")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

func (a *adder) pickPort() (int, error) {
	if a.o.Port != 0 {
		return a.o.Port, nil
	}
	if a.old.Port != 0 {
		return a.old.Port, nil
	}
	if p, _, ok := OwnedForward(a.e.Home, a.sum.Machine); ok && p != 0 {
		return p, nil // the block a former run wrote
	}
	used := a.reg.UsedPorts(a.sum.Machine, a.o.Host)
	for _, p := range a.pr.Free {
		if !used[p] && p >= FirstForwardPort && p <= LastForwardPort {
			return p, nil
		}
	}
	return 0, exitf(ExitFailed, "no free forward port in %d-%d on %s", FirstForwardPort, LastForwardPort, a.o.Host)
}

func (a *adder) hubAdmin(ctx context.Context) (credential, error) {
	if a.sum.Reach == ReachForward {
		target, err := a.forwardTarget()
		if err != nil {
			return credential{}, err
		}
		f := &Forward{Host: HostPattern(a.o.Host), Machine: a.sum.Machine, Name: a.sum.Name, Target: target}
		if a.o.ForwardSocket {
			f.Socket = filepath.ToSlash(filepath.Join(a.pr.State, "hub.sock"))
		} else if p, ok := a.adopted(target, ""); ok && a.o.Port == 0 {
			f.Port = p
		} else if f.Port, err = a.pickPort(); err != nil {
			return credential{}, err
		}
		a.fw = f
		a.sum.Port = f.Port
	}
	url, err := a.remoteURL(a.sum.Port)
	if err != nil {
		return credential{}, err
	}
	a.sum.HubURL = url

	if a.sum.Reach == ReachPull {
		return a.pullAdmin(ctx)
	}
	c := a.pr.Connected
	if c.Token && c.URL != "" && c.URL == url && (c.Reach == a.sum.Reach || c.Reach == "" && a.sum.Reach == ReachDirect) &&
		(a.fw == nil || c.Dial == a.dialFor()) {
		a.e.emit("hub", "skip", fmt.Sprintf("%s is already connected to %s", a.o.Host, url), nil)
		return credential{keep: true}, nil
	}
	if a.o.CodeStdin {
		if url == "" {
			return credential{}, exitf(ExitUsage, "pass --hub-url: the address %s uses to reach the hub", a.o.Host)
		}
		a.e.emit("hub", "skip", "using the pairing code from stdin", nil)
		return credential{secret: a.o.Code}, nil
	}
	if a.e.Admin == nil {
		if !a.e.Interactive || a.e.ReadSecret == nil {
			return credential{}, exitf(ExitNeedsPerson, "no admin channel to the hub: pass --hub-admin \"ssh <hub host> wy\", or --code-stdin with a code from the board's Add machine")
		}
		if url == "" {
			return credential{}, exitf(ExitUsage, "pass --hub-url: the address %s uses to reach the hub", a.o.Host)
		}
		code, err := a.e.ReadSecret(fmt.Sprintf("Open %s/#/more/devices -> Add machine, and paste the code here: ", strings.TrimRight(url, "/")))
		if err != nil || strings.TrimSpace(code) == "" {
			return credential{}, exitf(ExitNeedsPerson, "no pairing code given")
		}
		return credential{secret: strings.TrimSpace(code)}, nil
	}
	pc, err := MintMachineCode(ctx, a.e.Admin, a.sum.Name, a.sum.Machine, url)
	if err != nil {
		return credential{}, exitf(ExitFailed, "%v", err)
	}
	if url == "" {
		url = pc.Origin()
		a.sum.HubURL = url
	}
	if url == "" || isLoopbackURL(url) && a.sum.Reach == ReachDirect && a.o.HubURL == "" {
		return credential{}, exitf(ExitUsage, "the hub has no address a remote can use: pass --hub-url")
	}
	a.e.emit("hub", "ok", fmt.Sprintf("made a machine pairing code for %s through %s (valid 10 minutes, passed on stdin)", a.sum.Name, a.adminName()), nil)
	return credential{secret: pc.Secret}, nil
}

// adopted is Adopted for the person's own forwards: the block wy wrote for this machine in
// watchyard.conf is wy's (ssh -G shows it too), never the person's.
func (a *adder) adopted(target, socket string) (int, bool) {
	if _, _, owned := OwnedForward(a.e.Home, a.sum.Machine); owned {
		return 0, false
	}
	return Adopted(a.cfg, target, socket)
}

func (a *adder) dialFor() string {
	if a.fw == nil {
		return ""
	}
	if isLoopbackURL(a.e.OperatorHub) && a.fw.Socket == "" {
		return "" // the remote URL is already the forwarded loopback port
	}
	return a.fw.Dial()
}

func (a *adder) pullAdmin(ctx context.Context) (credential, error) {
	if len(a.pre.HostKeys) == 0 {
		return credential{}, exitf(ExitFailed, "cannot read the host keys of %s (/etc/ssh/ssh_host_*_key.pub); register it by hand with `wy hub pull add --fingerprint SHA256:…`", a.o.Host)
	}
	t := PullTarget{Name: a.sum.Name, User: a.cfg.User(), Host: a.cfg.HostName(), Port: a.cfg.Port(), Jump: a.cfg.ProxyJump()}
	if t.Host == "" {
		t.Host = HostPattern(a.o.Host)
	}
	if t.User == "" {
		if i := strings.LastIndexByte(a.o.Host, '@'); i > 0 {
			t.User = a.o.Host[:i]
		}
	}
	added, err := AddPullTarget(ctx, a.e.Admin, t, a.sum.Machine, a.pre.HostKeys)
	if err != nil {
		return credential{}, exitf(ExitFailed, "%v", err)
	}
	a.sum.PullID, a.sum.PullName = added.Target, added.Name
	a.e.emit("hub", "ok", fmt.Sprintf("registered pull target %s (%s) on the hub with %d pinned host key(s)", added.Name, added.Target, len(a.pre.HostKeys)), nil)
	return credential{pubKey: added.PublicKey}, nil
}

// --- 5. forward -----------------------------------------------------------------------

func (a *adder) forward(ctx context.Context) error {
	f := a.fw
	if p, ok := a.adopted(f.Target, f.Socket); ok && (f.Socket != "" || p == f.Port) {
		a.e.emit("forward", "skip", fmt.Sprintf("your ssh config already forwards %s to %s for %s (adopted)", f.Listen(), f.Target, a.o.Host), nil)
		a.old.ForwardAdopted = true
	} else {
		res, err := WriteForward(a.e.Home, a.e.StateDir, *f, a.e.Command, a.e.now())
		if err != nil {
			return exitf(ExitFailed, "writing the forward block: %v", err)
		}
		if res.Set != "" {
			a.old.ForwardSet = res.Set
		}
		a.old.ForwardAdopted = false
		a.sum.ChangedFw = res.Changed
		status := "skip"
		text := fmt.Sprintf("~/.ssh/watchyard.conf already forwards %s for %s", f.Listen(), a.o.Host)
		if res.Changed {
			status, text = "ok", fmt.Sprintf("wrote the forward block for %s in ~/.ssh/watchyard.conf (port %s, backup %s)", HostPattern(a.o.Host), f.Listen(), res.Set)
		}
		a.e.emit("forward", status, text, nil)
	}
	a.fwArgs = []string{"-o", "ExitOnForwardFailure=yes", "-R", f.R()}
	return nil
}

// runFwd runs a remote call with this run's forward when in forward mode; if the port is
// already forwarded by another connection, it retries without -R (4.2 step 5).
func (a *adder) runFwd(ctx context.Context, words []string, stdin []byte) (sshx.Result, error) {
	c := sshx.Call{Host: a.o.Host, Remote: words, Extra: a.fwArgs}
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	res, err := a.e.SSH.Run(ctx, c)
	var se *sshx.Error
	if len(a.fwArgs) > 0 && errors.As(err, &se) && se.Class == sshx.ClassForward {
		// Something else holds the port. Only a hub may get the secret.
		if err := a.checkPortHolder(ctx); err != nil {
			return res, err
		}
		c.Extra = nil
		if stdin != nil {
			c.Stdin = bytes.NewReader(stdin)
		}
		return a.e.SSH.Run(ctx, c)
	}
	return res, err
}

// checkPortHolder runs when the forward port is already taken on the remote, before the
// pairing secret travels to it: over a plain-http forwarded URL, whatever listens there
// must answer /health like this operator's hub (the same version and schema). Another
// user's process on a shared host must not receive the secret. The check is skipped for
// https (the certificate names the hub) and when the URL is not the forwarded loopback port.
// /health carries no hub id, so version and schema are the identity available.
func (a *adder) checkPortHolder(ctx context.Context) error {
	url := a.sum.HubURL
	if !strings.HasPrefix(url, "http://") || a.fw == nil || a.fw.Socket != "" {
		return nil
	}
	want, err := a.operatorHealth(ctx)
	if err != nil {
		return exitf(ExitFailed, "port %d on %s is already taken, and this machine's hub did not answer /health, so the holder cannot be checked: %v", a.fw.Port, a.o.Host, err)
	}
	res, err := a.r.run(ctx, []string{RemoteSy, "machine", "probe", "--json", "--hub-url", url}, nil)
	if err != nil {
		return exitf(ExitFailed, "%v", err)
	}
	var pr ProbeReport
	if res.Exit != 0 || json.Unmarshal(firstJSON(res.Stdout), &pr) != nil || pr.Hub == nil || !pr.Hub.Reachable ||
		pr.Hub.Version != want.Version || pr.Hub.Schema != want.Schema {
		return exitf(ExitFailed, "port %d on %s is already forwarded or in use, and what answers there is not your hub (its /health differs): free the port or pass another --port", a.fw.Port, a.o.Host)
	}
	return nil
}

func (a *adder) operatorHealth(ctx context.Context) (client.Health, error) {
	if a.e.HubHealth != nil {
		return a.e.HubHealth(ctx)
	}
	c := client.New(a.e.OperatorHub, "")
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c.Health(ctx)
}

// --- 6. setup on the remote -----------------------------------------------------------

func (a *adder) setupWords(cred credential) ([]string, error) {
	w := []string{RemoteSy, "setup", "--yes", "--json", "--login", "never"}
	switch {
	case a.sum.Reach == ReachPull:
		w = append(w, "--hub", "pull")
	case cred.keep:
		w = append(w, "--hub", "keep")
	default:
		w = append(w, "--hub", "join:"+a.sum.HubURL, "--code-stdin")
		if a.o.InsecureLAN {
			w = append(w, "--insecure-lan")
		}
		if a.sum.Reach == ReachForward {
			if d := a.dialFor(); d != "" {
				w = append(w, "--hub-dial", d)
			}
			w = append(w, "--reach", "forward")
		}
	}
	if a.o.Accounts != "" {
		w = append(w, "--accounts", a.o.Accounts)
	}
	if a.o.Share != "" {
		w = append(w, "--share", a.o.Share)
	}
	for _, x := range w {
		if err := sshx.CheckWord(x); err != nil {
			return nil, exitf(ExitUsage, "%v", err)
		}
	}
	return w, nil
}

func (a *adder) setup(ctx context.Context, cred credential) (setupReply, error) {
	words, err := a.setupWords(cred)
	if err != nil {
		return setupReply{}, err
	}
	var stdin []byte
	if cred.secret != "" {
		stdin = []byte(cred.secret + "\n")
	}
	res, err := a.runFwd(ctx, words, stdin)
	if err != nil {
		return setupReply{}, exitf(ExitFailed, "%v", err)
	}
	var rep setupReply
	_ = json.NewDecoder(bytes.NewReader(jsonStart(res.Stdout))).Decode(&rep) // one indented object
	switch res.Exit {
	case 0:
	case ExitNoClaude:
		return rep, exitf(ExitNoClaude, "Claude Code is not installed on %s: install Claude Code on %s, then run this again (wy stays installed)", a.o.Host, a.o.Host)
	case ExitVerify:
		msg := "the hub step failed"
		if rep.Hub != nil && rep.Hub.Error != "" {
			msg = rep.Hub.Error
		}
		return rep, exitf(ExitFailed, "wy setup on %s set up the machine but could not join the hub: %s (%s)", a.o.Host, msg, firstLine(res.Stderr))
	default:
		return rep, exitf(ExitFailed, "wy setup on %s failed (exit %d): %s", a.o.Host, res.Exit, lastLine(res.Stderr))
	}
	a.e.emit("setup", "ok", fmt.Sprintf("wy setup ran on %s (%s)", a.o.Host, a.sum.Reach), nil)
	if a.sum.Reach == ReachPull {
		aw := []string{RemoteSy, "machine", "authorize-pull", "--key-stdin", "--target", a.sum.PullID}
		if a.o.PullFrom != "" {
			aw = append(aw, "--from", a.o.PullFrom)
		}
		if a.pr != nil && !a.pr.Restrict {
			aw = append(aw, "--no-restrict")
		}
		r, err := a.r.run(ctx, aw, []byte(cred.pubKey+"\n"))
		if err != nil {
			return rep, exitf(ExitFailed, "%v", err)
		}
		if r.Exit != 0 {
			return rep, exitf(ExitFailed, "authorize-pull on %s failed (exit %d): %s", a.o.Host, r.Exit, firstLine(r.Stderr))
		}
		a.e.emit("setup", "ok", fmt.Sprintf("authorized the hub's pull key on %s (forced command wy outbox drain --stdio)", a.o.Host), nil)
	}
	return rep, nil
}

// jsonStart skips anything printed before the report object.
func jsonStart(out []byte) []byte {
	if i := bytes.IndexByte(out, '{'); i >= 0 {
		return out[i:]
	}
	return out
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// --- 7. verify ------------------------------------------------------------------------

func (a *adder) verify(ctx context.Context) error {
	if a.e.Admin == nil {
		a.e.emit("verify", "skip", "no admin channel: check the board's Machines screen", nil)
		return nil
	}
	if a.sum.Reach == ReachPull {
		row, err := PullRunWait(ctx, a.e.Admin, a.sum.PullName)
		if err != nil || row.State != "ok" {
			detail := ""
			if row.LastError != nil {
				detail = ": " + *row.LastError
			}
			if err != nil {
				detail = ": " + err.Error()
			}
			return exitf(ExitVerify, "%s is set up, but the hub's first pull did not succeed (state %s%s); see wy hub pull list on the hub", a.o.Host, row.State, detail)
		}
		a.sum.Verified = true
		a.e.emit("verify", "ok", fmt.Sprintf("the hub pulled %s", a.sum.Name), nil)
		return nil
	}
	var m HubMachine
	var err error
	for i := 0; i < 3; i++ {
		m, err = MachineShow(ctx, a.e.Admin, a.sum.Machine)
		if err == nil && m.SeenWithin(a.e.now(), time.Minute) {
			a.sum.Verified = true
			a.e.emit("verify", "ok", fmt.Sprintf("the hub saw %s just now", a.sum.Name), nil)
			return nil
		}
		var ae *AdminError
		if errors.As(err, &ae) && ae.NotImplemented() {
			a.warn("verify", "the hub's wy cannot show machines yet; check the board's Machines screen")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		return exitf(ExitVerify, "%s joined, but asking the hub about it failed: %v", a.o.Host, err)
	}
	return exitf(ExitVerify, "%s joined, but the hub has not seen it in the last minute; run `ssh %s .local/bin/wy doctor`", a.o.Host, a.o.Host)
}

// --- 8. logins ------------------------------------------------------------------------

func (a *adder) logins(ctx context.Context, rep setupReply) {
	login := a.o.Login
	if login == "" {
		login = "never"
		if a.e.Interactive {
			login = "ask"
		}
	}
	for _, n := range rep.Plan.Needs {
		if n.Kind != "login" || n.Folder == "" {
			continue
		}
		if strings.ContainsAny(n.Folder, "\x00\r\n") {
			a.warn("login", fmt.Sprintf("%s reported a folder name with control characters; log it in by hand", a.o.Host))
			continue
		}
		cmd := LoginCommand(a.o.Host, n.Folder)
		if sshx.CheckWord(n.Folder) != nil || login != "ask" || !a.e.Interactive || a.e.Ask == nil ||
			!a.e.Ask(fmt.Sprintf("Log in %s on %s now?", n.Folder, a.o.Host), true) {
			a.sum.Logins = append(a.sum.Logins, cmd)
			continue
		}
		code, err := a.e.SSH.RunTTY(ctx, sshx.Call{Host: a.o.Host, Remote: []string{RemoteSy, "account", "login", n.Folder}})
		if err != nil || code != 0 {
			a.warn("login", fmt.Sprintf("logging in %s on %s did not finish; run: %s", n.Folder, a.o.Host, cmd))
			a.sum.Logins = append(a.sum.Logins, cmd)
			continue
		}
		a.e.emit("login", "ok", fmt.Sprintf("logged in %s on %s", n.Folder, a.o.Host), nil)
	}
	if len(a.sum.Logins) > 0 {
		a.e.emit("login", "skip", fmt.Sprintf("%d folder(s) still need a login: %s", len(a.sum.Logins), strings.Join(a.sum.Logins, "; ")), nil)
	}
}

// LoginCommand is the command that logs a folder in on the remote, quoted to be shown and
// pasted into the operator's shell: ~ stays literal for the remote wy (an unquoted one would
// expand here), and a folder name that is not a plain word is quoted for both shells.
func LoginCommand(host, folder string) string {
	words := []string{RemoteSy, "account", "login", folder}
	if sshx.CheckWord(folder) == nil {
		q := make([]string, len(words))
		for i, w := range words {
			q[i] = shellQuote(w)
		}
		return "ssh -t " + host + " " + strings.Join(q, " ")
	}
	// The remote shell reads the words again: single-quote each for it, then the whole
	// command line for the local shell.
	rem := make([]string, len(words))
	for i, w := range words {
		rem[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return "ssh -t " + host + " " + shellQuote(strings.Join(rem, " "))
}

// shellQuote quotes one word for a POSIX shell (plain words stay as they are).
func shellQuote(w string) string {
	if w != "" && !strings.ContainsAny(w, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// --- 9. direct push -------------------------------------------------------------------

func (a *adder) directPush(ctx context.Context) error {
	if !a.o.DirectPush {
		return nil
	}
	if a.e.DirectPush == nil {
		return errors.New("direct push is not set up on this machine (wy direct-push on here first)")
	}
	server, private, secret, err := a.e.DirectPush()
	if err != nil {
		return err
	}
	w := []string{RemoteSy, "direct-push", "on", "--server", server, "--private=" + strconv.FormatBool(private), "--secret-stdin"}
	res, err := a.r.run(ctx, w, secret)
	if err != nil {
		return err
	}
	if res.Exit != 0 {
		return fmt.Errorf("wy direct-push on failed on %s (exit %d): %s", a.o.Host, res.Exit, firstLine(res.Stderr))
	}
	a.e.emit("direct_push", "ok", fmt.Sprintf("direct push is on for %s", a.o.Host), nil)
	return nil
}

// --- 10. registry ---------------------------------------------------------------------

// saveRegistry stores the entry. done says the run reached the end: only then does a machine
// that left pull mode lose the hub's key and its pull target.
func (a *adder) saveRegistry(ctx context.Context, done bool) error {
	if a.sum.Machine == "" {
		return nil
	}
	e := Entry{Name: a.sum.Name, Machine: a.sum.Machine, SSH: a.o.Host, Reach: a.sum.Reach, OS: a.sum.OS, Arch: a.sum.Arch,
		SyVersion: a.sum.Sy, HubURL: a.sum.HubURL, PullTarget: a.sum.PullID, Hub: a.old.Hub,
		ForwardSet: a.old.ForwardSet, ForwardAdopted: a.old.ForwardAdopted}
	if a.fw != nil {
		e.Port, e.Socket, e.ForwardTarget = a.fw.Port, a.fw.Socket, a.fw.Target
	}
	if a.old.PullTarget != "" && e.Reach != ReachPull {
		// Leaving pull mode: the hub's key must not stay in authorized_keys, nor the target on
		// the hub. PullTarget stays in the entry until both are gone, so a rerun or
		// `wy machine remove` finishes the job.
		if !done || !a.dropPull(ctx, a.old.PullTarget) {
			e.PullTarget = a.old.PullTarget
		}
	}
	if a.old.Reach == ReachForward && e.Reach != ReachForward && a.old.ForwardSet != "" {
		// Leaving forward: its block goes (3.6).
		if _, err := RemoveForward(a.e.Home, a.e.StateDir, a.old.Machine, a.old.ForwardSet, a.e.Command, a.e.now()); err != nil {
			a.warn("forward", "could not remove the old forward block: "+err.Error())
		}
		e.ForwardSet = ""
	}
	a.reg.Upsert(e, a.e.now())
	if err := a.reg.Save(); err != nil {
		return err
	}
	a.e.emit("registry", "ok", fmt.Sprintf("saved %s in %s", a.sum.Name, RegistryPath(a.e.StateDir)), nil)
	return nil
}

// dropPull removes the hub's pull key from the remote (authorize-pull --remove) and the pull
// target from the hub; true only when both succeeded.
func (a *adder) dropPull(ctx context.Context, target string) bool {
	ok := true
	out, err := a.r.run(ctx, []string{RemoteSy, "machine", "authorize-pull", "--remove", "--target", target}, nil)
	switch {
	case err != nil:
		a.warn("pull", fmt.Sprintf("could not remove the hub's key from %s: %v", a.o.Host, err))
		ok = false
	case out.Exit != 0:
		a.warn("pull", fmt.Sprintf("could not remove the hub's key from %s: %s", a.o.Host, firstLine(out.Stderr)))
		ok = false
	default:
		a.e.emit("pull", "ok", "removed the hub's pull key from ~/.ssh/authorized_keys on "+a.o.Host, nil)
	}
	if a.e.Admin == nil {
		a.warn("pull", "no admin channel: remove the pull target on the hub with wy hub pull remove")
		return false
	}
	name := pullNameFor(ctx, a.e.Admin, target, a.old.Name)
	if _, err := a.e.Admin.Hub(ctx, []string{"pull", "remove", name}, nil); err != nil {
		a.warn("pull", fmt.Sprintf("could not remove pull target %s on the hub: %v", name, err))
		return false
	}
	a.e.emit("pull", "ok", "removed pull target "+name+" on the hub", nil)
	return ok
}

func (a *adder) summaryLine() {
	via := map[string]string{ReachDirect: "direct", ReachForward: "forward, through your SSH connections", ReachPull: "pulled by the hub"}[a.sum.Reach]
	where := a.sum.HubURL
	if a.sum.Reach == ReachPull {
		where = "the hub"
	}
	a.sum.Next = fmt.Sprintf("Next: ssh %s and start Claude.", a.o.Host)
	a.e.emit("done", "ok", fmt.Sprintf("%s reports to %s (%s). %s", a.sum.Name, where, via, a.sum.Next), nil)
}

// keep the io import for Env docs (ReadSecret readers are supplied by the command layer).
var _ io.Reader = (*os.File)(nil)
