package warp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/mraj602/switchyard/internal/brand"
)

// Marker starts the first line of every tab config sy writes (T8). A file without it is
// never touched; a file with it whose hash differs from what sy wrote belongs to the person.
const Marker = "# sy-warp-tab v1 "

// Prefix and Suffix make the file name of a tab config: switchyard-<label>.toml.
const (
	Prefix = brand.Slug + "-"
	Suffix = ".toml"
)

// legacyPrefix is the prefix tab configs had before a rename ("" when there was none):
// those files are still read, opened and, when unchanged, moved away (docs/spec/rename.md).
func legacyPrefix() string {
	if !brand.Renamed() {
		return ""
	}
	return brand.OldSlug() + "-"
}

// stemLabel returns the label of a tab config stem (file name without .toml), with either
// prefix, and whether it carries the legacy one.
func stemLabel(stem string) (label string, legacy bool) {
	l, ok := strings.CutPrefix(stem, Prefix)
	if lp := legacyPrefix(); !ok && lp != "" {
		l, ok = strings.CutPrefix(stem, lp)
		legacy = ok
	}
	if !ok || !labelRE.MatchString(l) {
		return "", false
	}
	return l, legacy
}

var labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

// FileName is the tab config file name of label.
func FileName(label string) string { return Prefix + label + Suffix }

