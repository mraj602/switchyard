// Package service installs the hub as a user service for `sy setup --hub here`
// (setup.md step 4): a systemd user unit on Linux (the same unit as
// deploy/systemd/switchyard-hub.service, with `sy` by absolute path), a LaunchAgent on macOS
// and a Task Scheduler task on Windows. It implements model.Service on the Runner pattern of
// internal/timer: nothing real runs unless the caller hands in a real Runner and the real
// home folder; tests use a temp home and a recording Runner.
//
// Phase 3, WP-D. Depends on: internal/timer (Runner, ExecRunner, UTF16LE), model.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/timer"
)

// Defaults of the hub service.
const (
	DefaultName   = "switchyard-hub"
	DefaultListen = "127.0.0.1:8787"
)

// Runner runs an external command (systemctl, loginctl, launchctl, schtasks). It is the
// timer package's Runner, so timer.ExecRunner{} is the real one.
type Runner = timer.Runner

// ExecRunner runs real commands.
type ExecRunner = timer.ExecRunner

// Env is everything Install, Uninstall and Status depend on.
type Env struct {
	GOOS     string
	Home     string
	UID      int
	User     string
	SyPath   string // absolute path of the sy binary the service runs
	StateDir string // for the log (macOS) and the task XML (Windows)
	Getenv   func(string) string
	Runner   Runner
	Out      io.Writer // hints (linger)

	// Pairs, when PairsSet, are the environment written into the unit instead of the
	// folder overrides read from Getenv. Refresh sets them from the installed unit so a
	// rewrite keeps exactly the environment the service was installed with.
	Pairs    [][2]string
	PairsSet bool
}

// passEnv are the folder overrides copied into the unit so the service sees the same folders
// as the shell that ran `sy setup`.
var passEnv = []string{"SY_HOME", "SY_CONFIG_DIR", "SY_STATE_DIR", "SY_DATA_DIR"}

