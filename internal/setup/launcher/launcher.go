// Package launcher renders the `claude-N` launchers wy writes for folders that have none
// (setup.md 8.3), parses their marker, and hashes them so "still ours" can be told later.
//
// A Unix launcher is a /bin/sh script, a Windows one a .cmd file. The second line of both is
// the marker `# wy-launcher v1 slot=<folder> label=<label>` (`rem` instead of `#` in a .cmd).
// The plugin root is not set here: it comes from settings (5.2).
//
// Depends on: nothing.
package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
)

// Marker is the text that identifies a launcher as ours (second line of the file).
// LegacyMarker is the one launchers written before the rename carry: still recognised, never
// written (legacy compat).
const (
	Marker       = "wy-launcher v1"
	LegacyMarker = "sy-launcher v1" // rename:keep: legacy compat
)

// Spec describes one launcher.
type Spec struct {
	// Slot is the config folder in "~/.claude-2" form (or an absolute path outside home).
	Slot string
	// Label is the account label; empty is omitted from the marker.
	Label string
}

// FileName is the file name of launcher name ("claude-2") for goos: ".cmd" on Windows.
func FileName(goos, name string) string {
	if goos == "windows" {
		return name + ".cmd"
	}
	return name
}

// Check reports why s cannot be rendered safely for goos (nil when it can). Render itself
// never fails; callers that take the label or folder from outside call Check first.
//
//   - The label, if set, must be a valid label (`^[a-z0-9][a-z0-9-]{0,23}$`).
//   - The folder must not contain control characters (a newline would end a comment line).
//   - Windows: the .cmd file is written as UTF-8 without a BOM and cmd.exe reads it in the
//     OEM code page, so a non-ASCII folder would be mangled; `"` cannot be quoted inside
//     `set "..."` and `^` is cmd's escape character (a trailing one continues a `rem` line).
//     Such folders get no launcher (`chcp 65001` would change the person's console for good).
func Check(goos string, s Spec) error {
	if s.Label != "" && !labelRE.MatchString(s.Label) {
		return fmt.Errorf("invalid label %q", s.Label)
	}
	for _, r := range s.Slot {
		if unicode.IsControl(r) {
			return fmt.Errorf("folder %q contains a control character", s.Slot)
		}
		if goos == "windows" {
			if r > unicode.MaxASCII {
				return fmt.Errorf("folder %q is not ASCII and cannot be written into a .cmd launcher", s.Slot)
			}
			if r == '"' || r == '^' {
				return fmt.Errorf("folder %q contains %q, which cannot be written safely into a .cmd launcher", s.Slot, r)
			}
		}
	}
	return nil
}

var labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

// Render returns the launcher file content for goos. Every interpolated value is made safe
// for its context even if Check was skipped: comment lines are kept on one line, sh values
// are quoted, and `%` is doubled in a .cmd file.
func Render(goos string, s Spec) []byte {
	if goos == "windows" {
		return renderWindows(s)
	}
	return renderUnix(s)
}

func markerLine(s Spec) string {
	m := Marker + " slot=" + oneLine(s.Slot)
	if s.Label != "" {
		m += " label=" + oneLine(s.Label)
	}
	return m
}

func renderUnix(s Spec) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# " + markerLine(s) + "\n")
	b.WriteString("# Claude Code as " + oneLine(s.Slot) + ". Written by wy setup; wy uninstall removes it.\n")
	b.WriteString("CLAUDE_CONFIG_DIR=" + shValue(s.Slot) + "\n")
	b.WriteString("export CLAUDE_CONFIG_DIR\n")
	b.WriteString("exec claude \"$@\"\n")
	return []byte(b.String())
}

func renderWindows(s Spec) []byte {
	dir := cmdValue(s.Slot)
	lines := []string{
		"@echo off",
		"rem " + cmdText(markerLine(s)),
		"rem Claude Code as " + cmdText(oneLine(s.Slot)) + ". Written by wy setup; wy uninstall removes it.",
		`@set "CLAUDE_CONFIG_DIR=` + dir + `"`,
		"@claude %*",
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

// oneLine replaces every control character (CR, LF, tab, ...) with a space, so an
// interpolated value can never end a comment line and start a command.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// cmdText makes literal text safe in a .cmd file: `%` is doubled (a single one starts a
// variable or batch-parameter expansion, even in a `rem` line) and `^` is doubled (a
// trailing one continues the line).
func cmdText(s string) string {
	return strings.NewReplacer("%", "%%", "^", "^^").Replace(s)
}

// shValue quotes the folder for sh: "~/x" becomes "$HOME/x" in double quotes, anything else
// single-quoted.
func shValue(slot string) string {
	if rest, ok := strings.CutPrefix(slot, "~/"); ok {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
		return `"$HOME/` + r.Replace(rest) + `"`
	}
	return "'" + strings.ReplaceAll(slot, "'", `'\''`) + "'"
}

// cmdValue is the folder inside `set "CLAUDE_CONFIG_DIR=..."`: quoted, so & | < > ^ are
// literal there; `%` is doubled (percent expansion happens even inside quotes) and control
// characters become spaces. `"` is refused by Check.
func cmdValue(slot string) string {
	esc := func(p string) string { return strings.ReplaceAll(oneLine(strings.ReplaceAll(p, "/", `\`)), "%", "%%") }
	if rest, ok := strings.CutPrefix(slot, "~/"); ok {
		return `%USERPROFILE%\` + esc(rest)
	}
	return esc(slot)
}

// Info is the parsed marker of a launcher file.
type Info struct {
	Slot  string
	Label string
}

// ParseMarker reads the marker from the second line of content. ok is false when the second
// line is not a wy-launcher v1 marker (or the sy-launcher v1 one of a launcher written before
// the rename: legacy compat).
func ParseMarker(content []byte) (Info, bool) {
	lines := strings.SplitN(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n", 3)
	if len(lines) < 2 {
		return Info{}, false
	}
	l := strings.TrimSpace(lines[1])
	switch {
	case strings.HasPrefix(l, "# "):
		l = l[2:]
	case strings.HasPrefix(l, "rem "):
		l = strings.NewReplacer("%%", "%", "^^", "^").Replace(l[4:])
	default:
		return Info{}, false
	}
	rest, ok := strings.CutPrefix(l, Marker)
	if !ok {
		rest, ok = strings.CutPrefix(l, LegacyMarker)
	}
	if !ok || (rest != "" && rest[0] != ' ') {
		return Info{}, false
	}
	var info Info
	for _, f := range strings.Fields(rest) {
		if v, ok := strings.CutPrefix(f, "slot="); ok {
			info.Slot = v
		} else if v, ok := strings.CutPrefix(f, "label="); ok {
			info.Label = v
		}
	}
	return info, true
}

// Hash is the SHA-256 (hex) recorded in the ledger for "still ours".
func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// StillOurs reports whether content still has the hash wy recorded: a launcher whose hash
// differs belongs to the person now (uninstall and undo leave it).
func StillOurs(content []byte, recordedHash string) bool {
	return recordedHash != "" && Hash(content) == recordedHash
}

// Name is the launcher name of a folder given as "~/.claude-2": "claude-2". The default
// folder "~/.claude" is "claude" (never written by wy).
func Name(slot string) string {
	return strings.TrimPrefix(path.Base(strings.ReplaceAll(slot, `\`, "/")), ".")
}
