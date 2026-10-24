package auth

import (
	"strings"
	"testing"
)

func TestTokenShape(t *testing.T) {
	for kind, prefix := range map[string]string{"machine": "wy_m_", "desktop": "wy_d_", "phone": "wy_p_"} {
		seen := map[string]bool{}
		for i := 0; i < 200; i++ {
			tok, err := NewToken(kind)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(tok, prefix) || len(tok) != 48 || !LooksLikeToken(tok) {
				t.Fatalf("%s token %q (%d chars)", kind, tok, len(tok))
			}
			if seen[tok] {
				t.Fatal("repeated token")
			}
			seen[tok] = true
		}
	}
	if _, err := NewToken("satellite"); err == nil {
		t.Error("unknown kind accepted")
	}
	for _, bad := range []string{"", "wy_m_short", "wy_m_" + strings.Repeat("-", 43), "xx_m_" + strings.Repeat("a", 43)} {
		if LooksLikeToken(bad) {
			t.Errorf("LooksLikeToken(%q)", bad)
		}
	}
	tok, _ := NewToken("phone")
	if len(Hash(tok)) != 32 || string(Hash(tok)) == tok {
		t.Error("hash")
	}
	if h := Hint(tok); !strings.HasPrefix(h, "wy_p_") || !strings.HasSuffix(h, tok[len(tok)-4:]) || len(h) > 15 {
		t.Errorf("hint %q", h)
	}
}

func TestScopesByKind(t *testing.T) {
	want := map[string]string{"machine": "write", "desktop": "read,write", "phone": "push,read"}
	for kind, w := range want {
		s, ok := ScopesFor(kind)
		if !ok {
			t.Fatalf("%s unknown", kind)
		}
		got := map[string]bool{}
		for _, x := range s {
			got[x] = true
		}
		for _, x := range strings.Split(w, ",") {
			if !got[x] {
				t.Errorf("%s lacks %s: %v", kind, x, s)
			}
		}
		if len(s) != len(strings.Split(w, ",")) {
			t.Errorf("%s scopes %v, want %s", kind, s, w)
		}
	}
}
