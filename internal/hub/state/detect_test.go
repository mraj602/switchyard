package state

import (
	"strings"
	"testing"
)

func TestReadsAsQuestion(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
		q    string // expected question text; "" = don't check
	}{
		// real fixture
		{"fixture 03", "Which colour do you prefer: red, green or blue?", true, "Which colour do you prefer: red, green or blue?"},
		{"fixture DONE", "DONE", false, ""},
		{"fixture truncated", "I didn't run it. Writing ...[truncated]", false, ""},
		// 4a
		{"plain question", "I made the change. Does that look right?", true, ""},
		{"full width", "これでいいですか？", true, ""},
		{"question in bold", "**Should I proceed?**", true, "Should I proceed?"},
		{"question in parens", "Ship it (ok?)", true, ""},
		{"question then emoji", "Want this merged? 🙂", true, ""},
		{"quoted question", `He asked "is it ready?"`, true, ""},
		{"last paragraph only", "Is this fine?\n\nI updated the file.", false, ""},
		{"earlier paragraph question ignored", "What now?\n\nAll tests pass.", false, ""},
		{"rhetorical mid-paragraph", "Why does this matter? Because the cache is stale.", false, ""},
		{"rhetorical at end counts (known limit)", "Isn't that nice?", true, ""},
		{"multi-line paragraph picks ? line", "Details below.\nWhich one do you want?", true, "Which one do you want?"},
		// code
		{"question mark in fenced block", "Here is the regex:\n\n```\n^a?b?$\n```", false, ""},
		{"question mark in unclosed fence", "Try this:\n```js\nx = a ? b : c?", false, ""},
		{"question mark in inline code", "Use the optional chain `a?.b?`", false, ""},
		{"question after code block", "```\nfoo\n```\n\nShould I run it?", true, ""},
		// URLs
		{"url with query at end", "See https://example.com/search?", false, ""},
		{"url query then question", "See https://example.com/a?b=1 for details. Right?", true, ""},
		// too short
		{"lone question mark", "?", false, ""},
		{"empty", "", false, ""},
		{"whitespace", " \n\n ", false, ""},
		// 4b
		{"numbered options after question", "Which approach?\n1. Rewrite it\n2. Patch it", true, "Which approach?"},
		{"numbered options after colon in previous paragraph", "You can pick one of these:\n\n1) Rewrite\n2) Patch", true, "You can pick one of these:"},
		{"lettered options", "Pick one:\nA. fast\nB. safe", true, ""},
		{"bold option bullets", "Here are the choices:\n- **Option 1**: fast\n- **Option 2**: safe", true, ""},
		{"one option is not enough", "Next step:\n1. Deploy", false, ""},
		{"a list after a plain colon lead-in is a report, not a choice", "Three agents are running now:\n1. Fixing limits.\n2. Fixing the tap.\n3. Research.", false, ""},
		{"what I changed, numbered", "Here is what I changed:\n\n1. The parser\n2. The tests", false, ""},
		{"a colon lead-in that asks for a pick", "Which one should I build:\n1. Limits\n2. Sessions", true, "Which one should I build:"},
		{"options without lead-in", "Summary of changes\n1. Added x\n2. Fixed y", false, ""},
		{"options after plain statement", "I did the following.\n\n1. Added x\n2. Fixed y", false, ""},
		// 4c
		{"should i no mark", "I can also refactor the module. Should I go ahead", true, ""},
		{"do you want", "Done. Do you want me to commit.", true, ""},
		{"would you like", "Would you like tests too.", true, ""},
		{"want me to", "Tests pass! Want me to open a PR!", true, ""},
		{"which", "Which database should I use", true, ""},
		{"let me know if", "I fixed it. Let me know if it works.", true, ""},
		{"let me know which", "Let me know which you prefer.", true, ""},
		{"can you confirm", "Can you confirm the path.", true, ""},
		{"bold lead in", "**Shall I continue**", true, ""},
		{"mid-sentence 'which' is not enough", "I fixed the test which failed.", false, ""},
		{"plain statement", "All tests pass and the build is green.", false, ""},
		{"statement with trailing code", "Updated the config. Run `make test`.", false, ""},
		{"long question is cut", strings.Repeat("a", 200) + "?", true, strings.Repeat("a", 120)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, q := ReadsAsQuestion(tc.msg)
			if got != tc.want {
				t.Fatalf("ReadsAsQuestion(%q) = %v, want %v", tc.msg, got, tc.want)
			}
			if got && tc.q != "" && q != tc.q {
				t.Errorf("question text = %q, want %q", q, tc.q)
			}
			if !got && q != "" {
				t.Errorf("question text %q for a non-question", q)
			}
		})
	}
}
