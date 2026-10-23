// Package render prints a model.Plan as text (the layout of setup.md 6.3 and 11) and as the
// `--json` report. Settings `env` values and `mcpServers` values never appear in either:
// text prints pointers only, JSON replaces those values with "<redacted>".
//
// Depends on: setup/model, identity (Tilde).
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mraj602/switchyard/internal/identity"
	"github.com/mraj602/switchyard/internal/setup/model"
)

// Redacted replaces secret-bearing JSON values in output.
const Redacted = "<redacted>"

// Input is everything a renderer needs.
type Input struct {
	Mode    string // model.ModeDryRun, ModeApplied, ModePartial, ModeRolledBack
	Primary string // "~/.claude"
	State   *model.State
	Plan    model.Plan
	// After an apply:
	Backup  string
	Applied []string
	Hub     *model.HubResult
	Doctor  json.RawMessage
	// Uninstall: the steps of `sy uninstall` outside the plan (JSON only; the command prints
	// their text).
	Uninstall *model.UninstallSteps
}

// Redact returns a copy of the plan that is safe to print: the Before and Value of every
// json_set / json_delete under /env or /mcpServers are replaced with "<redacted>".
func Redact(p model.Plan) model.Plan {
	out := p
	out.Actions = make([]model.Action, len(p.Actions))
	for i, a := range p.Actions {
		if (a.Op == model.OpJSONSet || a.Op == model.OpJSONDelete) && secretPointer(a.Pointer) {
			if len(a.Before) > 0 {
				a.Before = json.RawMessage(strconv.Quote(Redacted))
			}
			if len(a.Value) > 0 {
				a.Value = json.RawMessage(strconv.Quote(Redacted))
			}
		}
		out.Actions[i] = a
	}
	return out
}

func secretPointer(ptr string) bool {
	for _, k := range []string{"/env", "/mcpServers"} {
		if ptr == k || strings.HasPrefix(ptr, k+"/") {
			return true
		}
	}
	return false
}

