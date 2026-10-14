package warp

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PluginCommit is the commit Warp's plugin marketplace checkout is at, "" when unknown. root
// is a Claude Code plugin root (a folder holding known_marketplaces.json); marketplace is
// the marketplace name (claude-code-warp). It reads installLocation from
// known_marketplaces.json and then the checkout's HEAD from the files of its .git folder
// (HEAD, the loose ref, packed-refs). It never runs git and never writes.
func PluginCommit(root, marketplace string) string {
	dir := PluginCheckout(root, marketplace)
	if dir == "" {
		return ""
	}
	return HeadOf(filepath.Join(dir, ".git"))
}

// PluginCheckout is the installLocation known_marketplaces.json of root records for
// marketplace ("" when there is none).
func PluginCheckout(root, marketplace string) string {
	b, err := os.ReadFile(filepath.Join(root, "known_marketplaces.json"))
	if err != nil {
		return ""
	}
	var known map[string]struct {
		InstallLocation string `json:"installLocation"`
	}
	if json.Unmarshal(b, &known) != nil {
		return ""
	}
	return known[marketplace].InstallLocation
}

func isSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// HeadOf resolves HEAD of a .git folder to a commit id without running git ("" when it
// cannot).
func HeadOf(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	if isSHA(head) {
		return head
	}
	ref, ok := strings.CutPrefix(head, "ref: ")
	if !ok || strings.Contains(ref, "..") {
		return ""
	}
	if b, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		if s := strings.TrimSpace(string(b)); isSHA(s) {
			return s
		}
	}
	f, err := os.Open(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sha, name, ok := strings.Cut(sc.Text(), " ")
		if ok && name == ref && isSHA(sha) {
			return sha
		}
	}
	return ""
}

var commandsRE = regexp.MustCompile(`(?m)^commands\s*=\s*(\[.*\])\s*$`)

// TabCommand is the first command a tab config runs (the launcher path Render writes),
// "" when the content has none.
func TabCommand(content []byte) string {
	m := commandsRE.FindSubmatch(content)
	if m == nil {
		return ""
	}
	var cmds []string
	if json.Unmarshal(m[1], &cmds) != nil || len(cmds) == 0 {
		return ""
	}
	return cmds[0]
}
