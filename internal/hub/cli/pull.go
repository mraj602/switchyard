package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/pull"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/localtime"
	"github.com/mraj602/watchyard/internal/version"
)

// ErrNotImplemented is mapped by cmd/wy to exit status 3 (a subcommand this build lacks).
var ErrNotImplemented = errors.New("not implemented in this build")

// Test seams: the helper-program runner (ssh-keygen, ssh-keyscan) and the ssh starter.
var (
	pullRunner  pull.Runner
	pullStarter pull.Starter
)

// PullUsage is the help of `wy hub pull`.
const PullUsage = `Usage: wy hub pull <command> [flags]

  add <name> --ssh [user@]host [--port 22] [--machine <m_...>] [--interval 60s]
             (--host-keys-stdin | --fingerprint SHA256:...) [--yes] [--json]
  list [--json]
  run [<name>] [--wait] [--json]
  trust <name> (--host-keys-stdin | --fingerprint SHA256:...)
  enable <name> | disable <name>
  remove <name> [--revoke]
  key [--json]

Every command takes --db <file> and --dir <pull folder> (default <state>/pull).
`

// Pull is `wy hub pull add|list|run|trust|enable|disable|remove|key` (machines.md 3.4). Like
// `wy hub pair` it works on the database directly, so it runs on the hub host or through the
// admin channel.
func Pull(args []string, stdout, stderr io.Writer) error {
	return PullIn(os.Stdin, args, stdout, stderr)
}

// PullIn is Pull with an explicit stdin (host keys, confirmations).
func PullIn(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, PullUsage)
		return ErrUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return pullAdd(stdin, rest, stdout, stderr)
	case "list":
		return pullList(rest, stdout, stderr)
	case "run":
		return pullRun(rest, stdout, stderr)
	case "trust":
		return pullTrust(stdin, rest, stdout, stderr)
	case "enable":
		return pullToggle(true, rest, stdout, stderr)
	case "disable":
		return pullToggle(false, rest, stdout, stderr)
	case "remove":
		return pullRemove(rest, stdout, stderr)
	case "key":
		return pullKey(rest, stdout, stderr)
	}
	fmt.Fprintf(stderr, "wy hub pull: unknown command %q\n\n%s", sub, PullUsage)
	return ErrUsage
}

// pullDir is --dir / --pull-dir, else <state>/pull.
func pullDir(flagDir string) (string, error) {
	if flagDir != "" {
		return flagDir, nil
	}
	sd, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(sd, "pull"), nil
}

func hubName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "hub"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

var (
	pullNameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	machineIDRe   = regexp.MustCompile(`^m_[0-9A-HJKMNP-TV-Z]{26}$`)
	fingerprintRe = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
)

type pullCommon struct {
	db, dir *string
}

func pullFlags(name string, stderr io.Writer) (*flag.FlagSet, pullCommon) {
	fs, db := newFlags("pull "+name, stderr)
	dir := fs.String("dir", "", "pull folder (default <state>/pull)")
	return fs, pullCommon{db, dir}
}

