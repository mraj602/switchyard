package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// pairEnv points the state folder (the pairing pepper) at a temp dir and makes an empty hub
// database, returning its path.
func pairEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SY_HOME", filepath.Join(root, "sy"))
	for _, v := range []string{"SY_CONFIG_DIR", "SY_STATE_DIR", "SY_DATA_DIR"} {
		t.Setenv(v, "")
	}
	db := filepath.Join(root, "hub.db")
	st, err := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return db
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestHubPairJSONShape(t *testing.T) {
	db := pairEnv(t)
	var out, errb bytes.Buffer
	err := Pair([]string{"--db", db, "--kind", "phone", "--name", "Anna", "--url", "https://hub.example.ts.net", "--json"}, &out, &errb)
	if err != nil {
		t.Fatal(err, errb.String())
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("--json without --wait prints one line: %q", out.String())
	}
	var c struct {
		ID, Kind, Code, Secret, URL string
		ExpiresAt                   string `json:"expires_at"`
		Name                        string
		Command                     *string
		QR                          struct {
			Size int
			Rows []string
		}
	}
	if err := json.Unmarshal(out.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	digits := strings.Replace(c.Code, "-", "", 1)
	if !strings.HasPrefix(c.ID, "pc_") || c.Kind != "phone" || c.Name != "Anna" || len(c.Code) != 7 || len(c.Secret) != 22 || c.Command != nil ||
		!strings.HasPrefix(c.URL, "https://hub.example.ts.net/#/pair?s="+c.Secret+"&c="+digits+"&h=") || !strings.Contains(c.URL, "&k=") {
		t.Errorf("shape %+v", c)
	}
	exp, err := time.Parse(time.RFC3339Nano, c.ExpiresAt)
	if err != nil || time.Until(exp) < 9*time.Minute || time.Until(exp) > 10*time.Minute+time.Second {
		t.Errorf("expires_at %q (%v)", c.ExpiresAt, err)
	}
	want, _ := pair.NewMatrix(c.URL)
	if c.QR.Size != want.Size || strings.Join(c.QR.Rows, "") != strings.Join(want.Rows, "") {
		t.Error("qr is not the matrix of url")
	}
	// The code is live in the database and claimable with the printed digits.
	st, _ := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	defer st.Close()
	if r, err := pair.NewService(st, "").Claim(context.Background(), "1.1.1.1", pair.ClaimRequest{Code: c.Code, ExpectKind: "phone"}); err != nil || r.Device.Name != "Anna" {
		t.Errorf("claim with the printed code: %+v %v", r.Device, err)
	}
}

func TestHubPairMachineCodePrintsTheConnectCommand(t *testing.T) {
	db := pairEnv(t)
	var out, errb bytes.Buffer
	if err := Pair([]string{"--db", db, "--kind", "machine", "--url", "https://hub.example.ts.net"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	if !strings.Contains(out.String(), "sy connect https://hub.example.ts.net --code ") || strings.Contains(out.String(), "\x1b[") {
		t.Errorf("output %q", out.String())
	}
}

func TestHubPairErrorsAndUsage(t *testing.T) {
	db := pairEnv(t)
	var out, errb bytes.Buffer
	if err := Pair([]string{"--db", db, "--url", "https://h.example.ts.net"}, &out, &errb); !errors.Is(err, ErrUsage) {
		t.Errorf("no --kind: %v", err)
	}
	if err := Pair([]string{"--db", db, "--kind", "phone", "--ttl", "5s"}, &out, &errb); !errors.Is(err, ErrUsage) {
		t.Errorf("tiny ttl: %v", err)
	}
	if err := Pair([]string{"--db", db, "--kind", "phone", "extra"}, &out, &errb); !errors.Is(err, ErrUsage) {
		t.Errorf("extra argument: %v", err)
	}
	if err := Pair([]string{"--bogus"}, &out, &errb); !errors.Is(err, ErrUsage) {
		t.Errorf("unknown flag: %v", err)
	}
	// No address anywhere: the CLI says what to pass.
	err := Pair([]string{"--db", db, "--kind", "phone"}, &out, &errb)
	if !errors.Is(err, pair.ErrNoURL) || !strings.Contains(err.Error(), "--url https://") {
		t.Errorf("no url: %v", err)
	}
	// hub.public_url is used.
	st, _ := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	_ = st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO setting (key, value, updated_at) VALUES ('hub.public_url', '"https://pub.example.ts.net"', 0)`)
		return err
	})
	st.Close()
	out.Reset()
	if err := Pair([]string{"--db", db, "--kind", "phone", "--json"}, &out, &errb); err != nil || !strings.Contains(out.String(), "https://pub.example.ts.net/#/pair?s=") {
		t.Errorf("setting not used: %v %s", err, out.String())
	}
	// http for a phone: refused with the fix, allowed with --insecure (and says what is lost).
	err = Pair([]string{"--db", db, "--kind", "phone", "--url", "http://hub.example.ts.net:8787"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "tailscale serve --bg 8787") {
		t.Errorf("http phone: %v", err)
	}
	out.Reset()
	if err := Pair([]string{"--db", db, "--kind", "phone", "--url", "http://hub.example.ts.net:8787", "--insecure"}, &out, &errb); err != nil ||
		!strings.Contains(out.String(), "cannot be installed or receive pushes") {
		t.Errorf("--insecure: %v %s", err, out.String())
	}
	// Not the hub machine.
	err = Pair([]string{"--db", filepath.Join(t.TempDir(), "none.db"), "--kind", "phone", "--url", "https://h.example.ts.net"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "run this on the hub machine") || !strings.Contains(err.Error(), "Add phone") {
		t.Errorf("no hub db: %v", err)
	}
}

func TestTerminalQRDecodesBackToTheURL(t *testing.T) {
	db := pairEnv(t)
	st, _ := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	defer st.Close()
	c, err := pair.NewService(st, "").Create(context.Background(), pair.CreateRequest{Kind: "phone", URL: "https://hub.example.ts.net"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printCode(&out, c, true)
	text := out.String()
	if !strings.HasPrefix(text, "Scan with your phone's camera:") || !strings.Contains(text, "or open  https://hub.example.ts.net  and enter  "+c.Code) ||
		!strings.Contains(text, "The code works once, for ") {
		t.Errorf("text:\n%s", text)
	}
	var qrLines []string
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "  \x1b[30;47m") {
			qrLines = append(qrLines, strings.TrimPrefix(ln, "  "))
		}
	}
	got, err := pair.ParseTerminal(strings.Join(qrLines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := pair.NewMatrix(c.URL)
	if got.Size != want.Size || strings.Join(got.Rows, "") != strings.Join(want.Rows, "") {
		t.Error("the QR drawn in the terminal does not decode to the encoder's matrix of the pairing URL")
	}
	// Without a terminal or with --qr off the QR is left out and the link is printed.
	out.Reset()
	printCode(&out, c, false)
	if strings.Contains(out.String(), "\x1b[") || !strings.Contains(out.String(), c.URL) {
		t.Errorf("no-QR output:\n%s", out.String())
	}
}

func TestPhonePairWaitsForTheClaim(t *testing.T) {
	db := pairEnv(t)
	old := pairPollEvery
	pairPollEvery = 10 * time.Millisecond
	t.Cleanup(func() { pairPollEvery = old })

	out := &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- PhonePair([]string{"--db", db, "--url", "https://hub.example.ts.net", "--json"}, out, &bytes.Buffer{})
	}()
	var first struct{ Secret string }
	deadline := time.Now().Add(5 * time.Second)
	for first.Secret == "" {
		if time.Now().After(deadline) {
			t.Fatalf("no code printed: %q", out.String())
		}
		if line, _, ok := strings.Cut(out.String(), "\n"); ok {
			_ = json.Unmarshal([]byte(line), &first)
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, _ := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	defer st.Close()
	if _, err := pair.NewService(st, "").Claim(context.Background(), "1.1.1.1", pair.ClaimRequest{Secret: first.Secret, Name: "Pixel", ExpectKind: "phone", Cookie: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("--wait did not notice the claim")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output %q", out.String())
	}
	var second struct {
		Claimed bool
		Device  struct {
			ID, Kind, Name string
			Scopes         []string
		}
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil || !second.Claimed || second.Device.Kind != "phone" || second.Device.Name != "Pixel" ||
		strings.Join(second.Device.Scopes, ",") != "read,push" || !strings.HasPrefix(second.Device.ID, "dev_") {
		t.Errorf("claimed line %s (%v)", lines[1], err)
	}
}

func TestPairWaitReportsExpiry(t *testing.T) {
	db := pairEnv(t)
	old := pairPollEvery
	pairPollEvery = 10 * time.Millisecond
	t.Cleanup(func() { pairPollEvery = old })
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- Pair([]string{"--db", db, "--kind", "phone", "--url", "https://hub.example.ts.net", "--wait", "--json"}, out, &bytes.Buffer{})
	}()
	// Expire the code once --wait has created it: poll for the row instead of sleeping a
	// fixed time, which under load can run the UPDATE before the code exists (matching no
	// row) and leave --wait watching a live 10-minute code.
	st, err := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	deadline := time.Now().Add(10 * time.Second)
	for expired := false; !expired; {
		if err := st.Write(context.Background(), func(tx *store.Tx) error {
			res, err := tx.Exec(`UPDATE pair_code SET expires_at = 1`)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			expired = n > 0
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if !expired {
			if time.Now().After(deadline) {
				t.Fatal("--wait never created a code")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrExpired) {
			t.Fatalf("err %v, want ErrExpired (exit 3)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("--wait ignored the expiry")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || lines[1] != `{"claimed":false,"status":"expired"}` {
		t.Errorf("output %q", out.String())
	}
}

func TestOwnerLinkCommand(t *testing.T) {
	db := pairEnv(t)
	var out, errb bytes.Buffer
	if err := OwnerLink([]string{"--db", db}, &out, &errb); !errors.Is(err, pair.ErrNoURL) {
		t.Errorf("no url: %v", err)
	}
	if err := OwnerLink([]string{"--db", db, "--url", "https://hub.example.ts.net", "--json"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	var l struct{ ID, URL, Secret string }
	if err := json.Unmarshal(out.Bytes(), &l); err != nil || !strings.HasPrefix(l.ID, "pc_") || l.URL != "https://hub.example.ts.net/#/owner?s="+l.Secret || len(l.Secret) != 22 {
		t.Fatalf("%s %v", out.String(), err)
	}
	st, _ := store.Open(context.Background(), db, store.Options{NoBackupBeforeMigrate: true})
	defer st.Close()
	c, err := pair.NewService(st, "").OwnerSignIn(context.Background(), "1.1.1.1", l.Secret, "")
	if err != nil || !strings.HasPrefix(c.Token, "sy_o_") {
		t.Errorf("sign in with the link: %v", err)
	}
	out.Reset()
	if err := OwnerLink([]string{"--db", db, "--url", "https://hub.example.ts.net"}, &out, &errb); err != nil || !strings.Contains(out.String(), "/#/owner?s=") {
		t.Errorf("text: %v %s", err, out.String())
	}
	if err := OwnerLink([]string{"--db", db, "--url", "http://hub.example.com"}, &out, &errb); err == nil {
		t.Error("owner link over plain http to a public name")
	}
}
