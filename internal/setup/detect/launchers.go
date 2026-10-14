package detect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mraj602/switchyard/internal/setup/launcher"
	"github.com/mraj602/switchyard/internal/setup/model"
)

var (
	launcherFileRE = regexp.MustCompile(`^(claude-[0-9A-Za-z_-]+)(\.cmd)?$`)
	nameRE         = `claude(?:-[0-9A-Za-z_-]+)?`
	fnRE           = regexp.MustCompile(`(?:^|[\s;&|{(])(?:function\s+)?(` + nameRE + `) *\(\)`)
	fnKeywordRE    = regexp.MustCompile(`(?:^|[\s;&|{(])function\s+(` + nameRE + `)\b`)
	aliasRE        = regexp.MustCompile(`(?:^|[\s;&|{(])alias\s+(` + nameRE + `)=`)
	pathTokenRE    = regexp.MustCompile(`(?:~|\$HOME|\$\{HOME\}|/)[^\s"'();|&<>` + "`" + `]*`)
)

// rcFiles are the shell start-up files scanned statically (step 1.5).
func (x *detector) rcFiles() []string {
	var out []string
	for _, n := range []string{".bashrc", ".bash_profile", ".profile", ".zshrc"} {
		out = append(out, filepath.Join(x.home, n))
	}
	fishDir := filepath.Join(x.home, ".config", "fish")
	_ = filepath.WalkDir(fishDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.HasSuffix(p, ".fish") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (x *detector) folderLauncherName(f model.Folder) string {
	if f.Default {
		return "claude"
	}
	return launcher.Name(f.Dir)
}

// detectLaunchers is step 1.5 (launchers) and, from the scripts they call, the hand-made
// MCP sync of step 1.6.
func (x *detector) detectLaunchers(ctx context.Context) {
	seen := map[string]bool{}
	add := func(l model.Launcher) {
		k := l.Name + "\x00" + l.Kind + "\x00" + l.Path
		if !seen[k] {
			seen[k] = true
			x.st.Launchers = append(x.st.Launchers, l)
		}
	}

	// Executables named claude-* on PATH and in ~/.local/bin.
	dirs := []string{filepath.Join(x.home, ".local", "bin")}
	dirs = append(dirs, filepath.SplitList(x.Getenv("PATH"))...)
	var files []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			m := launcherFileRE.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			p := filepath.Join(d, e.Name())
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() || (x.GOOS != "windows" && fi.Mode()&0o111 == 0) {
				continue
			}
			l := model.Launcher{Name: m[1], Kind: model.LauncherFile, Path: x.tilde(p)}
			if b, err := readSmall(p); err == nil {
				sum := sha256.Sum256(b)
				l.Hash = hex.EncodeToString(sum[:])
				if info, ok := launcher.ParseMarker(b); ok {
					l.Ours, l.Folder = true, info.Slot
				}
				if bytes.Contains(b, []byte("mcpServers")) && !l.Ours {
					l.MentionsMCP = true
				}
			}
			add(l)
			if !l.Ours {
				files = append(files, p)
			}
		}
	}

	// Shell functions and aliases: the probe, then the static scan.
	names := []string{"claude"}
	for _, f := range x.st.Folders {
		if n := x.folderLauncherName(f); n != "claude" {
			names = append(names, n)
		}
	}
	sort.Strings(names[1:])
	if x.ShellProbe != nil {
		if out, err := x.ShellProbe(ctx, names); err == nil {
			for n, kind := range parseProbe(out, names) {
				add(model.Launcher{Name: n, Kind: kind})
			}
		}
	} else if out, err := realProbe(ctx, x.Getenv("SHELL"), names); err == nil {
		for n, kind := range parseProbe(out, names) {
			add(model.Launcher{Name: n, Kind: kind})
		}
	}
	var scripts []string
	for _, rc := range x.rcFiles() {
		b, err := readSmall(rc)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "#") {
				continue
			}
			for _, m := range fnRE.FindAllStringSubmatch(line, -1) {
				add(model.Launcher{Name: m[1], Kind: model.LauncherFunction, Path: x.tilde(rc)})
			}
			for _, m := range fnKeywordRE.FindAllStringSubmatch(line, -1) {
				add(model.Launcher{Name: m[1], Kind: model.LauncherFunction, Path: x.tilde(rc)})
			}
			for _, m := range aliasRE.FindAllStringSubmatch(line, -1) {
				add(model.Launcher{Name: m[1], Kind: model.LauncherAlias, Path: x.tilde(rc)})
			}
			if strings.Contains(line, `eval "claude-$`) {
				x.warn = append(x.warn, x.tilde(rc)+" defines launchers with eval; only the shell probe can see them")
			}
			for _, p := range x.pathTokens(line) {
				if strings.Contains(filepath.Base(p), "claude") {
					scripts = append(scripts, p)
				}
			}
		}
	}

	x.scanScripts(append(files, scripts...), add)

	// Per-folder launcher: file (ours first) > function > alias.
	rank := map[string]int{model.LauncherFile: 1, model.LauncherFunction: 2, model.LauncherAlias: 3}
	for i := range x.st.Folders {
		f := &x.st.Folders[i]
		name := x.folderLauncherName(*f)
		var best *model.Launcher
		for j := range x.st.Launchers {
			l := &x.st.Launchers[j]
			if l.Name != name || rank[l.Kind] == 0 {
				continue
			}
			if best == nil || rank[l.Kind] < rank[best.Kind] || (l.Kind == best.Kind && l.Ours && !best.Ours) {
				best = l
			}
		}
		if best != nil {
			f.Launcher = &model.FolderLauncher{Name: name, Kind: best.Kind, Ours: best.Ours}
			if best.Folder == "" {
				best.Folder = f.Folder
			}
		}
	}
}