// TabPath is label's tab config inside folder, joined with the folder's own separator (a
// Windows folder keeps `\` even when a test pretends Windows on Unix).
func TabPath(folder, label string) string {
	if strings.Contains(folder, `\`) && !strings.Contains(folder, "/") {
		return strings.TrimRight(folder, `\`) + `\` + FileName(label)
	}
	if strings.Contains(folder, "/") && !strings.Contains(folder, `\`) {
		return path.Join(folder, FileName(label))
	}
	return filepath.Join(folder, FileName(label))
}

// Stem is the tab config name Warp knows it by (the file name without .toml).
func Stem(label string) string { return Prefix + label }

// LabelOf returns the label of a tab config file name or path ("" if it is not one).
func LabelOf(path string) string {
	l, _ := stemLabel(stemOf(path))
	return l
}

// stemOf is the file name of path without .toml ("" if it does not end in .toml).
func stemOf(path string) string {
	name := filepath.Base(strings.ReplaceAll(path, `\`, "/"))
	if !strings.HasSuffix(name, Suffix) {
		return ""
	}
	return strings.TrimSuffix(name, Suffix)
}

// IsLegacyTab reports whether path is a tab config named with the prefix from before a
// rename.
func IsLegacyTab(path string) bool {
	l, legacy := stemLabel(stemOf(path))
	return l != "" && legacy
}

// FileURI is the URI that opens the tab config file at path, whichever prefix it has.
func FileURI(channel, path string) string {
	return Scheme(channel) + "://tab_config/" + stemOf(path)
}

// IsTabPath reports whether path is a switchyard-<label>.toml inside a tab_configs folder.
func IsTabPath(path string) bool {
	p := strings.ReplaceAll(path, `\`, "/")
	dir := p[:max(strings.LastIndex(p, "/"), 0)]
	return LabelOf(p) != "" && strings.HasSuffix(dir, "/tab_configs")
}

// TabURI is the URI that opens label's tab config: warp://tab_config/switchyard-<label>
// (warppreview:// for Preview). Warp matches the file stem case-insensitively.
func TabURI(channel, label string) string {
	return Scheme(channel) + "://tab_config/" + Stem(label)
}

var tabURIRE = regexp.MustCompile(`^(?:warp|warppreview)://tab_config/([a-z0-9-]+)$`)

// ValidTabURI reports whether u is exactly a TabURI, or one for a tab config with the
// legacy prefix (the second fixed URI pattern T8 lets the opener open, after the focus
// allow-list of T7).
func ValidTabURI(u string) bool {
	m := tabURIRE.FindStringSubmatch(u)
	if len(u) > 128 || m == nil {
		return false
	}
	l, _ := stemLabel(m[1])
	return l != ""
}

// Spec describes one tab config.
type Spec struct {
	// Slot is the account's config folder in "~/.claude-2" form (marker only).
	Slot string
	// Label is the account label ([a-z0-9-], setup.md 3).
	Label string
	// Colour is one of the six names (Colours); "" leaves the line out.
	Colour string
	// Command is the account's launcher by absolute path (a .cmd on Windows).
	Command string
}

// Check reports why s cannot be rendered (nil when it can).
func Check(s Spec) error {
	if !labelRE.MatchString(s.Label) {
		return fmt.Errorf("invalid label %q", s.Label)
	}
	if s.Colour != "" && !ValidName(s.Colour) {
		return fmt.Errorf("invalid colour %q", s.Colour)
	}
	if s.Command == "" {
		return fmt.Errorf("%s has no launcher", s.Slot)
	}
	for _, f := range []string{s.Slot, s.Command} {
		for _, r := range f {
			if unicode.IsControl(r) {
				return fmt.Errorf("%q contains a control character", f)
			}
		}
	}
	if bad := unsafeInShell(s.Command); bad != "" {
		return fmt.Errorf("the launcher path %q has %s: Warp runs a tab config's commands in the pane's shell (bash, zsh, fish or PowerShell, which sy cannot know and which quote differently), so sy writes only paths that need no quoting; move the launcher to a plain path", s.Command, bad)
	}
	if strings.Contains(s.Slot, " label=") {
		return fmt.Errorf("folder %q cannot be written into the marker", s.Slot)
	}
	return nil
}

// shellSpecial are the characters some shell Warp may run a tab config's commands in
// (bash, zsh, fish, PowerShell, cmd) treats specially inside a word: quotes, expansion,
// globbing, separators, redirection, grouping, comments, PowerShell's `,` (an array) and
// cmd's `%` and `^`.
const shellSpecial = "'\"`$;&|<>()[]{}*?!#%^,"

// unsafeInShell names what in the command path would need shell quoting ("" when none):
// white space, a shell special character, or a backslash in a Unix path (a Windows path's
// `\` is its separator and plain to PowerShell and cmd).
func unsafeInShell(cmd string) string {
	for _, r := range cmd {
		switch {
		case unicode.IsSpace(r):
			return "white space"
		case strings.ContainsRune(shellSpecial, r):
			return fmt.Sprintf("a %q", r)
		case r == '\\' && strings.HasPrefix(cmd, "/"):
			return `a "\\"`
		}
	}
	return ""
}

// Render returns the tab config of s (Check it first; Render itself never fails):
//
//	# sy-warp-tab v1 slot=~/.claude-2 label=work (written by sy term warp on; sy term warp off removes it)
//	name = "Switchyard · work"
//	title = "work"
//	color = "yellow"
//
//	[[panes]]
//	id = "main"
//	type = "terminal"
//	commands = ["/home/u/.local/bin/claude-2"]
func Render(s Spec) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%sslot=%s label=%s (written by sy term warp on; sy term warp off removes it)\n", Marker, s.Slot, s.Label)
	fmt.Fprintf(&b, "name = %s\n", tomlString("Switchyard · "+s.Label))
	fmt.Fprintf(&b, "title = %s\n", tomlString(s.Label))
	if s.Colour != "" {
		fmt.Fprintf(&b, "color = %s\n", tomlString(s.Colour))
	}
	b.WriteString("\n[[panes]]\n")
	b.WriteString("id = \"main\"\n")
	b.WriteString("type = \"terminal\"\n")
	fmt.Fprintf(&b, "commands = [%s]\n", tomlString(s.Command))
	return b.Bytes()
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Info is what the marker line of a tab config says.
type Info struct {
	Slot  string
	Label string
}

var markerRE = regexp.MustCompile(`^# sy-warp-tab v1 slot=(.*) label=([a-z0-9][a-z0-9-]{0,23}) \(`)

// ParseMarker reads the marker of a tab config (first line only).
func ParseMarker(content []byte) (Info, bool) {
	line, _, _ := bytes.Cut(content, []byte("\n"))
	m := markerRE.FindSubmatch(bytes.TrimRight(line, "\r"))
	if m == nil {
		return Info{}, false
	}
	return Info{Slot: string(m[1]), Label: string(m[2])}, true
}

// Hash is the SHA-256 (hex) of a tab config's bytes, as the ledger records it.
func Hash(content []byte) string {
	s := sha256.Sum256(content)
	return hex.EncodeToString(s[:])
}

// Found is one switchyard-*.toml file in a tab_configs folder.
type Found struct {
	Path   string `json:"path"`
	Label  string `json:"label"`
	SHA256 string `json:"sha256"`
	// Marker: the file starts with sy's marker line. A file without it is never touched.
	Marker bool `json:"marker"`
	// Slot is the folder the marker names.
	Slot string `json:"slot,omitempty"`
}

// maxTab bounds what Scan reads of one file.
const maxTab = 1 << 20

// Scan lists the switchyard-*.toml files of folder, by name. A missing folder is no files.
func Scan(folder string) ([]Found, error) {
	if folder == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(folder)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Found
	for _, e := range ents {
		l := LabelOf(e.Name())
		if l == "" {
			continue
		}
		// Anything else under that name (a link, a folder, a huge file) is listed without a
		// marker: it is the person's and is never touched.
		f := Found{Path: filepath.Join(folder, e.Name()), Label: l}
		if fi, err := os.Lstat(f.Path); err == nil && fi.Mode().IsRegular() && fi.Size() <= maxTab {
			if b, err := os.ReadFile(f.Path); err == nil {
				f.SHA256 = Hash(b)
				if info, ok := ParseMarker(b); ok {
					f.Marker, f.Slot = true, info.Slot
				}
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
