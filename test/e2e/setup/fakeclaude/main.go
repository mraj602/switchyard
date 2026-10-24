// Command fakeclaude is a stand-in for the `claude` binary, for the end-to-end tests of
// `wy setup`. It mimics, with files on disk, everything `wy` calls:
//
//	claude --version
//	claude auth status --json | auth login --claudeai
//	claude plugin marketplace add|remove|list|update ...
//	claude plugin install|uninstall|enable|disable|list|update|validate ...
//	claude agents --json
//
// Build it under the name `claude` and put its folder first on PATH:
//
//	go build -o "$DIR/claude" ./test/e2e/setup/fakeclaude
//
// Behaviour follows docs/spec/setup.md 5.2 and 13 (Claude Code 2.1.296). The Contract test
// (WY_E2E_REAL_CLAUDE=1) compares it with the real binary.
//
// Environment:
//
//	CLAUDE_CONFIG_DIR            the folder; unset means ~/.claude with state in ~/.claude.json
//	CLAUDE_CODE_PLUGIN_CACHE_DIR plugin root (else settings.json env, else <folder>/plugins)
//	FAKE_AGENTS                  JSON printed by `agents --json` (default [])
//	FAKE_CLAUDE_LOG              file that gets one JSON line per run:
//	                             {"config_dir":"...","unset":false,"args":["plugin","list"]}
//	FAKE_CLAUDE_FAIL             comma list of subcommands that exit 1 ("plugin install",
//	                             "install", "auth status", "marketplace add", "plugin", ...)
//	FAKE_CLAUDE_BREAK_LINK=1     a linked settings.json is replaced by a regular file when written
//	FAKE_CLAUDE_SLOW=<ms>        sleep before doing anything else
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Version is what --version prints.
const Version = "2.1.296 (Claude Code)"

const pluginCacheEnv = "CLAUDE_CODE_PLUGIN_CACHE_DIR"

type app struct {
	env            func(string) string
	stdin          io.Reader
	stdout, stderr io.Writer
	now            func() time.Time
}

func main() {
	a := &app{env: os.Getenv, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, now: time.Now}
	os.Exit(a.run(os.Args[1:]))
}

// folder is the config folder the process runs as.
type folder struct {
	dir       string // unresolved
	stateFile string
	unset     bool
}

func (a *app) home() string {
	if h := a.env("HOME"); h != "" {
		return h
	}
	return a.env("USERPROFILE")
}

func (a *app) folder() folder {
	if v := a.env("CLAUDE_CONFIG_DIR"); v != "" {
		return folder{dir: filepath.Clean(v), stateFile: filepath.Join(filepath.Clean(v), ".claude.json")}
	}
	h := a.home()
	return folder{dir: filepath.Join(h, ".claude"), stateFile: filepath.Join(h, ".claude.json"), unset: true}
}

func (a *app) printf(format string, v ...any)  { fmt.Fprintf(a.stdout, format, v...) }
func (a *app) eprintf(format string, v ...any) { fmt.Fprintf(a.stderr, format, v...) }

func has(args []string, flag string) bool {
	for _, x := range args {
		if x == flag {
			return true
		}
	}
	return false
}

// positional returns the args that are not flags.
func positional(args []string) []string {
	var out []string
	for _, x := range args {
		if !strings.HasPrefix(x, "-") {
			out = append(out, x)
		}
	}
	return out
}

var wordRE = regexp.MustCompile(`^[a-z][a-z-]*$`)

// subcommandKey is the leading command words ("plugin marketplace add"), for FAKE_CLAUDE_FAIL.
func subcommandKey(args []string) string {
	var w []string
	for _, x := range args {
		if x == "--version" || x == "-v" {
			return "version"
		}
		if strings.HasPrefix(x, "-") {
			continue
		}
		if !wordRE.MatchString(x) || len(w) == 3 {
			break
		}
		w = append(w, x)
	}
	return strings.Join(w, " ")
}

func (a *app) injected(key string) bool {
	for _, e := range strings.Split(a.env("FAKE_CLAUDE_FAIL"), ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if key == e || strings.HasSuffix(key, " "+e) || strings.HasPrefix(key, e+" ") {
			return true
		}
	}
	return false
}

func (a *app) logCall(f folder, args []string) {
	p := a.env("FAKE_CLAUDE_LOG")
	if p == "" {
		return
	}
	line := enc(map[string]any{"config_dir": a.env("CLAUDE_CONFIG_DIR"), "unset": f.unset, "args": args})
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	_, _ = fh.Write(append(line, '\n'))
}

// touchState creates the state file if missing and rewrites it (bumping numStartups), as
// Claude Code does on every run. wy must neither back it up nor restore it.
func (a *app) touchState(f folder) error {
	if err := os.MkdirAll(filepath.Dir(f.stateFile), 0o755); err != nil {
		return err
	}
	o, err := readObj(f.stateFile)
	if err != nil {
		o = newObj() // unreadable: Claude Code would move it aside; the fake starts over
	}
	n := 0
	if raw, ok := o.get("numStartups"); ok {
		n, _ = strconv.Atoi(string(raw))
	}
	o.set("numStartups", n+1)
	return writeThrough(f.stateFile, o.bytes(), 0o600, false)
}

