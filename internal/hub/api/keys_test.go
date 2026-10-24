package api_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/pair"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
)

type keysHub struct {
	t     *testing.T
	st    *store.Store
	ks    *hubkeys.Service
	pair  *pair.Service
	url   string
	owner string
}

func newKeysHub(t *testing.T) *keysHub {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	hk, _ := hubkeys.FromSeed(bytes.Repeat([]byte{1}, 32))
	ks := hubkeys.New(st, hk)
	pepper := testPepperPath(t)
	srv := api.New(api.Config{Store: st, PingEvery: time.Hour, PublicURL: "https://hub.example.ts.net", Keys: ks})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { srv.Close(); hs.Close(); st.Close() })
	ps := pair.NewService(st, pepper)
	ps.Keys = ks
	h := &keysHub{t: t, st: st, ks: ks, pair: ps, url: hs.URL}
	h.owner = ownerSessionToken(t, st).Token
	return h
}

func (h *keysHub) call(method, path, token string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.url+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return res.StatusCode, m
}

func asDoc(t *testing.T, v any) keys.Doc {
	t.Helper()
	b, _ := json.Marshal(v)
	var d keys.Doc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func testPhoneKeys() keys.DeviceKeysNew {
	sk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sb, _ := sk.PublicKey.Bytes()
	bk, _ := ecdh.P256().GenerateKey(rand.Reader)
	a := make([]byte, 16)
	rand.Read(a)
	return keys.DeviceKeysNew{Sign: keys.KeySpec{Alg: keys.AlgP256, Pub: keys.B64(sb)},
		Box: keys.KeySpec{Alg: keys.AlgP256, Pub: keys.B64(bk.PublicKey().Bytes())}, Auth: keys.B64(a)}
}

func testMachineKeys(t *testing.T) keys.DeviceKeysNew {
	dk, err := keys.NewDeviceKeys(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := dk.Public()
	return p
}

func TestKeysClaimReplyAndEndpoints(t *testing.T) {
	h := newKeysHub(t)
	ctx := context.Background()

	// A phone scans the QR: keys sealed with k=; the reply carries the hub key, the
	// certificate (reg qr), the link settings and no owner key yet.
	code, err := h.pair.Create(ctx, pair.CreateRequest{Kind: "phone", URL: "https://hub.example.ts.net"})
	if err != nil {
		t.Fatal(err)
	}
	_, frag, _ := strings.Cut(code.URL, "#/pair?")
	q, _ := url.ParseQuery(frag)
	if q.Get("h") != h.ks.Key.FP {
		t.Fatalf("QR h=%q", q.Get("h"))
	}
	k, _ := keys.UnB64(q.Get("k"))
	pk := testPhoneKeys()
	pj, _ := json.Marshal(pk)
	sealed, _ := keys.SealKeys(k, code.Secret, pj, rand.Reader)
	st, reply := h.call("POST", "/v1/pair/claim", "", map[string]any{"secret": code.Secret, "keys_sealed": sealed})
	if st != 200 || reply["reg"] != "qr" || reply["owner_key"] != nil {
		t.Fatalf("claim: %d %v", st, reply)
	}
	if hk := reply["hub_key"].(map[string]any); hk["fp"] != h.ks.Key.FP || hk["alg"] != "ed25519" {
		t.Fatalf("hub_key %v", hk)
	}
	if l := reply["link"].(map[string]any); l["ping_s"] != float64(180) || l["urls"].([]any)[0] != "wss://hub.example.ts.net/v1/link" {
		t.Fatalf("link %v", l)
	}
	cert, err := keys.VerifyCert(asDoc(t, reply["cert"]), keys.Trust{Hub: h.ks.Key.Pub}, false)
	if err != nil || cert.Body.Sign.Pub != pk.Sign.Pub {
		t.Fatalf("cert: %v", err)
	}
	phoneTok, phoneID := reply["token"].(string), reply["device"].(string)
	// QR-registered keys cannot be replaced over the API.
	if st, _ := h.call("PUT", "/v1/devices/self/keys", phoneTok, testPhoneKeys()); st != 409 {
		t.Fatalf("PUT keys over qr: %d", st)
	}

	// A machine with a typed code and clear keys: reg tofu, replaceable once.
	mc, _ := h.pair.Create(ctx, pair.CreateRequest{Kind: "machine", Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", URL: "https://hub.example.ts.net"})
	st, reply = h.call("POST", "/v1/pair/claim", "", map[string]any{"code": mc.Code, "keys": testMachineKeys(t)})
	if st != 200 || reply["reg"] != "tofu" || reply["cert"] == nil {
		t.Fatalf("typed claim: %d %v", st, reply)
	}
	machineTok, machineID := reply["token"].(string), reply["device"].(string)
	if st, m := h.call("PUT", "/v1/devices/self/keys", machineTok, testMachineKeys(t)); st != 200 || m["cert"] == nil {
		t.Fatalf("PUT keys: %d %v", st, m)
	}
	if st, _ := h.call("PUT", "/v1/devices/"+phoneID+"/keys", machineTok, testMachineKeys(t)); st != 403 {
		t.Fatalf("PUT another device's keys: %d", st)
	}
	// Downgrade on the QR path: clear keys with a secret.
	c2, _ := h.pair.Create(ctx, pair.CreateRequest{Kind: "phone", URL: "https://hub.example.ts.net"})
	if st, m := h.call("POST", "/v1/pair/claim", "", map[string]any{"secret": c2.Secret, "keys": pk}); st != 400 || m["error"] != "key registration invalid" {
		t.Fatalf("clear keys on the QR path: %d %v", st, m)
	}

	// Owner key: the machine proposes, an owner session confirms.
	ownerPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))
	doc, _ := keys.NewOwnerKeyDoc(ownerPriv, h.ks.Key.FP, time.Now())
	if st, m := h.call("POST", "/v1/keys/owner", machineTok, map[string]any{"doc": doc}); st != 201 || m["state"] != "pending" {
		t.Fatalf("propose: %d %v", st, m)
	}
	if _, m := h.call("GET", "/v1/keys", machineTok, nil); m["pending"] != nil || m["owner_key"] != nil {
		t.Fatalf("machine sees %v", m)
	}
	_, m := h.call("GET", "/v1/keys", h.owner, nil)
	if p := m["pending"].([]any); len(p) != 1 {
		t.Fatalf("owner sees %v", m)
	}
	fp := keys.Fingerprint(ownerPriv.Public().(ed25519.PublicKey))
	if st, _ := h.call("POST", "/v1/keys/owner/confirm", machineTok, map[string]any{"fp": fp}); st != 403 {
		t.Fatalf("machine confirms: %d", st)
	}
	if st, m := h.call("POST", "/v1/keys/owner/confirm", h.owner, map[string]any{"fp": keys.GroupFP(fp)}); st != 200 || m["state"] != "active" {
		t.Fatalf("confirm: %d %v", st, m)
	}
	_, m = h.call("GET", "/v1/keys", machineTok, nil)
	ok := m["owner_key"].(map[string]any)
	if ok["fp"] != fp {
		t.Fatalf("active %v", ok)
	}
	if o, err := keys.VerifyOwnerKey(asDoc(t, ok["doc"]), h.ks.Key.Pub); err != nil || !o.Confirmed {
		t.Fatalf("owner doc: %v", err)
	}

	// Certify the machine with the owner key.
	st, m = h.call("GET", "/v1/devices/"+machineID+"/cert", machineTok, nil)
	if st != 200 || m["owner_signed"] != false {
		t.Fatalf("cert: %d %v", st, m)
	}
	body, _ := asDoc(t, m["cert"]).BodyBytes()
	sig := keys.SignDoc(keys.TypeDeviceCert, body, keys.ByOwner, ownerPriv)
	if st, _ := h.call("POST", "/v1/devices/"+machineID+"/cert", machineTok, map[string]any{"fp": fp, "sig": sig.Sig}); st != 200 {
		t.Fatalf("certify: %d", st)
	}
	if _, m := h.call("GET", "/v1/devices/"+machineID+"/cert", machineTok, nil); m["owner_signed"] != true {
		t.Fatalf("after certify: %v", m)
	}

	// Revoking the phone puts its keys on the hub-signed revocation list.
	// (The machine's replaced keys are already on it.)
	if st, m := h.call("GET", "/v1/keys/revocations", phoneTok, nil); st != 200 || m["doc"] == nil {
		t.Fatalf("list after the key replacement: %d %v", st, m)
	}
	if st, _ := h.call("DELETE", "/v1/devices/"+phoneID, h.owner, nil); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	_, m = h.call("GET", "/v1/keys/revocations", machineTok, nil)
	tr, _ := h.ks.Trust(ctx)
	rl, err := keys.VerifyRevocations(asDoc(t, m["doc"]), tr)
	if err != nil || !rl.Body.RevokedDevice(phoneID) || !rl.Body.RevokedKey(cert.Body.SignFP()) {
		t.Fatalf("revocations: %v %+v", err, rl.Body)
	}
}

func TestKeysEndpointsWithoutHubKey(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	if st, _ := h.do("GET", "/v1/keys", tok, nil); st != 503 {
		t.Fatalf("GET /v1/keys without a hub key: %d", st)
	}
}

// Removing a machine revokes its devices; their keys go on the revocation list as for
// DELETE /v1/devices/{id} (keys.md 6).
func TestMachineRemovePublishesRevocation(t *testing.T) {
	h := newKeysHub(t)
	ctx := context.Background()
	const mid = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4R"
	mc, _ := h.pair.Create(ctx, pair.CreateRequest{Kind: "machine", Machine: mid, URL: "https://hub.example.ts.net"})
	st, reply := h.call("POST", "/v1/pair/claim", "", map[string]any{"code": mc.Code, "keys": testMachineKeys(t)})
	if st != 200 {
		t.Fatalf("claim: %d %v", st, reply)
	}
	devID := reply["device"].(string)
	cert, err := h.ks.Cert(ctx, devID)
	if err != nil {
		t.Fatal(err)
	}
	if st, m := h.call("DELETE", "/v1/machines/"+mid, h.owner, nil); st != 204 {
		t.Fatalf("remove: %d %v", st, m)
	}
	rl, err := h.ks.Revocations(ctx)
	if err != nil || rl == nil || !rl.Body.RevokedDevice(devID) || !rl.Body.RevokedKey(cert.Body.SignFP()) {
		t.Fatalf("revocations after machine removal: %v %+v", err, rl)
	}
}
