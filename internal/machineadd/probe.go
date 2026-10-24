package machineadd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/version"
)

// Forward ports (machines.md 3.3): fixed per machine, the first free one in this range.
const (
	FirstForwardPort = 47830
	LastForwardPort  = 47849
)

// preProbeScript is the read-only probe before wy is installed (4.2 step 2): home, login
// shell, ssh version, claude on the login PATH, an existing wy and its SHA-256, Rosetta, and
// the public host keys. $1 is the folder of the host keys (/etc/ssh). Nothing is secret.
const preProbeScript = `echo "home=$HOME"; s=${SHELL:-sh}; echo "shell=$s"; echo "sshv=$(ssh -V 2>&1 | head -n 1)"; ` +
	`c=$("$s" -lc "command -v claude" 2>/dev/null | tail -n 1); echo "claude=$c"; ` +
	`if [ -n "$c" ]; then echo "claudev=$("$c" --version 2>/dev/null | head -n 1)"; fi; ` +
	`if [ -x .local/bin/wy ]; then echo "wy=$(.local/bin/wy version --short 2>/dev/null)"; ` +
	`if command -v sha256sum >/dev/null 2>&1; then echo "wysha=$(sha256sum < .local/bin/wy)"; ` +
	`elif command -v shasum >/dev/null 2>&1; then echo "wysha=$(shasum -a 256 < .local/bin/wy)"; fi; fi; ` +
	`if [ "$(uname -s)" = Darwin ]; then echo "rosetta=$(sysctl -n sysctl.proc_translated 2>/dev/null)"; fi; ` +
	`for f in "$1"/ssh_host_*_key.pub; do if [ -r "$f" ]; then echo "hostkey=$(cat "$f")"; fi; done; echo end=1`

// PreProbe is the pre-install probe's answer.
type PreProbe struct {
	Home          string   `json:"home"`
	Shell         string   `json:"shell"`
	SSHVersion    string   `json:"ssh_version"`
	Claude        string   `json:"claude,omitempty"`
	ClaudeVersion string   `json:"claude_version,omitempty"`
	Wy            string   `json:"wy,omitempty"` // installed wy version, "" when none
	WySHA256      string   `json:"wy_sha256,omitempty"`
	Rosetta       bool     `json:"rosetta,omitempty"`
	HostKeys      []string `json:"host_keys,omitempty"`
}

func parsePreProbe(out []byte) (PreProbe, error) {
	kv := parseKV(out)
	if kv["end"] != "1" {
		return PreProbe{}, errors.New("the remote probe did not finish (is the login shell POSIX-like enough to run sh?)")
	}
	p := PreProbe{Home: kv["home"], Shell: kv["shell"], SSHVersion: kv["sshv"], Claude: kv["claude"],
		ClaudeVersion: kv["claudev"], Wy: kv["wy"], WySHA256: strings.ToLower(firstField(kv["wysha"])), Rosetta: kv["rosetta"] == "1"}
	for _, k := range parseKVAll(out, "hostkey") {
		if f := strings.Fields(k); len(f) >= 2 {
			p.HostKeys = append(p.HostKeys, f[0]+" "+f[1])
		}
	}
	return p, nil
}

// ProbeReport is `wy machine probe --json` (4.5), run on the remote with the new wy. Read
// only except that it creates machine.json. Nothing in it is secret.
type ProbeReport struct {
	V        int           `json:"v"`
	Machine  string        `json:"machine"`
	Name     string        `json:"name"`
	OS       string        `json:"os"`
	Arch     string        `json:"arch"`
	Wy       string        `json:"wy"`
	Home     string        `json:"home"`
	State    string        `json:"state"`
	Shell    string        `json:"shell,omitempty"`
	SSH      string        `json:"ssh,omitempty"`
	Restrict bool          `json:"restrict"` // the remote's OpenSSH knows `restrict` (7.2+)
	Claude   *ProbeClaude  `json:"claude"`
	Folders  []ProbeFolder `json:"folders"`
	Free     []int         `json:"free_ports"`
	Linger   *bool         `json:"linger"`
	Hub      *ProbeHub     `json:"hub"`
	// Connected is this machine's current hub link (config.toml and the token file).
	Connected ProbeConnected `json:"connected"`
}

