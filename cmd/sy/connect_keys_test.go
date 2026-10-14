package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/hub/api"
	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
)

// keyHub is a real hub API with a signing key, and a pairing service on the same pepper.
func keyHub(t *testing.T) (*httptest.Server, *pair.Service, *hubkeys.Service) {
	t.Helper()
	dir := t.TempDir()
	old := api.PairPepperPath
	api.PairPepperPath = filepath.Join(dir, "pair_pepper")
	t.Cleanup(func() { api.PairPepperPath = old })
	st, err := store.Open(context.Background(), filepath.Join(dir, "hub.db"), store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	hk, _ := hubkeys.FromSeed(bytes.Repeat([]byte{1}, 32))
	ks := hubkeys.New(st, hk)
	srv := api.New(api.Config{Store: st, PingEvery: time.Hour, Keys: ks})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { srv.Close(); hs.Close(); st.Close() })
	ps := pair.NewService(st, api.PairPepperPath)
	ps.Keys = ks
	return hs, ps, ks
}

func machineCode(t *testing.T, ps *pair.Service, hubURL string) string {
	t.Helper()
	c, err := ps.Create(context.Background(), pair.CreateRequest{Kind: "machine", URL: hubURL})
	if err != nil {
		t.Fatal(err)
	}
	return c.Code
}

func TestConnectCodeRegistersKeysAndPins(t *testing.T) {
	deps, _, _ := sandbox(t)
	hs, ps, ks := keyHub(t)
	var out, errb bytes.Buffer
	if err := connect(context.Background(), deps, []string{hs.URL, "--code", machineCode(t, ps, hs.URL), "--no-timer"}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	state := deps.dirs.State.Path
	p, err := keys.ReadPin(keys.Path(state, keys.HubPinFile))
	if err != nil || p.FP != ks.Key.FP || p.How != keys.PinTOFU {
		t.Fatalf("hub pin %+v %v", p, err)
	}
	dk, err := keys.ReadDeviceKeys(state)
	if err != nil || !strings.HasPrefix(dk.Device, "dev_") {
		t.Fatalf("device.json %+v %v", dk.Device, err)
	}
	c, err := ks.Cert(context.Background(), dk.Device)
	if err != nil || c.Body.Reg != keys.RegTOFU {
		t.Fatalf("hub cert %v", err)
	}
	if _, err := os.Stat(keys.Path(state, keys.OwnerPinFile)); !os.IsNotExist(err) {
		t.Fatal("owner pinned with no owner key active")
	}
}

func TestConnectCodeRefusesTamperedReplyAndOtherHubKey(t *testing.T) {
	hs, ps, _ := keyHub(t)
	target, _ := url.Parse(hs.URL)
	// A proxy that swaps the hub key in the claim reply for its own.
	evil := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ModifyResponse = func(r *http.Response) error {
		if r.Request.URL.Path != "/v1/pair/claim" || r.StatusCode != 200 {
			return nil
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["hub_key"] = map[string]string{"alg": "ed25519", "pub": keys.B64(evil), "fp": keys.Fingerprint(evil)}
		b, _ = json.Marshal(m)
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		r.Header.Del("Content-Length")
		return nil
	}
	px := httptest.NewServer(rp)
	defer px.Close()

	deps, _, _ := sandbox(t)
	var out, errb bytes.Buffer
	err := connect(context.Background(), deps, []string{px.URL, "--code", machineCode(t, ps, hs.URL), "--no-timer"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "tampered") {
		t.Fatalf("tampered reply: %v", err)
	}
	state := deps.dirs.State.Path
	if _, serr := os.Stat(filepath.Join(state, client.TokenFile)); !os.IsNotExist(serr) {
		t.Fatal("token kept after a tampered reply")
	}
	if _, serr := os.Stat(keys.Path(state, keys.HubPinFile)); !os.IsNotExist(serr) {
		t.Fatal("hub pinned after a tampered reply")
	}

	// A machine that already pinned another hub key refuses this hub.
	deps2, _, _ := sandbox(t)
	if err := keys.SetPin(keys.Path(deps2.dirs.State.Path, keys.HubPinFile), keys.NewPin(evil, keys.PinTOFU, time.Now())); err != nil {
		t.Fatal(err)
	}
	err = connect(context.Background(), deps2, []string{hs.URL, "--code", machineCode(t, ps, hs.URL), "--no-timer"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "repin-hub") {
		t.Fatalf("other pinned hub: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(deps2.dirs.State.Path, client.TokenFile)); !os.IsNotExist(serr) {
		t.Fatal("token kept for a hub with another key")
	}
}
