package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
)

// `wy hub keys register` and `certify`: the hub host's half of wy machine add's keys step.
func TestHubKeysRegisterAndCertify(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WY_STATE_DIR", state)
	db := filepath.Join(t.TempDir(), "hub.db")
	ctx := context.Background()
	st, err := store.Open(ctx, db, store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	const mid = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"
	m, err := auth.Create(ctx, st, auth.CreateRequest{Kind: "machine", Name: "gpu-box", Machine: mid})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	dk, _ := keys.NewDeviceKeys(time.Now(), rand.Reader)
	pub, _ := dk.Public()
	in, _ := json.Marshal(pub)

	runIn := func(stdin []byte, args ...string) (string, error) {
		var out, errb bytes.Buffer
		err := KeysIn(bytes.NewReader(stdin), args, &out, &errb)
		return out.String() + errb.String(), err
	}
	if _, err := runIn(in, "register", m.Device.ID, "--db", db, "--keys-stdin"); err == nil {
		t.Fatal("register without --machine accepted")
	}
	if out, err := runIn(in, "register", m.Device.ID, "--db", db, "--machine", "m_01JA2W5N0G8H3Q7S2V6X9Y1ZZZ", "--keys-stdin"); err == nil {
		t.Fatalf("register for another machine accepted: %s", out)
	}
	out, err := runIn(in, "register", m.Device.ID, "--db", db, "--machine", mid, "--keys-stdin", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var rep struct {
		Device      string            `json:"device"`
		Reg         string            `json:"reg"`
		Cert        keys.Doc          `json:"cert"`
		OwnerSigned bool              `json:"owner_signed"`
		HubKey      hubkeys.PublicKey `json:"hub_key"`
		OwnerKey    *keys.Doc         `json:"owner_key"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(out, err)
	}
	hk, err := hubkeys.Load(filepath.Join(state, hubkeys.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	c, err := keys.VerifyCert(rep.Cert, hk.Trust(), false)
	if err != nil || rep.Reg != keys.RegSSH || c.Body.Reg != keys.RegSSH || c.Body.Sign != pub.Sign || rep.HubKey.FP != hk.FP || rep.OwnerSigned || rep.OwnerKey != nil {
		t.Fatalf("register reply %s %v", out, err)
	}

	// An owner signature through certify, once the owner key is active.
	st, _ = store.Open(ctx, db, store.Options{NoBackupBeforeMigrate: true})
	_, owner, _ := ed25519.GenerateKey(rand.Reader)
	od, _ := keys.NewOwnerKeyDoc(owner, hk.FP, time.Now())
	if _, err := hubkeys.New(st, hk).ProposeAndConfirm(ctx, od); err != nil {
		t.Fatal(err)
	}
	st.Close()
	ofp := keys.Fingerprint(owner.Public().(ed25519.PublicKey))
	if out, err := runIn([]byte("AAAA\n"), "certify", m.Device.ID, "--db", db, "--fp", ofp, "--sig-stdin"); err == nil {
		t.Fatalf("bad signature accepted: %s", out)
	}
	sig := keys.SignDoc(keys.TypeDeviceCert, c.Raw, keys.ByOwner, owner).Sig
	if out, err := runIn([]byte(sig+"\n"), "certify", m.Device.ID, "--db", db, "--fp", ofp, "--sig-stdin"); err != nil || !strings.Contains(out, "certified") {
		t.Fatalf("certify: %s %v", out, err)
	}
	out, err = runIn(in, "register", m.Device.ID, "--db", db, "--machine", mid, "--keys-stdin", "--json")
	if err != nil || !strings.Contains(out, `"owner_signed":true`) || strings.Contains(out, `"owner_key":null`) {
		t.Fatalf("rerun after certify: %s %v", out, err)
	}
}
