// Package tui is the interactive side of `sy setup` (docs/spec/setup.md 3): the five screens
// Detect, Accounts, Sharing, Hub and Done, drawn on a terminal in raw mode (keys: Enter
// continue, Esc back, q quit, space toggle, arrows move, a add an account), with a numbered
// line-prompt fallback for `TERM=dumb` or a terminal without raw mode. No TUI framework: the
// caller puts the terminal in raw mode (golang.org/x/term) and hands Run the reader and
// writer, so tests drive it with scripted keys over a fake terminal.
//
// Every screen's answers map one-to-one onto model.Choices (the flags of setup.md 4). Run
// never writes a file and never runs a command: quitting (`q`) changes nothing.
//
// Phase 3, WP-F. Depends on: setup/model, identity (Tilde).
package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mraj602/switchyard/internal/setup/model"
)

// ErrQuit is returned when the person quits (`q`, or end of input): nothing is applied.
var ErrQuit = errors.New("setup: quit, nothing changed")

// Options configures Run.
type Options struct {
	In  io.Reader
	Out io.Writer
	// Raw is true when the terminal is in raw mode (keys); false gives numbered line prompts.
	Raw bool
}

// Screen names, in order (the screens of docs/design/screens/).
var Screens = []string{"Main", "SetupSharing (accounts)", "SetupSharing (sharing)", "SetupHub", "SetupDone"}

const (
	scrMain = iota
	scrAccounts
	scrSharing
	scrHub
	scrDone
)

// Hub options shown on the hub screen.
const (
	hubKeep    = "keep"
	hubHere    = "here"
	hubJoin    = "join"
	hubAnother = "another" // "I'll run the hub on another server": later, with instructions
	hubLater   = "later"
)

type ui struct {
	st  model.State
	c   model.Choices
	o   Options
	in  *bufio.Reader
	scr int

	share   map[model.Switch]bool
	shareAt int
	hubOpts []string
	hubAt   int
	note    string // one-line message under the current screen
}

// Run shows the screens and returns the choices. ErrQuit means the person quit.
func Run(st model.State, c model.Choices, o Options) (model.Choices, error) {
	u := &ui{st: st, c: c, o: o, in: bufio.NewReader(o.In)}
	ss, err := model.ResolveShare(c.Share, st.MCP.HandMade)
	if err != nil {
		return c, err
	}
	u.share = map[model.Switch]bool{}
	for _, s := range model.AllSwitches {
		u.share[s] = ss.On[s]
	}
	u.hubOpts = nil
	if st.Hub.URL != nil && st.Hub.Healthy {
		u.hubOpts = append(u.hubOpts, hubKeep)
	}
	u.hubOpts = append(u.hubOpts, hubHere, hubJoin, hubAnother, hubLater)
	// Interactive default: keep a healthy hub, else "just this computer" (setup.md 4 --hub).
	want := c.Hub.Mode
	switch want {
	case "", model.HubKeep:
		want = u.hubOpts[0]
	case model.HubJoin:
		want = hubJoin
	}
	for i, h := range u.hubOpts {
		if h == want {
			u.hubAt = i
		}
	}
	if o.Raw {
		err = u.runRaw()
	} else {
		err = u.runLines()
	}
	if err != nil {
		return c, err
	}
	return u.result(), nil
}

// result maps the answers onto Choices.
func (u *ui) result() model.Choices {
	c := u.c
	def, _ := model.ResolveShare(nil, u.st.MCP.HandMade)
	same := true
	var on []string
	for _, s := range model.AllSwitches {
		if u.share[s] != def.On[s] {
			same = false
		}
		if u.share[s] {
			on = append(on, string(s))
		}
	}
	switch {
	case same:
		// leave c.Share as given (defaults)
	case len(on) == 0:
		c.Share = []string{"none"}
	default:
		c.Share = on
	}
	switch u.hubOpts[u.hubAt] {
	case hubKeep:
		c.Hub = model.HubChoice{Mode: model.HubKeep}
	case hubHere:
		c.Hub = model.HubChoice{Mode: model.HubHere}
	case hubJoin:
		c.Hub.Mode = model.HubJoin
	default:
		c.Hub = model.HubChoice{Mode: model.HubLater}
	}
	if c.Login == "" {
		c.Login = "ask"
	}
	c.Interactive = true
	return c
}

// ---- text of each screen ------------------------------------------------------------------

