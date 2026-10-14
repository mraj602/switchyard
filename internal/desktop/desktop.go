package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/machineadd"
	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/service"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/version"
)

// Controller is the hub user service as `sy desktop hub` uses it: *service.Service.
type Controller interface {
	Status(ctx context.Context, name string) (model.ServiceStatus, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
}

// Env is everything the four actions read from this machine. The command layer fills it from
// the real machine; tests replace the admin channel, the service controller and the HTTP
// client, so nothing real runs.
type Env struct {
	Dirs   paths.Dirs
	Config config.Config
	// Admin is the admin channel of machines.md 4.4 (nil: none) and AdminName how the person
	// gave it ("local", "ssh oci sy").
	Admin     machineadd.Admin
	AdminName string
	// AdminErr is why the configured admin channel (machines.hub_admin) could not be used,
	// when it could not: `info` reports it, `token` and `owner-link` fail with it.
	AdminErr error
	Service  Controller
	GOOS     string
	// Version is sy's version; empty means version.Version.
	Version string
	// Stdin is read for --code-stdin: the first line.
	Stdin io.Reader
	// HTTP returns a client with the timeout, dialing through hub.dial; nil means the
	// real one (client.HTTPClient).
	HTTP func(timeout time.Duration) (*http.Client, error)
	// Sleep waits between health polls; nil means time.Sleep.
	Sleep func(time.Duration)
}

// Error is a failed action with its exit code (desktop.md C4: 3 refused by the hub, 4 not
// possible here, 5 hub unreachable; 1 is any other failure).
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func fail(code int, format string, a ...any) *Error {
	return &Error{Code: code, Err: fmt.Errorf(format, a...)}
}

func (e Env) version() string {
	if e.Version != "" {
		return e.Version
	}
	return version.Version
}

func (e Env) goos() string {
	if e.GOOS != "" {
		return e.GOOS
	}
	return runtime.GOOS
}

func (e Env) httpClient(timeout time.Duration) (*http.Client, error) {
	if e.HTTP != nil {
		return e.HTTP(timeout)
	}
	return client.HTTPClient(e.Config.Hub.Dial, timeout)
}

func (e Env) sleep(d time.Duration) {
	if e.Sleep != nil {
		e.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (e Env) hubDB() string { return filepath.Join(e.Dirs.Data.Path, "hub.db") }

func (e Env) hubDBExists() bool {
	_, err := os.Stat(e.hubDB())
	return err == nil
}

func (e Env) machine() (identity.Machine, error) {
	return identity.LoadOrCreateMachine(e.Dirs.State.Path, identity.MachineOptions{})
}

// health asks GET /health of the hub sy is connected to. Version is "" when unreachable.
func (e Env) health(ctx context.Context, timeout time.Duration) (string, bool) {
	if e.Config.Hub.URL == "" {
		return "", false
	}
	hc, err := e.httpClient(timeout)
	if err != nil {
		return "", false
	}
	h, err := (&client.Client{BaseURL: e.Config.Hub.URL, HTTP: hc}).Health(ctx)
	if err != nil {
		return "", false
	}
	return h.Version, true
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RunInfo is `sy desktop info --json` (C4). It never fails because the hub or the service is
// out of reach: an unconnected machine reports connected: false.
func RunInfo(ctx context.Context, e Env) (Info, error) {
	m, err := e.machine()
	if err != nil {
		return Info{}, err
	}
	hubURL := e.Config.Hub.URL
	_, tokErr := client.ReadToken(e.Dirs.State.Path)
	info := Info{V: InfoV, Sy: e.version(), Machine: InfoMachine{ID: m.ID, Name: m.Name}}
	info.Hub = InfoHub{URL: strp(hubURL), Connected: hubURL != "" && tokErr == nil, Dial: strp(e.Config.Hub.Dial)}
	if v, ok := e.health(ctx, 3*time.Second); ok {
		info.Hub.Reachable = true
		info.Hub.Version = strp(v)
	}
	dbHere := e.hubDBExists()
	info.Hub.Local = dbHere && isLoopbackURL(hubURL)
	if e.Service != nil {
		if st, err := e.Service.Status(ctx, service.DefaultName); err == nil && (dbHere || st.Installed) {
			info.Hub.Service = strp(serviceState(st))
		}
	}
	if e.AdminErr != nil {
		info.AdminError = strp(e.AdminErr.Error())
	}
	switch {
	case e.AdminName != "":
		info.Admin = strp(e.AdminName)
	case e.Config.Machines.HubAdmin != "":
		info.Admin = strp(e.Config.Machines.HubAdmin)
	case dbHere:
		info.Admin = strp(AdminLocal)
	}
	info.Outbox = outboxInfo(e.Dirs.State.Path)
	return info, nil
}

// estBytesPerRecord and countUpTo keep `info` quick during a long outage, as machine_info does.
const (
	estBytesPerRecord = 400
	countUpTo         = 8 << 20
)

func outboxInfo(stateDir string) InfoOutbox {
	var o InfoOutbox
	dir := filepath.Join(stateDir, "outbox")
	if _, err := os.Stat(dir); err != nil {
		return o
	}
	ob := outbox.New(dir)
	if st, err := ob.Stat(); err == nil {
		if st.UnflushedBytes > countUpTo {
			o.Pending = int(st.UnflushedBytes / estBytesPerRecord)
		} else if n, _, err := ob.Backlog(st.Cursor); err == nil {
			o.Pending = int(n)
		}
	}
	if fs := client.ReadStatus(dir); !fs.OK && fs.Error != "" {
		o.LastError = strp(fs.Error)
	}
	return o
}

func serviceState(st model.ServiceStatus) string {
	switch {
	case !st.Installed:
		return ServiceMissing
	case st.Active:
		return ServiceRunning
	}
	return ServiceStopped
}

// TokenOptions are the flags of `sy desktop token`.
type TokenOptions struct {
	CodeStdin bool
	Name      string
}

var (
	// NameRE is the app's device name rule (desktop.md C11). No spaces: the name travels as
	// a remote command word through an ssh admin channel (sshx.CheckWord).
	NameRE   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
	digitsRE = regexp.MustCompile(`^\d{3}[- ]?\d{3}$`)
)

// notConnected is the exit 4 of the actions that need the hub's address.
func (e Env) needHub() (string, error) {
	if e.Config.Hub.Dial != "" {
		return "", fail(ExitNotHere, "forward mode (hub.dial) is not supported in the desktop app")
	}
	if e.Config.Hub.URL == "" {
		return "", fail(ExitNotHere, "sy is not connected to a hub: run `sy setup`")
	}
	return e.Config.Hub.URL, nil
}

// adminErr maps a failed admin command to an exit code: ssh's own failure (255) means the
// hub host cannot be reached (5); a hub older than this feature is "not possible here" (4).
// Any other failure (the command did not start, an argument the channel refuses) is 1 with
// its own message: the channel exists, so "no admin channel" would mislead.
func adminErr(err error) error {
	var ae *machineadd.AdminError
	if errors.As(err, &ae) {
		switch {
		case ae.Exit == 255:
			return &Error{Code: ExitUnreachable, Err: err}
		case ae.NotImplemented():
			return &Error{Code: ExitNotHere, Err: err}
		}
		return &Error{Code: 1, Err: err}
	}
	return &Error{Code: 1, Err: err}
}

// noAdmin is the exit 4 of token and owner-link without a usable admin channel.
func (e Env) noAdmin(hint string) error {
	if e.AdminErr != nil {
		return fail(ExitNotHere, "machines.hub_admin cannot be used: %v", e.AdminErr)
	}
	return fail(ExitNotHere, "no admin channel to the hub: set machines.hub_admin (sy machine add --hub-admin) %s", hint)
}

func firstJSON(out []byte) []byte {
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && (l[0] == '{' || l[0] == '[') {
			return []byte(l)
		}
	}
	return out
}

// RunToken is `sy desktop token` (C4). Without CodeStdin it asks the admin channel for a
// desktop token for this machine; with it, it claims the pairing code (or secret) on the
// first line of stdin, expecting a desktop code. The token is returned once; sy stores none.
func RunToken(ctx context.Context, e Env, o TokenOptions) (Token, error) {
	name := o.Name
	if name == "" {
		name = "desktop"
	}
	if !NameRE.MatchString(name) {
		return Token{}, &Error{Code: ExitUsage, Err: errors.New("--name must be 1 to 40 characters: letters, digits, . _ - (no spaces)")}
	}
	hubURL, err := e.needHub()
	if err != nil {
		return Token{}, err
	}
	m, err := e.machine()
	if err != nil {
		return Token{}, err
	}
	if o.CodeStdin {
		return e.claimToken(ctx, hubURL, m.ID, name)
	}
	if e.Admin == nil {
		return Token{}, e.noAdmin("or pair with a code from the board (Devices, Add device, Desktop)")
	}
	out, err := e.Admin.Hub(ctx, []string{"token", "create", "--kind", "desktop", "--machine", m.ID, "--name", name, "--json"}, nil)
	if err != nil {
		return Token{}, adminErr(err)
	}
	var r struct {
		Device string   `json:"device"`
		Token  string   `json:"token"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(firstJSON(out), &r); err != nil || r.Token == "" || r.Device == "" {
		return Token{}, fail(1, "the hub's token command answered without a token")
	}
	return Token{Device: r.Device, Token: r.Token, Scopes: r.Scopes, Hub: TokenHub{URL: hubURL}}, nil
}

func (e Env) claimToken(ctx context.Context, hubURL, machine, name string) (Token, error) {
	if e.Stdin == nil {
		return Token{}, fail(ExitUsage, "--code-stdin: no stdin")
	}
	line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return Token{}, &Error{Code: ExitUsage, Err: errors.New("--code-stdin: no code on stdin")}
	}
	req := client.ClaimRequest{Name: name, Machine: machine, ExpectKind: "desktop", Client: "sy/" + e.version()}
	if digitsRE.MatchString(line) {
		req.Code = line
	} else {
		req.Secret = line
	}
	hc, err := e.httpClient(20 * time.Second)
	if err != nil {
		return Token{}, fail(ExitNotHere, "%v", err)
	}
	r, err := client.Claim(ctx, hubURL, hc, req)
	if err != nil {
		var se *client.StatusError
		if errors.As(err, &se) {
			if se.Status >= 500 {
				return Token{}, &Error{Code: ExitUnreachable, Err: err}
			}
			return Token{}, &Error{Code: ExitRefused, Err: err}
		}
		return Token{}, &Error{Code: ExitUnreachable, Err: fmt.Errorf("cannot reach the hub at %s: %w", hubURL, err)}
	}
	if r.Kind != "" && r.Kind != "desktop" {
		return Token{}, fail(ExitRefused, "the code made a %s device, not a desktop", r.Kind)
	}
	// Bind to the URL sy dialled, not the hub's own idea of its address (r.Hub.URL): the
	// board looks the token up by the configured URL.
	return Token{Device: r.Device, Token: r.Token, Scopes: r.Scopes, Hub: TokenHub{URL: hubURL}}, nil
}

// RunOwnerLink is `sy desktop owner-link` (C4): the admin channel's `hub owner-link --json`
// for the hub address sy uses.
func RunOwnerLink(ctx context.Context, e Env) (OwnerLink, error) {
	hubURL, err := e.needHub()
	if err != nil {
		return OwnerLink{}, err
	}
	if e.Admin == nil {
		return OwnerLink{}, e.noAdmin("or run `sy hub owner-link` on the hub")
	}
	args := []string{"owner-link", "--url", hubURL, "--json"}
	if strings.HasPrefix(hubURL, "http://") && e.Config.Hub.InsecureLAN {
		args = append(args, "--insecure")
	}
	out, err := e.Admin.Hub(ctx, args, nil)
	if err != nil {
		return OwnerLink{}, adminErr(err)
	}
	var r OwnerLink
	if err := json.Unmarshal(firstJSON(out), &r); err != nil || r.URL == "" {
		return OwnerLink{}, fail(1, "the hub's owner-link command answered without a link")
	}
	return OwnerLink{URL: r.URL, ExpiresAt: r.ExpiresAt}, nil
}

// RunHub is `sy desktop hub status|start|stop` (C4) for the hub user service on this
// machine. start and stop exit 4 when the service is not installed.
func RunHub(ctx context.Context, e Env, action string) (HubService, error) {
	if e.Service == nil {
		return HubService{}, fail(ExitNotHere, "no service manager on this machine")
	}
	unit := service.UnitName(e.goos(), service.DefaultName)
	status := func() (HubService, error) {
		st, err := e.Service.Status(ctx, service.DefaultName)
		if err != nil {
			return HubService{}, err
		}
		_, healthy := e.health(ctx, 2*time.Second)
		return HubService{Service: serviceState(st), Unit: unit, Health: healthy}, nil
	}
	switch action {
	case ActionStatus:
		return status()
	case ActionStart, ActionStop:
	default:
		return HubService{}, &Error{Code: ExitUsage, Err: fmt.Errorf("unknown action %q (want status, start or stop)", action)}
	}
	var err error
	if action == ActionStart {
		err = e.Service.Start(ctx, service.DefaultName)
	} else {
		err = e.Service.Stop(ctx, service.DefaultName)
	}
	if errors.Is(err, service.ErrNotInstalled) {
		return HubService{}, &Error{Code: ExitNotHere, Err: err}
	}
	if err != nil {
		return HubService{}, err
	}
	// The manager answers before the hub does: give /health a few seconds to follow.
	want := action == ActionStart
	for i := 0; ; i++ {
		_, healthy := e.health(ctx, time.Second)
		if healthy == want || i >= 20 || e.Config.Hub.URL == "" {
			break
		}
		e.sleep(250 * time.Millisecond)
	}
	return status()
}