// pinKeys resolves the host keys for add and trust from --host-keys-stdin, --fingerprint or
// (at a terminal) an interactive confirmation of the scanned fingerprints.
func pinKeys(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, fromStdin bool, fp string, yes bool, host string, port int) ([]string, error) {
	switch {
	case fromStdin && fp != "":
		return nil, errors.New("give --host-keys-stdin or --fingerprint, not both")
	case fromStdin:
		return pull.ParseHostKeys(io.LimitReader(stdin, 1<<20))
	}
	scanned, err := pull.Scan(ctx, pullRunner, host, port)
	if err != nil {
		return nil, err
	}
	if fp != "" {
		if !fingerprintRe.MatchString(fp) {
			return nil, fmt.Errorf("%q is not a SHA256:... fingerprint", fp)
		}
		return pull.PinByFingerprint(scanned, fp)
	}
	if yes || !isTerminal(stdin) {
		return nil, errors.New("host keys are not pinned: pass --host-keys-stdin or --fingerprint SHA256:... (wy never learns a host key without being told)")
	}
	fmt.Fprintf(stdout, "The server %s:%d presents these host keys:\n", host, port)
	for _, k := range scanned {
		f, _ := pull.Fingerprint(k)
		fmt.Fprintf(stdout, "  %s  %s\n", f, strings.Fields(k)[0])
	}
	fmt.Fprint(stdout, "Check them against the server (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub). Pin them? [y/N] ")
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return nil, errors.New("host keys not pinned")
	}
	return scanned, nil
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func pullAdd(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	fs, c := pullFlags("add", stderr)
	sshTarget := fs.String("ssh", "", "[user@]host")
	port := fs.Int("port", 22, "SSH port")
	machine := fs.String("machine", "", "machine id (makes the pull device now)")
	interval := fs.Duration("interval", 60*time.Second, "pull interval (15s to 1h)")
	hostKeys := fs.Bool("host-keys-stdin", false, "read the server's public host keys from stdin")
	fp := fs.String("fingerprint", "", "pin the scanned host keys with this fingerprint")
	yes := fs.Bool("yes", false, "do not ask")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 || *sshTarget == "" {
		fmt.Fprint(stderr, PullUsage)
		return ErrUsage
	}
	name := pos[0]
	if !pullNameRe.MatchString(name) {
		return fmt.Errorf("%q is not a valid target name (letters, digits, . _ -)", name)
	}
	if *machine != "" && !machineIDRe.MatchString(*machine) {
		return fmt.Errorf("%q is not a machine id (m_ + ULID)", *machine)
	}
	if *interval < 15*time.Second || *interval > time.Hour {
		return errors.New("--interval must be between 15s and 1h")
	}
	user, host := "", *sshTarget
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		user, host = host[:i], host[i+1:]
	}
	if user == "" {
		user = os.Getenv("USER")
	}
	t := store.PullTarget{Name: name, MachineID: *machine, Host: host, Port: *port, User: user,
		IntervalS: int(*interval / time.Second)}
	if t.RemoteSy == "" {
		t.RemoteSy = ".local/bin/wy"
	}
	if err := pull.ValidateTarget(t); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	keys, err := pinKeys(ctx, stdin, stdout, stderr, *hostKeys, *fp, *yes, host, *port)
	if err != nil {
		return err
	}
	t.HostKeys = keys
	dir, err := pullDir(*c.dir)
	if err != nil {
		return err
	}
	st, err := openHubStore(ctx, *c.db)
	if err != nil {
		return err
	}
	defer st.Close()
	pub, err := pull.EnsureKey(ctx, dir, hubName(), pullRunner)
	if err != nil {
		return err
	}
	added, err := st.AddPullTarget(ctx, t)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{"target": added.ID, "name": added.Name, "public_key": pub})
	}
	fmt.Fprintf(stdout, "Added pull target %s (%s).\nAdd this line to ~/.ssh/authorized_keys on the server (wy machine add does it for you):\n\n"+
		"  restrict,command=\"<path to wy> outbox drain --stdio\" %s sy-pull:%s\n", added.Name, added.ID, pub, added.ID)
	return nil
}

type pullRow struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Machine   *string `json:"machine"`
	Target    string  `json:"target"`
	State     string  `json:"state"`
	Enabled   bool    `json:"enabled"`
	IntervalS int     `json:"interval_s"`
	LastTry   *string `json:"last_try"`
	LastOK    *string `json:"last_ok"`
	LastError *string `json:"last_error"`
	NextAt    *string `json:"next_at"`
	FailCount int     `json:"fail_count"`
}

func rowOf(t store.PullTarget, frozen bool) pullRow {
	s := pull.Status(t, frozen)
	r := pullRow{ID: t.ID, Name: t.Name, Target: s.Target, State: s.State, Enabled: t.Enabled, IntervalS: t.IntervalS,
		LastTry: s.LastTry, LastOK: s.LastOK, LastError: s.LastError, NextAt: s.NextAt, FailCount: t.FailCount}
	if t.MachineID != "" {
		m := t.MachineID
		r.Machine = &m
	}
	return r
}