func (u *ui) tilde(p string) string {
	if u.st.Home != "" {
		if rel, err := filepath.Rel(u.st.Home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}

// DetectText is the body of screen Main (setup.md 3 step 1): one line per folder, what was
// already set up by hand with a verdict, and "Nothing has been changed yet."
func DetectText(st model.State) string {
	var b strings.Builder
	if st.Claude.Path == "" {
		b.WriteString("Claude Code was not found. Install it first: https://docs.anthropic.com/claude-code\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Claude Code %s at %s\n\n", st.Claude.Version, st.Claude.Path)
	b.WriteString("Accounts\n")
	for _, f := range st.Folders {
		b.WriteString("  " + folderLine(f) + "\n")
	}
	var hand []string
	links := 0
	for _, f := range st.Folders {
		for _, it := range f.Items {
			if it.State == model.ItemLinked || it.State == model.ItemElsewhere {
				links++
			}
		}
	}
	if links > 0 {
		hand = append(hand, fmt.Sprintf("keep     %d links between folders", links))
	}
	seen := map[string]bool{}
	for _, l := range st.Launchers {
		if l.Ours || seen[l.Kind] {
			continue
		}
		seen[l.Kind] = true
		n := 0
		for _, x := range st.Launchers {
			if x.Kind == l.Kind && !x.Ours {
				n++
			}
		}
		hand = append(hand, fmt.Sprintf("keep     %d launcher(s): %s", n, kindName(l.Kind)))
	}
	if st.MCP.HandMade {
		hand = append(hand, "keep     MCP sync by your launcher ("+st.MCP.Script+")")
	}
	for _, t := range st.SettingsTargets {
		if t.StatusLine != nil && t.StatusLine.Command != "" && !t.StatusLine.Ours {
			hand = append(hand, "wrap     status line in "+tildeOf(st.Home, t.Path))
		}
		if len(t.PrototypeHooks) > 0 {
			hand = append(hand, fmt.Sprintf("note     %d prototype hooks in %s (kept until a hub is live)", len(t.PrototypeHooks), tildeOf(st.Home, t.Path)))
		}
	}
	if len(hand) > 0 {
		b.WriteString("\nAlready set up by hand\n")
		for _, h := range hand {
			b.WriteString("  " + h + "\n")
		}
	}
	for _, w := range st.Warnings {
		b.WriteString("  ! " + w + "\n")
	}
	if len(st.IncompleteSets) > 0 {
		b.WriteString("  ! an earlier run was interrupted: " + strings.Join(st.IncompleteSets, ", ") + "\n")
	}
	b.WriteString("\nNothing has been changed yet.\n")
	return b.String()
}

func tildeOf(home, p string) string {
	u := &ui{st: model.State{Home: home}}
	return u.tilde(p)
}

func kindName(k string) string {
	switch k {
	case model.LauncherFunction:
		return "shell functions"
	case model.LauncherAlias:
		return "aliases"
	case model.LauncherScript:
		return "scripts"
	}
	return "files"
}

func folderLine(f model.Folder) string {
	label := f.Label
	if label == "" {
		label = strings.TrimPrefix(filepath.Base(f.Folder), ".")
	}
	status := "not logged in"
	if f.LoggedIn {
		status = "logged in"
		if f.Email != "" {
			status = f.Email + "  logged in"
		}
		if f.Plan != "" {
			status += " · " + planName(f.Plan)
		}
	}
	line := fmt.Sprintf("%-12s %-10s %s", f.Folder, label, status)
	if f.Launcher != nil {
		line += "   launcher: " + f.Launcher.Name + " (" + strings.TrimSuffix(kindName(f.Launcher.Kind), "s") + ")"
	}
	return line
}

func planName(p string) string {
	if p == "" {
		return p
	}
	return strings.ToUpper(p[:1]) + p[1:]
}

func (u *ui) accountsText() string {
	var b strings.Builder
	b.WriteString("Accounts\n")
	for _, f := range u.st.Folders {
		b.WriteString("  " + folderLine(f) + "\n")
	}
	for _, a := range u.c.Add {
		col := a.Colour
		if col == "" {
			col = "next colour"
		}
		fmt.Fprintf(&b, "  + new folder   %-10s %s   log in after setup\n", a.Label, col)
	}
	return b.String()
}

var switchNames = map[model.Switch]string{
	model.SwitchSettings:     "Settings",
	model.SwitchInstructions: "Instructions",
	model.SwitchAddons:       "Skills, agents, commands",
	model.SwitchPlugins:      "Plugins",
	model.SwitchMCP:          "MCP servers",
	model.SwitchHistory:      "History and memory",
	model.SwitchPrompts:      "Prompt history, plans",
}

// shareState is the row state of a switch on this machine (setup.md 3 step 3).
func (u *ui) shareState(s model.Switch) string {
	if s == model.SwitchMCP {
		if u.st.MCP.HandMade {
			return "your launcher"
		}
		return "sync"
	}
	if s == model.SwitchPlugins {
		return "shared plugin root"
	}
	var prim *model.Folder
	for i := range u.st.Folders {
		if u.st.Folders[i].Primary {
			prim = &u.st.Folders[i]
		}
	}
	items := model.ItemsFor(s)
	has := false
	if prim != nil {
		for _, it := range items {
			if st, ok := prim.Items[it.Name]; ok && st.State != model.ItemAbsent && st.State != "" {
				has = true
			}
		}
	}
	if !has {
		return "nothing to share yet"
	}
	linked, elsewhere, differs, todo := 0, 0, 0, 0
	for _, f := range u.st.Folders {
		if f.Primary {
			continue
		}
		for _, it := range items {
			st := f.Items[it.Name]
			switch {
			case st.State == model.ItemLinked:
				linked++
			case st.State == model.ItemElsewhere:
				elsewhere++
			case st.Compare == model.CompareDiffers:
				differs++
			default:
				todo++
			}
		}
	}
	switch {
	case differs > 0:
		return "separate (differs)"
	case todo == 0 && elsewhere > 0:
		return "shared elsewhere"
	case todo == 0 && linked > 0:
		return "linked by hand"
	}
	return "link"
}

func (u *ui) sharingText(cursor bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sharing (primary %s)\n", u.primary())
	for i, s := range model.AllSwitches {
		mark := "[ ]"
		if u.share[s] {
			mark = "[x]"
		}
		pre := "  "
		if cursor && i == u.shareAt {
			pre = "> "
		}
		if !cursor {
			pre = fmt.Sprintf("%d ", i+1)
		}
		line := fmt.Sprintf("%s%s %-26s %s", pre, mark, switchNames[s], u.shareState(s))
		if s == model.SwitchPlugins && u.share[model.SwitchSettings] {
			line += "  (on while Settings is on)"
		}
		if s == model.SwitchHistory {
			line += "  (two accounts resuming one session write into one transcript)"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("  Never shared: login and tokens, live sessions, caches, usage counters\n")
	return b.String()
}

func (u *ui) primary() string {
	for _, f := range u.st.Folders {
		if f.Primary {
			return f.Folder
		}
	}
	return "~/.claude"
}

func (u *ui) hubLabel(h string) string {
	switch h {
	case hubKeep:
		return "Keep " + *u.st.Hub.URL
	case hubHere:
		return "Just this computer"
	case hubJoin:
		l := "Connect to my hub"
		if u.c.Hub.URL != "" {
			l += " (" + u.c.Hub.URL + ")"
		}
		return l
	case hubAnother:
		return "I'll run the hub on another server"
	}
	return "Later"
}

func (u *ui) hubText(cursor bool) string {
	var b strings.Builder
	b.WriteString("Hub\n")
	for i, h := range u.hubOpts {
		mark := "( )"
		if i == u.hubAt {
			mark = "(•)"
		}
		pre := "  "
		if cursor && i == u.hubAt {
			pre = "> "
		}
		if !cursor {
			pre = fmt.Sprintf("%d ", i+1)
		}
		b.WriteString(pre + mark + " " + u.hubLabel(h) + "\n")
	}
	switch u.hubOpts[u.hubAt] {
	case hubAnother:
		b.WriteString("  On that server: docker run ... ghcr.io/<owner>/switchyard-hub, then\n" +
			"  sy hub token create --kind machine; connect later with sy connect <url> --token-stdin\n")
	case hubJoin:
		b.WriteString("  The token is asked for with echo off after the plan is accepted.\n")
	}
	if t := u.st.Tailscale; t != nil && t.Up && t.DNSName != "" && u.hubOpts[u.hubAt] == hubHere {
		b.WriteString("  Tailscale is up: the hub can be reached at " + t.DNSName + " if you run `tailscale serve`.\n")
	}
	return b.String()
}

func (u *ui) doneText() string {
	var b strings.Builder
	b.WriteString("Phone and done\n")
	b.WriteString("  Phone: later (sy phone pair)\n")
	b.WriteString("  Next: the plan is shown, and nothing changes until you accept it.\n")
	return b.String()
}

var footers = []string{
	"Enter continue · e change something · q quit",
	"a add an account · Enter continue · Esc back · q quit",
	"↑↓ move · space toggle · Enter continue · Esc back · q quit",
	"↑↓ choose · Enter continue · Esc back · q quit",
	"Enter show the plan · Esc back · q quit",
}

func (u *ui) screenText(cursor bool) string {
	var body string
	switch u.scr {
	case scrMain:
		body = DetectText(u.st)
	case scrAccounts:
		body = u.accountsText()
	case scrSharing:
		body = u.sharingText(cursor)
	case scrHub:
		body = u.hubText(cursor)
	default:
		body = u.doneText()
	}
	head := fmt.Sprintf("sy setup — %d/5 %s\n\n", u.scr+1, Screens[u.scr])
	s := head + body
	if u.note != "" {
		s += "\n" + u.note + "\n"
	}
	return s
}

// ---- raw mode ----------------------------------------------------------------------------

// Key names produced by readKey.
const (
	keyEnter = "enter"
	keyEsc   = "esc"
	keyUp    = "up"
	keyDown  = "down"
	keySpace = "space"
	keyBack  = "backspace"
	keyEOF   = "eof"
)

// clear is the escape sequence that starts every frame.
const clear = "\x1b[H\x1b[2J"

func (u *ui) draw() {
	text := u.screenText(true) + "\n" + footers[u.scr] + "\n"
	io.WriteString(u.o.Out, clear+strings.ReplaceAll(text, "\n", "\r\n"))
}

func (u *ui) readKey() string {
	b, err := u.in.ReadByte()
	if err != nil {
		return keyEOF
	}
	switch b {
	case '\r', '\n':
		return keyEnter
	case ' ':
		return keySpace
	case 0x7f, 0x08:
		return keyBack
	case 3: // Ctrl-C in raw mode
		return "q"
	case 0x1b:
		// An escape sequence arrives in one read; a lone Esc does not have bytes behind it.
		if u.in.Buffered() == 0 {
			return keyEsc
		}
		nx, _ := u.in.Peek(1)
		if len(nx) == 0 || (nx[0] != '[' && nx[0] != 'O') {
			return keyEsc
		}
		u.in.ReadByte()
		c, err := u.in.ReadByte()
		if err != nil {
			return keyEsc
		}
		switch c {
		case 'A':
			return keyUp
		case 'B':
			return keyDown
		}
		return "seq"
	}
	return string(rune(b))
}

// readLineRaw reads an edited line in raw mode, echoing it after prompt. ok is false on Esc.
func (u *ui) readLineRaw(prompt string) (string, bool, error) {
	var buf []rune
	for {
		io.WriteString(u.o.Out, "\r\x1b[K"+prompt+string(buf))
		k := u.readKey()
		switch k {
		case keyEnter:
			io.WriteString(u.o.Out, "\r\n")
			return strings.TrimSpace(string(buf)), true, nil
		case keyEsc:
			return "", false, nil
		case keyEOF:
			return "", false, ErrQuit
		case keyBack:
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case keySpace:
			buf = append(buf, ' ')
		default:
			if len([]rune(k)) == 1 {
				buf = append(buf, []rune(k)...)
			}
		}
	}
}

func (u *ui) runRaw() error {
	for {
		u.draw()
		u.note = ""
		k := u.readKey()
		switch k {
		case "q", keyEOF:
			return ErrQuit
		case keyEsc:
			if u.scr > 0 {
				u.scr--
			}
			continue
		}
		switch u.scr {
		case scrMain:
			if k == keyEnter || k == "e" {
				u.scr++
			}
		case scrAccounts:
			switch k {
			case keyEnter:
				u.scr++
			case "a":
				if err := u.addRaw(); err != nil {
					return err
				}
			}
		case scrSharing:
			switch k {
			case keyUp:
				if u.shareAt > 0 {
					u.shareAt--
				}
			case keyDown:
				if u.shareAt < len(model.AllSwitches)-1 {
					u.shareAt++
				}
			case keySpace:
				u.toggle(model.AllSwitches[u.shareAt])
			case keyEnter:
				u.scr++
			}
		case scrHub:
			switch k {
			case keyUp:
				if u.hubAt > 0 {
					u.hubAt--
				}
			case keyDown:
				if u.hubAt < len(u.hubOpts)-1 {
					u.hubAt++
				}
			case keyEnter:
				if u.hubOpts[u.hubAt] == hubJoin {
					url, ok, err := u.readLineRaw("Hub URL: ")
					if err != nil {
						return err
					}
					if !ok || url == "" {
						continue
					}
					u.c.Hub = model.HubChoice{Mode: model.HubJoin, URL: url}
				}
				u.scr++
			}
		case scrDone:
			if k == keyEnter {
				return nil
			}
		}
	}
}

func (u *ui) addRaw() error {
	s, ok, err := u.readLineRaw("New account label (label or label:colour): ")
	if err != nil || !ok || s == "" {
		return err
	}
	return u.addAccount(s)
}

func (u *ui) addAccount(s string) error {
	label, colour, _ := strings.Cut(s, ":")
	label = strings.TrimSpace(label)
	if !model.ValidLabel(label) {
		u.note = "! invalid label " + strconv.Quote(label) + " (a-z, 0-9 and -, up to 24 characters)"
		return nil
	}
	if colour != "" {
		if _, ok := model.ResolveColour(strings.TrimSpace(colour)); !ok {
			u.note = "! invalid colour " + strconv.Quote(colour) + " (a palette name or #rrggbb)"
			return nil
		}
	}
	u.c.Add = append(u.c.Add, model.LabelColour{Label: label, Colour: strings.TrimSpace(colour)})
	return nil
}

func (u *ui) toggle(s model.Switch) {
	if s == model.SwitchPlugins && u.share[model.SwitchSettings] && u.share[s] {
		u.note = "Plugins stay on while Settings is on (enabled plugins live in settings.json)"
		return
	}
	u.share[s] = !u.share[s]
	if s == model.SwitchSettings && u.share[s] {
		u.share[model.SwitchPlugins] = true
	}
}

// ---- line prompts ------------------------------------------------------------------------

func (u *ui) ask(prompt string) (string, error) {
	io.WriteString(u.o.Out, prompt)
	line, err := u.in.ReadString('\n')
	if err != nil && line == "" {
		return "", ErrQuit
	}
	line = strings.TrimSpace(line)
	if line == "q" {
		return "", ErrQuit
	}
	return line, nil
}

func (u *ui) runLines() error {
	for {
		io.WriteString(u.o.Out, "\n"+u.screenText(false))
		u.note = ""
		var (
			ans string
			err error
		)
		switch u.scr {
		case scrMain:
			ans, err = u.ask("Continue? [Enter = yes, q = quit] ")
		case scrAccounts:
			ans, err = u.ask("Add an account: type label[:colour], or Enter to continue (b back, q quit): ")
		case scrSharing:
			ans, err = u.ask("Toggle by number (e.g. 3 5), Enter to continue (b back, q quit): ")
		case scrHub:
			ans, err = u.ask(fmt.Sprintf("Choose 1-%d [%d] (b back, q quit): ", len(u.hubOpts), u.hubAt+1))
		default:
			ans, err = u.ask("Enter to show the plan (b back, q quit): ")
		}
		if err != nil {
			return err
		}
		if ans == "b" {
			if u.scr > 0 {
				u.scr--
			}
			continue
		}
		switch u.scr {
		case scrMain:
			u.scr++
		case scrAccounts:
			if ans == "" {
				u.scr++
				continue
			}
			if err := u.addAccount(ans); err != nil {
				return err
			}
		case scrSharing:
			if ans == "" {
				u.scr++
				continue
			}
			for _, w := range strings.FieldsFunc(ans, func(r rune) bool { return r == ' ' || r == ',' }) {
				n, err := strconv.Atoi(w)
				if err != nil || n < 1 || n > len(model.AllSwitches) {
					u.note = "! not a switch number: " + w
					continue
				}
				u.toggle(model.AllSwitches[n-1])
			}
		case scrHub:
			if ans != "" {
				n, err := strconv.Atoi(ans)
				if err != nil || n < 1 || n > len(u.hubOpts) {
					u.note = "! choose a number from 1 to " + strconv.Itoa(len(u.hubOpts))
					continue
				}
				u.hubAt = n - 1
			}
			if u.hubOpts[u.hubAt] == hubJoin {
				url, err := u.ask("Hub URL: ")
				if err != nil {
					return err
				}
				if url == "" {
					continue
				}
				u.c.Hub = model.HubChoice{Mode: model.HubJoin, URL: url}
			}
			u.scr++
		default:
			return nil
		}
	}
}

// Confirm asks a yes/no question with a default, in raw or line mode. End of input is "no".
func Confirm(in io.Reader, out io.Writer, raw bool, question string, def bool) bool {
	hint := " [Y/n] "
	if !def {
		hint = " [y/N] "
	}
	r := bufio.NewReader(in)
	io.WriteString(out, question+hint)
	if raw {
		for {
			b, err := r.ReadByte()
			if err != nil {
				io.WriteString(out, "\r\n")
				return false
			}
			switch b {
			case '\r', '\n':
				io.WriteString(out, "\r\n")
				return def
			case 'y', 'Y':
				io.WriteString(out, "y\r\n")
				return true
			case 'n', 'N', 'q', 3, 0x1b:
				io.WriteString(out, "n\r\n")
				return false
			}
		}
	}
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}
