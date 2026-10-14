package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/directpush"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/version"
)

// directPushStdin is where `on --secret-stdin` reads (a test seam).
var directPushStdin io.Reader = os.Stdin

const directPushUsage = `usage: sy direct-push on [--server URL] [--private=true|false] [--after 2m] [--secret-stdin]
       sy direct-push off
       sy direct-push status [--json]
       sy direct-push test
       sy direct-push policy [--json] [--file F] [--fetch]
`

// runDirectPush is `sy direct-push on|off|status|test|policy` (machines.md 5.2, 5.3).
func runDirectPush(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, directPushUsage)
		return errUsage
	}
	switch args[0] {
	case "on":
		return directPushOn(args[1:], stdout, stderr)
	case "off":
		return directPushOff(args[1:], stdout, stderr)
	case "status":
		return directPushStatus(args[1:], stdout, stderr)
	case "test":
		return directPushTest(args[1:], stdout, stderr)
	case "policy":
		return directPushPolicy(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "sy direct-push: unknown command %q\n%s", args[0], directPushUsage)
	return errUsage
}

type directEnv struct {
	dirs paths.Dirs
	file *config.File
	cfg  config.Config
}

func loadDirectEnv() (directEnv, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return directEnv{}, err
	}
	f, err := config.Load(d.Config.Path)
	if err != nil {
		return directEnv{}, err
	}
	cfg, err := f.Typed()
	if err != nil {
		return directEnv{}, err
	}
	return directEnv{dirs: d, file: f, cfg: cfg}, nil
}

func (e directEnv) options() (directpush.Options, error) {
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return directpush.Options{}, err
	}
	return directpush.Options{StateDir: e.dirs.State.Path, OutboxDir: filepath.Join(e.dirs.State.Path, "outbox"),
		MachineID: m.ID, Machine: m.Name, Config: e.cfg, Client: "sy/" + version.Version}, nil
}

func directPushOn(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("direct-push on", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "ntfy server origin (default the current one, else https://ntfy.sh)")
	private := fs.Bool("private", true, "send only \"1 session needs you\" (default true on ntfy.sh)")
	after := fs.Duration("after", 0, "how long without hub contact counts as unreachable (default 2m, 3m in pull mode)")
	secretStdin := fs.Bool("secret-stdin", false, `read {"topic": "...", "token": "tk_..."} from stdin`)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "sy direct-push on: unexpected argument %q (the topic is a secret: use --secret-stdin)\n", fs.Arg(0))
		return errUsage
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	e, err := loadDirectEnv()
	if err != nil {
		return err
	}
	srv := e.cfg.Push.Direct.Server
	if *server != "" {
		srv = strings.TrimRight(*server, "/")
	}
	if srv == "" {
		srv = config.DefaultDirectServer
	}
	if err := directpush.CheckServer(srv); err != nil {
		return err
	}
	priv := *private
	if !set["private"] {
		// push.md 5.5: private on ntfy.sh unless asked otherwise; elsewhere keep the setting.
		priv = e.cfg.Push.Direct.Private || directpush.ServerHost(srv) == "ntfy.sh"
	}
	if set["after"] && (*after < 10*time.Second || *after > 24*time.Hour) {
		return fmt.Errorf("sy direct-push on: --after must be between 10s and 24h")
	}

	state := e.dirs.State.Path
	if *secretStdin {
		sec, err := readDirectSecret(directPushStdin)
		if err != nil {
			return err
		}
		if err := directpush.SaveSecret(state, sec); err != nil {
			return err
		}
	} else if _, err := directpush.LoadSecret(state); err != nil {
		f, ok := directPushStdin.(*os.File)
		if !ok || !term.IsTerminal(int(f.Fd())) {
			return fmt.Errorf("sy direct-push on: no ntfy topic yet; pass it on stdin with --secret-stdin as {\"topic\": \"...\", \"token\": \"...\"}")
		}
		sec, err := promptDirectSecret(f, stderr)
		if err != nil {
			return err
		}
		if err := directpush.SaveSecret(state, sec); err != nil {
			return err
		}
	}

	if err := e.file.Set("push.direct.enabled", []string{"true"}); err != nil {
		return err
	}
	if err := e.file.Set("push.direct.server", []string{srv}); err != nil {
		return err
	}
	if err := e.file.Set("push.direct.private", []string{strconv.FormatBool(priv)}); err != nil {
		return err
	}
	if set["after"] {
		if err := e.file.Set("push.direct.after_s", []string{strconv.Itoa(int(after.Seconds()))}); err != nil {
			return err
		}
	}
	if err := e.file.Save(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Direct push is on: %s, private %v. The topic is in %s (never in config.toml).\n",
		directpush.ServerHost(srv), priv, directpush.SecretPath(state))
	fmt.Fprintln(stdout, "Check it reaches your phone with `sy direct-push test`.")
	return nil
}