// JSON renders the --json report (setup.md 6.3): one object, "v": 1.
func JSON(in Input) ([]byte, error) {
	p := Redact(in.Plan)
	if p.Actions == nil {
		p.Actions = []model.Action{}
	}
	if p.Keep == nil {
		p.Keep = []model.Keep{}
	}
	if p.Conflicts == nil {
		p.Conflicts = []model.Conflict{}
	}
	if p.Needs == nil {
		p.Needs = []model.Need{}
	}
	r := model.Report{V: 1, Mode: in.Mode, Primary: in.Primary, State: in.State, Plan: p,
		Backup: in.Backup, Applied: in.Applied, Hub: in.Hub, Doctor: in.Doctor, Uninstall: in.Uninstall}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// itemOrder is the order items are listed in on screen (setup.md 11).
var itemOrder = map[string]int{"settings.json": 0, "CLAUDE.md": 1, "commands": 2, "skills": 3, "plugins": 4,
	"projects": 5, "history.jsonl": 6, "plans": 7, "agents": 8, "rules": 9, "keybindings.json": 10}

func isDir(name string) bool {
	for _, it := range model.ShareItems {
		if it.Name == name {
			return it.Kind == model.KindDir
		}
	}
	return false
}

func itemLabel(name string) string {
	if isDir(name) {
		return name + "/"
	}
	return name
}

func sortItems(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		a, aok := itemOrder[names[i]]
		b, bok := itemOrder[names[j]]
		if aok != bok {
			return aok
		}
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
}

type writer struct {
	b    strings.Builder
	home string
}

func (w *writer) line(format string, a ...any) { fmt.Fprintf(&w.b, format+"\n", a...) }
func (w *writer) blank()                       { w.b.WriteString("\n") }
func (w *writer) tilde(p string) string {
	if w.home == "" || !filepath.IsAbs(p) {
		return p
	}
	return identity.Tilde(w.home, p)
}

func runes(s string) int { return utf8.RuneCountInString(s) }

func pad(s string, n int) string {
	if d := n - runes(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Text renders the plan as the text of setup.md 6.3: sections in apply order, one line per
// action or group, paths with `~`, the count line last.
func Text(in Input) string {
	p := in.Plan
	w := &writer{}
	if in.State != nil {
		w.home = in.State.Home
	}
	title := "sy setup — plan"
	if in.Mode == model.ModeDryRun {
		title += " (dry run: nothing changed)"
	}
	if in.Mode == model.ModeApplied {
		title = "sy setup — applied"
	}
	if in.Mode == model.ModePartial {
		title = "sy setup — partly applied (hub step failed)"
	}
	if in.Mode == model.ModeRolledBack {
		title = "sy setup — rolled back"
	}
	w.line("%s", pad(title, 76-runes(p.PlanID))+p.PlanID)

	w.blank()
	w.accounts(in)
	w.blank()
	w.sharing(in)
	if sec := w.plugin(p); sec {
		// plugin section printed
	}
	w.statusLine(p)
	w.launchers(in)
	w.config(p)
	w.mcp(p)
	w.hub(p)
	w.hubFailure(in)
	w.needs(in)
	w.warnings(p)

	s := p.Summary
	w.blank()
	w.line("%s", countLine(s))
	return w.b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func countLine(s model.Summary) string {
	return strings.Join([]string{
		plural(s.Changes, "change", "changes"),
		plural(s.BackedUp, "file backed up", "files backed up"),
		plural(s.LinksChanged, "link changed", "links changed"),
		plural(s.LoginsTouched, "login touched", "logins touched"),
	}, " · ")
}

// --- accounts -----------------------------------------------------------------------------

func launcherKind(k string, ours bool, plural bool) string {
	var s string
	switch k {
	case model.LauncherFunction:
		s = "shell function"
	case model.LauncherAlias:
		s = "alias"
	case model.LauncherFile:
		s = "file"
	default:
		s = k
	}
	if ours {
		s = "sy launcher"
	}
	if plural {
		s += "s"
	}
	return s
}

// labels returns the label and colour each folder has after the plan.
func configLabels(p model.Plan) map[string]string {
	out := map[string]string{}
	for _, a := range p.Actions {
		if a.Op != model.OpConfigSet {
			continue
		}
		var m map[string]any
		if json.Unmarshal(a.Value, &m) != nil {
			continue
		}
		for k, v := range m {
			if f, ok := strings.CutPrefix(k, "slots.labels."); ok {
				out[f], _ = v.(string)
			}
		}
	}
	return out
}

func (w *writer) accounts(in Input) {
	w.line("Accounts")
	p := in.Plan
	labels := configLabels(p)
	written := map[string]string{} // folder -> launcher file written for it (from the note)
	for _, a := range p.Actions {
		if a.Op == model.OpWriteFile && a.Step == model.StepLaunchers {
			if _, f, ok := strings.Cut(a.Note, " for "); ok {
				if _, seen := written[f]; !seen {
					written[f] = strings.TrimSuffix(filepath.Base(a.Path), ".cmd")
				}
			}
		}
	}
	type row struct {
		folder, label, status, launcher, marker string
		in                                      bool
		fname                                   string
	}
	var rows []row
	if in.State != nil {
		for _, f := range in.State.Folders {
			label := f.Label
			if l, ok := labels[f.Folder]; ok {
				label = l
			}
			if label == "" {
				label = strings.TrimPrefix(filepath.Base(f.Dir), ".")
			}
			r := row{folder: f.Folder, label: label, in: f.LoggedIn, fname: ""}
			r.status = "not logged in"
			r.marker = "?"
			if f.LoggedIn {
				r.status = "logged in"
				if f.Plan != "" {
					r.status += " · " + capital(f.Plan)
				}
				r.marker = "="
			}
			switch {
			case f.Launcher != nil:
				r.launcher = f.Launcher.Name + " (" + launcherKind(f.Launcher.Kind, f.Launcher.Ours, false) + ")"
				r.fname = f.Launcher.Name
			case written[f.Folder] != "":
				r.launcher = written[f.Folder] + " (new)"
				r.fname = written[f.Folder]
			default:
				r.launcher = "none"
			}
			rows = append(rows, r)
		}
	}
	for _, a := range p.Actions {
		if a.Op == model.OpMkdir && a.Step == model.StepFolders {
			f := w.tilde(a.Path)
			r := row{folder: f, label: a.Note, status: "new folder", marker: "+"}
			if l, ok := labels[f]; ok {
				r.label = l
			}
			r.launcher = "none"
			if written[f] != "" {
				r.launcher, r.fname = written[f]+" (new)", written[f]
			}
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		w.line("  · no Claude Code folders found")
		return
	}
	wf, wl, ws := 0, 0, 0
	for _, r := range rows {
		if r.marker == "?" {
			continue
		}
		wf, wl, ws = max(wf, runes(r.folder)), max(wl, runes(r.label)), max(ws, runes(r.status))
	}
	wf, wl, ws = wf+3, wl+3, ws+4
	for i := 0; i < len(rows); i++ {
		r := rows[i]
		if r.marker == "?" {
			// Consecutive folders that are not logged in and share a launcher kind are one range.
			j := i
			for j+1 < len(rows) && rows[j+1].marker == "?" && kindOf(rows[j+1].launcher) == kindOf(r.launcher) {
				j++
			}
			if j > i {
				last := rows[j]
				w.line("  ? %s … %s   %s    launchers: %s … %s (%ss)", r.folder, last.folder, r.status,
					r.fname, last.fname, kindOf(r.launcher))
				i = j
				continue
			}
		}
		w.line("  %s %s%s%s%s", r.marker, pad(r.folder, wf), pad(r.label, wl), pad(r.status, ws), "launcher: "+r.launcher)
	}
}

func kindOf(launcher string) string {
	if i := strings.Index(launcher, "("); i >= 0 {
		return strings.TrimSuffix(launcher[i+1:], ")")
	}
	return launcher
}

// --- sharing ------------------------------------------------------------------------------

func (w *writer) sharing(in Input) {
	p := in.Plan
	prim := in.Primary
	if prim == "" {
		prim = "~/.claude"
	}
	w.line("Sharing (primary %s)", prim)
	empty := true

	// Links adopted by hand.
	linked := map[string]bool{}
	folders := map[string]bool{}
	links := 0
	elsewhere := map[string][]string{}
	var nothing []string
	for _, k := range p.Keep {
		switch k.What {
		case "link":
			links++
			rel := filepath.Base(k.Path)
			linked[rel] = true
			folders[filepath.Dir(k.Path)] = true
		case "link-elsewhere":
			rel := filepath.Base(k.Path)
			elsewhere[rel] = append(elsewhere[rel], filepath.Dir(k.Path))
		case "nothing-to-share":
			nothing = append(nothing, k.Path)
		}
	}
	if links > 0 {
		var items []string
		for n := range linked {
			items = append(items, n)
		}
		sortItems(items)
		for i := range items {
			items[i] = itemLabel(items[i])
		}
		total := len(folders)
		if in.State != nil {
			total = 0
			for _, f := range in.State.Folders {
				if !f.Primary {
					total++
				}
			}
		}
		w.line("  = %s", strings.Join(items, ", "))
		w.line("      linked by hand in %d of %d folders (%s, kept)", len(folders), total, plural(links, "link", "links"))
		empty = false
	}
	for _, rel := range sortedKeysOf(elsewhere) {
		w.line("  = %s   shared elsewhere in %s", itemLabel(rel), strings.Join(elsewhere[rel], ", "))
		empty = false
	}

	// Planned changes of the sharing step, grouped per operation and item.
	type key struct{ op, item, extra string }
	groups := map[key][]string{}
	var order []key
	for _, a := range p.Actions {
		if a.Step != model.StepSharing {
			continue
		}
		var k key
		var where string
		switch a.Op {
		case model.OpLink, model.OpReplaceWithLink:
			k = key{string(a.Op), filepath.Base(a.Path), a.Kind}
			where = w.tilde(filepath.Dir(a.Path))
		case model.OpMove:
			w.line("  ~ move %s → %s", w.tilde(a.From), w.tilde(a.To))
			empty = false
			continue
		case model.OpMoveToBackup:
			w.line("  ~ save %s into the backup set", w.tilde(a.Path))
			empty = false
			continue
		case model.OpMergeLines:
			w.line("  ~ merge %s into %s   %s", w.tilde(a.From), w.tilde(a.Path), a.Note)
			empty = false
			continue
		case model.OpWriteFile:
			w.line("  + write %s   %s", w.tilde(a.Path), a.Note)
			empty = false
			continue
		case model.OpJSONSet, model.OpJSONDelete:
			w.jsonLine(a)
			empty = false
			continue
		default:
			continue
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], where)
	}
	for _, k := range order {
		verb, mark := "link", "+"
		if k.op == string(model.OpReplaceWithLink) {
			verb, mark = "link (original saved)", "~"
		}
		w.line("  %s %s %s   in %s", mark, verb, itemLabel(k.item), strings.Join(groups[k], ", "))
		empty = false
	}
	for _, c := range p.Conflicts {
		w.line("  ! %s %s   differs from the primary: %s   [%s]", c.Folder, itemLabel(c.Item), c.Resolution, strings.Join(c.Choices, "|"))
		empty = false
	}
	if len(nothing) > 0 {
		sort.SliceStable(nothing, func(i, j int) bool { return itemOrder[nothing[i]] < itemOrder[nothing[j]] })
		for i := range nothing {
			nothing[i] = itemLabel(nothing[i])
		}
		w.line("  · %s   nothing to share yet", strings.Join(nothing, ", "))
		empty = false
	}
	for _, k := range p.Keep {
		if k.What == "mcp" {
			w.line("  = MCP servers   synced by your launcher (%s)", w.tilde(k.Path))
			empty = false
		}
	}
	if empty {
		w.line("  · nothing to do")
	}
}

func sortedKeysOf(m map[string][]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sortItems(out)
	return out
}

// jsonLine prints one json_set / json_delete action. Values are printed only for the
// status line; everything else shows the pointer.
func (w *writer) jsonLine(a model.Action) {
	mark, verb := "~", ""
	if a.Op == model.OpJSONDelete {
		mark = "−"
	}
	ptr := strings.TrimPrefix(strings.ReplaceAll(a.Pointer, "/", "."), ".")
	tail := ""
	if len(a.Covers) > 1 {
		tail = fmt.Sprintf("   (shared by %d folders)", len(a.Covers))
	}
	if a.Before == nil && a.Op == model.OpJSONSet {
		mark = "+"
	}
	if a.Note != "" && !strings.HasPrefix(a.Pointer, "/statusLine") {
		tail += "   " + a.Note
	}
	w.line("  %s %s%s  %s%s", mark, verb, w.tilde(a.File), ptr, tail)
	if strings.HasPrefix(a.Pointer, "/statusLine") {
		if a.Before != nil {
			w.line("      was  %s", plainValue(a.Before))
		}
		w.line("      now  %s", plainValue(a.Value))
	}
}

func plainValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) == nil && m["command"] != "" {
		return m["command"]
	}
	return string(raw)
}

// --- plugin -------------------------------------------------------------------------------

func (w *writer) plugin(p model.Plan) bool {
	var acts []model.Action
	for _, a := range p.Actions {
		if a.Step == model.StepPlugin {
			acts = append(acts, a)
		}
	}
	var keeps []model.Keep
	for _, k := range p.Keep {
		if k.What == "plugin" {
			keeps = append(keeps, k)
		}
	}
	if len(acts) == 0 && len(keeps) == 0 {
		return false
	}
	w.blank()
	w.line("Plugin")
	for _, k := range keeps {
		w.line("  = %s   %s", w.tilde(k.Path), k.Note)
	}
	for _, a := range acts {
		switch a.Op {
		case model.OpWriteTree:
			w.line("  + write  %s/   %s", w.tilde(a.Path), a.Note)
		case model.OpRun:
			argv := append([]string(nil), a.Argv...)
			key := "enabledPlugins"
			if len(argv) > 3 && argv[2] == "marketplace" && argv[3] == "add" && len(argv) > 4 {
				argv[4] = "…"
				key = "extraKnownMarketplaces"
			}
			as := "~/.claude"
			if d := a.Env["CLAUDE_CONFIG_DIR"]; d != nil {
				as = w.tilde(*d)
			}
			w.line("  + run    %s   as %s", strings.Join(argv, " "), as)
			var edits []string
			for _, t := range a.Touches {
				if strings.HasSuffix(t, "settings.json") {
					edits = append(edits, fmt.Sprintf("%s (%s)", w.tilde(t), key))
				} else {
					edits = append(edits, w.tilde(t))
				}
			}
			if len(edits) > 0 {
				w.line("             edits %s", strings.Join(edits, ", "))
			}
			if len(a.Covers) > 1 {
				note := ""
				if a.Note != "" {
					note = " (" + a.Note + ")"
				}
				w.line("             covers %d folders%s", len(a.Covers), note)
			} else if a.Note != "" {
				w.line("             %s", a.Note)
			}
		}
	}
	return true
}

// --- status line, launchers, config, mcp, hub, needs -----------------------------

func (w *writer) statusLine(p model.Plan) {
	var acts []model.Action
	for _, a := range p.Actions {
		if a.Step == model.StepStatusLine {
			acts = append(acts, a)
		}
	}
	if len(acts) == 0 {
		return
	}
	w.blank()
	w.line("Status line")
	for _, a := range acts {
		w.jsonLine(a)
	}
}

func (w *writer) launchers(in Input) {
	p := in.Plan
	w.blank()
	w.line("Launchers")
	n := 0
	for _, a := range p.Actions {
		if a.Step == model.StepLaunchers {
			w.line("  + write  %s   %s", w.tilde(a.Path), a.Note)
			n++
		}
	}
	if n > 0 {
		return
	}
	all := in.State != nil && len(in.State.Folders) > 0
	if in.State != nil {
		for _, f := range in.State.Folders {
			if f.Launcher == nil && !f.Default {
				all = false
			}
		}
	}
	if all {
		w.line("  · none written (every folder already has one)")
	} else {
		w.line("  · none written")
	}
}

func (w *writer) config(p model.Plan) {
	for _, a := range p.Actions {
		if a.Op != model.OpConfigSet {
			continue
		}
		w.blank()
		w.line("Config")
		var m map[string]json.RawMessage
		_ = json.Unmarshal(a.Value, &m)
		var parts []string
		labels := 0
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if (keys[i] == "share.primary") != (keys[j] == "share.primary") {
				return keys[i] == "share.primary"
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			switch {
			case k == "share.primary":
				parts = append(parts, k)
			case strings.HasPrefix(k, "slots.labels."):
				labels++
			case strings.HasPrefix(k, "slots.colours."):
			default:
				parts = append(parts, fmt.Sprintf("%s = %s", k, string(m[k])))
			}
		}
		if labels > 0 {
			parts = append(parts, fmt.Sprintf("labels for %s", plural(labels, "folder", "folders")))
		}
		mark := "+"
		for _, pre := range a.Pre {
			if pre.Path == a.Path && pre.State.Type == model.TypeFile {
				mark = "~"
			}
		}
		w.line("  %s %s   %s", mark, w.tilde(a.Path), strings.Join(parts, ", "))
	}
}

func (w *writer) mcp(p model.Plan) {
	for _, a := range p.Actions {
		if a.Op != model.OpMCPSync {
			continue
		}
		w.blank()
		w.line("MCP servers")
		var parts []string
		if a.Names != nil {
			if n := len(a.Names.Added); n > 0 {
				parts = append(parts, fmt.Sprintf("%d added", n))
			}
			if n := len(a.Names.Changed); n > 0 {
				parts = append(parts, fmt.Sprintf("%d changed", n))
			}
			if n := len(a.Names.Removed); n > 0 {
				parts = append(parts, fmt.Sprintf("%d removed", n))
			}
		}
		w.line("  ~ sync mcpServers in %s   (%s)", plural(len(a.Files), "file", "files"), strings.Join(parts, ", "))
		if a.Names != nil {
			var names []string
			names = append(names, a.Names.Added...)
			names = append(names, a.Names.Changed...)
			names = append(names, a.Names.Removed...)
			w.line("      servers: %s", strings.Join(names, ", "))
		}
	}
}

func (w *writer) hub(p model.Plan) {
	w.blank()
	w.line("Hub")
	n := 0
	for _, a := range p.Actions {
		switch a.Op {
		case model.OpService:
			w.line("  + service %s   %s", a.Name, a.Note)
			n++
		case model.OpConnect:
			note, _, _ := strings.Cut(a.Note, ";")
			switch {
			case a.Reach == model.HubPull:
				w.line("  + hub: %s, timer every minute", note)
			case a.Dial != "":
				w.line("  + connect %s through %s (reach %s; %s), timer every minute", a.URL, a.Dial, a.Reach, note)
			case a.Reach != "":
				w.line("  + connect %s (reach %s; %s), timer every minute", a.URL, a.Reach, note)
			default:
				w.line("  + connect %s (%s), timer every minute", a.URL, note)
			}
			n++
		}
	}
	if n > 0 {
		return
	}
	for _, k := range p.Keep {
		if k.What != "hub" {
			continue
		}
		if k.Note == "later" {
			w.line("  · later   (sy connect <url> --token-stdin, or sy setup --hub join:<url>)")
		} else {
			w.line("  = %s", k.Note)
		}
		return
	}
	w.line("  · later")
}

// hubFailure names the hub step that did not happen and how to finish it (exit 6).
func (w *writer) hubFailure(in Input) {
	h := in.Hub
	if h == nil || h.Error == "" {
		return
	}
	step := h.Step
	if step == "" {
		step = "hub"
	}
	w.line("  ✗ not done: %s: %s", step, h.Error)
	if h.Fix != "" {
		w.line("    fix: %s", h.Fix)
	}
}

func (w *writer) needs(in Input) {
	p := in.Plan
	if len(p.Needs) == 0 {
		return
	}
	w.blank()
	w.line("Needs you")
	launcherOf := map[string]string{}
	if in.State != nil {
		for _, f := range in.State.Folders {
			if f.Launcher != nil {
				launcherOf[f.Folder] = f.Launcher.Name
			}
		}
	}
	var logins []model.Need
	for _, n := range p.Needs {
		if n.Kind == "login" {
			logins = append(logins, n)
		}
	}
	cmd := func(n model.Need, full bool) string {
		c := n.Command
		if l := launcherOf[n.Folder]; l != "" {
			c = l + " auth login --claudeai"
		}
		if !full {
			c = strings.TrimSuffix(c, " --claudeai") + " …"
		}
		return c
	}
	switch len(logins) {
	case 0:
	case 1:
		w.line("  ? log in %s: %s", logins[0].Folder, cmd(logins[0], true))
	default:
		first, last := logins[0], logins[len(logins)-1]
		w.line("  ? log in %s … %s when you want them: %s", first.Folder, last.Folder, cmd(first, false))
	}
	for _, n := range p.Needs {
		if n.Kind != "login" {
			w.line("  ? %s: %s", n.Kind, n.Command)
		}
	}
}

func (w *writer) warnings(p model.Plan) {
	if len(p.Warnings) == 0 {
		return
	}
	w.blank()
	w.line("Notes")
	for _, s := range p.Warnings {
		w.line("  ! %s", s)
	}
}
