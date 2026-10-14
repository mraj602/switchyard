// Package pluginmkt writes the local directory marketplace `sy` owns (`sy-local`, setup.md
// 5.2) from the embedded plugin and installs, enables, disables and removes the switchyard
// plugin through a claudecli.Runner. It implements model.PluginInstaller.
//
// The marketplace folder is written to a temporary sibling and renamed into place, so Claude
// Code never sees a half-written tree. Every hook command in the copy calls `sy` by absolute
// path (hooks then work when Claude Code is started without ~/.local/bin on PATH).
//
// Phase 3, WP-D. Depends on: plugin (embed), claudecli, model.
package pluginmkt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/mraj602/switchyard/internal/claudecli"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/plugin"
)

// Names fixed by setup.md 5.2 (and, for the pane, docs/spec/terminal.md T5).
const (
	MarketplaceName = "sy-local"
	PluginName      = plugin.Name // "switchyard"
	PluginID        = PluginName + "@" + MarketplaceName
	PaneName        = plugin.PaneName // "switchyard-pane", phase 7
	PaneID          = PaneName + "@" + MarketplaceName
)

// Installer implements model.PluginInstaller on a claudecli.Runner. Source is the plugin tree
// to write; nil means the embedded plugin.FS(). PaneSource is the pane mod's tree; nil means
// the embedded plugin.PaneFS().
type Installer struct {
	Runner     claudecli.Runner
	Source     fs.FS
	PaneSource fs.FS
}

var _ model.PluginInstaller = (*Installer)(nil)

// New returns an Installer for the embedded plugin.
func New(r claudecli.Runner) *Installer { return &Installer{Runner: r} }

func (i *Installer) source() fs.FS {
	if i.Source != nil {
		return i.Source
	}
	return plugin.FS()
}

func (i *Installer) paneSource() fs.FS {
	if i.PaneSource != nil {
		return i.PaneSource
	}
	return plugin.PaneFS()
}

// ---- hooks.json rewrite ------------------------------------------------------------------------

// hookPrefix is how the embedded hooks call sy: it must be on PATH.
const hookPrefix = "command -v sy >/dev/null 2>&1 && sy "

// shellQuote single-quotes s for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// HookCommand rewrites one embedded hook command to call sy by absolute path:
// `command -v sy >/dev/null 2>&1 && sy hook Stop || true` becomes
// `[ -x '/home/u/.local/bin/sy' ] && '/home/u/.local/bin/sy' hook Stop || true`.
// On Windows the path is written with forward slashes (hooks run under a POSIX shell).
func HookCommand(cmd, syPath string) (string, error) {
	rest, ok := strings.CutPrefix(cmd, hookPrefix)
	if !ok {
		return "", fmt.Errorf("pluginmkt: unrecognised hook command %q", cmd)
	}
	p := syPath
	if runtime.GOOS == "windows" {
		p = filepath.ToSlash(p)
	}
	q := shellQuote(p)
	return "[ -x " + q + " ] && " + q + " " + rest, nil
}

// RewriteHooks returns hooksJSON with every hook command passed through HookCommand. Only
// the command strings change: events, matchers, order and formatting stay byte for byte.
func RewriteHooks(hooksJSON []byte, syPath string) ([]byte, error) {
	var doc any
	if err := json.Unmarshal(hooksJSON, &doc); err != nil {
		return nil, fmt.Errorf("pluginmkt: hooks.json: %w", err)
	}
	var cmds []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if s, ok := e.(string); ok && k == "command" {
					cmds = append(cmds, s)
				} else {
					walk(e)
				}
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
	out := hooksJSON
	seen := map[string]bool{}
	for _, c := range cmds {
		if seen[c] {
			continue
		}
		seen[c] = true
		n, err := HookCommand(c, syPath)
		if err != nil {
			return nil, err
		}
		oldLit, newLit := jsonString(c), jsonString(n)
		if !bytes.Contains(out, oldLit) {
			return nil, fmt.Errorf("pluginmkt: hook command %q is written with unusual escapes", c)
		}
		out = bytes.ReplaceAll(out, oldLit, newLit)
	}
	return out, nil
}

func jsonString(s string) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return bytes.TrimSpace(b.Bytes())
}

var versionField = regexp.MustCompile(`("version"\s*:\s*)"[^"]*"`)

// setVersion replaces the first "version" string of a JSON document (plugin.json).
func setVersion(doc []byte, version string) []byte {
	done := false
	return versionField.ReplaceAllFunc(doc, func(m []byte) []byte {
		if done {
			return m
		}
		done = true
		sub := versionField.FindSubmatch(m)
		return append(append([]byte{}, sub[1]...), jsonString(version)...)
	})
}

