package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/service"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/update"
	"github.com/mraj602/watchyard/internal/version"
)

// fleetDeps supplies the machine registry and the SSH runner for `wy update --machines`
// (machines.md 7.4). machine_fleet.go sets it from an init(); it is nil only in a test
// binary that leaves that file out.
var fleetDeps func() (update.Registry, update.Remote, error)

// updateEnv is everything runUpdate reads from the machine; tests replace it.
type updateEnv struct {
	stdin        io.Reader
	interactive  bool
	exe          string // resolved running binary
	current      string
	dirs         paths.Dirs
	cfg          config.Config
	getenv       func(string) string
	goos, goarch string
	run          update.RunFunc
	postUpdate   func(ctx context.Context, exe, old string, stdout, stderr io.Writer) error
	hubHealth    func(ctx context.Context, url string) (client.Health, error)
	fleet        func() (update.Registry, update.Remote, error)
}

func osUpdateEnv() (updateEnv, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return updateEnv{}, err
	}
	exe, err := os.Executable()
	if err == nil {
		if r, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = r
		}
	}
	if err != nil {
		return updateEnv{}, fmt.Errorf("cannot tell which wy is running: %w", err)
	}
	var cfg config.Config
	if f, err := config.Load(d.Config.Path); err == nil {
		cfg, _ = f.Typed()
	} else {
		cfg = config.Defaults()
	}
	goos, goarch := update.Platform()
	return updateEnv{
		stdin: os.Stdin, interactive: term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())),
		exe: exe, current: version.Version, dirs: d, cfg: cfg, getenv: os.Getenv, goos: goos, goarch: goarch,
		postUpdate: update.StartPostUpdate,
		hubHealth: func(ctx context.Context, url string) (client.Health, error) {
			if url == cfg.Hub.URL {
				// This machine's hub: through hub.dial when it is set (a forward).
				cl, err := client.ForConfig(cfg, "")
				if err != nil {
					return client.Health{}, err
				}
				return cl.Health(ctx)
			}
			return client.New(url, "").Health(ctx)
		},
		fleet: func() (update.Registry, update.Remote, error) {
			if fleetDeps == nil {
				return nil, nil, errors.New("--machines: no machine registry in this build")
			}
			return fleetDeps()
		},
	}, nil
}

// runUpdate is `wy update [--version v] [--check] [--yes] [--json] [--allow-downgrade] [--no-attestation]
// [--from-file f --sums-file s] [--rollback] [--machines all|a,b]` (machines.md 7.3, 7.4).
// Exit codes: 0 updated or current; 2 usage; 3 verification failed; 4 download failed;
// 5 refused; 1 replace, post-update or (with --machines) any machine failed.
func runUpdate(args []string, stdout, stderr io.Writer) error {
	env, err := osUpdateEnv()
	if err != nil {
		return err
	}
	return updateMain(context.Background(), env, args, stdout, stderr)
}

type updateFlags struct {
	version        string
	check, yes     bool
	asJSON         bool
	allowDowngrade bool
	fromFile       string
	sumsFile       string
	rollback       bool
	machines       string
	postUpdate     string
	noAttestation  bool
}

func (e updateEnv) source() update.Source {
	base := e.getenv("WY_INSTALL_BASE_URL")
	if base == "" {
		base = e.cfg.Update.BaseURL
	}
	return update.Source{BaseURL: base}
}

// attestation is the build-attestation policy: on when gh is installed, unless the person
// turned it off (--no-attestation, WY_VERIFY_ATTESTATION=0, update.verify_attestation =
// false); WY_VERIFY_ATTESTATION=1 makes a missing gh an error.
func (e updateEnv) attestation(f updateFlags) update.Attestation {
	switch env := e.getenv("WY_VERIFY_ATTESTATION"); {
	case f.noAttestation || env == "0":
		return update.Attestation{}
	case env == "1":
		return update.Attestation{Required: true}
	default:
		return update.Attestation{Auto: e.cfg.Update.VerifyAttestation}
	}
}