func isFrozen(ctx context.Context, st *store.Store) bool {
	_, ok, _ := st.AlertSetting(ctx, pull.SettingFrozen)
	return ok
}

func pullList(args []string, stdout, stderr io.Writer) error {
	fs, c := pullFlags("list", stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openHubStore(ctx, *c.db)
	if err != nil {
		return err
	}
	defer st.Close()
	ts, err := st.PullTargets(ctx)
	if err != nil {
		return err
	}
	frozen := isFrozen(ctx, st)
	rows := make([]pullRow, 0, len(ts))
	for _, t := range ts {
		rows = append(rows, rowOf(t, frozen))
	}
	return printRows(stdout, rows, *asJSON)
}

func printRows(stdout io.Writer, rows []pullRow, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(stdout).Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "No pull targets. Add one with `wy hub pull add`.")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTARGET\tSTATE\tLAST OK\tNEXT\tLAST ERROR")
	str := func(p *string) string {
		if p == nil {
			return "-"
		}
		return *p
	}
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Target, r.State, localtime.Ptr(r.LastOK), localtime.Ptr(r.NextAt), str(r.LastError))
	}
	return tw.Flush()
}

func pullRun(args []string, stdout, stderr io.Writer) error {
	fs, c := pullFlags("run", stderr)
	wait := fs.Bool("wait", false, "wait for the run to finish")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parse(fs, args)
	if err != nil || len(pos) > 1 {
		return ErrUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dir, err := pullDir(*c.dir)
	if err != nil {
		return err
	}
	st, err := openHubStore(ctx, *c.db)
	if err != nil {
		return err
	}
	defer st.Close()
	var targets []store.PullTarget
	if len(pos) == 1 {
		t, err := st.PullTargetByName(ctx, pos[0])
		if err != nil {
			return fmt.Errorf("no pull target %q (wy hub pull list)", pos[0])
		}
		targets = []store.PullTarget{t}
	}
	sc := pull.New(pull.Config{Store: st, Ingester: &ingest.Ingester{Store: st}, Dir: dir, HubVersion: version.Version,
		Starter: pullStarter, Logf: func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) }})
	if isFrozen(ctx, st) {
		return errors.New("the hub is frozen for a move; pulls are paused")
	}
	started := st.Now().UnixMilli()
	if pull.Alive(dir) {
		// A hub process is running: it owns the runs (one run per target, one ingester). Ask
		// it through the database and, with --wait, wait for the rows to show the runs.
		list := targets
		if len(pos) == 0 {
			var err error
			if list, err = st.DuePullTargets(ctx, started); err != nil {
				return err
			}
		}
		scheduled := map[string]int64{}
		for _, t := range list {
			if len(pos) == 1 && (t.State == store.PullStateHostKey || t.State == store.PullStateRefused) {
				return fmt.Errorf("target %s is %s; fix it first (wy hub pull trust / remove)", t.Name, t.State)
			}
			at, err := st.SchedulePullAt(ctx, t.ID)
			if err != nil {
				return err
			}
			scheduled[t.ID] = at
		}
		if *wait {
			deadline := time.Now().Add(pull.Ladder[0] + 2*time.Minute)
			for _, t := range list {
				if err := waitForRun(ctx, st, t.ID, scheduled[t.ID], deadline, 200*time.Millisecond); err != nil {
					return err
				}
			}
		}
	} else if len(targets) == 1 {
		if err := sc.RunTarget(ctx, targets[0]); err != nil {
			return err
		}
	} else {
		sc.Tick(ctx)
	}
	ts, err := st.PullTargets(ctx)
	if err != nil {
		return err
	}
	var rows []pullRow
	for _, t := range ts {
		if len(targets) == 1 && t.ID != targets[0].ID {
			continue
		}
		rows = append(rows, rowOf(t, false))
	}
	if err := printRows(stdout, rows, *asJSON); err != nil {
		return err
	}
	for _, r := range rows {
		if len(targets) == 1 && r.State != api.PullOK && r.State != api.PullNew && *wait {
			return fmt.Errorf("pull %s ended in state %s", r.Name, r.State)
		}
	}
	return nil
}

