package board

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTokenRoundTripMode0600(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadToken(dir, "https://hub.example"); err != ErrNoToken {
		t.Fatalf("empty state: %v", err)
	}
	if err := SaveToken(dir, Token{URL: "https://hub.example", Device: "dev_1", Token: "wy_d_secret"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(TokenFile(dir))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Join(dir, "board"))
		if di.Mode().Perm()&0o077 != 0 {
			t.Errorf("board folder mode %v is open to others", di.Mode().Perm())
		}
	}
	got, err := LoadToken(dir, "https://HUB.example/")
	if err != nil || got.Token != "wy_d_secret" || got.Device != "dev_1" || got.V != 1 {
		t.Fatalf("load: %+v %v", got, err)
	}
}

func TestTokenDroppedWhenURLDiffers(t *testing.T) {
	dir := t.TempDir()
	if err := SaveToken(dir, Token{URL: "https://a.example", Device: "d", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(dir, "https://b.example"); err != ErrNoToken {
		t.Fatalf("other URL: %v", err)
	}
	if _, err := os.Stat(TokenFile(dir)); !os.IsNotExist(err) {
		t.Errorf("the token for another hub was kept: %v", err)
	}
}

func TestTokenCorruptFileIsDropped(t *testing.T) {
	dir := t.TempDir()
	if err := SaveToken(dir, Token{URL: "https://a.example", Device: "d", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TokenFile(dir), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(dir, "https://a.example"); err != ErrNoToken {
		t.Fatalf("corrupt: %v", err)
	}
}

func TestTokenNeverFormatsItsSecret(t *testing.T) {
	tok := Token{URL: "https://a.example", Device: "dev_1", Token: "wy_d_topsecret"}
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		if s := fmt.Sprintf(f, tok); strings.Contains(s, "topsecret") {
			t.Errorf("%s leaks the token: %s", f, s)
		}
	}
}

func TestRemoveToken(t *testing.T) {
	dir := t.TempDir()
	if ok, err := RemoveToken(dir); ok || err != nil {
		t.Fatalf("nothing to remove: %v %v", ok, err)
	}
	_ = SaveToken(dir, Token{URL: "https://a.example", Device: "d", Token: "t"})
	if ok, err := RemoveToken(dir); !ok || err != nil {
		t.Fatalf("remove: %v %v", ok, err)
	}
}

func TestTokenName(t *testing.T) {
	for in, want := range map[string]string{
		"laptop":                "terminal-laptop",
		"my box.local":          "terminal-my-box.local",
		"büro-pc":               "terminal-b-ro-pc",
		strings.Repeat("x", 80): "terminal-" + strings.Repeat("x", 31),
	} {
		if got := TokenName(in); got != want {
			t.Errorf("TokenName(%q) = %q, want %q", in, got, want)
		}
	}
}