func (e updateEnv) options(f updateFlags) update.Options {
	o := update.Options{
		Source: e.source(), Version: f.version, Current: e.current, Exe: e.exe,
		StateDir: e.dirs.State.Path, GOOS: e.goos, GOARCH: e.goarch, AllowDowngrade: f.allowDowngrade,
		FromFile: f.fromFile, SumsFile: f.sumsFile, Run: e.run,
		Attestation: e.attestation(f),
	}
	// On a hub host the new binary must open the database as it is. A database that exists
	// but cannot be read leaves the guard unable to vouch: Apply and Rollback refuse.
	for _, p := range e.hubDBPaths() {
		v, err := store.ReadSchemaVersion(context.Background(), p)
		if err != nil {
			o.HubDBErr = fmt.Sprintf("%s: %v", p, err)
			break
		}
		if v > o.HubSchema {
			o.HubSchema = v
		}
	}
	return o
}

// hubDBPaths are the hub database files this machine may serve: <data>/hub.db, and what the
// installed hub service really opens (its --db, or the data folder from the WY_* variables
// in its unit, which a non-interactive `wy update` over ssh does not carry).
func (e updateEnv) hubDBPaths() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(filepath.Join(e.dirs.Data.Path, "hub.db"))
	home, _ := os.UserHomeDir()
	name := service.DefaultName
	if led, err := ledger.Read(e.dirs.State.Path); err == nil && led.HubService != nil && led.HubService.Name != "" {
		name = led.HubService.Name
	}
	se := service.Env{GOOS: e.goos, Home: home, StateDir: e.dirs.State.Path, Getenv: e.getenv}
	args, env, ok := service.InstalledUnit(se, name)
	if !ok {
		return out
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--db" && i+1 < len(args):
			add(args[i+1])
		case strings.HasPrefix(args[i], "--db="):
			add(strings.TrimPrefix(args[i], "--db="))
		}
	}
	unitEnv := map[string]string{}
	for _, kv := range env {
		unitEnv[kv[0]] = kv[1]
	}
	if len(unitEnv) > 0 {
		if d, err := paths.Resolve(paths.Env{GOOS: e.goos, Home: home, Getenv: func(k string) string { return legacyEnvLookup(unitEnv, k) }, Exists: paths.StatExists}); err == nil {
			add(filepath.Join(d.Data.Path, "hub.db"))
		}
	}
	return out
}

func updateMain(ctx context.Context, e updateEnv, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f updateFlags
	fs.StringVar(&f.version, "version", "", "update to this version (default latest)")
	fs.BoolVar(&f.check, "check", false, "only say what is current and what is latest")
	fs.BoolVar(&f.noAttestation, "no-attestation", false, "do not check the build attestation with gh (the checksum is still checked)")
	fs.BoolVar(&f.yes, "yes", false, "do not ask for confirmation")
	fs.BoolVar(&f.asJSON, "json", false, "print the result as JSON")
	fs.BoolVar(&f.allowDowngrade, "allow-downgrade", false, "allow a version older than the installed one")
	fs.StringVar(&f.fromFile, "from-file", "", "install this archive (with --sums-file) instead of downloading")
	fs.StringVar(&f.sumsFile, "sums-file", "", "the SHA256SUMS that lists --from-file")
	fs.BoolVar(&f.rollback, "rollback", false, "swap the previous binary (wy.old) back")
	fs.StringVar(&f.machines, "machines", "", "also update every added machine: all or name,name")
	fs.StringVar(&f.postUpdate, "post-update", "", "internal: run the steps after an update; the value is the old version")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy update: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	usage := func(msg string) error {
		fmt.Fprintf(stderr, "wy update: %s\n", msg)
		return errUsage
	}
	switch {
	case f.postUpdate != "":
		return updatePost(ctx, f, stdout, stderr)
	case f.rollback && (f.check || f.machines != "" || f.fromFile != "" || f.version != ""):
		return usage("--rollback takes no other option except --yes and --json")
	case f.check && f.machines != "":
		return usage("--check and --machines do not go together")
	case (f.fromFile == "") != (f.sumsFile == ""):
		return usage("--from-file and --sums-file go together")
	case f.machines != "" && f.fromFile != "":
		return usage("--machines downloads for each machine itself; it takes no --from-file")
	}
	switch {
	case f.rollback:
		return updateRollback(ctx, e, f, stdout, stderr)
	case f.check:
		return updateCheck(ctx, e, f, stdout, stderr)
	case f.machines != "":
		return updateFleet(ctx, e, f, stdout, stderr)
	}
	_, err := updateSelf(ctx, e, f, stdout, stderr)
	return err
}

