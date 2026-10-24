package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// RefreshResult is what Refresh did.
type RefreshResult struct {
	Installed bool // a unit file was found
	Rewritten bool // its content changed for this binary and was written again
	Restarted bool
	// Migrated names the unit from before the rename that was replaced by one under the
	// current name, keeping its arguments and environment (rename.md 2.2).
	Migrated string
}

// Refresh is `wy update`'s hub service step (machines.md 7.3 step 7): when the service is
// installed here, rewrite its unit if this binary renders it differently (keeping the
// arguments it was installed with), then restart it so the new binary serves. Nothing is
// installed where there is no unit. On Windows the task is only restarted.
func (s *Service) Refresh(ctx context.Context, name string) (RefreshResult, error) {
	var res RefreshResult
	spec, err := normalize(model.ServiceSpec{Name: name})
	if err != nil {
		return res, err
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	e := s.Env
	if inst := Installed(e, spec.Name); inst != spec.Name {
		return s.migrate(ctx, spec, inst)
	}
	p := InstalledPath(e, spec.Name)
	cur, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.Installed = true
	switch e.GOOS {
	case "linux":
		args, ok := systemdExecArgs(string(cur))
		if ok {
			spec.Args = args
			e.Pairs, e.PairsSet = SystemdEnv(string(cur)), true
			if want := SystemdUnit(e, spec); want != string(cur) {
				if err := writeFile(p, []byte(want), 0o644); err != nil {
					return res, err
				}
				res.Rewritten = true
				if out, err := s.run("systemctl", "--user", "daemon-reload"); err != nil {
					return res, fmt.Errorf("systemctl --user daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
				}
			}
		}
		// try-restart: a stopped service stays stopped.
		if out, err := s.run("systemctl", "--user", "try-restart", spec.Name+".service"); err != nil {
			return res, fmt.Errorf("systemctl --user try-restart %s.service: %v: %s", spec.Name, err, strings.TrimSpace(string(out)))
		}
		res.Restarted = true
	case "darwin":
		label := fmt.Sprintf("gui/%d/%s", e.UID, LaunchdLabel(spec.Name))
		if args, ok := launchdProgramArgs(string(cur)); ok {
			spec.Args = args
			e.Pairs, e.PairsSet = LaunchdEnv(string(cur)), true
			if want := LaunchdPlist(e, spec); want != string(cur) {
				if err := writeFile(p, []byte(want), 0o644); err != nil {
					return res, err
				}
				res.Rewritten = true
				_, _ = s.run("launchctl", "bootout", label)
				if out, err := s.run("launchctl", "bootstrap", fmt.Sprintf("gui/%d", e.UID), p); err != nil {
					return res, fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
				}
				res.Restarted = true
				return res, nil
			}
		}
		if out, err := s.run("launchctl", "kickstart", "-k", label); err != nil {
			return res, fmt.Errorf("launchctl kickstart -k %s: %v: %s", label, err, strings.TrimSpace(string(out)))
		}
		res.Restarted = true
	case "windows":
		task := WindowsTask(spec.Name)
		_, _ = s.run("schtasks", "/End", "/TN", task)
		if out, err := s.run("schtasks", "/Run", "/TN", task); err != nil {
			return res, fmt.Errorf("schtasks /Run: %v: %s", err, strings.TrimSpace(string(out)))
		}
		res.Restarted = true
	default:
		return res, fmt.Errorf("service: no user service support for %s", e.GOOS)
	}
	return res, nil
}

// migrate replaces the service installed as old (its name before the rename) with one named
// spec.Name: the same arguments and environment, then the old one stopped and removed, then
// the new one installed and started (rename.md 2.2).
func (s *Service) migrate(ctx context.Context, spec model.ServiceSpec, old string) (RefreshResult, error) {
	res := RefreshResult{Installed: true, Migrated: UnitName(s.Env.GOOS, old)}
	args, env, ok := InstalledUnit(s.Env, old)
	if s.Env.GOOS == "windows" {
		args, ok = windowsTaskArgs(InstalledPath(s.Env, old))
	}
	if ok {
		spec.Args = args
	}
	c := *s
	if env != nil {
		c.Env.Pairs, c.Env.PairsSet = currentEnvNames(env), true
	}
	if err := c.Install(ctx, spec); err != nil { // Install removes old first
		return res, err
	}
	res.Rewritten, res.Restarted = true, true
	return res, nil
}

// currentEnvNames renames the variables of the command's old name (SY_HOME, ...) in a legacy
// unit's environment to the current prefix (rename.md 2.13).
func currentEnvNames(env [][2]string) [][2]string {
	op := brand.OldEnvPrefix()
	if op == "" {
		return env
	}
	out := make([][2]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv[0], op) {
			kv[0] = brand.EnvPrefix + strings.TrimPrefix(kv[0], op)
		}
		out = append(out, kv)
	}
	return out
}

var taskArguments = regexp.MustCompile(`(?s)<Arguments>(.*?)</Arguments>`)

// windowsTaskArgs reads the arguments of a task XML that WindowsTaskXML wrote (UTF-16LE),
// undoing windowsArg.
func windowsTaskArgs(path string) ([]string, bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(b)%2 != 0 {
		return nil, false
	}
	u := make([]rune, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, rune(uint16(b[i])|uint16(b[i+1])<<8))
	}
	m := taskArguments.FindStringSubmatch(strings.TrimPrefix(string(u), "\ufeff"))
	if m == nil {
		return nil, false
	}
	var out []string
	var cur strings.Builder
	quoted, have := false, false
	v := html.UnescapeString(m[1])
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && i+1 < len(v) && v[i+1] == '"':
			cur.WriteByte('"')
			i++
		case c == '"':
			quoted, have = !quoted, true
		case c == ' ' && !quoted:
			if have || cur.Len() > 0 {
				out = append(out, cur.String())
			}
			cur.Reset()
			have = false
		default:
			cur.WriteByte(c)
		}
	}
	if have || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, len(out) > 0
}

