package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/store"
)

func TestExportImportUnfreezeCLI(t *testing.T) {
	ctx := context.Background()
	srcState := t.TempDir()
	t.Setenv("SY_STATE_DIR", srcState)
	os.WriteFile(filepath.Join(srcState, "pair_pepper"), []byte("pep"), 0o600)
	os.WriteFile(filepath.Join(srcState, "hub_sign.key"), make([]byte, 32), 0o600)
	srcDir := t.TempDir()
	srcDB := filepath.Join(srcDir, "hub.db")
	out, err := run(t, "token", "create", "--kind", "machine", "--machine", "m_01", "--name", "laptop", "--db", srcDB, "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var c struct{ Token string }
	json.Unmarshal([]byte(out), &c)

	pass := filepath.Join(t.TempDir(), "pass")
	os.WriteFile(pass, []byte("hunter2 hunter2\n"), 0o600)
	arch := filepath.Join(t.TempDir(), "x", "hub.syx")
	out, err = run(t, "export", "--db", srcDB, "--out", arch, "--moved-to", "https://new.example", "--passphrase-file", pass, "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var er struct {
		Path      string
		Encrypted bool
		Frozen    bool
	}
	if err := json.Unmarshal([]byte(out), &er); err != nil || !er.Encrypted || !er.Frozen || er.Path != arch {
		t.Fatalf("%s %v", out, err)
	}
	// frozen: visible in the store
	st, err := store.Open(ctx, srcDB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := st.Frozen(ctx); f == nil || f.MovedTo == nil || *f.MovedTo != "https://new.example" {
		t.Errorf("not frozen: %+v", f)
	}
	st.Close()

	// import needs the passphrase and --yes (no terminal in tests)
	dstDir := t.TempDir()
	dstState := t.TempDir()
	t.Setenv("SY_STATE_DIR", dstState)
	dstDB := filepath.Join(dstDir, "hub.db")
	if _, err := run(t, "import", arch, "--db", dstDB, "--listen", "127.0.0.1:1", "--yes"); err == nil {
		t.Fatal("encrypted import without a passphrase")
	}
	out, err = run(t, "import", arch, "--db", dstDB, "--listen", "127.0.0.1:1", "--yes", "--passphrase-file", pass, "--public-url", "https://new.example")
	if err != nil {
		t.Fatal(out, err)
	}
	if !strings.Contains(out, "sy connect <new url> --keep-token") {
		t.Errorf("next steps missing: %s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dstState, "pair_pepper")); string(b) != "pep" {
		t.Errorf("pepper %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dstState, "hub_sign.key")); len(b) != 32 {
		t.Errorf("hub key not restored (%d bytes)", len(b))
	}
	// the old token authenticates against the imported hub
	d, err := store.Open(ctx, dstDB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := auth.New(d).Lookup(ctx, c.Token)
	if err != nil || dev.Name != "laptop" {
		t.Fatalf("old token against the imported hub: %+v %v", dev, err)
	}
	if f, _ := d.Frozen(ctx); f != nil {
		t.Error("imported hub frozen")
	}
	d.Close()

	// importing again into the now non-empty hub is refused, exit 5 class
	_, err = run(t, "import", arch, "--db", dstDB, "--listen", "127.0.0.1:1", "--yes", "--passphrase-file", pass)
	var ref *RefusedError
	if !errors.As(err, &ref) || !strings.Contains(err.Error(), "already has data") {
		t.Fatalf("got %v", err)
	}

	// unfreeze on the source
	out, err = run(t, "unfreeze", "--db", srcDB)
	if err != nil || !strings.Contains(out, "unfrozen") {
		t.Fatal(out, err)
	}
	out, _ = run(t, "unfreeze", "--db", srcDB)
	if !strings.Contains(out, "was not frozen") {
		t.Errorf("%s", out)
	}
	// usage
	if _, err := run(t, "import", "--db", dstDB); !errors.Is(err, ErrUsage) {
		t.Errorf("import without a file: %v", err)
	}
}

func TestImportRefusesLiveHubAddress(t *testing.T) {
	if hubAnswers("127.0.0.1:1") {
		t.Fatal("nothing listens there")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer s.Close()
	addr := strings.TrimPrefix(s.URL, "http://")
	t.Setenv("SY_STATE_DIR", t.TempDir())
	f := filepath.Join(t.TempDir(), "x.syx")
	os.WriteFile(f, []byte("x"), 0o600)
	_, err := run(t, "import", f, "--db", filepath.Join(t.TempDir(), "hub.db"), "--listen", addr, "--yes")
	if err == nil || !strings.Contains(err.Error(), "stop the hub first") {
		t.Fatalf("got %v", err)
	}
}
