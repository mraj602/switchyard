package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
)

func TestHubKeysShowConfirmAndRevoke(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WY_STATE_DIR", state)
	db := filepath.Join(t.TempDir(), "hub.db")
	ctx := context.Background()
	st, err := store.Open(ctx, db, store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	out, err := run(t, "keys", "show", "--db", db, "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var show struct {
		HubKey   hubkeys.PublicKey `json:"hub_key"`
		OwnerKey any               `json:"owner_key"`
	}
	if err := json.Unmarshal([]byte(out), &show); err != nil || !keys.ValidFP(show.HubKey.FP) || show.OwnerKey != nil {
		t.Fatalf("show: %s %v", out, err)
	}
	fi, err := os.Stat(filepath.Join(state, hubkeys.KeyFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("hub key file: %v", err)
	}

	// A proposal from a machine, confirmed on the hub machine.
	hk, _ := hubkeys.Load(filepath.Join(state, hubkeys.KeyFile))
	st, _ = store.Open(ctx, db, store.Options{NoBackupBeforeMigrate: true})
	ks := hubkeys.New(st, hk)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	doc, _ := keys.NewOwnerKeyDoc(priv, hk.FP, time.Now())
	p, err := ks.Propose(ctx, doc, "dev_laptop")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := auth.Create(ctx, st, auth.CreateRequest{Kind: "machine", Name: "box", Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"})
	dk, _ := keys.NewDeviceKeys(time.Now(), rand.Reader)
	pub, _ := dk.Public()
	if err := st.Write(ctx, func(tx *store.Tx) error { _, err := ks.RegisterTx(tx, m.Device, pub, keys.RegTOFU); return err }); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out, _ = run(t, "keys", "show", "--db", db)
	if !strings.Contains(out, "pending") || !strings.Contains(out, keys.GroupFP(p.FP)) {
		t.Fatalf("show pending: %s", out)
	}
	if out, err := run(t, "keys", "confirm-owner", "--db", db, "nope"); err == nil {
		t.Fatalf("bad fp accepted: %s", out)
	}
	if out, err := run(t, "keys", "confirm-owner", "--db", db, keys.GroupFP(p.FP)); err != nil || !strings.Contains(out, "is active") {
		t.Fatalf("confirm: %s %v", out, err)
	}
	if out, _ := run(t, "keys", "show", "--db", db); !strings.Contains(out, "(active") {
		t.Fatalf("show active: %s", out)
	}

	// token revoke puts the machine's keys on the hub-signed revocation list.
	if out, err := run(t, "token", "revoke", "--db", db, "--yes", m.Device.ID); err != nil {
		t.Fatal(out, err)
	}
	st, _ = store.Open(ctx, db, store.Options{NoBackupBeforeMigrate: true})
	defer st.Close()
	rl, err := hubkeys.New(st, hk).Revocations(ctx)
	if err != nil || rl == nil || !rl.Body.RevokedDevice(m.Device.ID) || !rl.Body.RevokedKey(signFP(t, dk)) {
		t.Fatalf("revocations: %+v %v", rl, err)
	}
}

func signFP(t *testing.T, dk keys.DeviceKeys) string {
	k, err := dk.SignKey()
	if err != nil {
		t.Fatal(err)
	}
	return keys.Fingerprint(k.Public().(ed25519.PublicKey))
}
