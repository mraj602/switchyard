// Package timer installs the OS timer that runs `wy flush` every minute and at boot:
// a systemd user timer on Linux, a LaunchAgent on macOS, and instructions plus a Task
// Scheduler XML file on Windows (docs/spec/setup.md).
//
// Nothing here touches the real system unless the caller hands in a real Runner and the
// user's real home folder; tests use a fake home and a recording Runner.
package timer

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
)

// Command is what the timer runs (arguments after the wy binary): `wy tick` (share sync,
// scan, agents every 5 minutes, flush).
var Command = []string{"tick", "--quiet"}

// Names of the units and jobs.
const (
	SystemdName    = brand.Slug + "-flush"
	LaunchdLabel   = "com." + brand.Slug + ".flush"
	WindowsTask    = brand.Name + " Flush"
	WindowsXMLFile = SystemdName + ".task.xml"
)

// Runner runs an external command. Tests inject a recording fake so nothing real runs.
type Runner interface {
	Run(name string, args ...string) (output []byte, err error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Env is everything Install and Uninstall depend on.
type Env struct {
	GOOS     string
	Home     string
	UID      int
	User     string
	WyPath   string // absolute path of the wy binary the timer runs
	StateDir string // for logs and the Windows XML
	Getenv   func(string) string
	Runner   Runner
	Out      io.Writer // hints and instructions
	// KeepLegacyState makes Install give the timer of the current name the state the one of
	// the old name had (enabled or not, running or not) when it replaces that one: the
	// post-update move, rename.md 2.3. Otherwise Install enables and starts the timer.
	KeepLegacyState bool
}

// passEnv are the folder overrides copied into the unit, so a timer started by the service
// manager sees the same folders as the shell that ran `wy connect`.
var passEnv = []string{"WY_HOME", "WY_CONFIG_DIR", "WY_STATE_DIR", "WY_DATA_DIR"}

func (e Env) envPairs() [][2]string {
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

// Result lists what Install did.
type Result struct {
	Files []string // files written
	Notes []string // things the user should know (fallbacks, linger)
	// Removed are the files of the timer installed under the name before a rename, which
	// Install replaced (rename.md 2.3).
	Removed []string
}

// SystemdDir is where the user units live.
func SystemdDir(e Env) string {
	if e.Getenv != nil {
		if x := e.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
			return filepath.Join(x, "systemd", "user")
		}
	}
	return filepath.Join(e.Home, ".config", "systemd", "user")
}

// LaunchAgentPath is the plist location.
func LaunchAgentPath(e Env) string {
	return filepath.Join(e.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
}

// ---- unit text (golden-tested) ----------------------------------------------------------

// systemdQuote quotes s for an ExecStart= or Environment= word. systemd splits on
// whitespace and understands double quotes and C-style escapes; a raw CR or LF would end
// the line and start a new directive, so every control byte is written as a \xNN escape
// (systemd decodes it back to the same byte). Keep in sync with internal/service.
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

// SystemdService renders watchyard-flush.service.
func SystemdService(e Env) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Watchyard: send queued records to the hub\n")
	b.WriteString("Documentation=https://github.com/mraj602/watchyard\n\n")
	b.WriteString("[Service]\nType=oneshot\n")
	for _, kv := range e.envPairs() {
		fmt.Fprintf(&b, "Environment=%s\n", systemdEnvQuote(kv[0]+"="+kv[1]))
	}
	args := []string{systemdQuote(e.WyPath)}
	for _, a := range Command {
		args = append(args, systemdQuote(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(args, " "))
	b.WriteString("Nice=10\nTimeoutStartSec=60\n")
	return b.String()
}

// SystemdTimer renders watchyard-flush.timer: 60 s after the previous run finished, and
// 30 s after boot.
func SystemdTimer() string {
	return "[Unit]\n" +
		"Description=Watchyard: flush every minute and at boot\n\n" +
		"[Timer]\n" +
		"OnBootSec=30s\n" +
		"OnUnitInactiveSec=60s\n" +
		"AccuracySec=1s\n" +
		"Unit=" + SystemdName + ".service\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// LaunchdPlist renders the LaunchAgent (paths in it are always slash paths: it is only for macOS).
func LaunchdPlist(e Env) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + LaunchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(e.WyPath) + `</string>
`)
	for _, a := range Command {
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
	b.WriteString(`	<key>RunAtLoad</key>
	<true/>
	<key>StartInterval</key>
	<integer>60</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>LowPriorityIO</key>
	<true/>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(path.Join(filepath.ToSlash(e.StateDir), "flush.log")) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(path.Join(filepath.ToSlash(e.StateDir), "flush.log")) + `</string>
</dict>
</plist>
`)
	return b.String()
}

// WindowsTaskXML renders a Task Scheduler definition: at logon and every minute.
func WindowsTaskXML(e Env) string {
	args := strings.Join(Command, " ")
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Watchyard: send queued records to the hub</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>PT1M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
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
    <ExecutionTimeLimit>PT1M</ExecutionTimeLimit>
    <Hidden>true</Hidden>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(e.WyPath) + `</Command>
      <Arguments>` + xmlEscape(args) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// ---- install / uninstall ----------------------------------------------------------------

func (e Env) printf(format string, a ...any) {
	if e.Out != nil {
		fmt.Fprintf(e.Out, format, a...)
	}
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// Install writes the timer files and enables the timer. A failing service manager is
// reported in the returned error but the files stay, so the user can finish by hand.
func Install(e Env) (Result, error) {
	var res Result
	st := state{Enabled: true, Active: true}
	if e.KeepLegacyState {
		st = legacyState(e)
	}
	res.Removed = removeLegacy(e)
	switch e.GOOS {
	case "linux":
		return res, installSystemd(e, &res, st)
	case "darwin":
		return res, installLaunchd(e, &res, st)
	case "windows":
		return res, installWindows(e, &res)
	}
	return res, fmt.Errorf("no timer support for %s; run `wy tick --quiet` every minute from cron", e.GOOS)
}

// Uninstall disables the timer and removes its files. Missing pieces are not errors.
func Uninstall(e Env) (removed []string, err error) {
	removed = removeLegacy(e)
	rm := func(p string) {
		if os.Remove(p) == nil {
			removed = append(removed, p)
		}
	}
	switch e.GOOS {
	case "linux":
		_, _ = e.Runner.Run("systemctl", "--user", "disable", "--now", SystemdName+".timer")
		dir := SystemdDir(e)
		rm(filepath.Join(dir, SystemdName+".timer"))
		rm(filepath.Join(dir, SystemdName+".service"))
		// Folders Install created are removed again when they are empty (os.Remove never
		// removes a folder that still holds something), so an undo leaves no trace.
		_ = os.Remove(dir)
		_ = os.Remove(filepath.Dir(dir))
		_, _ = e.Runner.Run("systemctl", "--user", "daemon-reload")
	case "darwin":
		_, _ = e.Runner.Run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", e.UID, LaunchdLabel))
		rm(LaunchAgentPath(e))
	case "windows":
		rm(filepath.Join(e.StateDir, WindowsXMLFile))
		e.printf("Remove the scheduled task with:\n  schtasks /Delete /TN \"%s\" /F\n", WindowsTask)
	}
	return removed, nil
}

// ---- the name before a rename (rename.md 2.3) ----------------------------------------------

type legacyNames struct{ systemd, label, task, xml string }

func legacy() (legacyNames, bool) {
	if !brand.Renamed() {
		return legacyNames{}, false
	}
	o := brand.OldSlug()
	return legacyNames{o + "-flush", "com." + o + ".flush", brand.OldName() + " Flush", o + "-flush.task.xml"}, true
}

// LegacyUnit is the unit file (Linux, the .service with the environment), plist (macOS) or
// task XML (Windows) of a timer installed under the project's name before a rename, or ""
// when there is none.
func LegacyUnit(e Env) string {
	n, ok := legacy()
	if !ok {
		return ""
	}
	var p string
	switch e.GOOS {
	case "linux":
		p = filepath.Join(SystemdDir(e), n.systemd+".service")
		if !exists(p) && exists(filepath.Join(SystemdDir(e), n.systemd+".timer")) {
			return filepath.Join(SystemdDir(e), n.systemd+".timer")
		}
	case "darwin":
		p = filepath.Join(e.Home, "Library", "LaunchAgents", n.label+".plist")
	case "windows":
		p = filepath.Join(e.StateDir, n.xml)
	default:
		return ""
	}
	if !exists(p) {
		return ""
	}
	return p
}

// removeLegacy disables the timer of the name before a rename and removes its files, so it
// never runs next to the new one. On Windows the task itself is only named for the person to
// delete, as Uninstall does.
func removeLegacy(e Env) (removed []string) {
	n, ok := legacy()
	if !ok || LegacyUnit(e) == "" {
		return nil
	}
	rm := func(p string) {
		if os.Remove(p) == nil {
			removed = append(removed, p)
		}
	}
	switch e.GOOS {
	case "linux":
		_, _ = e.Runner.Run("systemctl", "--user", "disable", "--now", n.systemd+".timer")
		rm(filepath.Join(SystemdDir(e), n.systemd+".timer"))
		rm(filepath.Join(SystemdDir(e), n.systemd+".service"))
		_, _ = e.Runner.Run("systemctl", "--user", "daemon-reload")
	case "darwin":
		_, _ = e.Runner.Run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", e.UID, n.label))
		rm(filepath.Join(e.Home, "Library", "LaunchAgents", n.label+".plist"))
	case "windows":
		rm(filepath.Join(e.StateDir, n.xml))
		e.printf("Remove the scheduled task from before the rename with:\n  schtasks /Delete /TN \"%s\" /F\n", n.task)
	}
	return removed
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// state is whether the timer starts by itself (enabled, loaded) and whether it runs now.
type state struct{ Enabled, Active bool }

// legacyState reads the state of the timer of the name before a rename; both on when there
// is none or the service manager gives no answer.
func legacyState(e Env) state {
	st := state{Enabled: true, Active: true}
	n, ok := legacy()
	if !ok || LegacyUnit(e) == "" {
		return st
	}
	word := func(args ...string) string {
		out, _ := e.Runner.Run(args[0], args[1:]...)
		return strings.TrimSpace(string(out))
	}
	switch e.GOOS {
	case "linux":
		if w := word("systemctl", "--user", "is-enabled", n.systemd+".timer"); w != "" {
			st.Enabled = strings.HasPrefix(w, "enabled") || w == "linked" || w == "alias"
		}
		if w := word("systemctl", "--user", "is-active", n.systemd+".timer"); w != "" {
			st.Active = w == "active" || w == "activating"
		}
	case "darwin":
		if _, err := e.Runner.Run("launchctl", "print", fmt.Sprintf("gui/%d/%s", e.UID, n.label)); err != nil {
			st = state{}
		}
	}
	return st
}

func installSystemd(e Env, res *Result, st state) error {
	dir := SystemdDir(e)
	svc, tm := filepath.Join(dir, SystemdName+".service"), filepath.Join(dir, SystemdName+".timer")
	if err := writeFile(svc, SystemdService(e)); err != nil {
		return err
	}
	if err := writeFile(tm, SystemdTimer()); err != nil {
		return err
	}
	res.Files = append(res.Files, svc, tm)
	cmds := [][]string{{"--user", "daemon-reload"}}
	switch {
	case st.Enabled && st.Active:
		cmds = append(cmds, []string{"--user", "enable", "--now", SystemdName + ".timer"})
	case st.Enabled:
		cmds = append(cmds, []string{"--user", "enable", SystemdName + ".timer"})
	case st.Active:
		cmds = append(cmds, []string{"--user", "start", SystemdName + ".timer"})
	}
	for _, args := range cmds {
		if out, err := e.Runner.Run("systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s\nThe unit files are in %s; start the timer later with:\n  systemctl --user enable --now %s.timer\nOr, without systemd, run `wy tick --quiet` every minute from cron.",
				strings.Join(args, " "), err, strings.TrimSpace(string(out)), dir, SystemdName)
		}
	}
	// Linger keeps the user's timers running after logout (needed on SSH servers). We only
	// say so; changing it needs the user's decision.
	if e.User == "" {
		return nil
	}
	out, err := e.Runner.Run("loginctl", "show-user", e.User, "--property=Linger")
	if err != nil || !strings.Contains(string(out), "Linger=yes") {
		note := "To keep the timer running when you are logged out (SSH servers), run once:\n  loginctl enable-linger " + e.User
		res.Notes = append(res.Notes, note)
		e.printf("%s\n", note)
	}
	return nil
}

func installLaunchd(e Env, res *Result, st state) error {
	p := LaunchAgentPath(e)
	if err := writeFile(p, LaunchdPlist(e)); err != nil {
		return err
	}
	res.Files = append(res.Files, p)
	if !st.Enabled && !st.Active {
		return nil // the old agent was not loaded: the file is written, nothing is loaded
	}
	domain := fmt.Sprintf("gui/%d", e.UID)
	_, _ = e.Runner.Run("launchctl", "bootout", domain+"/"+LaunchdLabel) // replace an older copy
	if out, err := e.Runner.Run("launchctl", "bootstrap", domain, p); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s\nThe agent file is %s; load it with:\n  launchctl bootstrap %s %s",
			err, strings.TrimSpace(string(out)), p, domain, p)
	}
	return nil
}

// UTF16LE encodes s as UTF-16LE with a byte order mark, which schtasks /XML requires.
func UTF16LE(s string) []byte {
	units := []uint16{0xFEFF}
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
	}
	out := make([]byte, 0, len(units)*2)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func installWindows(e Env, res *Result) error {
	p := filepath.Join(e.StateDir, WindowsXMLFile)
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, UTF16LE(WindowsTaskXML(e)), 0o600); err != nil {
		return err
	}
	res.Files = append(res.Files, p)
	note := fmt.Sprintf("Windows: create the scheduled task by running, in a normal (not admin) prompt:\n  schtasks /Create /TN \"%s\" /XML \"%s\"\nIt runs `wy tick --quiet` at logon and every minute.", WindowsTask, p)
	res.Notes = append(res.Notes, note)
	e.printf("%s\n", note)
	return nil
}