// ProbeClaude is where claude is and its version.
type ProbeClaude struct {
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

// ProbeFolder is one Claude config folder and who is logged in (from wy's cached login).
type ProbeFolder struct {
	Folder   string `json:"folder"`
	LoggedIn bool   `json:"logged_in"`
	Email    string `json:"email,omitempty"`
}

// ProbeHub is the remote's own GET <hub-url>/health, 5 s timeout.
type ProbeHub struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	TLSOK     bool   `json:"tls_ok"`
	Version   string `json:"version,omitempty"`
	Schema    int    `json:"schema,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ProbeConnected is the current hub configuration of the remote.
type ProbeConnected struct {
	URL   string `json:"url,omitempty"`
	Reach string `json:"reach,omitempty"`
	Dial  string `json:"dial,omitempty"`
	Token bool   `json:"token"`
}

// ProbeOptions configures Probe (the remote side of `wy machine probe`).
type ProbeOptions struct {
	StateDir   string
	ConfigPath string
	Home       string
	HubURL     string
	Ports      [2]int // inclusive range; zero = none checked
	Getenv     func(string) string
	// Listen tests a port (nil = net.Listen on 127.0.0.1).
	Listen func(port int) error
	// Health checks the hub (nil = a real GET /health with 5 s).
	Health func(ctx context.Context, url string) (client.Health, error)
	// Run runs a helper program (claude --version, ssh -V, loginctl); nil = exec.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func execOut(ctx context.Context, name string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, name, args...).CombinedOutput()
}

var sshVerRe = regexp.MustCompile(`OpenSSH_(\d+)\.(\d+)`)

// supportsRestrict reads `ssh -V` output: OpenSSH 7.2 and later know `restrict` in
// authorized_keys. Unknown versions are assumed to (every supported OS has a newer one).
func supportsRestrict(sshV string) bool {
	m := sshVerRe.FindStringSubmatch(sshV)
	if m == nil {
		return true
	}
	maj, _ := strconv.Atoi(m[1])
	mnr, _ := strconv.Atoi(m[2])
	return maj > 7 || maj == 7 && mnr >= 2
}

// ParsePorts reads "47830-47849".
func ParsePorts(s string) ([2]int, error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		b = a
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(a))
	hi, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi || hi-lo > 100 {
		return [2]int{}, fmt.Errorf("--ports: want A-B (at most 100 ports), got %q", s)
	}
	return [2]int{lo, hi}, nil
}

// Probe answers `wy machine probe --json` on the remote.
func Probe(ctx context.Context, o ProbeOptions) (ProbeReport, error) {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Run == nil {
		o.Run = execOut
	}
	m, err := identity.LoadOrCreateMachine(o.StateDir, identity.MachineOptions{})
	if err != nil {
		return ProbeReport{}, err
	}
	r := ProbeReport{V: 1, Machine: m.ID, Name: m.Name, OS: runtime.GOOS, Arch: runtime.GOARCH, Wy: version.Version,
		Home: o.Home, State: o.StateDir, Shell: o.Getenv("SHELL"), Folders: []ProbeFolder{}, Free: []int{}}
	if out, err := o.Run(ctx, "ssh", "-V"); err == nil || len(out) > 0 {
		r.SSH = firstLine(out)
	}
	r.Restrict = supportsRestrict(r.SSH)
	var extra []string
	cfg := config.Defaults()
	if f, err := config.Load(o.ConfigPath); err == nil {
		if c, err := f.Typed(); err == nil {
			cfg = c
			extra = c.Slots.Extra
		}
	}
	r.Connected = ProbeConnected{URL: cfg.Hub.URL, Reach: cfg.Hub.Reach, Dial: cfg.Hub.Dial}
	if fi, err := os.Stat(filepath.Join(o.StateDir, client.TokenFile)); err == nil && fi.Size() > 0 {
		r.Connected.Token = true
	}
	if p := lookClaude(o.Home); p != "" {
		r.Claude = &ProbeClaude{Path: p}
		if out, err := o.Run(ctx, p, "--version"); err == nil {
			r.Claude.Version = firstLine(out)
		}
	}
	slots, _ := (identity.Locator{Home: o.Home, FS: identity.OSFS{}}).Discover(extra, o.Getenv)
	st := &identity.Store{StateDir: o.StateDir, FS: identity.OSFS{}}
	for _, s := range slots {
		f := ProbeFolder{Folder: s.Name}
		if l, err := st.Login(s); err == nil && l.HasSeat {
			f.LoggedIn, f.Email = true, l.Seat.Email
		}
		r.Folders = append(r.Folders, f)
	}
	if o.Ports[0] > 0 {
		listen := o.Listen
		if listen == nil {
			listen = func(p int) error {
				l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
				if err != nil {
					return err
				}
				return l.Close()
			}
		}
		for p := o.Ports[0]; p <= o.Ports[1]; p++ {
			if listen(p) == nil {
				r.Free = append(r.Free, p)
			}
		}
	}
	if runtime.GOOS == "linux" {
		if out, err := o.Run(ctx, "loginctl", "show-user", o.Getenv("USER"), "-p", "Linger"); err == nil {
			v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Linger="))
			on := v == "yes"
			if v == "yes" || v == "no" {
				r.Linger = &on
			}
		}
	}
	if o.HubURL != "" {
		r.Hub = probeHub(ctx, o)
	}
	return r, nil
}

func probeHub(ctx context.Context, o ProbeOptions) *ProbeHub {
	h := &ProbeHub{URL: o.HubURL}
	health := o.Health
	if health == nil {
		health = func(ctx context.Context, url string) (client.Health, error) {
			c := client.New(url, "")
			c.HTTP = &http.Client{Timeout: 5 * time.Second}
			return c.Health(ctx)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := health(cctx, o.HubURL)
	if err != nil {
		h.Error = firstLine([]byte(err.Error()))
		var ce *tls.CertificateVerificationError
		h.TLSOK = !errors.As(err, &ce) && !strings.Contains(err.Error(), "x509:")
		return h
	}
	h.Reachable, h.TLSOK, h.Version, h.Schema = true, true, res.Version, res.Schema
	return h
}

// lookClaude finds claude like the setup detector: PATH, then the usual install folders.
func lookClaude(home string) string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	for _, p := range []string{filepath.Join(home, ".local", "bin", "claude"), filepath.Join(home, ".claude", "local", "claude"),
		"/usr/local/bin/claude", "/opt/homebrew/bin/claude"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
