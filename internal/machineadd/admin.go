package machineadd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/sshx"
)

// Admin is the admin channel (machines.md 4.4): how the operator runs `wy hub …` on the hub
// host. Hub runs `wy hub <args>` with stdin there and returns its stdout. A nonzero exit is
// an *AdminError.
type Admin interface {
	Hub(ctx context.Context, args []string, stdin []byte) ([]byte, error)
	// Describe is the channel as the person gave it ("local", "ssh hub-host wy").
	Describe() string
}

// AdminError is a failed admin command.
type AdminError struct {
	Args   []string
	Exit   int
	Stderr string
}

func (e *AdminError) Error() string {
	msg := firstLine([]byte(e.Stderr))
	if msg == "" {
		msg = fmt.Sprintf("exit %d", e.Exit)
	}
	return fmt.Sprintf("wy hub %s (admin channel): %s", strings.Join(e.Args[:min(len(e.Args), 2)], " "), msg)
}

// NotImplemented reports whether the hub's wy is too old for the command (an old stub
// answers exit 3 "not implemented").
func (e *AdminError) NotImplemented() bool {
	return e.Exit == 3 && strings.Contains(e.Stderr, "not implemented")
}

// LocalAdmin runs `wy hub …` in this process against <data>/hub.db (a merged hub). The
// command layer supplies Run (internal/hub/cli), which this package does not import.
type LocalAdmin struct {
	Run func(ctx context.Context, args []string, stdin []byte) (stdout, stderr []byte, exit int)
}

// Hub implements Admin.
func (l LocalAdmin) Hub(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	out, errb, code := l.Run(ctx, args, stdin)
	if code != 0 {
		return out, &AdminError{Args: args, Exit: code, Stderr: string(errb)}
	}
	return out, nil
}

// Describe implements Admin.
func (LocalAdmin) Describe() string { return "local" }

// PrefixAdmin runs a command prefix followed by `hub …` (`ssh hub-host wy`, `ssh hub-host docker
// compose -f /srv/wy/compose.yml exec -T hub wy`). The prefix is split into words, no
// shell; wy never interprets it beyond appending arguments. When the prefix starts with ssh
// the appended words are single-quoted for the remote shell and the prefix is checked for
// options that turn host key checking off.
type PrefixAdmin struct {
	Words []string
	Env   []string // nil: inherit
}

// NewPrefixAdmin checks a prefix.
func NewPrefixAdmin(prefix string) (PrefixAdmin, error) {
	w := sshx.SplitWords(prefix)
	if len(w) == 0 {
		return PrefixAdmin{}, errors.New("--hub-admin: empty command prefix")
	}
	if isSSH(w[0]) {
		if err := sshx.CheckForbidden(w[1:]); err != nil {
			return PrefixAdmin{}, err
		}
	}
	return PrefixAdmin{Words: w}, nil
}

func isSSH(prog string) bool {
	b := filepath.Base(prog)
	return b == "ssh" || b == "ssh.exe"
}

// Hub implements Admin.
func (p PrefixAdmin) Hub(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	for _, a := range args {
		if err := sshx.CheckWord(a); err != nil {
			return nil, err
		}
	}
	tail := append([]string{"hub"}, args...)
	argv := append([]string(nil), p.Words...)
	if isSSH(p.Words[0]) {
		argv = append(argv, sshx.QuoteAll(tail))
	} else {
		argv = append(argv, tail...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = p.Env
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.Bytes(), &AdminError{Args: args, Exit: ee.ExitCode(), Stderr: errb.String()}
		}
		return nil, fmt.Errorf("--hub-admin %s: %w", strings.Join(p.Words, " "), err)
	}
	return out.Bytes(), nil
}

// Describe implements Admin.
func (p PrefixAdmin) Describe() string { return strings.Join(p.Words, " ") }