// readDirectSecret reads one JSON object {topic, token?} (at most 4 KiB).
func readDirectSecret(r io.Reader) (directpush.Secret, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4<<10))
	if err != nil {
		return directpush.Secret{}, err
	}
	var s directpush.Secret
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return directpush.Secret{}, errors.New(`sy direct-push on: stdin must be {"topic": "...", "token": "..."} (token optional)`)
	}
	s.Topic, s.Token = strings.TrimSpace(s.Topic), strings.TrimSpace(s.Token)
	return s, s.Validate()
}

// promptDirectSecret asks for the topic and token at a terminal, echo off.
func promptDirectSecret(f *os.File, stderr io.Writer) (directpush.Secret, error) {
	read := func(prompt string) (string, error) {
		fmt.Fprint(stderr, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		return strings.TrimSpace(string(b)), err
	}
	topic, err := read("ntfy topic: ")
	if err != nil {
		return directpush.Secret{}, err
	}
	token, err := read("ntfy access token (Enter for none): ")
	if err != nil {
		return directpush.Secret{}, err
	}
	s := directpush.Secret{Topic: topic, Token: token}
	return s, s.Validate()
}

func directPushOff(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		fmt.Fprint(stderr, directPushUsage)
		return errUsage
	}
	e, err := loadDirectEnv()
	if err != nil {
		return err
	}
	if err := e.file.Set("push.direct.enabled", []string{"false"}); err != nil {
		return err
	}
	if err := e.file.Save(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Direct push is off. The topic stays in %s; delete it if you no longer use it.\n", directpush.SecretPath(e.dirs.State.Path))
	return nil
}

// directStatus is `sy direct-push status --json`. It never holds the topic or token.
type directStatus struct {
	Enabled     bool                `json:"enabled"`
	Server      string              `json:"server"`
	ServerHost  string              `json:"server_host"`
	Private     bool                `json:"private"`
	AfterS      int                 `json:"after_s"`
	TopicSet    bool                `json:"topic_set"`
	TokenSet    bool                `json:"token_set"`
	Policy      directStatusPolicy  `json:"policy"`
	LastContact *string             `json:"last_contact"`
	Pending     []directStatusEntry `json:"episodes"`
	Recent      []directpush.Marker `json:"recent"`
}

type directStatusPolicy struct {
	Cached    bool    `json:"cached"`
	FetchedAt *string `json:"fetched_at"`
	Hub       string  `json:"hub,omitempty"`
	Rules     int     `json:"rules"`
	PublicURL *string `json:"public_url"`
}

type directStatusEntry struct {
	Session string `json:"session"`
	Trigger string `json:"trigger"`
	Episode string `json:"episode"`
	Since   string `json:"since"`
	DueAt   string `json:"due_at"`
	Blocked string `json:"blocked"`
}

func directPushStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("direct-push status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errUsage
	}
	e, err := loadDirectEnv()
	if err != nil {
		return err
	}
	o, err := e.options()
	if err != nil {
		return err
	}
	now := time.Now()
	d := e.cfg.Push.Direct
	st := directStatus{Enabled: d.Enabled, Server: d.Server, ServerHost: directpush.ServerHost(d.Server), Private: d.Private,
		AfterS: d.AfterS, Pending: []directStatusEntry{}, Recent: []directpush.Marker{}}
	if sec, err := directpush.LoadSecret(o.StateDir); err == nil {
		st.TopicSet, st.TokenSet = true, sec.Token != ""
	}
	pol, cached := directpush.LoadPolicy(o.StateDir)
	st.Policy = directStatusPolicy{Cached: cached, Hub: pol.Hub, Rules: len(pol.Rules), PublicURL: pol.PublicURL}
	if pol.FetchedAt != "" {
		st.Policy.FetchedAt = &pol.FetchedAt
	}
	lc := directpush.LastContact(o.OutboxDir)
	if !lc.IsZero() {
		s := lc.UTC().Format(time.RFC3339)
		st.LastContact = &s
	}
	markers := directpush.Markers(o.StateDir)
	eps, err := directpush.Scan(o.OutboxDir, o.MachineID, now)
	if err != nil {
		return err
	}
	for _, v := range directpush.Evaluate(eps, pol, now, lc, o.After(), markers) {
		st.Pending = append(st.Pending, directStatusEntry{Session: v.Session, Trigger: v.Trigger, Episode: v.Key,
			Since: v.Since.UTC().Format(time.RFC3339), DueAt: v.DueAt.UTC().Format(time.RFC3339), Blocked: v.Blocked})
	}
	for _, m := range markers {
		if t, err := time.Parse(time.RFC3339Nano, m.At); err == nil && now.Sub(t) < 24*time.Hour {
			st.Recent = append(st.Recent, m)
		}
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(st)
	}
	onOff := map[bool]string{true: "on", false: "off"}
	fmt.Fprintf(stdout, "Direct push: %s (%s, private %v, unreachable after %ds)\n", onOff[st.Enabled], st.ServerHost, st.Private, st.AfterS)
	switch {
	case !st.TopicSet:
		fmt.Fprintln(stdout, "Topic: not set (sy direct-push on --secret-stdin)")
	case st.TokenSet:
		fmt.Fprintln(stdout, "Topic: set, with an access token")
	default:
		fmt.Fprintln(stdout, "Topic: set")
	}
	if cached {
		fmt.Fprintf(stdout, "Policy: %d rules from hub %s, fetched %s\n", st.Policy.Rules, dpOrDash(st.Policy.Hub), dpOrDash(pol.FetchedAt))
	} else {
		fmt.Fprintln(stdout, "Policy: none cached; using the defaults (30 s permission, 60 s question, failures at once)")
	}
	if st.LastContact != nil {
		fmt.Fprintf(stdout, "Last hub contact: %s\n", *st.LastContact)
	} else {
		fmt.Fprintln(stdout, "Last hub contact: none recorded")
	}
	for _, p := range st.Pending {
		state := "would push now"
		if p.Blocked != "" {
			state = "not pushed: " + p.Blocked
		}
		fmt.Fprintf(stdout, "  %s %s since %s, due %s: %s\n", p.Session, p.Trigger, p.Since, p.DueAt, state)
	}
	for _, m := range st.Recent {
		fmt.Fprintf(stdout, "  pushed %s %s at %s: %s\n", m.Session, m.Trigger, m.At, m.Outcome)
	}
	return nil
}

func dpOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func directPushTest(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		fmt.Fprint(stderr, directPushUsage)
		return errUsage
	}
	e, err := loadDirectEnv()
	if err != nil {
		return err
	}
	o, err := e.options()
	if err != nil {
		return err
	}
	if _, err := directpush.SendTest(context.Background(), o); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Sent \"Switchyard direct push test from %s\" via %s.\n", o.Machine, directpush.ServerHost(e.cfg.Push.Direct.Server))
	return nil
}

func directPushPolicy(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("direct-push policy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "install a policy file (a saved GET /v1/push/direct-policy reply) as the cache")
	fetch := fs.Bool("fetch", false, "fetch the policy from the hub now")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || (*file != "" && *fetch) {
		fmt.Fprint(stderr, directPushUsage)
		return errUsage
	}
	e, err := loadDirectEnv()
	if err != nil {
		return err
	}
	state := e.dirs.State.Path
	switch {
	case *file != "":
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		var p directpush.Policy
		if err := json.Unmarshal(b, &p); err != nil {
			return fmt.Errorf("%s: %w", *file, err)
		}
		if err := directpush.SavePolicy(state, p, time.Now()); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Installed the policy (%d rules) in %s.\n", len(p.Rules), directpush.PolicyPath(state))
		return nil
	case *fetch:
		if e.cfg.Hub.URL == "" {
			return errors.New("sy direct-push policy --fetch: no hub configured")
		}
		token, err := client.ReadToken(state)
		if err != nil {
			return fmt.Errorf("sy direct-push policy --fetch: no hub token: %w", err)
		}
		cl, err := client.ForConfig(e.cfg, token)
		if err != nil {
			return err
		}
		p, _, err := directpush.RefreshPolicy(context.Background(), state, cl, time.Now(), true)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Fetched the policy (%d rules) from hub %s.\n", len(p.Rules), dpOrDash(p.Hub))
		return nil
	}
	p, _ := directpush.LoadPolicy(state)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}
