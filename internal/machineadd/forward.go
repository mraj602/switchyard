package machineadd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/sshx"
	"github.com/mraj602/watchyard/internal/version"
)

// The forward block (machines.md 3.3): one marked block per machine in ~/.ssh/watchyard.conf
// and, once, `Include watchyard.conf` as the first line of ~/.ssh/config. Both files are
// journaled in a setup backup set, so `wy machine remove` and `wy setup --undo` take them out.

// Files in ~/.ssh.
const (
	ForwardConf = brand.Slug + ".conf"
	IncludeLine = "Include " + ForwardConf + "\n"
)

func includeLine(name string) string { return "Include " + name + "\n" }

// Forward describes one machine's RemoteForward.
type Forward struct {
	// Host is the ssh Host pattern: the host part of what the person typed (a `Host` line
	// never matches `user@`).
	Host    string
	Machine string
	Name    string
	// Listen is the remote end: a loopback port (Port) or a socket path (Socket).
	Port   int
	Socket string
	// Target is the hub as the operator reaches it: host:port.
	Target string
}

// HostPattern is the Host line value for a host word ([user@]host).
func HostPattern(hostWord string) string {
	if i := strings.LastIndexByte(hostWord, '@'); i >= 0 {
		return hostWord[i+1:]
	}
	return hostWord
}

// Listen is the left side of the RemoteForward.
func (f Forward) Listen() string {
	if f.Socket != "" {
		return f.Socket
	}
	return "127.0.0.1:" + strconv.Itoa(f.Port)
}

// Dial is the remote's hub.dial for this forward.
func (f Forward) Dial() string {
	if f.Socket != "" {
		return "unix:" + f.Socket
	}
	return "127.0.0.1:" + strconv.Itoa(f.Port)
}

// R is the -R argument that makes the forward for one connection (4.2 step 5).
func (f Forward) R() string { return f.Listen() + ":" + f.Target }

var nameSafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Block is the exact text of the block.
func (f Forward) Block() string {
	name := nameSafe.ReplaceAllString(f.Name, "_")
	var b strings.Builder
	fmt.Fprintf(&b, "# wy-forward v1 machine=%s name=%s (wy machine add; wy machine remove undoes it)\n", f.Machine, name)
	fmt.Fprintf(&b, "Host %s\n", f.Host)
	fmt.Fprintf(&b, "  RemoteForward %s %s\n", f.Listen(), f.Target)
	if f.Socket != "" {
		b.WriteString("  StreamLocalBindMask 0177\n")
	}
	b.WriteString("  ExitOnForwardFailure no\n  ServerAliveInterval 30\n  ServerAliveCountMax 3\n")
	fmt.Fprintf(&b, "# end wy-forward %s\n", name)
	return b.String()
}

func (f Forward) check() error {
	switch {
	case f.Host == "" || strings.ContainsAny(f.Host, " \t\r\n#\"'"):
		return fmt.Errorf("forward: bad host %q", f.Host)
	case f.Machine == "" || strings.ContainsAny(f.Machine, " \t\r\n"):
		return errors.New("forward: no machine id")
	case f.Socket == "" && (f.Port < 1 || f.Port > 65535):
		return fmt.Errorf("forward: bad port %d", f.Port)
	case strings.ContainsAny(f.Socket+f.Target, " \t\r\n\"'"):
		return errors.New("forward: bad socket or target")
	case f.Target == "":
		return errors.New("forward: no target")
	}
	return nil
}

// Adopted reports whether the person's own config (ssh -G) already forwards some port of
// the forward range, or the socket, to target: then nothing is written and that port is used.
func Adopted(c sshx.Config, target, socket string) (port int, ok bool) {
	for _, fw := range c.RemoteForwards() {
		f := strings.Fields(fw)
		if len(f) != 2 || f[1] != target {
			continue
		}
		if socket != "" {
			if f[0] == socket {
				return 0, true
			}
			continue
		}
		h, p, found := strings.Cut(f[0], ":")
		n, err := strconv.Atoi(p)
		if found && (h == "127.0.0.1" || h == "localhost") && err == nil && n >= FirstForwardPort && n <= LastForwardPort {
			return n, true
		}
	}
	return 0, false
}

// blockRange finds the block of a machine: its start, its end and whether it exists.
func blockRange(content []byte, machine string) (int, int, bool) {
	tag := "wy-forward"
	head := []byte("# " + tag + " v1 machine=" + machine + " ")
	i := bytes.Index(content, head)
	if i < 0 || (i > 0 && content[i-1] != '\n') {
		return 0, 0, false
	}
	rest := content[i:]
	j := bytes.Index(rest, []byte("\n# end "+tag+" "))
	if j < 0 {
		return 0, 0, false
	}
	k := bytes.IndexByte(rest[j+1:], '\n')
	end := i + j + 1 + len(rest[j+1:])
	if k >= 0 {
		end = i + j + 1 + k + 1
	}
	return i, end, true
}

