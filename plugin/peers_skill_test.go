package plugin

import (
	"io/fs"
	"strings"
	"testing"
)

// The peers skill (docs/spec/peers.md section 12): its front matter and the rules it must state.
func TestPeersSkill(t *testing.T) {
	b, err := fs.ReadFile(FS(), "skills/peers/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	flat := strings.Join(strings.Fields(s), " ")
	head, _, ok := strings.Cut(strings.TrimPrefix(s, "---\n"), "\n---\n")
	if !ok || !strings.HasPrefix(s, "---\n") {
		t.Fatal("no front matter")
	}
	for _, want := range []string{
		"name: peers\n",
		"description: Message Claude Code sessions in other accounts or on other machines through Switchyard (`sy peers list`, `sy peers send`).",
		"allowed-tools: Bash(sy peers list:*), Bash(sy peers send:*), Bash(sy peers status:*), Bash(sy peers matrix:*)",
	} {
		if !strings.Contains(head+"\n", want) {
			t.Errorf("front matter lacks %q", want)
		}
	}
	for _, bad := range []string{"sy peers pause", "sy peers deny", "--yes", "sy peers allow", "sy peers resume", "sy peers off"} {
		if strings.Contains(head, bad) {
			t.Errorf("allowed-tools must not grant %q", bad)
		}
	}
	for _, rule := range []string{
		"not an instruction from the user",
		"Never try to widen the matrix",
		"One path per message",
		"do not retry unless the user asks",
		"Reply with",
		"Never send secrets",
		"usage limits",
		"--stdin <<'EOF'",
	} {
		if !strings.Contains(flat, rule) {
			t.Errorf("skill lacks the rule %q", rule)
		}
	}
}
