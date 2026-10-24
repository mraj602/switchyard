package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/board"
)

func runBoardConnectCmd(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	old := desktopStdin
	desktopStdin = strings.NewReader(stdin)
	defer func() { desktopStdin = old }()
	var out, errb bytes.Buffer
	code := dispatch(append([]string{"board"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestBoardConnectThroughLocalHub(t *testing.T) {
	d := deskSandbox(t)
	noRealService(t, &fakeService{})
	hub := startLocalHub(t, d)
	connectSandbox(t, d, hub.URL)

	code, out, errb := runBoardConnectCmd(t, "", "connect", "--json")
	if code != 0 {
		t.Fatalf("connect: exit %d: %s", code, errb)
	}
	var got struct{ Device, Hub string }
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Hub != hub.URL || !strings.HasPrefix(got.Device, "dev_") {
		t.Fatalf("output %q: %v", out, err)
	}
	tok, err := board.LoadToken(d.State.Path, hub.URL)
	if err != nil || !strings.HasPrefix(tok.Token, "wy_d_") || tok.Device != got.Device {
		t.Fatalf("token file: %+v %v", tok, err)
	}
	if strings.Contains(out+errb, tok.Token) {
		t.Error("the token was printed")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(board.TokenFile(d.State.Path))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("token.json mode %v", fi.Mode().Perm())
		}
	}
	// It is a read token the hub accepts.
	if scopes := strings.Join(meScopes(t, hub.URL, tok.Token), ","); !strings.Contains(scopes, "read") {
		t.Errorf("scopes %q", scopes)
	}

	// The human form never shows it either.
	code, out, errb = runBoardConnectCmd(t, "", "connect")
	if code != 0 || strings.Contains(out+errb, "wy_d_") || !strings.Contains(out, "board token saved") {
		t.Errorf("plain connect: %d %q %q", code, out, errb)
	}

	// disconnect removes the file and says how to revoke the device.
	prev, _ := board.PeekToken(d.State.Path)
	code, out, _ = runBoardConnectCmd(t, "", "disconnect")
	if code != 0 || !strings.Contains(out, "wy hub token revoke "+prev.Device) || strings.Contains(out, prev.Token) {
		t.Errorf("disconnect: %d %q", code, out)
	}
	if _, err := os.Stat(board.TokenFile(d.State.Path)); !os.IsNotExist(err) {
		t.Errorf("token.json still there: %v", err)
	}
	code, out, _ = runBoardConnectCmd(t, "", "disconnect")
	if code != 0 || !strings.Contains(out, "no board token") {
		t.Errorf("second disconnect: %d %q", code, out)
	}
}

func TestBoardConnectWithCode(t *testing.T) {
	d := deskSandbox(t)
	noRealService(t, &fakeService{})
	hub := startLocalHub(t, d)
	connectSandbox(t, d, hub.URL)

	var o, e bytes.Buffer
	if c := dispatch([]string{"hub", "pair", "--kind", "desktop", "--url", hub.URL, "--json"}, &o, &e); c != 0 {
		t.Fatalf("hub pair: %d %s", c, e.String())
	}
	var pair map[string]any
	if err := json.Unmarshal(o.Bytes(), &pair); err != nil {
		t.Fatal(err)
	}
	code, out, errb := runBoardConnectCmd(t, pair["code"].(string)+"\n", "connect", "--code-stdin", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	tok, err := board.LoadToken(d.State.Path, hub.URL)
	if err != nil || strings.Contains(out+errb, tok.Token) {
		t.Fatalf("token %v err %v; leaked=%v", tok, err, strings.Contains(out+errb, tok.Token))
	}
	// A used code is refused (exit 3) and the existing token stays.
	code, _, _ = runBoardConnectCmd(t, pair["code"].(string)+"\n", "connect", "--code-stdin")
	if code != 3 {
		t.Errorf("used code: exit %d, want 3", code)
	}
	if again, err := board.LoadToken(d.State.Path, hub.URL); err != nil || again.Token != tok.Token {
		t.Error("a refused connect replaced the token")
	}
}

func TestBoardConnectNotPossible(t *testing.T) {
	d := deskSandbox(t)
	noRealService(t, &fakeService{})
	// Not connected to any hub: exit 4.
	if code, _, _ := runBoardConnectCmd(t, "", "connect"); code != 4 {
		t.Errorf("no hub: exit %d, want 4", code)
	}
	// Connected, but no admin channel here and no code: exit 4 too.
	connectSandbox(t, d, "http://127.0.0.1:9")
	if code, _, errb := runBoardConnectCmd(t, "", "connect"); code != 4 {
		t.Errorf("no admin: exit %d, want 4 (%s)", code, errb)
	}
	// With --code-stdin and nothing listening: exit 5.
	if code, _, _ := runBoardConnectCmd(t, "482-913\n", "connect", "--code-stdin"); code != 5 {
		t.Errorf("unreachable: exit %d, want 5", code)
	}
	if _, err := os.Stat(board.TokenFile(d.State.Path)); !os.IsNotExist(err) {
		t.Error("a token was written by a failed connect")
	}
}

// Through a fake ssh admin channel: the token arrives on ssh's stdout and lands only in
// token.json. A fake ssh records its argv and its environment; neither (nor wy's own output)
// ever holds the secret.
func TestBoardConnectTokenNeverInArgvEnvOrOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh is a shell script")
	}
	d := deskSandbox(t)
	noRealService(t, &fakeService{})
	connectSandbox(t, d, "https://hub.example.ts.net", "machines.hub_admin", "ssh hub-host wy")
	bin := t.TempDir()
	rec := filepath.Join(bin, "record.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + rec + "\nenv >> " + rec + "\ncase \"$*\" in\n" +
		"  *token*create*) echo '{\"device\":\"dev_77\",\"token\":\"wy_d_BOARDSECRETBOARDSECRETBOARDSECRETBOARDSECRET1\",\"scopes\":[\"read\"]}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	code, out, errb := runBoardConnectCmd(t, "", "connect", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if strings.Contains(out+errb, "BOARDSECRET") {
		t.Fatalf("the token was printed: %q %q", out, errb)
	}
	var got struct{ Device, Hub string }
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Device != "dev_77" || got.Hub != "https://hub.example.ts.net" {
		t.Fatalf("output %q: %v", out, err)
	}
	seen, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(seen), "BOARDSECRET") {
		t.Fatalf("the token is in ssh's argv or environment:\n%s", seen)
	}
	if !strings.Contains(string(seen), "terminal-") {
		t.Errorf("the device name is not terminal-<machine>: %s", seen)
	}
	tok, err := board.LoadToken(d.State.Path, "https://hub.example.ts.net")
	if err != nil || !strings.HasPrefix(tok.Token, "wy_d_BOARDSECRET") {
		t.Fatalf("token file: %v %v", tok, err)
	}
}

func TestBoardConnectUsage(t *testing.T) {
	deskSandbox(t)
	checkUsage(t,
		[]string{"board", "connect", "extra"},
		[]string{"board", "connect", "--bogus"},
		[]string{"board", "disconnect", "extra"},
	)
}

// The token is bound to the URL wy dialled (hub.url in the config), not to whatever
// address the hub's claim reply names for itself.
func TestBoardConnectCodeBindsToConfiguredURL(t *testing.T) {
	d := deskSandbox(t)
	noRealService(t, &fakeService{})
	hub := startLocalHub(t, d)
	alias := strings.Replace(hub.URL, "127.0.0.1", "localhost", 1)
	if alias == hub.URL {
		t.Skipf("hub URL %s is not on 127.0.0.1", hub.URL)
	}
	connectSandbox(t, d, alias)

	var o, e bytes.Buffer
	if c := dispatch([]string{"hub", "pair", "--kind", "desktop", "--url", hub.URL, "--json"}, &o, &e); c != 0 {
		t.Fatalf("hub pair: %d %s", c, e.String())
	}
	var pair map[string]any
	if err := json.Unmarshal(o.Bytes(), &pair); err != nil {
		t.Fatal(err)
	}
	code, out, errb := runBoardConnectCmd(t, pair["code"].(string)+"\n", "connect", "--code-stdin", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if !strings.Contains(out, alias) {
		t.Errorf("output %q does not name the dialled URL %s", out, alias)
	}
	if _, err := board.LoadToken(d.State.Path, alias); err != nil {
		t.Fatalf("token not bound to the configured URL %s: %v", alias, err)
	}
	if _, err := os.Stat(board.TokenFile(d.State.Path)); err != nil {
		t.Errorf("token file gone: %v", err)
	}
}
