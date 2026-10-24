// Package fakes holds test doubles of the model interfaces (Sharer, PluginInstaller,
// Service, Connector) that perform real file operations on real (temporary) trees, so that
// apply-then-undo property tests are meaningful without the packages that implement the
// interfaces in production (internal/share, internal/jsonedit, pluginmkt, service).
//
// Only for tests. Each double can inject a failure at the Nth mutating call, before or after
// its effect.
package fakes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// ErrInjected is the error a double returns at its injected failure.
var ErrInjected = errors.New("fakes: injected failure")

// Inject makes the Nth (1-based) mutating call fail. N == 0 never fails. With After the
// effect happens first and the call then reports the error (a half-finished action).
type Inject struct {
	N     int
	After bool

	mu    sync.Mutex
	calls int
}

// Calls returns how many mutating calls were made.
func (i *Inject) Calls() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
}

// step counts a call; the returned closure runs the effect and applies the injection.
func (i *Inject) run(effect func() error) error {
	i.mu.Lock()
	i.calls++
	hit := i.N != 0 && i.calls == i.N
	i.mu.Unlock()
	if hit && !i.After {
		return ErrInjected
	}
	if err := effect(); err != nil {
		return err
	}
	if hit {
		return ErrInjected
	}
	return nil
}

// Sharer is a model.Sharer on the real file system.
type Sharer struct {
	Inject
}

var _ model.Sharer = (*Sharer)(nil)

// Link makes path a symlink to target (temp name, then rename); LinkCopy copies a file.
func (s *Sharer) Link(path, target, kind string) error {
	return s.run(func() error { return link(path, target, kind) })
}

func link(path, target, kind string) error {
	if kind == model.LinkCopy {
		b, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		return atomicfile.Write(path, b, 0o644)
	}
	tmp := path + ".wy-tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReplaceWithLink moves the original to saveTo (or discards it) and links.
func (s *Sharer) ReplaceWithLink(path, target, kind, saveTo string) error {
	return s.run(func() error {
		if saveTo != "" {
			if err := backup.MoveTree(path, saveTo); err != nil {
				return err
			}
		} else if err := os.RemoveAll(path); err != nil {
			return err
		}
		return link(path, target, kind)
	})
}

// Move renames from to to.
func (s *Sharer) Move(from, to string) error {
	return s.run(func() error { return backup.MoveTree(from, to) })
}

// MergeLines appends the lines of from missing from path (union, exact bytes).
func (s *Sharer) MergeLines(path, from string) error {
	return s.run(func() error {
		a, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		var out []string
		for _, l := range strings.Split(strings.TrimRight(string(a)+"\n"+string(b), "\n"), "\n") {
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
			out = append(out, l)
		}
		mode := os.FileMode(0o644)
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
		return atomicfile.Write(path, []byte(strings.Join(out, "\n")+"\n"), mode)
	})
}

// MergeDir moves entries of src missing from dst into dst, moves identical ones into
// saveDir and leaves differing ones.
func (s *Sharer) MergeDir(dst, src, saveDir string) (model.MergeResult, error) {
	var res model.MergeResult
	err := s.run(func() error {
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
			if _, err := os.Lstat(to); errors.Is(err, os.ErrNotExist) {
				if err := backup.MoveTree(from, to); err != nil {
					return err
				}
				res.Moved = append(res.Moved, e.Name())
				continue
			}
			if sameTree(from, to) {
				if err := os.MkdirAll(saveDir, 0o700); err != nil {
					return err
				}
				if err := backup.MoveTree(from, filepath.Join(saveDir, e.Name())); err != nil {
					return err
				}
				res.Dropped = append(res.Dropped, e.Name())
				continue
			}
			res.Conflicts = append(res.Conflicts, e.Name())
		}
		return nil
	})
	return res, err
}

func sameTree(a, b string) bool {
	ha, e1 := backup.TreeHash(a)
	hb, e2 := backup.TreeHash(b)
	return e1 == nil && e2 == nil && ha == hb
}

// JSONSet sets the value at pointer (creating parents), rewriting the resolved file.
func (s *Sharer) JSONSet(file, pointer string, value json.RawMessage) (model.JSONResult, error) {
	var res model.JSONResult
	err := s.run(func() error {
		var err error
		res, err = editJSON(file, pointer, value, false)
		return err
	})
	return res, err
}

// JSONDelete removes the key at pointer.
func (s *Sharer) JSONDelete(file, pointer string) (model.JSONResult, error) {
	var res model.JSONResult
	err := s.run(func() error {
		var err error
		res, err = editJSON(file, pointer, nil, true)
		return err
	})
	return res, err
}

// MCPSync copies the primary file's mcpServers into every other file whose differ.
func (s *Sharer) MCPSync(_ context.Context, req model.MCPSyncRequest) (model.MCPSyncResult, error) {
	var res model.MCPSyncResult
	err := s.run(func() error {
		var prim string
		for _, f := range req.Files {
			if f.Primary {
				prim = f.StateFile
			}
		}
		if prim == "" && len(req.Files) > 0 {
			prim = req.Files[0].StateFile
		}
		want, ok, err := backup.ReadPointer(prim, "/mcpServers")
		if err != nil || !ok {
			return err
		}
		for _, f := range req.Files {
			if f.StateFile == prim {
				continue
			}
			cur, present, err := backup.ReadPointer(f.StateFile, "/mcpServers")
			if err != nil {
				return err
			}
			if present && bytes.Equal(cur, want) {
				continue
			}
			if _, err := editJSON(f.StateFile, "/mcpServers", want, false); err != nil {
				return err
			}
			res.Written = append(res.Written, f.StateFile)
		}
		return nil
	})
	return res, err
}