// MarketplaceJSON renders .claude-plugin/marketplace.json. It lists both plugins: switchyard
// and the opt-in pane mod (phase 7), which is installed only by `sy term setup --pane`.
func MarketplaceJSON(version, description, paneDescription string) []byte {
	doc := fmt.Sprintf(`{
  "name": %s,
  "owner": {"name": "switchyard"},
  "metadata": {"description": %s, "version": %s},
  "plugins": [
    {
      "name": %s,
      "source": "./plugins/%s",
      "description": %s,
      "version": %s
    },
    {
      "name": %s,
      "source": "./plugins/%s",
      "description": %s,
      "version": %s
    }
  ]
}
`, jsonString(MarketplaceName), jsonString("Local marketplace written by sy setup"), jsonString(version),
		jsonString(PluginName), PluginName, jsonString(description), jsonString(version),
		jsonString(PaneName), PaneName, jsonString(paneDescription), jsonString(version))
	return []byte(doc)
}

// manifestDescription is the description of a plugin tree's plugin.json, or def.
func manifestDescription(src fs.FS, def string) string {
	if b, err := fs.ReadFile(src, ".claude-plugin/plugin.json"); err == nil {
		var pj struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(b, &pj) == nil && pj.Description != "" {
			return pj.Description
		}
	}
	return def
}

// ---- WriteMarketplace --------------------------------------------------------------------------

// ErrPathSeparator is returned for a marketplace path that contains the PATH list separator:
// Claude Code refuses such plugin paths.
var ErrPathSeparator = errors.New("pluginmkt: path contains the PATH separator")

// WriteMarketplace implements model.PluginInstaller.
func (i *Installer) WriteMarketplace(dir, syPath, version string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("pluginmkt: marketplace folder %q is not absolute", dir)
	}
	if !filepath.IsAbs(syPath) {
		return fmt.Errorf("pluginmkt: sy path %q is not absolute", syPath)
	}
	sep := string(os.PathListSeparator)
	if runtime.GOOS == "windows" {
		// "C:\..." contains ':' legitimately; the separator that matters there is ';'.
		sep = ";"
	}
	if strings.Contains(dir, sep) {
		return fmt.Errorf("%w (%q in %s); choose a state folder without it", ErrPathSeparator, sep, dir)
	}
	if version == "" {
		version = "0.0.0"
	}
	src := i.source()
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return err
	}
	cleanup := func(err error) error { _ = os.RemoveAll(tmp); return err }
	if err := os.Chmod(tmp, 0o755); err != nil && runtime.GOOS != "windows" {
		return cleanup(err)
	}

	pane := i.paneSource()
	desc := manifestDescription(src, "Switchyard for Claude Code")
	paneDesc := manifestDescription(pane, "Switchyard session board pane")
	mj := filepath.Join(tmp, ".claude-plugin", "marketplace.json")
	if err := writeFile(mj, MarketplaceJSON(version, desc, paneDesc), 0o644); err != nil {
		return cleanup(err)
	}
	err = copyTree(src, filepath.Join(tmp, "plugins", PluginName), func(p string, data []byte) ([]byte, error) {
		switch p {
		case "hooks/hooks.json":
			return RewriteHooks(data, syPath)
		case ".claude-plugin/plugin.json":
			return setVersion(data, version), nil
		}
		return data, nil
	})
	if err != nil {
		return cleanup(err)
	}
	err = copyTree(pane, filepath.Join(tmp, "plugins", PaneName), func(p string, data []byte) ([]byte, error) {
		if p == ".claude-plugin/plugin.json" {
			return setVersion(data, version), nil
		}
		return paneFile(p, data, syPath) // pane.go: the __SY_PATH__ rewrite
	})
	if err != nil {
		return cleanup(err)
	}
	return swapDir(tmp, dir)
}

// copyTree writes every file of src under dst, passing each through edit (path in slash
// form relative to src). Executable files stay executable.
func copyTree(src fs.FS, dst string, edit func(p string, data []byte) ([]byte, error)) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		if data, err = edit(p, data); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if fi, err := d.Info(); err == nil && fi.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return writeFile(out, data, mode)
	})
}

func writeFile(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, mode)
}

// swapDir renames tmp over dir. An existing dir is first renamed aside (a folder cannot be
// renamed over a non-empty folder) and removed afterwards; if the final rename fails the old
// folder is put back.
func swapDir(tmp, dir string) error {
	var old string
	if _, err := os.Lstat(dir); err == nil {
		var b [4]byte
		_, _ = rand.Read(b[:])
		old = dir + ".old-" + hex.EncodeToString(b[:])
		if err := os.Rename(dir, old); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		if old != "" {
			_ = os.Rename(old, dir)
		}
		_ = os.RemoveAll(tmp)
		return err
	}
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return nil
}

// PluginDir is the plugin folder inside a marketplace folder.
func PluginDir(marketplaceDir string) string {
	return filepath.Join(marketplaceDir, "plugins", PluginName)
}

// PaneDir is the pane mod's folder inside a marketplace folder.
func PaneDir(marketplaceDir string) string {
	return filepath.Join(marketplaceDir, "plugins", PaneName)
}

