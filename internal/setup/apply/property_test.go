package apply_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/backup/fakes"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/testtree"
)

// fakeExec is an Exec that creates a plugin-cache tree and edits two registries, like
// `claude plugin install` does.
type fakeExec struct {
	root string
	fail bool
}

func (f *fakeExec) Run(_ context.Context, argv []string, _ map[string]*string) ([]byte, error) {
	if f.fail {
		return []byte(`{"outcome":"error","error":"boom"}`), nil
	}
	dir := filepath.Join(f.root, "cache", "wy-local", "watchyard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte("{}"), 0o644); err != nil {
		return nil, err
	}
	// Like the real CLI, add wy's entry to the registry and keep everyone else's.
	reg := filepath.Join(f.root, "installed_plugins.json")
	doc := map[string]any{}
	if b, err := os.ReadFile(reg); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
	}
	plugins, _ := doc["plugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}
	plugins["watchyard@wy-local"] = []any{map[string]any{"v": 1}}
	doc["plugins"] = plugins
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(reg, b, 0o644); err != nil {
		return nil, err
	}
	return []byte(`{"outcome":"ok"}`), nil
}

type world struct {
	root   string // everything under it is snapshotted except state/
	state  string
	plan   model.Plan
	sharer *fakes.Sharer
	plug   *fakes.Plugins
	exec   *fakeExec
}

func ignoreState(rel string) bool { return rel == "state" || strings.HasPrefix(rel, "state/") }

func snap(t *testing.T, root string) testtree.Snap {
	return testtree.Take(t, root, testtree.Opts{Ignore: ignoreState})
}

func randJSON(rng *rand.Rand) string {
	m := map[string]any{
		"theme":   fmt.Sprintf("t%d", rng.Intn(9)),
		"env":     map[string]any{"A": "1"},
		"padding": rng.Intn(5),
	}
	if rng.Intn(2) == 0 {
		m["statusLine"] = map[string]any{"type": "command", "command": "bash x.sh", "padding": 0}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b) + "\n"
}

var modes = []os.FileMode{0o600, 0o644, 0o755, 0o640}

func randTime(rng *rand.Rand) time.Time {
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rng.Intn(100000)) * time.Minute)
}