// Snapshot is backup.Capture.
func (s *Sharer) Snapshot(path string) (model.PathState, error) { return backup.Capture(path) }

func editJSON(file, pointer string, value json.RawMessage, del bool) (model.JSONResult, error) {
	var res model.JSONResult
	real, err := filepath.EvalSymlinks(file)
	if err != nil {
		return res, err
	}
	res.Target = real
	fi, err := os.Stat(real)
	if err != nil {
		return res, err
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return res, err
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return res, err
	}
	toks := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i := range toks {
		toks[i] = strings.ReplaceAll(strings.ReplaceAll(toks[i], "~1", "/"), "~0", "~")
	}
	m := doc
	for _, t := range toks[:len(toks)-1] {
		next, ok := m[t].(map[string]any)
		if !ok {
			if del {
				return res, nil
			}
			next = map[string]any{}
			m[t] = next
		}
		m = next
	}
	last := toks[len(toks)-1]
	if old, ok := m[last]; ok {
		res.BeforePresent = true
		res.Before, _ = json.Marshal(old)
	}
	if del {
		delete(m, last)
	} else {
		var v any
		d := json.NewDecoder(bytes.NewReader(value))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			return res, err
		}
		m[last] = v
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return res, err
	}
	res.Changed = true
	return res, atomicfile.Write(real, append(out, '\n'), fi.Mode().Perm())
}

// Plugins is a model.PluginInstaller that writes a tiny marketplace tree.
type Plugins struct {
	Inject
}

var _ model.PluginInstaller = (*Plugins)(nil)

// WriteMarketplace writes dir/.claude-plugin/marketplace.json and plugins/watchyard/x.
func (p *Plugins) WriteMarketplace(dir, wyPath, version string) error {
	return p.run(func() error {
		if _, err := os.Lstat(dir); err == nil {
			return fmt.Errorf("fakes: %s exists", dir)
		}
		tmp := dir + ".tmp"
		_ = os.RemoveAll(tmp)
		for rel, body := range map[string]string{
			".claude-plugin/marketplace.json": `{"name":"wy-local","version":"` + version + `"}`,
			"plugins/watchyard/hooks.json":    `{"wy":"` + wyPath + `"}`,
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(tmp, rel)), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(tmp, rel), []byte(body), 0o644); err != nil {
				return err
			}
		}
		return os.Rename(tmp, dir)
	})
}

// Install is a no-op.
func (p *Plugins) Install(context.Context, claudecli.Folder, string) error { return nil }

// Enable is a no-op.
func (p *Plugins) Enable(context.Context, claudecli.Folder) error { return nil }

// Disable is a no-op.
func (p *Plugins) Disable(context.Context, claudecli.Folder, string) error { return nil }

// Uninstall is a no-op.
func (p *Plugins) Uninstall(context.Context, claudecli.Folder) error { return nil }

// Service is a model.Service that writes the unit file and remembers what is installed.
type Service struct {
	Inject
	mu        sync.Mutex
	installed map[string]bool
	// RemoveUnit makes Uninstall delete the unit file the way a real service package does.
	RemoveUnit bool
	units      map[string]string
	specs      map[string]model.ServiceSpec
}

// Spec is the spec the named service was last installed with.
func (s *Service) Spec(name string) model.ServiceSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.specs[name]
}

var _ model.Service = (*Service)(nil)

// Install writes spec.Unit and marks the service installed.
func (s *Service) Install(_ context.Context, spec model.ServiceSpec) error {
	return s.run(func() error {
		if err := os.MkdirAll(filepath.Dir(spec.Unit), 0o755); err != nil {
			return err
		}
		if err := atomicfile.Write(spec.Unit, []byte("[Service]\nname="+spec.Name+"\n"), 0o644); err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.installed == nil {
			s.installed, s.units, s.specs = map[string]bool{}, map[string]string{}, map[string]model.ServiceSpec{}
		}
		s.installed[spec.Name], s.units[spec.Name], s.specs[spec.Name] = true, spec.Unit, spec
		return nil
	})
}

// Uninstall marks the service not installed (and removes the unit with RemoveUnit).
func (s *Service) Uninstall(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.installed, name)
	if s.RemoveUnit {
		_ = os.Remove(s.units[name])
	}
	return nil
}

// Status reports the remembered state.
func (s *Service) Status(_ context.Context, name string) (model.ServiceStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return model.ServiceStatus{Installed: s.installed[name], Active: s.installed[name]}, nil
}

// Installed lists installed service names.
func (s *Service) Installed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.installed {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Connector is a model.Connector that writes TokenPath and ConfigPath like a connect does.
type Connector struct {
	Inject
	TokenPath, ConfigPath string
	// Unhealthy makes Connect report Healthy=false.
	Unhealthy bool
	// Got is the last request (the token is kept only so tests can assert it never leaks).
	Got model.ConnectRequest
}

var _ model.Connector = (*Connector)(nil)

// Connect writes the token file and the hub.url line of the config.
func (c *Connector) Connect(_ context.Context, req model.ConnectRequest) (model.ConnectResult, error) {
	c.Got = req
	err := c.run(func() error {
		if err := os.WriteFile(c.TokenPath, []byte(req.Token), 0o600); err != nil {
			return err
		}
		return os.WriteFile(c.ConfigPath, []byte("[hub]\nurl = \""+req.URL+"\"\n"), 0o600)
	})
	if err != nil {
		return model.ConnectResult{}, err
	}
	return model.ConnectResult{Healthy: !c.Unhealthy, Timer: true, Message: "connected"}, nil
}

// MintMachineToken returns a fixed token.
func (c *Connector) MintMachineToken(context.Context, string) (string, error) {
	return "machine-token", nil
}

// WaitHealthy returns at once.
func (c *Connector) WaitHealthy(context.Context, string, int) error { return nil }
