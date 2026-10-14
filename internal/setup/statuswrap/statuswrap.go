// Package statuswrap wraps and unwraps the Claude Code status line command (setup.md 9.1).
//
// A status line command C that is not ours becomes `S statusline --wrap 'C'` (POSIX single
// quotes; a quote inside C becomes '\”). A command that is already ours is never wrapped
// again: only the path to sy is updated.
//
// Phase 3, WP-A. Depends on: nothing.
package statuswrap

import (
	"errors"
	"strings"
)

// Wrap returns the command to store in settings for status line command cmd and the
// absolute path syPath of sy. An empty cmd gives the bare `S statusline`. A cmd that is
// already ours is returned with only the path to sy replaced (idempotent, never nested).
func Wrap(cmd, syPath string) string {
	if words, ok := parseOurs(cmd); ok {
		if len(words) == 4 {
			return build(syPath, &words[3])
		}
		return build(syPath, nil)
	}
	if strings.TrimSpace(cmd) == "" {
		return build(syPath, nil)
	}
	return build(syPath, &cmd)
}

// Unwrap returns the original command of a wrapped status line (`S statusline --wrap 'C'`).
// ok is false when cmd is not ours or has no --wrap (the caller then uses the ledger).
func Unwrap(cmd string) (orig string, ok bool) {
	words, ours := parseOurs(cmd)
	if !ours || len(words) != 4 {
		return "", false
	}
	return words[3], true
}

// IsOurs reports whether cmd matches `<path>/sy[.exe] statusline [--wrap <one shell word>]`.
func IsOurs(cmd string) bool {
	_, ok := parseOurs(cmd)
	return ok
}

func build(syPath string, wrapped *string) string {
	s := quoteIfNeeded(syPath) + " statusline"
	if wrapped != nil {
		s += " --wrap " + Quote(*wrapped)
	}
	return s
}

func parseOurs(cmd string) ([]string, bool) {
	cmd = strings.TrimSpace(cmd)
	if cmd != "" && (cmd[0] == '\'' || cmd[0] == '"') {
		words, err := Words(cmd)
		if err != nil {
			return nil, false
		}
		return checkOurs(words)
	}
	// An unquoted path is taken raw, so a Windows path with backslashes still matches.
	path, rest, _ := strings.Cut(cmd, " ")
	words, err := Words(rest)
	if err != nil {
		return nil, false
	}
	return checkOurs(append([]string{path}, words...))
}

func checkOurs(words []string) ([]string, bool) {
	if len(words) != 2 && len(words) != 4 {
		return nil, false
	}
	base := words[0]
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if base != "sy" && base != "sy.exe" {
		return nil, false
	}
	if words[1] != "statusline" {
		return nil, false
	}
	if len(words) == 4 && words[2] != "--wrap" {
		return nil, false
	}
	return words, true
}

// Quote returns s as one POSIX shell word in single quotes.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quoteIfNeeded(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("/._-+:@%,=~", r))
	}) < 0 {
		return s
	}
	return Quote(s)
}

// ErrUnterminated is returned by Words for an unterminated quote or a trailing backslash.
var ErrUnterminated = errors.New("statuswrap: unterminated quote")

// Words splits s into shell words by POSIX rules, without any expansion: whitespace
// separates words; single quotes keep everything; double quotes keep everything except that
// a backslash escapes `$`, backquote, `"`, `\` and newline; outside quotes a backslash
// escapes the next character (backslash-newline disappears). `$` is always literal.
func Words(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	have := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		case c == '\'':
			have = true
			i++
			for ; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
			if i >= len(rs) {
				return nil, ErrUnterminated
			}
		case c == '"':
			have = true
			i++
			for ; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) && strings.ContainsRune("$`\"\\\n", rs[i+1]) {
					i++
					if rs[i] == '\n' {
						continue
					}
				}
				cur.WriteRune(rs[i])
			}
			if i >= len(rs) {
				return nil, ErrUnterminated
			}
		case c == '\\':
			if i+1 >= len(rs) {
				return nil, ErrUnterminated
			}
			i++
			have = true
			if rs[i] != '\n' {
				cur.WriteRune(rs[i])
			}
		default:
			have = true
			cur.WriteRune(c)
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out, nil
}