func (e Env) envPairs() [][2]string {
	if e.PairsSet {
		return e.Pairs
	}
	if e.Getenv == nil {
		return nil
	}
	var out [][2]string
	for _, k := range passEnv {
		if v := e.Getenv(k); v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

func (e Env) printf(format string, a ...any) {
	if e.Out != nil {
		fmt.Fprintf(e.Out, format, a...)
	}
}

// Service implements model.Service.
type Service struct{ Env Env }

var _ model.Service = (*Service)(nil)

// New returns a Service for e.
func New(e Env) *Service { return &Service{Env: e} }

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// normalize fills the defaults of a spec and validates its name.
func normalize(spec model.ServiceSpec) (model.ServiceSpec, error) {
	if spec.Name == "" {
		spec.Name = DefaultName
	}
	if !nameRE.MatchString(spec.Name) {
		return spec, fmt.Errorf("service: invalid name %q", spec.Name)
	}
	if spec.Listen == "" {
		spec.Listen = DefaultListen
	}
	if len(spec.Args) == 0 {
		spec.Args = []string{"hub", "serve", "--listen", spec.Listen}
	}
	return spec, nil
}

// LaunchdLabel is the LaunchAgent label for a service name.
func LaunchdLabel(name string) string {
	if name == DefaultName {
		return "com.switchyard.hub"
	}
	return "com.switchyard." + name
}

// WindowsTask is the Task Scheduler name for a service name.
func WindowsTask(name string) string {
	if name == DefaultName {
		return "Switchyard Hub"
	}
	return "Switchyard " + name
}

// WindowsXMLFile is the task definition's file name inside Env.StateDir.
func WindowsXMLFile(name string) string { return name + ".task.xml" }

// SystemdDir is where the user units live.
func SystemdDir(e Env) string {
	if e.Getenv != nil {
		if x := e.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
			return filepath.Join(x, "systemd", "user")
		}
	}
	return filepath.Join(e.Home, ".config", "systemd", "user")
}

// SystemdUnitPath is the unit file of a service.
func SystemdUnitPath(e Env, name string) string {
	return filepath.Join(SystemdDir(e), name+".service")
}

// LaunchAgentPath is the plist location.
func LaunchAgentPath(e Env, name string) string {
	return filepath.Join(e.Home, "Library", "LaunchAgents", LaunchdLabel(name)+".plist")
}

// UnitPath is where Install writes the unit, plist or task XML for spec on e.GOOS.
func UnitPath(e Env, spec model.ServiceSpec) string {
	if spec.Unit != "" {
		return spec.Unit
	}
	switch e.GOOS {
	case "darwin":
		return LaunchAgentPath(e, spec.Name)
	case "windows":
		return filepath.Join(e.StateDir, WindowsXMLFile(spec.Name))
	}
	return SystemdUnitPath(e, spec.Name)
}

// ---- file text (golden-tested) -------------------------------------------------------------------

// systemdQuote quotes s for an ExecStart= or Environment= word. systemd splits on
// whitespace and understands double quotes and C-style escapes; a raw CR or LF would end
// the line and start a new directive, so every control byte is written as a \xNN escape
// (systemd decodes it back to the same byte). Keep in sync with internal/timer.
func systemdQuote(s string) string { return systemdQuoteWord(s, true) }

// systemdEnvQuote quotes a NAME=value word for Environment=, where (unlike ExecStart=) "$"
// is not special and must not be doubled (systemd.exec(5): no variable expansion there).
func systemdEnvQuote(s string) string { return systemdQuoteWord(s, false) }

func systemdQuoteWord(s string, dollar bool) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$%;") && !hasControl(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' || c == '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '%':
			b.WriteString("%%")
		case c == '$' && dollar:
			b.WriteString("$$")
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// SystemdUnit renders the user unit. It matches deploy/systemd/switchyard-hub.service except
// that ExecStart names sy by absolute path and Environment lines carry the folder overrides.
func SystemdUnit(e Env, spec model.ServiceSpec) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Switchyard hub (merged mode)\n")
	b.WriteString("Documentation=https://github.com/mraj602/switchyard\n")
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	for _, kv := range e.envPairs() {
		fmt.Fprintf(&b, "Environment=%s\n", systemdEnvQuote(kv[0]+"="+kv[1]))
	}
	args := []string{systemdQuote(e.SyPath)}
	for _, a := range spec.Args {
		args = append(args, systemdQuote(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(args, " "))
	b.WriteString("Restart=on-failure\nRestartSec=3\nTimeoutStopSec=20\n")
	b.WriteString("NoNewPrivileges=yes\nPrivateTmp=yes\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return b.String()
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// LaunchdPlist renders the LaunchAgent: started at load, restarted when it exits non-zero.
func LaunchdPlist(e Env, spec model.ServiceSpec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(LaunchdLabel(spec.Name)) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(e.SyPath) + `</string>
`)
	for _, a := range spec.Args {
		b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	if pairs := e.envPairs(); len(pairs) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, kv := range pairs {
			b.WriteString("\t\t<key>" + xmlEscape(kv[0]) + "</key>\n\t\t<string>" + xmlEscape(kv[1]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	log := xmlEscape(filepath.Join(e.StateDir, "hub.log"))
	b.WriteString(`	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>3</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + log + `</string>
	<key>StandardErrorPath</key>
	<string>` + log + `</string>
</dict>
</plist>
`)
	return b.String()
}

// windowsArg quotes one argument for a Task Scheduler <Arguments> string.
func windowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// WindowsTaskXML renders the Task Scheduler definition: at logon, restarted when it fails,
// no time limit.
func WindowsTaskXML(e Env, spec model.ServiceSpec) string {
	var args []string
	for _, a := range spec.Args {
		args = append(args, windowsArg(a))
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Switchyard hub (merged mode)</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Hidden>true</Hidden>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(e.SyPath) + `</Command>
      <Arguments>` + xmlEscape(strings.Join(args, " ")) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// ---- install / uninstall / status ---------------------------------------------------------------

func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Service) run(name string, args ...string) ([]byte, error) {
	if s.Env.Runner == nil {
		return nil, errors.New("service: no Runner")
	}
	return s.Env.Runner.Run(name, args...)
}

// Install writes the unit and starts the service. A failing service manager is reported in
// the returned error but the file stays, so the person can finish by hand.
func (s *Service) Install(ctx context.Context, spec model.ServiceSpec) error {
	spec, err := normalize(spec)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(s.Env.SyPath) {
		return fmt.Errorf("service: sy path %q is not absolute", s.Env.SyPath)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch s.Env.GOOS {
	case "linux":
		return s.installSystemd(spec)
	case "darwin":
		return s.installLaunchd(spec)
	case "windows":
		return s.installWindows(spec)
	}
	return fmt.Errorf("service: no user service support for %s; run `sy hub serve` from your own supervisor", s.Env.GOOS)
}

func (s *Service) installSystemd(spec model.ServiceSpec) error {
	e := s.Env
	p := UnitPath(e, spec)
	if err := writeFile(p, []byte(SystemdUnit(e, spec)), 0o644); err != nil {
		return err
	}
	unit := spec.Name + ".service"
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", unit}} {
		if out, err := s.run("systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s\nThe unit file is %s; start the hub later with:\n  systemctl --user enable --now %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(out)), p, unit)
		}
	}
	if e.Out != nil && e.User != "" {
		if out, err := s.run("loginctl", "show-user", e.User, "--property=Linger"); err == nil && strings.Contains(string(out), "Linger=no") {
			e.printf("%s\n", LingerNote(e.User))
		}
	}
	return nil
}

func (s *Service) installLaunchd(spec model.ServiceSpec) error {
	e := s.Env
	p := UnitPath(e, spec)
	if err := writeFile(p, []byte(LaunchdPlist(e, spec)), 0o644); err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", e.UID)
	_, _ = s.run("launchctl", "bootout", domain+"/"+LaunchdLabel(spec.Name)) // replace an older copy
	if out, err := s.run("launchctl", "bootstrap", domain, p); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s\nThe agent file is %s; load it with:\n  launchctl bootstrap %s %s",
			err, strings.TrimSpace(string(out)), p, domain, p)
	}
	return nil
}

func (s *Service) installWindows(spec model.ServiceSpec) error {
	e := s.Env
	p := UnitPath(e, spec)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := writeFile(p, timer.UTF16LE(WindowsTaskXML(e, spec)), 0o600); err != nil {
		return err
	}
	task := WindowsTask(spec.Name)
	if out, err := s.run("schtasks", "/Create", "/TN", task, "/XML", p, "/F"); err != nil {
		return fmt.Errorf("schtasks /Create: %v: %s\nThe task file is %s; create it by hand with:\n  schtasks /Create /TN \"%s\" /XML \"%s\"",
			err, strings.TrimSpace(string(out)), p, task, p)
	}
	if out, err := s.run("schtasks", "/Run", "/TN", task); err != nil {
		return fmt.Errorf("schtasks /Run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall stops the service and removes its files. Missing pieces are not errors.
func (s *Service) Uninstall(ctx context.Context, name string) error {
	spec, err := normalize(model.ServiceSpec{Name: name})
	if err != nil {
		return err
	}
	e := s.Env
	p := UnitPath(e, spec)
	rm := func(path string) error {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	switch e.GOOS {
	case "linux":
		_, _ = s.run("systemctl", "--user", "disable", "--now", spec.Name+".service")
		if err := rm(p); err != nil {
			return err
		}
		_, _ = s.run("systemctl", "--user", "daemon-reload")
	case "darwin":
		_, _ = s.run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", e.UID, LaunchdLabel(spec.Name)))
		return rm(p)
	case "windows":
		task := WindowsTask(spec.Name)
		_, _ = s.run("schtasks", "/End", "/TN", task)
		_, _ = s.run("schtasks", "/Delete", "/TN", task, "/F")
		return rm(p)
	default:
		return fmt.Errorf("service: no user service support for %s", e.GOOS)
	}
	return nil
}

// Status reports whether the service is installed and running. On Linux it also sets
// LingerOff when `loginctl show-user` says Linger=no ("the hub stops when you log out; run
// `loginctl enable-linger`"; sy never runs that itself).
func (s *Service) Status(ctx context.Context, name string) (model.ServiceStatus, error) {
	var st model.ServiceStatus
	spec, err := normalize(model.ServiceSpec{Name: name})
	if err != nil {
		return st, err
	}
	e := s.Env
	_, statErr := os.Stat(UnitPath(e, spec))
	st.Installed = statErr == nil
	switch e.GOOS {
	case "linux":
		if st.Installed {
			out, _ := s.run("systemctl", "--user", "is-active", spec.Name+".service")
			st.Active = strings.TrimSpace(string(out)) == "active"
		}
		if e.User != "" {
			out, err := s.run("loginctl", "show-user", e.User, "--property=Linger")
			st.LingerOff = err == nil && strings.Contains(string(out), "Linger=no")
		}
	case "darwin":
		if st.Installed {
			out, err := s.run("launchctl", "print", fmt.Sprintf("gui/%d/%s", e.UID, LaunchdLabel(spec.Name)))
			st.Active = err == nil && strings.Contains(string(out), "state = running")
		}
	case "windows":
		out, err := s.run("schtasks", "/Query", "/TN", WindowsTask(spec.Name))
		if err == nil {
			st.Installed = true
			st.Active = strings.Contains(string(out), "Running")
		}
	}
	return st, nil
}

// LingerNote is the one-line hint printed when LingerOff is set.
func LingerNote(user string) string {
	return "the hub stops when you log out; run `loginctl enable-linger " + user + "`"
}