// AdminSSHHost is the host of an `ssh [opts] <host> …` prefix, "" for any other prefix.
func AdminSSHHost(prefix string) string {
	w := sshx.SplitWords(prefix)
	if len(w) < 2 || !isSSH(w[0]) {
		return ""
	}
	for i := 1; i < len(w); i++ {
		a := w[i]
		if strings.HasPrefix(a, "-") {
			if len(a) == 2 && strings.ContainsRune("BbcDEeFIiJLlmOoPpRSWw", rune(a[1])) {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

// PairCode is the part of `wy hub pair --json` (api.yaml PairCode) machine add uses. Secret
// is the 128-bit secret; it is never printed or logged.
type PairCode struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Secret  string `json:"secret"`
	URL     string `json:"url"`
	Expires string `json:"expires_at"`
}

// Origin is the hub address of the pairing URL (https://hub/#/pair?... -> https://hub).
func (c PairCode) Origin() string {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// MintMachineCode makes a machine pairing code bound to the machine id (4.2 step 4).
func MintMachineCode(ctx context.Context, a Admin, name, machine, hubURL string) (PairCode, error) {
	args := []string{"pair", "--kind", "machine", "--name", name, "--machine", machine, "--ttl", "10m", "--json"}
	if hubURL != "" {
		args = append(args, "--url", hubURL)
	}
	out, err := a.Hub(ctx, args, nil)
	if err != nil {
		return PairCode{}, err
	}
	var c PairCode
	if err := json.Unmarshal(firstJSON(out), &c); err != nil || c.Secret == "" {
		return PairCode{}, errors.New("the hub's pair command answered without a secret")
	}
	return c, nil
}

// PullAdded is `wy hub pull add --json`.
type PullAdded struct {
	Target    string `json:"target"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// PullRow is one row of `wy hub pull list|run --json`.
type PullRow struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Machine   *string `json:"machine"`
	Target    string  `json:"target"`
	State     string  `json:"state"`
	LastError *string `json:"last_error"`
}

// PullTarget is what the hub needs to reach the remote itself: ssh -G's view of the host
// the person typed (the hub has no access to the person's ~/.ssh/config).
type PullTarget struct {
	Name, User, Host, Jump string
	Port                   int
}

// AddPullTarget registers the remote on the hub (4.2 step 4, pull) with its host keys on
// stdin, or finds the target a previous run registered, and returns it with the hub's pull
// public key.
func AddPullTarget(ctx context.Context, a Admin, t PullTarget, machine string, hostKeys []string) (PullAdded, error) {
	if rows, err := PullList(ctx, a); err == nil {
		// The target of this machine (by machine id) is the one to reuse. A target of the same
		// name that belongs to another machine is not: the new one gets a name of its own.
		for _, r := range rows {
			if machine != "" && r.Machine != nil && *r.Machine == machine {
				key, kerr := PullKey(ctx, a)
				if kerr != nil {
					return PullAdded{}, kerr
				}
				return PullAdded{Target: r.ID, Name: r.Name, PublicKey: key}, nil
			}
		}
		for _, r := range rows {
			if r.Name != t.Name {
				continue
			}
			if r.Machine == nil || machine == "" {
				// Registered without a machine id: the same name means the same target.
				key, kerr := PullKey(ctx, a)
				if kerr != nil {
					return PullAdded{}, kerr
				}
				return PullAdded{Target: r.ID, Name: r.Name, PublicKey: key}, nil
			}
			t.Name = uniquePullName(rows, t.Name, machine)
			break
		}
	}
	args := []string{"pull", "add", t.Name, "--ssh", t.User + "@" + t.Host, "--port", fmt.Sprint(t.Port)}
	if t.Jump != "" {
		args = append(args, "--jump", t.Jump)
	}
	if machine != "" {
		args = append(args, "--machine", machine)
	}
	args = append(args, "--host-keys-stdin", "--yes", "--json")
	out, err := a.Hub(ctx, args, []byte(strings.Join(hostKeys, "\n")+"\n"))
	if err != nil {
		return PullAdded{}, err
	}
	var r PullAdded
	if err := json.Unmarshal(firstJSON(out), &r); err != nil || r.Target == "" || r.PublicKey == "" {
		return PullAdded{}, errors.New("the hub's pull add answered without a target and key")
	}
	return r, nil
}

// uniquePullName suffixes name with the end of the machine id until no target has it.
func uniquePullName(rows []PullRow, name, machine string) string {
	id := strings.ToLower(strings.TrimPrefix(machine, "m_"))
	if len(id) > 4 {
		id = id[len(id)-4:]
	}
	cand := name + "-" + id
	for n := 2; ; n++ {
		taken := false
		for _, r := range rows {
			taken = taken || r.Name == cand
		}
		if !taken {
			return cand
		}
		cand = fmt.Sprintf("%s-%s-%d", name, id, n)
	}
}

// PullList is `wy hub pull list --json`.
func PullList(ctx context.Context, a Admin) ([]PullRow, error) {
	out, err := a.Hub(ctx, []string{"pull", "list", "--json"}, nil)
	if err != nil {
		return nil, err
	}
	var rows []PullRow
	if err := json.Unmarshal(firstJSON(out), &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// pullNameFor is the hub's name of a pull target id (the hub's commands take names);
// fallback when the hub does not list it.
func pullNameFor(ctx context.Context, a Admin, id, fallback string) string {
	if rows, err := PullList(ctx, a); err == nil {
		for _, r := range rows {
			if r.ID == id {
				return r.Name
			}
		}
	}
	return fallback
}

// PullKey is `wy hub pull key --json`.
func PullKey(ctx context.Context, a Admin) (string, error) {
	out, err := a.Hub(ctx, []string{"pull", "key", "--json"}, nil)
	if err != nil {
		return "", err
	}
	var r struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(firstJSON(out), &r); err != nil || r.PublicKey == "" {
		return "", errors.New("the hub has no pull key")
	}
	return r.PublicKey, nil
}

// PullRunWait is `wy hub pull run <name> --wait --json`: the row after one run.
func PullRunWait(ctx context.Context, a Admin, name string) (PullRow, error) {
	out, err := a.Hub(ctx, []string{"pull", "run", name, "--wait", "--json"}, nil)
	var rows []PullRow
	if jerr := json.Unmarshal(firstJSON(out), &rows); jerr == nil {
		for _, r := range rows {
			if r.Name == name {
				return r, err
			}
		}
	}
	if err != nil {
		return PullRow{}, err
	}
	return PullRow{}, fmt.Errorf("the hub did not report pull target %s", name)
}

// HubMachine is the part of `wy hub machine show <id> --json` (the Machine read model,
// machines.md 6.1) machine add and list use.
type HubMachine struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Reach       *string `json:"reach"`
	LastSeen    *string `json:"last_seen"`
	Backlog     *int64  `json:"backlog"`
	SyVersion   *string `json:"sy_version"`
	Silent      bool    `json:"silent"`
	LiveSession int     `json:"live_sessions"`
}

// SeenWithin reports whether last_seen is within d of now.
func (m HubMachine) SeenWithin(now time.Time, d time.Duration) bool {
	if m.LastSeen == nil {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, *m.LastSeen)
	return err == nil && now.Sub(t) <= d
}

// MachineShow is `wy hub machine show <id> --json`.
func MachineShow(ctx context.Context, a Admin, id string) (HubMachine, error) {
	out, err := a.Hub(ctx, []string{"machine", "show", id, "--json"}, nil)
	if err != nil {
		return HubMachine{}, err
	}
	var m HubMachine
	if err := json.Unmarshal(firstJSON(out), &m); err != nil {
		// Accept {"machine": {...}} too.
		var w struct {
			Machine HubMachine `json:"machine"`
		}
		if json.Unmarshal(firstJSON(out), &w) != nil {
			return HubMachine{}, fmt.Errorf("unreadable answer from wy hub machine show: %w", err)
		}
		m = w.Machine
	}
	return m, nil
}

// firstJSON returns the first line of out that starts a JSON value (commands may print a
// second line, as `wy hub pair --wait` does).
func firstJSON(out []byte) []byte {
	for _, l := range bytes.Split(out, []byte("\n")) {
		l = bytes.TrimSpace(l)
		if len(l) > 0 && (l[0] == '{' || l[0] == '[') {
			return l
		}
	}
	return bytes.TrimSpace(out)
}
