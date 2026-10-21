package state

import (
	"regexp"
	"strings"
	"unicode"
)

// DetectorVersion is stored in session_state.detector.
const DetectorVersion = "q2"

var (
	inlineCode   = regexp.MustCompile("`[^`\n]*`")
	paraSplit    = regexp.MustCompile(`\n[ \t]*\n`)
	optionLine   = regexp.MustCompile(`^\s*(\d{1,2}|[A-Ha-h])[.)]\s+\S`)
	optionBold   = regexp.MustCompile(`(?i)^\s*[-*]\s+\*\*(Option|Choice)\b`)
	urlToken     = regexp.MustCompile(`^https?://\S+$`)
	questionHead = []string{"should i", "shall i", "do you want", "would you like", "want me to",
		"which ", "can you confirm", "let me know if", "let me know which"}
	// q2: a list after a ":" lead-in is a choice only when the lead-in asks for one, so
	// "Three agents are running now:" and "Here is what I changed:" are not questions.
	choiceWords = regexp.MustCompile(`(?i)\b(which|choose|choice|choices|pick|prefer|select|option|options|decide|would you|do you want|want me to|should i|shall i|let me know|your call)\b`)
)

const maxQuestionRunes = 120

// ReadsAsQuestion applies detector q2 (states.md section 4) to a Stop's last message. It
// returns whether the message reads as a question and the question text (at most 120
// characters). An empty message is never a question.
func ReadsAsQuestion(msg string) (bool, string) {
	text := strings.TrimSpace(stripCode(strings.ReplaceAll(msg, "\r\n", "\n")))
	if text == "" {
		return false, ""
	}
	paras := paraSplit.Split(text, -1)
	p := strings.TrimSpace(paras[len(paras)-1])
	prev := ""
	if len(paras) > 1 {
		prev = strings.TrimSpace(paras[len(paras)-2])
	}
	rawP := p
	p = trimTrailing(p)
	if p == "" || (!strings.Contains(p, "\n") && len([]rune(p)) < 3) {
		return false, ""
	}

	// 4a
	if strings.HasSuffix(p, "?") || strings.HasSuffix(p, "？") {
		toks := strings.Fields(p)
		if !urlToken.MatchString(toks[len(toks)-1]) {
			lines := strings.Split(p, "\n")
			q := ""
			for i := len(lines) - 1; i >= 0; i-- {
				l := trimTrailing(strings.TrimSpace(lines[i]))
				if strings.HasSuffix(l, "?") || strings.HasSuffix(l, "？") {
					q = l
					break
				}
			}
			return true, cut(q)
		}
	}
	// 4b: option lines are looked for in the untrimmed paragraph
	lines := strings.Split(rawP, "\n")
	first, n := -1, 0
	for i, l := range lines {
		if optionLine.MatchString(l) || optionBold.MatchString(l) {
			if first < 0 {
				first = i
			}
			n++
		}
	}
	if n >= 2 {
		before := prev
		if first > 0 {
			before = strings.TrimSpace(strings.Join(lines[:first], "\n"))
		}
		if before != "" {
			b := trimTrailing(before)
			bl := strings.Split(b, "\n")
			lead := strings.TrimSpace(bl[len(bl)-1])
			if strings.HasSuffix(b, "?") || (strings.HasSuffix(b, ":") && choiceWords.MatchString(lead)) {
				return true, cut(lead)
			}
		}
	}
	// 4c: last sentence
	s := strings.TrimRight(p, ".! \t\n")
	if i := strings.LastIndexAny(s, ".!\n"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.ToLower(strings.TrimLeft(s, " \t*_>-#"))
	for _, h := range questionHead {
		if strings.HasPrefix(s, h) {
			pl := strings.Split(p, "\n")
			return true, cut(strings.TrimSpace(pl[len(pl)-1]))
		}
	}
	return false, ""
}

// stripCode removes fenced code blocks (from a line starting with three backticks to the
// closing such line, or to the end) and inline code spans.
func stripCode(s string) string {
	var out []string
	in := false
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " \t"), "```") {
			in = !in
			continue
		}
		if !in {
			out = append(out, l)
		}
	}
	return inlineCode.ReplaceAllString(strings.Join(out, "\n"), "")
}

func trimTrailing(s string) string {
	return strings.TrimRightFunc(s, func(r rune) bool {
		switch r {
		case '*', '_', ')', ']', '"', '\'', '”', '’', '`':
			return true
		}
		return unicode.IsSpace(r) || unicode.Is(unicode.So, r)
	})
}

func cut(s string) string {
	r := []rune(strings.TrimLeft(s, "*_> \t"))
	if len(r) > maxQuestionRunes {
		r = r[:maxQuestionRunes]
	}
	return string(r)
}