// ---- runner calls ------------------------------------------------------------------------------

func check(res claudecli.Result, err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if !res.OK() {
		return fmt.Errorf("%s: outcome %q: %s", what, res.Outcome, res.Message)
	}
	return nil
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// Install implements model.PluginInstaller: `marketplace add <dir>` (or `marketplace update`
// when sy-local is already known for this root from the same folder), then `install
// switchyard@sy-local`. It always runs install, even when the plugin is listed, because
// installing from the folder that owns the root makes installPath canonical (5.2).
func (i *Installer) Install(ctx context.Context, folder claudecli.Folder, marketplaceDir string) error {
	known, err := i.Runner.PluginMarketplaceList(ctx, folder)
	if err != nil {
		return fmt.Errorf("claude plugin marketplace list: %w", err)
	}
	have := false
	for _, m := range known {
		if m.Name != MarketplaceName {
			continue
		}
		if m.Source == "" || samePath(m.Source, marketplaceDir) {
			have = true
			continue
		}
		// known from another folder (an old state folder): replace it
		res, err := i.Runner.PluginMarketplaceRemove(ctx, folder, MarketplaceName)
		if e := check(res, err, "claude plugin marketplace remove"); e != nil {
			return e
		}
	}
	if have {
		res, err := i.Runner.PluginMarketplaceUpdate(ctx, folder, MarketplaceName)
		if e := check(res, err, "claude plugin marketplace update"); e != nil {
			return e
		}
	} else {
		res, err := i.Runner.PluginMarketplaceAdd(ctx, folder, marketplaceDir)
		if e := check(res, err, "claude plugin marketplace add"); e != nil {
			return e
		}
	}
	res, err := i.Runner.PluginInstall(ctx, folder, PluginID)
	return check(res, err, "claude plugin install "+PluginID)
}

// Update is `sy update`'s plugin step for one plugin root, run as the folder that owns it
// (setup.md 5.2, machines.md 7.3 step 7): `claude plugin marketplace update sy-local`, then
// `claude plugin update switchyard@sy-local`. WriteMarketplace has rewritten the folder for
// the new version first.
func (i *Installer) Update(ctx context.Context, folder claudecli.Folder) error {
	res, err := i.Runner.PluginMarketplaceUpdate(ctx, folder, MarketplaceName)
	if e := check(res, err, "claude plugin marketplace update "+MarketplaceName); e != nil {
		return e
	}
	res, err = i.Runner.PluginUpdate(ctx, folder, PluginID)
	return check(res, err, "claude plugin update "+PluginID)
}

// Enable implements model.PluginInstaller.
func (i *Installer) Enable(ctx context.Context, folder claudecli.Folder) error {
	res, err := i.Runner.PluginEnable(ctx, folder, PluginID)
	return check(res, err, "claude plugin enable "+PluginID)
}

// Disable implements model.PluginInstaller.
func (i *Installer) Disable(ctx context.Context, folder claudecli.Folder, id string) error {
	if id == "" {
		return errors.New("pluginmkt: disable needs a plugin id")
	}
	res, err := i.Runner.PluginDisable(ctx, folder, id)
	return check(res, err, "claude plugin disable "+id)
}

// Uninstall implements model.PluginInstaller: `uninstall switchyard@sy-local`, then
// `marketplace remove sy-local`, each only if present (a repeated uninstall is not an error).
// Claude Code removes the record for every folder sharing folder's plugin root, so callers
// run it once per root.
func (i *Installer) Uninstall(ctx context.Context, folder claudecli.Folder) error {
	plugins, err := i.Runner.PluginList(ctx, folder)
	if err != nil {
		return fmt.Errorf("claude plugin list: %w", err)
	}
	for _, p := range plugins {
		if p.ID == PluginID {
			res, err := i.Runner.PluginUninstall(ctx, folder, PluginID)
			if e := check(res, err, "claude plugin uninstall "+PluginID); e != nil {
				return e
			}
			break
		}
	}
	known, err := i.Runner.PluginMarketplaceList(ctx, folder)
	if err != nil {
		return fmt.Errorf("claude plugin marketplace list: %w", err)
	}
	for _, m := range known {
		if m.Name == MarketplaceName {
			res, err := i.Runner.PluginMarketplaceRemove(ctx, folder, MarketplaceName)
			return check(res, err, "claude plugin marketplace remove "+MarketplaceName)
		}
	}
	return nil
}

// HooksEvents lists the events and matchers of a hooks.json as "Event" or "Event[matcher]"
// strings, sorted (for tests and `sy doctor`).
func HooksEvents(hooksJSON []byte) ([]string, error) {
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hooksJSON, &doc); err != nil {
		return nil, err
	}
	var out []string
	for ev, groups := range doc.Hooks {
		for _, g := range groups {
			if g.Matcher != "" {
				out = append(out, ev+"["+g.Matcher+"]")
			} else {
				out = append(out, ev)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