// waitForRun waits until a run that started after SchedulePullAt returned `scheduled` has
// been recorded. A run already in flight then (a freshly registered target is due at once, so
// the hub starts it before `wy machine add` has authorized the key) also records last_try_at
// afterwards, but leaves next_at alone because it was due before the schedule; only a run
// started after the call moves next_at off `scheduled`. A state that stops the schedule
// (host key, refused, disabled) ends the wait too, once something was recorded.
func waitForRun(ctx context.Context, st *store.Store, id string, scheduled int64, deadline time.Time, poll time.Duration) error {
	for time.Now().Before(deadline) {
		cur, err := st.PullTargetByID(ctx, id)
		if err != nil {
			return err
		}
		if cur.LastTryAt >= scheduled {
			if cur.NextAt != scheduled {
				return nil
			}
			switch cur.State {
			case store.PullStateHostKey, store.PullStateRefused, store.PullStateDisabled:
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	return nil
}

func pullTrust(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	fs, c := pullFlags("trust", stderr)
	hostKeys := fs.Bool("host-keys-stdin", false, "read the server's public host keys from stdin")
	fp := fs.String("fingerprint", "", "pin the scanned host keys with this fingerprint")
	yes := fs.Bool("yes", false, "do not ask")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return ErrUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := openHubStore(ctx, *c.db)
	if err != nil {
		return err
	}
	defer st.Close()
	t, err := st.PullTargetByName(ctx, pos[0])
	if err != nil {
		return fmt.Errorf("no pull target %q (wy hub pull list)", pos[0])
	}
	keys, err := pinKeys(ctx, stdin, stdout, stderr, *hostKeys, *fp, *yes, t.Host, t.Port)
	if err != nil {
		return err
	}
	if err := st.TrustPullKeys(ctx, t.ID, keys); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Pinned %d host key(s) for %s; the next pull is due now.\n", len(keys), t.Name)
	return nil
}

func pullToggle(enable bool, args []string, stdout, stderr io.Writer) error {
	name := "disable"
	if enable {
		name = "enable"
	}
	fs, c := pullFlags(name, stderr)
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openHubStore(ctx, *c.db)
	if err != nil {
		return err
	}
	defer st.Close()
	t, err := st.PullTargetByName(ctx, pos[0])
	if err != nil {
		return fmt.Errorf("no pull target %q (wy hub pull list)", pos[0])
	}
	if err := st.SetPullEnabled(ctx, t.ID, enable); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%sd %s\n", name, t.Name)
	return nil
}

func pullRemove(args []string, stdout, stderr io.Writer) error {
	fs, c := pullFlags("remove", stderr)
	revoke := fs.Bool("revoke", false, "also revoke the pull device")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openHubStore(ctx, *c.db)
	if err != nil {
		return err
	}
	defer st.Close()
	t, err := st.PullTargetByName(ctx, pos[0])
	if err != nil {
		return fmt.Errorf("no pull target %q (wy hub pull list)", pos[0])
	}
	if err := st.RemovePullTarget(ctx, t.ID, *revoke); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed pull target %s\n", t.Name)
	return nil
}

func pullKey(args []string, stdout, stderr io.Writer) error {
	fs, c := pullFlags("key", stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	dir, err := pullDir(*c.dir)
	if err != nil {
		return err
	}
	pub, err := pull.PublicKey(dir)
	if err != nil {
		return errors.New("the hub has no pull key yet: it is made by the first `wy hub pull add`")
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]string{"public_key": pub})
	}
	fmt.Fprintln(stdout, pub)
	return nil
}
