package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/hub/state"
	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/paths"
	"github.com/mraj602/switchyard/internal/termctl"
)

// TitleFlag is the argument that turns `sy hook` into the title entry:
// `sy hook --title <Event>` (docs/spec/terminal.md T6).
const TitleFlag = "--title"

// titleFile is what <state>/term/title-<session>.json holds: the last state word and the
// last title printed for that session.
type titleFile struct {
	Word  string `json:"word"`
	Title string `json:"title"`
}

// titleStdinWait is how long the title hook waits for its payload: under the hook's 1 s
// timeout, so a caller that never closes the pipe costs a late title, not a killed hook.
const titleStdinWait = 700 * time.Millisecond

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// RunTitle is `sy hook --title <Event>`: it reads the hook JSON from stdin, folds only that
// event onto the session's title file and prints the `terminalSequence` hook JSON when the
// title changed. It prints nothing when terminal.titles is off, when the slot has no label,
// when the event does not change the state word, or when the title is unchanged since the
// last print. It never fails the caller: every error and panic is swallowed (the hook is
// synchronous, with a 1 s timeout), and the only thing it ever writes to stdout is one JSON
// object. args are the arguments after "--title".
func RunTitle(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) {
	defer func() { _ = recover() }()
	event := ""
	if len(args) > 0 {
		event = args[0]
	}
	in, err := readStdinWait(stdin, titleStdinWait)
	if err != nil {
		return
	}
	var p struct {
		SessionID        string `json:"session_id"`
		Cwd              string `json:"cwd"`
		Event            string `json:"hook_event_name"`
		NotificationType string `json:"notification_type"`
		Error            string `json:"error"`
		LastMessage      string `json:"last_assistant_message"`
		Source           string `json:"source"`
	}
	if json.Unmarshal(in, &p) != nil || p.SessionID == "" {
		return
	}
	if event == "" {
		event = p.Event
	}

	pe := paths.FromOS()
	pe.Getenv = getenv
	dirs, err := paths.Resolve(pe)
	if err != nil {
		return
	}
	f, err := config.Load(dirs.Config.Path)
	if err != nil {
		return
	}
	cfg, _ := f.Typed()
	if !cfg.Terminal.Titles {
		return
	}
	home := pe.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	slot := identity.Locator{Home: home, FS: identity.OSFS{}}.Current(getenv)
	label := cfg.Slots.Labels[slot.Name]
	if label == "" {
		return
	}

	path := filepath.Join(dirs.State.Path, "term", "title-"+sessionFileKey(p.SessionID)+".json")
	var prev titleFile
	havePrev := false
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &prev) == nil {
		havePrev = true
	}

	word := titleWord(event, p.NotificationType, p.Error, p.LastMessage)
	if event == "SessionStart" && p.Source == "compact" && havePrev && prev.Word != "" {
		// A compaction mid-turn starts no new turn: keep the word the turn had.
		word = prev.Word
	}
	if word == "" {
		return
	}
	title := termctl.Title(label, termctl.ProjectOf(p.Cwd), word)
	if title == "" {
		return
	}

	// SessionStart always prints: a resumed session in a new tab has the title file of its
	// last run, and the new tab still needs the title.
	if havePrev && prev.Title == title && event != "SessionStart" {
		return
	}
	if seq := termctl.TerminalSequence(title); seq != nil {
		writeTitleFile(path, titleFile{Word: word, Title: title})
		_, _ = stdout.Write(append(seq, '\n'))
	}
}

// titleWord is the state word an event sets, or "" when it leaves the word as it was.
func titleWord(event, notificationType, errKind, lastMessage string) string {
	switch event {
	case "SessionStart":
		return termctl.WordDone
	case "SessionEnd":
		return termctl.WordEnded
	case "UserPromptSubmit":
		return termctl.WordWorking
	case "PermissionRequest":
		return termctl.WordPermission
	case "Notification":
		switch notificationType {
		case "permission_prompt":
			return termctl.WordPermission
		case "elicitation_dialog":
			return termctl.WordAsked
		case "idle_prompt":
			return termctl.WordDone
		}
	case "Stop":
		if q, _ := state.ReadsAsQuestion(lastMessage); q {
			return termctl.WordAsked
		}
		return termctl.WordDone
	case "StopFailure":
		if errKind == "rate_limit" {
			return termctl.WordLimit
		}
		return termctl.WordFailed
	}
	return ""
}

// sessionFileKey is the session id when it is safe in a file name, else a hash of it.
func sessionFileKey(id string) string {
	if sessionIDRe.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "h" + hex.EncodeToString(sum[:8])
}

// writeTitleFile replaces the file atomically (mode 0600); a failure is ignored.
func writeTitleFile(path string, t titleFile) {
	b, err := json.Marshal(t)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".title-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
	}
}