// build makes a random home and a random plan over it.
func build(t *testing.T, seed int64) *world {
	rng := rand.New(rand.NewSource(seed))
	b := testtree.New(t, "")
	w := &world{root: b.Root, state: filepath.Join(b.Root, "state"), sharer: &fakes.Sharer{}, plug: &fakes.Plugins{}}
	w.exec = &fakeExec{root: b.Abs("home/plugroot")}
	f := func(rel, content string) {
		b.File(rel, content, modes[rng.Intn(len(modes))]).Mtime(rel, randTime(rng))
	}
	f("home/primary/settings.json", randJSON(rng))
	f("home/primary/CLAUDE.md", "primary claude md\n")
	f("home/primary/history.jsonl", `{"timestamp":1,"a":1}`+"\n"+`{"timestamp":3,"a":3}`+"\n")
	f("home/primary/skills/s1/SKILL.md", "skill one")
	f("home/primary/projects/p1/a.jsonl", "p1 primary")
	f("home/primary/projects/p2/a.jsonl", "p2 same")
	b.Dir("home/primary/skills", 0o755)
	// folder a: links (one relative), files, dirs
	b.Symlink("home/a/settings.json", "../primary/settings.json")
	b.Symlink("home/a/plugins", "../primary/plugins")
	f("home/a/CLAUDE.md", "a claude md\n")
	f("home/a/history.jsonl", `{"timestamp":2,"a":2}`+"\n"+`{"timestamp":3,"a":3}`+"\n")
	f("home/a/skills/x/SKILL.md", "a skill")
	f("home/a/projects/p2/a.jsonl", "p2 same")
	f("home/a/projects/p3/a.jsonl", "p3 only in a")
	f("home/a/projects/p1/a.jsonl", "p1 different")
	f("home/a/projects/p2/b.jsonl", "p2 only in a")
	f("home/primary/rules/r.md", "a rule")
	f("home/primary/agent.md", "an agent")
	// folder b
	f("home/b/settings.json", randJSON(rng))
	f("home/b/CLAUDE.md", "b claude md\n")
	f("home/b/misc.txt", "misc")
	f("home/b/skills/y/SKILL.md", "b skill")
	b.Symlink("home/b/lnk", "$ROOT/home/primary/CLAUDE.md")
	if rng.Intn(2) == 0 { // an existing marketplace tree to be replaced
		f("home/mkt/.claude-plugin/marketplace.json", `{"old":true}`)
	}
	if rng.Intn(2) == 0 {
		f("home/plugroot/installed_plugins.json", `{"plugins":{"other@x":[{"v":1}]},"version":2}`)
		f("home/plugroot/known_marketplaces.json", `{"x":{}}`)
	}
	abs := b.Abs

	used := map[string]bool{}
	take := func(k string) bool {
		if used[k] {
			return false
		}
		used[k] = true
		return true
	}
	n := 0
	var gens = []func() *model.Action{
		func() *model.Action {
			n++
			return &model.Action{Op: model.OpMkdir, Path: abs(fmt.Sprintf("home/new%d/deep/d", n)), Mode: "0700", Step: model.StepFolders}
		},
		func() *model.Action {
			n++
			return &model.Action{Op: model.OpLink, Path: abs(fmt.Sprintf("home/a/new%d", n)), Target: abs("home/primary/CLAUDE.md"), Kind: "symlink", Step: model.StepSharing}
		},
		func() *model.Action { // re-point an existing relative link
			if !take("a/plugins") {
				return nil
			}
			return &model.Action{Op: model.OpLink, Path: abs("home/a/plugins"), Target: abs("home/primary/CLAUDE.md"), Kind: "symlink", Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("a/CLAUDE.md") {
				return nil
			}
			return &model.Action{Op: model.OpReplaceWithLink, Path: abs("home/a/CLAUDE.md"), Target: abs("home/primary/CLAUDE.md"), Kind: "symlink", Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("b/skills") {
				return nil
			}
			return &model.Action{Op: model.OpReplaceWithLink, Path: abs("home/b/skills"), Target: abs("home/primary/skills"), Kind: "symlink", Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("b/lnk") {
				return nil
			}
			return &model.Action{Op: model.OpReplaceWithLink, Path: abs("home/b/lnk"), Target: abs("home/primary/skills"), Kind: "symlink", Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("b/misc") {
				return nil
			}
			return &model.Action{Op: model.OpMove, From: abs("home/b/misc.txt"), To: abs("home/moved/deeper/misc.txt"), Step: model.StepSharing}
		},
		// the moves of a projects merge (5.3): a project the primary lacks, and a session file
		// of a project both have (depth 2)
		func() *model.Action {
			if !take("a/projects/p3") {
				return nil
			}
			return &model.Action{Op: model.OpMove, From: abs("home/a/projects/p3"), To: abs("home/primary/projects/p3"), Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("a/projects/p2/b") {
				return nil
			}
			return &model.Action{Op: model.OpMove, From: abs("home/a/projects/p2/b.jsonl"), To: abs("home/primary/projects/p2/b.jsonl"), Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("primary/rules") {
				return nil
			}
			return &model.Action{Op: model.OpMoveToBackup, Path: abs("home/primary/rules"), Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("primary/agent.md") {
				return nil
			}
			return &model.Action{Op: model.OpMoveToBackup, Path: abs("home/primary/agent.md"), Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("a/skills") {
				return nil
			}
			return &model.Action{Op: model.OpMove, From: abs("home/a/skills"), To: abs("home/skills-moved"), Step: model.StepSharing}
		},
		func() *model.Action {
			if !take("hist") {
				return nil
			}
			return &model.Action{Op: model.OpMergeLines, Path: abs("home/primary/history.jsonl"), From: abs("home/a/history.jsonl"), Step: model.StepSharing}
		},
		func() *model.Action {
			return &model.Action{Op: model.OpJSONSet, File: abs("home/primary/settings.json"), Via: []string{abs("home/a/settings.json")},
				Pointer: "/statusLine/command", Value: json.RawMessage(`"wy statusline"`), Step: model.StepStatusLine}
		},
		func() *model.Action {
			return &model.Action{Op: model.OpJSONSet, File: abs("home/b/settings.json"), Pointer: "/env/CLAUDE_CODE_PLUGIN_CACHE_DIR",
				Value: json.RawMessage(`"/x/plugins"`), Step: model.StepSharing}
		},
		func() *model.Action {
			return &model.Action{Op: model.OpJSONDelete, File: abs("home/b/settings.json"), Pointer: "/padding", Step: model.StepSharing}
		},
		func() *model.Action {
			n++
			return &model.Action{Op: model.OpWriteFile, Path: abs(fmt.Sprintf("home/bin/claude-%d", n)), Mode: "0755", ContentSHA256: "", Note: "launcher", Step: model.StepLaunchers}
		},
		func() *model.Action {
			if !take("b/CLAUDE.md") {
				return nil
			}
			return &model.Action{Op: model.OpWriteFile, Path: abs("home/b/CLAUDE.md"), Mode: "0600", Step: model.StepLaunchers}
		},
		func() *model.Action {
			if !take("mkt") {
				return nil
			}
			return &model.Action{Op: model.OpWriteTree, Path: abs("home/mkt"), Step: model.StepPlugin}
		},
		func() *model.Action {
			if !take("run") {
				return nil
			}
			root := abs("home/plugroot")
			return &model.Action{Op: model.OpRun, Argv: []string{"claude", "plugin", "install"}, Step: model.StepPlugin,
				Touches: []string{filepath.Join(root, "installed_plugins.json"), filepath.Join(root, "known_marketplaces.json")},
				Watch:   []string{root}}
		},
	}
	k := 2 + rng.Intn(7)
	var actions []model.Action
	for len(actions) < k {
		a := gens[rng.Intn(len(gens))]()
		if a == nil {
			continue
		}
		actions = append(actions, *a)
	}
	// plan order: by step
	var ordered []model.Action
	for _, st := range model.StepOrder {
		for _, a := range actions {
			if a.Step == st {
				ordered = append(ordered, a)
			}
		}
	}
	for i := range ordered {
		ordered[i].ID = fmt.Sprintf("a%d", i+1)
	}
	w.plan = model.Plan{PlanID: "p_test", Actions: ordered}
	return w
}

func contents(a model.Action) ([]byte, error) { return []byte("content of " + a.Path + "\n"), nil }

func (w *world) opts() apply.Options {
	return apply.Options{StateDir: w.state, Command: "test", SyVersion: "0.3.0", Machine: "m", SyPath: "/bin/wy"}
}

func (w *world) deps() apply.Deps {
	return apply.Deps{Sharer: w.sharer, Plugins: w.plug, Exec: w.exec, Contents: contents}
}

func undoDeps(w *world) backup.Deps { return backup.Deps{Sharer: &fakes.Sharer{}} }

const seeds = 60

func TestApplyThenUndoRestoresTree(t *testing.T) {
	for seed := int64(1); seed <= seeds; seed++ {
		w := build(t, seed)
		start := snap(t, w.root)
		res, err := apply.Run(context.Background(), w.plan, w.opts(), w.deps())
		if err != nil {
			t.Fatalf("seed %d: apply: %v", seed, err)
		}
		if res.Status != model.StatusApplied {
			t.Fatalf("seed %d: status %q", seed, res.Status)
		}
		if len(testtree.Diff(start, snap(t, w.root))) == 0 {
			t.Fatalf("seed %d: apply changed nothing", seed)
		}
		ur, err := backup.Undo(context.Background(), w.state, "", undoDeps(w), backup.Options{})
		if err != nil || ur.ExitCode != 0 {
			t.Fatalf("seed %d: undo: %v %+v", seed, err, ur)
		}
		if d := testtree.Diff(start, snap(t, w.root)); len(d) > 0 {
			t.Fatalf("seed %d: tree differs after undo:\n  %s", seed, strings.Join(d, "\n  "))
		}
	}
}

func TestFailureAtEveryActionRollsBack(t *testing.T) {
	for seed := int64(1); seed <= seeds; seed++ {
		probe := build(t, seed)
		n := len(probe.plan.Actions)
		for k := 1; k <= n; k++ {
			for _, mode := range []string{"hook-intent", "hook-acted", "sharer-before", "sharer-after"} {
				w := build(t, seed)
				start := snap(t, w.root)
				o := w.opts()
				switch mode {
				case "hook-intent", "hook-acted":
					ev := strings.TrimPrefix(mode, "hook-")
					o.TestHook = func(e string, seq int) error {
						if e == ev && seq == k {
							return fakes.ErrInjected
						}
						return nil
					}
				default:
					w.sharer.N, w.sharer.After = k, mode == "sharer-after"
					if k > 1 && w.sharer.N > n { // no such call
						continue
					}
				}
				res, err := apply.Run(context.Background(), w.plan, o, w.deps())
				if err == nil {
					if strings.HasPrefix(mode, "sharer") { // fewer sharer calls than actions
						continue
					}
					t.Fatalf("seed %d k %d %s: expected failure", seed, k, mode)
				}
				var ae *apply.Error
				if !errors.As(err, &ae) || ae.Code != apply.ExitFailed || res.Status != model.StatusRolledBack {
					t.Fatalf("seed %d k %d %s: err %v res %+v", seed, k, mode, err, res)
				}
				if d := testtree.Diff(start, snap(t, w.root)); len(d) > 0 {
					t.Fatalf("seed %d k %d %s: tree differs after rollback:\n  %s", seed, k, mode, strings.Join(d, "\n  "))
				}
			}
		}
	}
}

type crash struct{}

func TestCrashAfterEachWriteIsRecovered(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		probe := build(t, seed)
		n := len(probe.plan.Actions)
		for k := 1; k <= n; k++ {
			for _, ev := range []string{"intent", "acted", "done"} {
				w := build(t, seed)
				start := snap(t, w.root)
				o := w.opts()
				o.TestHook = func(e string, seq int) error {
					if e == ev && seq == k {
						panic(crash{})
					}
					return nil
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							if _, ok := r.(crash); !ok {
								panic(r)
							}
						}
					}()
					_, _ = apply.Run(context.Background(), w.plan, o, w.deps())
				}()
				ids, err := backup.InProgress(w.state)
				if err != nil || len(ids) != 1 {
					t.Fatalf("seed %d k %d %s: in-progress sets %v %v", seed, k, ev, ids, err)
				}
				ur, err := backup.Undo(context.Background(), w.state, ids[0], undoDeps(w), backup.Options{})
				if err != nil || ur.ExitCode != 0 {
					t.Fatalf("seed %d k %d %s: recover: %v %+v", seed, k, ev, err, ur)
				}
				if d := testtree.Diff(start, snap(t, w.root)); len(d) > 0 {
					t.Fatalf("seed %d k %d %s: tree differs after recovery:\n  %s", seed, k, ev, strings.Join(d, "\n  "))
				}
			}
		}
	}
}
