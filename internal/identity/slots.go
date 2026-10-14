package identity

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Slot is one Claude Code config folder on this machine (identity.md section 3).
type Slot struct {
	Dir       string // absolute, cleaned, symlinks not resolved
	Name      string // "~/.claude-2": the form sent to the hub
	StateFile string // the .claude.json that holds oauthAccount
	Default   bool   // this is ~/.claude with CLAUDE_CONFIG_DIR unset (state in ~/.claude.json)
	// Explicit: CLAUDE_CONFIG_DIR was set (by the user) to ~/.claude itself. Claude Code then
	// keeps its state in ~/.claude/.claude.json, a different file from the default view's
	// ~/.claude.json, so it is not Default and has its own login cache entry (Key).
	Explicit bool
}

// Key identifies the slot's login cache entry: the name, except that the explicit view of
// ~/.claude is a different state file (and possibly a different login) from the default one.
func (s Slot) Key() string {
	if s.Explicit {
		return s.Name + "#explicit"
	}
	return s.Name
}

// Locator finds slots under one home folder.
type Locator struct {
	Home string
	FS   FS
}

// Tilde writes dir as "~/rest" with "/" separators when it is inside home, else absolute.
func Tilde(home, dir string) string {
	home, dir = filepath.Clean(home), filepath.Clean(dir)
	if dir == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(dir, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return filepath.ToSlash(dir)
}

// expand turns "~", "~/x" into an absolute path under home; other paths are cleaned.
func (l Locator) expand(p string) string {
	switch {
	case p == "~":
		return filepath.Clean(l.Home)
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(l.Home, filepath.FromSlash(p[2:]))
	}
	return filepath.Clean(p)
}

// SlotFor builds the Slot for a config folder, applying the ~/.claude state file rule:
// for ~/.claude (compared after resolving symlinks on both sides) the state file is
// ~/.claude.json, for any other folder <folder>/.claude.json.
func (l Locator) SlotFor(dir string) Slot {
	dir = l.expand(dir)
	s := Slot{Dir: dir, Name: Tilde(l.Home, dir), StateFile: filepath.Join(dir, ".claude.json")}
	def := filepath.Join(l.Home, ".claude")
	if l.sameFolder(dir, def) {
		s.Default = true
		s.StateFile = filepath.Join(l.Home, ".claude.json")
	}
	return s
}

func (l Locator) sameFolder(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := l.FS.EvalSymlinks(a)
	rb, err2 := l.FS.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// Current is the slot a hook or status-line call belongs to: CLAUDE_CONFIG_DIR if set
// and non-empty, else ~/.claude. The default-slot rule (state in ~/.claude.json, variable
// unset for `claude auth status`) holds only when the variable is unset: set to ~/.claude
// (or a link to it) Claude Code reads <dir>/.claude.json (testdata/README.md).
func (l Locator) Current(getenv func(string) string) Slot {
	if v := getenv("CLAUDE_CONFIG_DIR"); v != "" {
		s := l.SlotFor(v)
		if s.Default {
			s.Default, s.Explicit = false, true
			s.StateFile = filepath.Join(s.Dir, ".claude.json")
		}
		return s
	}
	return l.SlotFor(filepath.Join(l.Home, ".claude"))
}

// Discover finds every slot: ~/.claude and the folders directly in home named
// ^\.claude([-_].+)?$, plus extra (config.toml slots.extra) and CLAUDE_CONFIG_DIR. A folder
// found by name or listed in extra counts only if it is a directory holding settings.json,
// .claude.json, projects/ or sessions/. The CLAUDE_CONFIG_DIR folder counts if it is a
// directory. The result is ~/.claude first, then the rest in natural name order.
func (l Locator) Discover(extra []string, getenv func(string) string) ([]Slot, error) {
	entries, err := l.FS.ReadDir(l.Home)
	if err != nil {
		return nil, fmt.Errorf("list home folder: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if claudeFolder.MatchString(e.Name()) && l.isDir(filepath.Join(l.Home, e.Name()), e) {
			dirs = append(dirs, filepath.Join(l.Home, e.Name()))
		}
	}
	for _, x := range extra {
		dirs = append(dirs, l.expand(x))
	}
	var out []Slot
	seen := map[string]bool{}
	add := func(dir string, force bool) {
		s := l.SlotFor(dir)
		if seen[s.Dir] || !(force || l.looksLikeSlot(s.Dir)) {
			return
		}
		seen[s.Dir] = true
		out = append(out, s)
	}
	for _, d := range dirs {
		add(d, false)
	}
	if v := getenv("CLAUDE_CONFIG_DIR"); v != "" {
		if fi, err := l.FS.Stat(l.expand(v)); err == nil && fi.IsDir() {
			add(v, true)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return naturalLess(out[i].Name, out[j].Name)
	})
	return out, nil
}

// isDir follows symlinks (~/.claude-2 may be a link).
func (l Locator) isDir(path string, e fs.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	fi, err := l.FS.Stat(path)
	return err == nil && fi.IsDir()
}

func (l Locator) looksLikeSlot(dir string) bool {
	fi, err := l.FS.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	for _, n := range []string{"settings.json", ".claude.json", "projects", "sessions"} {
		if _, err := l.FS.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// naturalLess orders names so that ~/.claude-2 comes before ~/.claude-10.
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			ia, ib := digitsEnd(a), digitsEnd(b)
			na, nb := strings.TrimLeft(a[:ia], "0"), strings.TrimLeft(b[:ib], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[ia:], b[ib:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digitsEnd(s string) int {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}