func jsonLine(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintln(w, string(b))
}

// fail prints the JSON failure (when asked) and returns the error with its exit code.
func updateFail(f updateFlags, stdout io.Writer, err error) error {
	code := update.ExitCode(err)
	if f.asJSON {
		jsonLine(stdout, map[string]any{"status": "failed", "error": err.Error(), "code": code})
	}
	return exitWith(code, err)
}

func (e updateEnv) confirm(f updateFlags, stderr io.Writer, prompt string) error {
	if f.yes || !e.interactive {
		return nil
	}
	fmt.Fprintf(stderr, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(e.stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return exitWith(0, errors.New("not updated"))
	}
	return nil
}

func displayPath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

// updateSelf updates this machine (machines.md 7.3) and, when it replaced the binary, runs
// the post-update steps with the new one. It is also the first step of --machines.
func updateSelf(ctx context.Context, e updateEnv, f updateFlags, stdout, stderr io.Writer) (update.Result, error) {
	o := e.options(f)
	if f.fromFile == "" && !f.yes && e.interactive {
		if c, err := update.Check(ctx, o); err == nil && c.Newer {
			if err := e.confirm(f, stderr, fmt.Sprintf("Update wy %s to %s?", e.current, c.To)); err != nil {
				return update.Result{}, err
			}
		}
	}
	res, err := update.Apply(ctx, o)
	if err != nil {
		return res, updateFail(f, stdout, err)
	}
	if res.Status == "current" {
		if f.asJSON {
			jsonLine(stdout, res)
		} else {
			fmt.Fprintf(stdout, "wy %s is already the version asked for; nothing to do\n", res.To)
		}
		return res, nil
	}
	postOut := stdout
	if f.asJSON {
		postOut = stderr
	}
	var perr error
	if e.postUpdate != nil {
		perr = e.postUpdate(ctx, e.exe, e.current, postOut, stderr)
	}
	if f.asJSON {
		jsonLine(stdout, res)
	} else {
		proof := "checksum ok, build attestation ok"
		if !res.Attested {
			proof = "checksum ok"
		}
		fmt.Fprintf(stdout, "wy %s downloaded, %s, installed to %s (was %s; %s kept for --rollback)\n",
			res.To, proof, displayPath(res.Path), res.From, filepath.Base(res.OldKept))
		if a := e.attestation(f); !res.Attested && (a.Auto || a.Required) {
			fmt.Fprintln(stderr, "note: the build attestation was not checked because the gh CLI is not installed. The checksum shows the download is intact, not who built it. Install gh (https://cli.github.com) to check it.")
		}
	}
	if perr != nil {
		return res, updateFail(f, io.Discard, update.PostUpdateError(perr))
	}
	return res, nil
}

func updatePost(ctx context.Context, f updateFlags, stdout, stderr io.Writer) error {
	rs := update.RunPostUpdate(ctx, f.postUpdate)
	for _, r := range rs {
		switch {
		case r.Error != "":
			fmt.Fprintf(stderr, "post-update %s: %s\n", r.Name, r.Error)
		case r.Detail != "":
			fmt.Fprintf(stdout, "%s: %s\n", r.Name, r.Detail)
		}
	}
	if update.PostUpdateFailed(rs) {
		return exitWith(update.ExitFailed, errors.New("some post-update steps failed"))
	}
	return nil
}

func updateRollback(ctx context.Context, e updateEnv, f updateFlags, stdout, stderr io.Writer) error {
	res, err := update.Rollback(ctx, e.options(f))
	if err != nil {
		return updateFail(f, stdout, err)
	}
	var perr error
	if e.postUpdate != nil {
		perr = e.postUpdate(ctx, e.exe, e.current, stderr, stderr)
	}
	if f.asJSON {
		jsonLine(stdout, res)
	} else {
		fmt.Fprintf(stdout, "rolled back to wy %s (%s kept the replaced binary)\n", res.To, filepath.Base(res.OldKept))
	}
	if perr != nil {
		return updateFail(f, io.Discard, update.PostUpdateError(perr))
	}
	return nil
}

func updateCheck(ctx context.Context, e updateEnv, f updateFlags, stdout, stderr io.Writer) error {
	res, err := update.Check(ctx, e.options(f))
	if err != nil {
		return updateFail(f, stdout, err)
	}
	out := map[string]any{"current": e.current, "latest": res.To, "newer": res.Newer, "archive": res.Archive}
	if url := e.cfg.Hub.URL; url != "" && e.hubHealth != nil {
		hc, cancel := context.WithTimeout(ctx, 5*time.Second)
		h, herr := e.hubHealth(hc, url)
		cancel()
		if herr == nil {
			out["hub"] = h.Version
			out["hub_record_v_max"] = h.VMax()
			out["hub_reads_this_version"] = h.VMax() >= record.MaxVersion
		}
	}
	if f.asJSON {
		jsonLine(stdout, out)
		return nil
	}
	fmt.Fprintf(stdout, "current: %s\nlatest:  %s\n", e.current, res.To)
	if res.Newer {
		fmt.Fprintln(stdout, "an update is available: wy update")
	} else {
		fmt.Fprintln(stdout, "up to date")
	}
	if h, ok := out["hub"]; ok {
		fmt.Fprintf(stdout, "hub:     %v", h)
		if out["hub_reads_this_version"] == false {
			fmt.Fprintf(stdout, " (reads records up to v%v: this wy writes v%d, so update the hub)", out["hub_record_v_max"], record.MaxVersion)
		}
		fmt.Fprintln(stdout)
	}
	return nil
}

func updateFleet(ctx context.Context, e updateEnv, f updateFlags, stdout, stderr io.Writer) error {
	reg, remote, err := e.fleet()
	if err != nil {
		return exitWith(update.ExitFailed, err)
	}
	all, err := reg.Machines()
	if err != nil {
		return exitWith(update.ExitFailed, err)
	}
	sel, err := update.SelectMachines(all, f.machines)
	if err != nil {
		fmt.Fprintf(stderr, "wy update: %v\n", err)
		return errUsage
	}
	if len(sel) == 0 {
		fmt.Fprintln(stderr, "wy update: no machines were added from here yet (wy machine add user@host)")
		return nil
	}
	if err := e.confirm(f, stderr, fmt.Sprintf("Update this machine and %d added machine(s)?", len(sel))); err != nil {
		return err
	}
	// 7.4: the operator updates itself first (unless --version is given and it is already
	// there, which Apply reports as current), then the fleet matches the operator's version.
	self := f
	self.yes = true
	res, err := updateSelf(ctx, e, self, io.Discard, stderr)
	if err != nil {
		return updateFail(f, stdout, err)
	}
	target := res.To
	if _, ok := update.CompareVersions(target, target); !ok {
		return exitWith(update.ExitUsage, fmt.Errorf("this wy is %q, not a release: pass --version to say what the machines should run", target))
	}
	results, failed := update.UpdateFleet(ctx, update.FleetOptions{
		Machines: sel, Version: target, Source: e.source(), Remote: remote, Attestation: e.attestation(f),
		CacheDir: filepath.Join(e.dirs.State.Path, "update", "cache"),
		Progress: func(r update.FleetResult) {
			if !f.asJSON {
				fmt.Fprintf(stderr, "%s: %s\n", r.Name, r.Outcome)
			}
		},
	})
	if f.asJSON {
		jsonLine(stdout, map[string]any{"version": target, "operator": res, "machines": results, "failed": failed})
	} else {
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "MACHINE\tVERSION\tOUTCOME")
		for _, r := range results {
			v := r.To
			if r.From != "" && r.From != r.To {
				v = r.From + " -> " + r.To
			}
			o := r.Outcome
			if r.Error != "" {
				o += ": " + r.Error
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, v, o)
		}
		tw.Flush()
	}
	if failed {
		return exitWith(update.ExitFailed, errors.New("some machines were not updated"))
	}
	return nil
}
