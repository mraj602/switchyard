package sshx

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// wordRe is the shape of a value that may be a remote command word (machines.md 4.3, plus
// `~`, which is literal inside single quotes in every shell, so folders like ~/.claude-2 pass).
// Anything else (a folder with a space, an odd label) goes on stdin as JSON instead.
var wordRe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,~-]+$`)

// CheckWord reports whether v may travel as a remote command word.
func CheckWord(v string) error {
	if !wordRe.MatchString(v) {
		return fmt.Errorf("%q cannot be a remote command word (only letters, digits and _./:@%%+=,~-); pass it on stdin", v)
	}
	return nil
}

// Quote single-quotes one word for the remote login shell. Words never contain a single
// quote (CheckWord, LintScript), so the quoting is plain: bash, dash, zsh, fish and the csh
// family all read 'word' as the literal word.
func Quote(w string) string { return "'" + w + "'" }

// QuoteAll quotes every word and joins them with spaces: the remote command line that ssh
// hands to the login shell.
func QuoteAll(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = Quote(w)
	}
	return strings.Join(q, " ")
}

// LintScript checks a fixed `sh -c` script: one line, no single quote (it is single-quoted),
// no `!` (csh history), no backquote and no backslash (fish reads `\\` and `\'` inside single
// quotes). A script that fails this is a programming error.
func LintScript(s string) error {
	if s == "" {
		return errors.New("sshx: empty script")
	}
	if i := strings.IndexAny(s, "'!`\\\n\r\x00"); i >= 0 {
		return fmt.Errorf("sshx: script has a forbidden character %q at %d", s[i], i)
	}
	return nil
}

// Script is the remote words of `sh -c '<script>' sy-remote <args...>`: a fixed POSIX
// script run by sh whatever the login shell is, with plain-word arguments ($1, $2, ...).
func Script(script string, args ...string) ([]string, error) {
	if err := LintScript(script); err != nil {
		return nil, err
	}
	for _, a := range args {
		if err := CheckWord(a); err != nil {
			return nil, err
		}
	}
	return append([]string{"sh", "-c", script, "sy-remote"}, args...), nil
}

// checkRemote validates the remote words of a call: each is either a plain word or fixed
// script text that passes LintScript (the third word of an `sh -c` call).
func checkRemote(words []string) error {
	for i, w := range words {
		if i == 2 && len(words) >= 3 && words[0] == "sh" && words[1] == "-c" {
			if err := LintScript(w); err != nil {
				return err
			}
			continue
		}
		if err := CheckWord(w); err != nil {
			return err
		}
	}
	return nil
}