// systemdExecArgs returns the arguments after the program of the unit's ExecStart= line,
// undoing systemdQuote. ok is false when there is no ExecStart or it does not parse.
func systemdExecArgs(unit string) ([]string, bool) {
	for _, ln := range strings.Split(unit, "\n") {
		v, found := strings.CutPrefix(ln, "ExecStart=")
		if !found {
			continue
		}
		words, ok := splitSystemd(v)
		if !ok || len(words) == 0 {
			return nil, false
		}
		return words[1:], true
	}
	return nil, false
}

// splitSystemd splits an ExecStart value into words as systemd does for what systemdQuote
// writes: plain words, or double-quoted words with \\, \", \xNN, %% and $$.
func splitSystemd(s string) ([]string, bool) { return splitSystemdWords(s, true) }

// splitSystemdWords is splitSystemd; dollar says whether "$$" is an escaped "$" (ExecStart=)
// or two plain characters (Environment=).
func splitSystemdWords(s string, dollar bool) ([]string, bool) {
	var out []string
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		var b strings.Builder
		if s[i] != '"' {
			for i < len(s) && s[i] != ' ' && s[i] != '\t' {
				b.WriteByte(s[i])
				i++
			}
			out = append(out, b.String())
			continue
		}
		i++ // opening quote
		closed := false
		for i < len(s) {
			c := s[i]
			switch {
			case c == '"':
				closed = true
				i++
			case c == '\\' && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '"'):
				b.WriteByte(s[i+1])
				i += 2
				continue
			case c == '\\' && i+3 < len(s) && s[i+1] == 'x':
				n, err := strconv.ParseUint(s[i+2:i+4], 16, 8)
				if err != nil {
					return nil, false
				}
				b.WriteByte(byte(n))
				i += 4
				continue
			case (c == '%' || (c == '$' && dollar)) && i+1 < len(s) && s[i+1] == c:
				b.WriteByte(c)
				i += 2
				continue
			default:
				b.WriteByte(c)
				i++
				continue
			}
			break
		}
		if !closed {
			return nil, false
		}
		out = append(out, b.String())
	}
	return out, true
}

var (
	plistArgs   = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>(.*?)</array>`)
	plistString = regexp.MustCompile(`<string>(.*?)</string>`)
)

// launchdProgramArgs returns the ProgramArguments after the program, unescaped.
func launchdProgramArgs(plist string) ([]string, bool) {
	m := plistArgs.FindStringSubmatch(plist)
	if m == nil {
		return nil, false
	}
	var words []string
	for _, w := range plistString.FindAllStringSubmatch(m[1], -1) {
		words = append(words, html.UnescapeString(w[1]))
	}
	if len(words) == 0 {
		return nil, false
	}
	return words[1:], true
}

// SystemdEnv returns the NAME=value pairs of every Environment= line of a unit, in order
// (what the service or timer was installed with; the install may have been made from a
// shell with other folder overrides than the process that now runs `wy update`).
func SystemdEnv(unit string) [][2]string {
	var out [][2]string
	for _, ln := range strings.Split(unit, "\n") {
		v, found := strings.CutPrefix(ln, "Environment=")
		if !found {
			continue
		}
		words, ok := splitSystemdWords(v, false)
		if !ok {
			continue
		}
		for _, w := range words {
			if k, val, ok := strings.Cut(w, "="); ok && k != "" {
				out = append(out, [2]string{k, val})
			}
		}
	}
	return out
}

var plistEnvDict = regexp.MustCompile(`(?s)<key>EnvironmentVariables</key>\s*<dict>(.*?)</dict>`)
var plistKeyString = regexp.MustCompile(`(?s)<key>(.*?)</key>\s*<string>(.*?)</string>`)

// LaunchdEnv returns the EnvironmentVariables of a plist, unescaped, in file order.
func LaunchdEnv(plist string) [][2]string {
	m := plistEnvDict.FindStringSubmatch(plist)
	if m == nil {
		return nil
	}
	var out [][2]string
	for _, kv := range plistKeyString.FindAllStringSubmatch(m[1], -1) {
		out = append(out, [2]string{html.UnescapeString(kv[1]), html.UnescapeString(kv[2])})
	}
	return out
}

// InstalledUnit reads the installed hub service's unit (not the Windows task) and returns the
// arguments after the program and the environment it was installed with. ok is false when
// there is no readable unit. `wy update` uses it to find the database the hub really opens:
// the unit may carry WY_DATA_DIR or --db that the updating shell does not.
func InstalledUnit(e Env, name string) (args []string, env [][2]string, ok bool) {
	spec, err := normalize(model.ServiceSpec{Name: name})
	if err != nil {
		return nil, nil, false
	}
	b, err := os.ReadFile(InstalledPath(e, Installed(e, spec.Name)))
	if err != nil {
		return nil, nil, false
	}
	switch e.GOOS {
	case "linux":
		args, ok = systemdExecArgs(string(b))
		env = SystemdEnv(string(b))
	case "darwin":
		args, ok = launchdProgramArgs(string(b))
		env = LaunchdEnv(string(b))
	}
	return args, env, ok
}