func (a *app) run(args []string) int {
	f := a.folder()
	a.logCall(f, args)
	if ms, _ := strconv.Atoi(a.env("FAKE_CLAUDE_SLOW")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if err := a.touchState(f); err != nil {
		a.eprintf("claude: %v\n", err)
		return 1
	}
	jsonOut := has(args, "--json")
	if key := subcommandKey(args); a.injected(key) {
		return a.fail(jsonOut, "injected failure: "+key)
	}
	pos := positional(args)
	if has(args, "--version") || has(args, "-v") || (len(pos) == 1 && pos[0] == "version") {
		a.printf("%s\n", Version)
		return 0
	}
	if len(pos) == 0 {
		a.printf("fakeclaude: interactive mode is not supported\n")
		return 1
	}
	switch pos[0] {
	case "auth":
		return a.auth(f, args, pos[1:])
	case "agents":
		return a.agents(args)
	case "plugin", "plugins":
		return a.plugin(f, args, pos[1:])
	}
	a.eprintf("error: unknown command '%s'\n", pos[0])
	return 1
}

// fail prints an error as a JSON outcome (when --json) or on stderr and returns exit 1.
func (a *app) fail(jsonOut bool, msg string) int {
	if jsonOut {
		a.printf("%s\n", enc(map[string]any{"outcome": "error", "message": msg}))
	} else {
		a.eprintf("%s\n", msg)
	}
	return 1
}

// ok prints a success outcome. extra keys are added after outcome and message.
func (a *app) ok(jsonOut bool, msg string, extra ...[2]any) int {
	if !jsonOut {
		a.printf("%s\n", msg)
		return 0
	}
	o := newObj()
	o.set("outcome", "ok")
	o.set("message", msg)
	for _, kv := range extra {
		o.set(kv[0].(string), kv[1])
	}
	a.printf("%s\n", o.compact())
	return 0
}

func (a *app) agents(args []string) int {
	out := strings.TrimSpace(a.env("FAKE_AGENTS"))
	if out == "" {
		out = "[]"
	}
	if has(args, "--json") {
		a.printf("%s\n", out)
	} else if out == "[]" {
		a.printf("No active sessions\n")
	} else {
		a.printf("%s\n", out)
	}
	return 0
}

// ---- auth ----------------------------------------------------------------------------------

func fakeUUID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	x := hex.EncodeToString(h[:16])
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
}

func (a *app) auth(f folder, args, pos []string) int {
	jsonOut := has(args, "--json")
	if len(pos) == 0 {
		return a.fail(false, "error: missing auth command (status, login, logout)")
	}
	switch pos[0] {
	case "status":
		st, _ := readObj(f.stateFile)
		acct := st.sub("oauthAccount")
		_, credErr := os.Stat(filepath.Join(f.dir, ".credentials.json"))
		_, hasAcct := st.get("oauthAccount")
		if !hasAcct || len(acct.keys) == 0 || credErr != nil {
			if jsonOut {
				o := newObj()
				o.set("loggedIn", false)
				o.set("authMethod", "none")
				o.set("apiProvider", "firstParty")
				a.printf("%s\n", o.bytes())
			} else {
				a.printf("Not logged in. Run claude auth login to authenticate.\n")
			}
			return 1
		}
		if !jsonOut {
			a.printf("Logged in as %s\n", acct.str("emailAddress"))
			return 0
		}
		o := newObj()
		o.set("loggedIn", true)
		o.set("authMethod", "claude.ai")
		o.set("apiProvider", "firstParty")
		o.set("analyticsDisabled", false)
		o.set("projectsDirectory", filepath.Join(f.dir, "projects"))
		o.set("configDirectory", f.dir)
		o.set("email", acct.str("emailAddress"))
		o.set("orgId", acct.str("organizationUuid"))
		o.set("orgName", acct.str("organizationName"))
		o.set("subscriptionType", "max")
		a.printf("%s\n", o.bytes())
		return 0
	case "login":
		return a.login(f)
	}
	return a.fail(false, "error: unknown auth command "+pos[0])
}

// login prints a fake authorize URL, reads one line ("<email>" or "<email>:<org>") and
// writes oauthAccount into the state file and a fake .credentials.json.
func (a *app) login(f folder) int {
	a.printf("Opening browser to sign in...\n")
	a.printf("If the browser did not open, visit: https://claude.ai/oauth/authorize?code=true&client_id=fake&response_type=code\n")
	a.printf("Paste code here if prompted > ")
	line, _ := bufio.NewReader(a.stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	email, org, _ := strings.Cut(line, ":")
	if !strings.Contains(email, "@") {
		a.printf("\n")
		a.eprintf("Login failed: invalid code\n")
		return 1
	}
	if org == "" {
		org = email + "'s Organization"
	}
	st, err := readObj(f.stateFile)
	if err != nil {
		st = newObj()
	}
	acct := newObj()
	acct.set("accountUuid", fakeUUID("account:"+email))
	acct.set("emailAddress", email)
	acct.set("organizationUuid", fakeUUID("org:"+org))
	acct.set("organizationName", org)
	acct.set("organizationRole", "admin")
	acct.set("workspaceRole", nil)
	acct.set("displayName", strings.SplitN(email, "@", 2)[0])
	st.set("oauthAccount", json.RawMessage(acct.compact()))
	if err := writeThrough(f.stateFile, st.bytes(), 0o600, false); err != nil {
		a.eprintf("claude: %v\n", err)
		return 1
	}
	cred := newObj()
	cred.set("claudeAiOauth", map[string]any{
		"accessToken": "fake-access-token", "refreshToken": "fake-refresh-token",
		"expiresAt": a.now().Add(8 * time.Hour).UnixMilli(), "scopes": []string{"user:inference"},
		"subscriptionType": "max",
	})
	if err := writeThrough(filepath.Join(f.dir, ".credentials.json"), cred.bytes(), 0o600, false); err != nil {
		a.eprintf("claude: %v\n", err)
		return 1
	}
	a.printf("\nLogin successful.\n")
	return 0
}