// pathTokens returns the files named on a line by ~, $HOME or an absolute path.
func (x *detector) pathTokens(line string) []string {
	var out []string
	for _, tok := range pathTokenRE.FindAllString(line, -1) {
		tok = strings.TrimRight(tok, ",;:")
		switch {
		case strings.HasPrefix(tok, "${HOME}"):
			tok = x.home + tok[len("${HOME}"):]
		case strings.HasPrefix(tok, "$HOME"):
			tok = x.home + tok[len("$HOME"):]
		case strings.HasPrefix(tok, "~"):
			tok = x.home + tok[1:]
		}
		if filepath.IsAbs(tok) {
			out = append(out, filepath.Clean(tok))
		}
	}
	return out
}

// scanScripts follows the scripts launchers call, three levels deep and at most 24 files,
// looking for `mcpServers` (a hand-made MCP sync).
func (x *detector) scanScripts(start []string, add func(model.Launcher)) {
	type item struct {
		path  string
		depth int
	}
	var queue []item
	for _, p := range start {
		queue = append(queue, item{p, 0})
	}
	done := map[string]bool{}
	for len(queue) > 0 && len(done) < 24 {
		it := queue[0]
		queue = queue[1:]
		if done[it.path] {
			continue
		}
		done[it.path] = true
		fi, err := os.Stat(it.path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > 256<<10 {
			continue
		}
		b, err := readSmall(it.path)
		if err != nil {
			continue
		}
		if _, ours := launcher.ParseMarker(b); ours {
			continue
		}
		mentions := bytes.Contains(b, []byte("mcpServers"))
		if mentions {
			x.st.MCP.HandMade = true
			if x.st.MCP.Script == "" {
				x.st.MCP.Script = x.tilde(it.path)
			}
		}
		if base := filepath.Base(it.path); strings.Contains(base, "claude") || mentions {
			add(model.Launcher{Name: base, Kind: model.LauncherScript, Path: x.tilde(it.path), MentionsMCP: mentions})
		}
		if it.depth < 3 {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				for _, p := range x.pathTokens(line) {
					queue = append(queue, item{p, it.depth + 1})
				}
			}
		}
	}
}

func readSmall(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

func realProbe(ctx context.Context, shell string, names []string) (string, error) {
	if shell == "" {
		return "", os.ErrNotExist
	}
	cctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	script := "for n in " + strings.Join(names, " ") + `; do type "$n"; done`
	cmd := exec.CommandContext(cctx, shell, "-ic", script)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "TERM=dumb")
	out, _ := cmd.CombinedOutput() // `type` exits 1 for a name that is not found
	return string(out), nil
}

// parseProbe reads the output of `type <name>` for each name: bash prints "<n> is a
// function", "<n> is aliased to ...", zsh "<n> is a shell function from ...".
func parseProbe(out string, names []string) map[string]string {
	res := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		for _, n := range names {
			rest, ok := strings.CutPrefix(line, n+" is ")
			if !ok {
				continue
			}
			switch {
			case strings.Contains(rest, "function"):
				res[n] = model.LauncherFunction
			case strings.HasPrefix(rest, "aliased") || strings.HasPrefix(rest, "an alias") || strings.Contains(rest, "alias for"):
				res[n] = model.LauncherAlias
			}
		}
	}
	return res
}

// detectMCP is step 1.6: the server names (value hashes only) in each folder's state file.
func (x *detector) detectMCP() {
	for i := range x.st.Folders {
		f := &x.st.Folders[i]
		if f.StateFile == "" {
			continue
		}
		var doc struct {
			MCP map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := x.readJSONFile(f.StateFile, &doc); err != nil {
			if !os.IsNotExist(err) {
				x.warn = append(x.warn, x.tilde(f.StateFile)+": mcpServers not read: "+err.Error())
			}
			continue
		}
		f.MCPNames = map[string]string{}
		for name, raw := range doc.MCP {
			var buf bytes.Buffer
			if json.Compact(&buf, raw) != nil {
				buf.Reset()
				buf.Write(raw)
			}
			sum := sha256.Sum256(buf.Bytes())
			f.MCPNames[name] = hex.EncodeToString(sum[:])
		}
	}
}
