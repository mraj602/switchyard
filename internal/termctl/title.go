package termctl

import (
	"encoding/json"
	"strings"
	"unicode"
)

// State words of a tab title (docs/spec/terminal.md T6).
const (
	WordWorking    = "working"
	WordPermission = "permission"
	WordAsked      = "asked"
	WordDone       = "done"
	WordFailed     = "failed"
	WordLimit      = "limit"
	WordEnded      = "ended"
)

// MaxTitleRunes is the longest title, ellipsis included.
const MaxTitleRunes = 60

// SafeText makes s safe to put in an OSC 2 payload and on one line: controls (C0, DEL, C1
// including U+009D and U+009C), ESC, BEL, bidi overrides and other format characters are
// dropped, runs of white space become one space, and a `;` (an OSC parameter separator)
// becomes `,`.
func SafeText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == ';':
			r = ','
		case unicode.IsSpace(r) || r == ' ' || r == ' ':
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r),
			r == unicode.ReplacementChar, !unicode.IsPrint(r):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Title is `<label> · <project> · <word>`, each part through SafeText, cut to
// MaxTitleRunes with an ellipsis. It is "" when the label or the word is empty. An empty
// project leaves its part out.
func Title(label, project, word string) string {
	label, project, word = SafeText(label), SafeText(project), SafeText(word)
	if label == "" || word == "" {
		return ""
	}
	parts := []string{label}
	if project != "" {
		parts = append(parts, project)
	}
	parts = append(parts, word)
	t := strings.Join(parts, " · ")
	if r := []rune(t); len(r) > MaxTitleRunes {
		t = strings.TrimRight(string(r[:MaxTitleRunes-1]), " ") + "…"
	}
	return t
}

// TerminalSequence is the hook output that sets a tab title:
// `{"terminalSequence":"\u001b]2;<title>\u0007"}` (one JSON object, no newline). The title
// goes through SafeText again, so nothing the caller passes can end the OSC early or start
// another sequence. It returns nil for an empty title.
func TerminalSequence(title string) []byte {
	title = SafeText(title)
	if title == "" {
		return nil
	}
	b, err := json.Marshal(struct {
		Seq string `json:"terminalSequence"`
	}{"\x1b]2;" + title + "\x07"})
	if err != nil {
		return nil
	}
	return b
}

// ProjectOf is the last folder of cwd, for either separator style.
func ProjectOf(cwd string) string {
	cwd = strings.TrimRight(cwd, `/\`)
	if i := strings.LastIndexAny(cwd, `/\`); i >= 0 {
		cwd = cwd[i+1:]
	}
	return cwd
}
