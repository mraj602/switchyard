// Command treesnap snapshots and compares file trees for the setup E2E harness. It is a thin
// CLI over internal/setup/testtree (the same snapshot the Go unit tests use), so apply
// followed by undo is checked byte for byte: type, mode, size, SHA-256, file mtime and raw
// symlink target.
//
//	treesnap snap  [flags] <root> <out.json>      write a snapshot
//	treesnap diff  <before.json> <after.json>     print differences, exit 1 if any
//	treesnap check [flags] <root> <before.json>   snapshot root now and diff (exit 1 if any)
//
// Flags (snap, check):
//
//	-ignore a,b,c      paths relative to root to skip, with everything under them
//	-no-mtime          do not compare file mtimes
//	-dir-mtimes        compare folder mtimes too
//	-claude-json       compare every .claude.json by its "oauthAccount" and "mcpServers"
//	                   keys only (Claude Code and the fake rewrite the rest on every run)
//
// With no -ignore, the setup harness defaults apply: .local/state/watchyard, .local/bin/wy.
// Pass -ignore ” to ignore nothing.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mraj602/watchyard/internal/setup/testtree"
)

// emptySubset is the reduced identity of a .claude.json without oauthAccount and mcpServers.
var emptySubset = func() string { s := sha256.Sum256([]byte("{}")); return "subset:" + hex.EncodeToString(s[:]) }()

const defaultIgnore = ".local/state/watchyard,.local/bin/wy"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "snap":
		fs, o := flags("snap")
		fs.Parse(os.Args[2:])
		if fs.NArg() != 2 {
			usage()
		}
		s := take(fs.Arg(0), o)
		write(fs.Arg(1), s)
	case "check":
		fs, o := flags("check")
		fs.Parse(os.Args[2:])
		if fs.NArg() != 2 {
			usage()
		}
		os.Exit(report(read(fs.Arg(1)), take(fs.Arg(0), o)))
	case "diff":
		if len(os.Args) != 4 {
			usage()
		}
		os.Exit(report(read(os.Args[2]), read(os.Args[3])))
	default:
		usage()
	}
}

type options struct {
	ignore     string
	noMtime    bool
	dirMtimes  bool
	claudeJSON bool
}

func flags(name string) (*flag.FlagSet, *options) {
	o := &options{}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.StringVar(&o.ignore, "ignore", defaultIgnore, "comma-separated paths to skip")
	fs.BoolVar(&o.noMtime, "no-mtime", false, "do not compare file mtimes")
	fs.BoolVar(&o.dirMtimes, "dir-mtimes", false, "compare folder mtimes")
	fs.BoolVar(&o.claudeJSON, "claude-json", false, "compare .claude.json by oauthAccount and mcpServers only")
	return fs, o
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: treesnap snap|check [flags] <root> <file.json> | treesnap diff <a.json> <b.json>")
	os.Exit(2)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "treesnap:", err)
	os.Exit(2)
}

func take(root string, o *options) testtree.Snap {
	var skip []string
	for _, p := range strings.Split(o.ignore, ",") {
		if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
			skip = append(skip, p)
		}
	}
	opts := testtree.Opts{
		DirMtimes: o.dirMtimes,
		NoMtime:   o.noMtime,
		Ignore: func(rel string) bool {
			for _, p := range skip {
				if rel == p || strings.HasPrefix(rel, p+"/") {
					return true
				}
			}
			return false
		},
	}
	s, err := testtree.TakeErr(root, opts)
	if err != nil {
		fatal(err)
	}
	if o.claudeJSON {
		for p, n := range s {
			if n.Type == "file" && filepath.Base(p) == ".claude.json" {
				s[p] = reduceClaudeJSON(filepath.Join(root, filepath.FromSlash(p)), n)
				// A state file with neither key has nothing to compare: Claude Code and
				// the fake create one in a folder on its first run of any command
				// (even `claude auth status`), so its existence is noise too.
				if s[p].SHA256 == emptySubset {
					delete(s, p)
				}
			}
		}
	}
	return s
}

// reduceClaudeJSON replaces the content identity of a .claude.json by the hash of its
// oauthAccount and mcpServers values (Go sorts map keys, so the form is canonical).
func reduceClaudeJSON(path string, n testtree.Node) testtree.Node {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	var doc map[string]json.RawMessage
	sub := map[string]json.RawMessage{}
	if json.Unmarshal(b, &doc) == nil {
		for _, k := range []string{"oauthAccount", "mcpServers"} {
			if v, ok := doc[k]; ok {
				sub[k] = v
			}
		}
	} else {
		sub["unparsable"] = json.RawMessage(`true`)
	}
	canon, _ := json.Marshal(sub)
	sum := sha256.Sum256(canon)
	n.SHA256 = "subset:" + hex.EncodeToString(sum[:])
	n.Size = 0
	n.MtimeNS = 0
	return n
}

func write(path string, s testtree.Snap) {
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		fatal(err)
	}
}

func read(path string) testtree.Snap {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	s := testtree.Snap{}
	if err := json.Unmarshal(b, &s); err != nil {
		fatal(fmt.Errorf("%s: %w", path, err))
	}
	return s
}

func report(want, got testtree.Snap) int {
	d := testtree.Diff(want, got)
	if len(d) == 0 {
		return 0
	}
	fmt.Println("trees differ:")
	for _, l := range d {
		fmt.Println("  " + l)
	}
	return 1
}