// hasInclude reports whether cfg already includes watchyard.conf before its first Host or
// Match line: `Include watchyard.conf`, quoted, with the full path, or through a glob
// (`Include ~/.ssh/*.conf`, `Include *`), alone or among other patterns.
func hasInclude(cfg []byte, home, name string) bool {
	sshDir := filepath.ToSlash(filepath.Join(home, ".ssh"))
	for _, l := range strings.Split(string(cfg), "\n") {
		t := strings.TrimSpace(l)
		low := strings.ToLower(t)
		if strings.HasPrefix(low, "host ") || strings.HasPrefix(low, "match ") {
			return false
		}
		f := splitConfigWords(t)
		if len(f) < 2 || !strings.EqualFold(f[0], "include") {
			continue
		}
		for _, pat := range f[1:] {
			switch {
			case strings.HasPrefix(pat, "~/"):
				pat = filepath.ToSlash(filepath.Join(home, pat[2:]))
			case !strings.HasPrefix(pat, "/") && !filepath.IsAbs(pat):
				pat = sshDir + "/" + pat // relative patterns are relative to ~/.ssh
			}
			if ok, _ := path.Match(pat, sshDir+"/"+name); ok {
				return true
			}
		}
	}
	return false
}

// splitConfigWords splits an ssh_config line into words, honouring double quotes.
func splitConfigWords(line string) []string {
	var out []string
	var cur strings.Builder
	inQ, have := false, false
	for _, r := range line {
		switch {
		case r == '"':
			inQ, have = !inQ, true
		case !inQ && (r == ' ' || r == '\t'):
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		case !inQ && r == '#':
			if have {
				out = append(out, cur.String())
			}
			return out
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

// OwnedForward finds the block wy wrote for this machine in ~/.ssh/watchyard.conf and
// returns its listening port or socket. Such a block is wy's own, never "adopted".
func OwnedForward(home, machine string) (port int, socket string, ok bool) {
	sshDir := filepath.Join(home, ".ssh")
	b, err := readOptional(filepath.Join(sshDir, ForwardConf))
	if err != nil {
		return 0, "", false
	}
	i, j, found := blockRange(b, machine)
	if !found {
		return 0, "", false
	}
	for _, l := range strings.Split(string(b[i:j]), "\n") {
		f := strings.Fields(l)
		if len(f) == 3 && strings.EqualFold(f[0], "RemoteForward") {
			if h, p, cut := strings.Cut(f[1], ":"); cut && h == "127.0.0.1" {
				n, _ := strconv.Atoi(p)
				return n, "", true
			}
			return 0, f[1], true
		}
	}
	return 0, "", true
}

// ForwardResult says what WriteForward did.
type ForwardResult struct {
	Changed bool
	Set     string // backup set id, when something was written
}

// lockWait takes setup.lock, waiting up to 30 seconds for another command (a second add
// running at the same time) to finish; apply.Lock itself never waits.
func lockWait(stateDir string) (func(), error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		unlock, err := apply.Lock(stateDir)
		if !errors.Is(err, apply.ErrLocked) || time.Now().After(deadline) {
			return unlock, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// journalLocked runs fn with the paths journaled as write_file entries of a new backup set
// (command goes in the manifest); the caller holds setup.lock. A failing fn rolls the set back.
func journalLocked(stateDir, command string, now time.Time, paths []string, fn func() error) (string, error) {
	set, err := backup.Create(stateDir, backup.Header{Command: command, WyVersion: version.Version}, now)
	if err != nil {
		return "", err
	}
	var specs []backup.PathSpec
	seen := map[string]bool{}
	for _, p := range paths {
		for _, d := range backup.MissingAncestors(p) {
			if !seen[d] {
				seen[d] = true
				specs = append(specs, backup.PathSpec{Path: d, Intended: &model.PathState{Type: model.TypeDir}})
			}
		}
		specs = append(specs, backup.PathSpec{Path: p, Save: backup.SaveCopy})
	}
	tx, err := set.Begin("f1", model.OpWriteFile, backup.Spec{Paths: specs})
	if err != nil {
		return "", err
	}
	ferr := fn()
	if err := tx.Finish(nil); err != nil && ferr == nil {
		ferr = err
	}
	if ferr != nil {
		// Roll the set back: nothing half-written stays.
		_, _ = backup.UndoSet(context.Background(), set, backup.Deps{Sharer: model.UnimplementedSharer{}}, backup.Options{}, true)
		return "", ferr
	}
	if err := set.SetStatus(model.StatusApplied); err != nil {
		return "", err
	}
	return set.ID(), nil
}

// WriteForward writes (or replaces) the machine's block and the Include line. It is
// idempotent: the same block twice changes nothing and makes no backup set.
func WriteForward(home, stateDir string, f Forward, command string, now time.Time) (ForwardResult, error) {
	if err := f.check(); err != nil {
		return ForwardResult{}, err
	}
	dir := filepath.Join(home, ".ssh")
	name := ForwardConf
	conf, cfgPath := filepath.Join(dir, name), filepath.Join(dir, "config")
	// The lock comes first: two adds at once must not both read the files and then each
	// write its own block over the other's.
	unlock, err := lockWait(stateDir)
	if err != nil {
		return ForwardResult{}, err
	}
	defer unlock()
	curConf, err := readOptional(conf)
	if err != nil {
		return ForwardResult{}, err
	}
	curCfg, err := readOptional(cfgPath)
	if err != nil {
		return ForwardResult{}, err
	}
	block := []byte(f.Block())
	newConf := append([]byte(nil), curConf...)
	if i, j, ok := blockRange(curConf, f.Machine); ok {
		newConf = append(append(append([]byte(nil), curConf[:i]...), block...), curConf[j:]...)
	} else {
		if len(newConf) > 0 && newConf[len(newConf)-1] != '\n' {
			newConf = append(newConf, '\n')
		}
		newConf = append(newConf, block...)
	}
	newCfg := curCfg
	if !hasInclude(curCfg, home, name) {
		newCfg = append([]byte(includeLine(name)), curCfg...)
	}
	if bytes.Equal(newConf, curConf) && bytes.Equal(newCfg, curCfg) {
		return ForwardResult{}, nil
	}
	id, err := journalLocked(stateDir, command, now, []string{cfgPath, conf}, func() error {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if !bytes.Equal(newConf, curConf) {
			if err := atomicfile.Write(conf, newConf, modeOr(conf, 0o600)); err != nil {
				return err
			}
		}
		if !bytes.Equal(newCfg, curCfg) {
			return atomicfile.Write(cfgPath, newCfg, modeOr(cfgPath, 0o600))
		}
		return nil
	})
	if err != nil {
		return ForwardResult{}, err
	}
	return ForwardResult{Changed: true, Set: id}, nil
}

// RemoveForward takes the machine's block out again. When the backup set that wrote it is
// the last change to both files, that set is undone, which restores them exactly (bytes,
// mode, mtime; a file or ~/.ssh it created is removed). Otherwise only the block's bytes go
// (and the Include line and watchyard.conf when no block is left), journaled in a new set.
func RemoveForward(home, stateDir string, machine, setID, command string, now time.Time) (bool, error) {
	dir := filepath.Join(home, ".ssh")
	name := ForwardConf
	conf, cfgPath := filepath.Join(dir, name), filepath.Join(dir, "config")
	unlock, err := lockWait(stateDir)
	if err != nil {
		return false, err
	}
	defer unlock()
	curConf, err := readOptional(conf)
	if err != nil {
		return false, err
	}
	i, j, ok := blockRange(curConf, machine)
	if !ok {
		return false, nil
	}
	if setID != "" {
		if done, err := undoIfUnchanged(stateDir, setID, conf, machine); err != nil || done {
			return done, err
		}
	}
	curCfg, err := readOptional(cfgPath)
	if err != nil {
		return false, err
	}
	newConf := append(append([]byte(nil), curConf[:i]...), curConf[j:]...)
	removeConf := len(bytes.TrimSpace(newConf)) == 0
	newCfg := curCfg
	if inc := includeLine(name); removeConf && bytes.HasPrefix(curCfg, []byte(inc)) {
		newCfg = curCfg[len(inc):]
	}
	_, err = journalLocked(stateDir, command, now, []string{cfgPath, conf}, func() error {
		if removeConf {
			if err := os.Remove(conf); err != nil {
				return err
			}
		} else if err := atomicfile.Write(conf, newConf, modeOr(conf, 0o600)); err != nil {
			return err
		}
		if !bytes.Equal(newCfg, curCfg) {
			return atomicfile.Write(cfgPath, newCfg, modeOr(cfgPath, 0o600))
		}
		return nil
	})
	return err == nil, err
}

// undoIfUnchanged undoes the set (the caller holds setup.lock) when every path it wrote is
// still as it left it and the file watchyard.conf had no block of this machine before the
// set: undoing a set that replaced an older block would bring that block back.
func undoIfUnchanged(stateDir, id, conf, machine string) (bool, error) {
	set, err := backup.Open(stateDir, id)
	if err != nil {
		return false, nil // gone (pruned): fall back to removing the block
	}
	m := set.Manifest()
	if m.Status != model.StatusApplied {
		return false, nil
	}
	for _, e := range m.Entries {
		for _, p := range e.Paths {
			if p.After == nil {
				return false, nil
			}
			if p.Path == conf && p.Before.Type != model.TypeAbsent {
				if p.Before.Saved == "" {
					return false, nil
				}
				if old, err := os.ReadFile(set.Abs(p.Before.Saved)); err != nil {
					return false, nil
				} else if _, _, had := blockRange(old, machine); had {
					return false, nil
				}
			}
			now, err := backup.Capture(p.Path)
			if err != nil || !backup.StateEq(now, *p.After) {
				return false, nil
			}
		}
	}
	res, err := backup.UndoSet(context.Background(), set, backup.Deps{Sharer: model.UnimplementedSharer{}}, backup.Options{}, false)
	if err != nil {
		return false, err
	}
	return len(res.Skipped) == 0, nil
}

func readOptional(p string) ([]byte, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func modeOr(p string, def os.FileMode) os.FileMode {
	if fi, err := os.Stat(p); err == nil {
		return fi.Mode().Perm()
	}
	return def
}
