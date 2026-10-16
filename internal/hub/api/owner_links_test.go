package api_test

import (
	"bytes"
	"context"
	"log"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// ownerLinkSecret is the s= of an owner link URL (<hub>/#/owner?s=<secret>).
func ownerLinkSecret(t *testing.T, link string) string {
	t.Helper()
	_, frag, ok := strings.Cut(link, "#/owner?")
	if !ok {
		t.Fatalf("not an owner link: %q", link)
	}
	q, err := url.ParseQuery(frag)
	if err != nil || q.Get("s") == "" {
		t.Fatalf("no secret in the owner link")
	}
	return q.Get("s")
}

// pairing.md 4.1: Make another device owner. Only an owner session may mint; the link works
// once, for 10 minutes, through POST /v1/session/owner, and makes a new owner session.
func TestOwnerLinksAPI(t *testing.T) {
	h := newPairHub(t)
	same := opts{origin: h.http.URL}
	owner := h.ownerCookie()
	phone, _ := h.phoneCookie("Pixel")

	// Not an owner: 401 without a token, 403 for a read or push device.
	if r := h.call("POST", "/v1/owner-links", map[string]any{}, same); r.code != 401 {
		t.Errorf("no token: %d", r.code)
	}
	if r := h.call("POST", "/v1/owner-links", map[string]any{}, opts{cookie: phone, origin: h.http.URL}); r.code != 403 {
		t.Errorf("phone: %d %s", r.code, r.body)
	}
	if r := h.call("POST", "/v1/owner-links", map[string]any{}, opts{token: h.token("desktop")}); r.code != 403 {
		t.Errorf("desktop token: %d %s", r.code, r.body)
	}
	// The cookie CSRF rules hold.
	if r := h.call("POST", "/v1/owner-links", map[string]any{}, opts{cookie: owner, ctype: "text/plain"}); r.code != 403 {
		t.Errorf("cookie + text/plain: %d", r.code)
	}
	if r := h.call("POST", "/v1/owner-links", map[string]any{}, opts{cookie: owner, origin: "https://evil.example"}); r.code != 403 {
		t.Errorf("cookie + foreign Origin: %d", r.code)
	}

	r := h.call("POST", "/v1/owner-links", map[string]any{}, opts{cookie: owner, origin: h.http.URL})
	if r.code != 201 {
		t.Fatalf("mint: %d %s", r.code, r.body)
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", r.header.Get("Cache-Control"))
	}
	m := r.json(t)
	link, _ := m["url"].(string)
	if !strings.HasPrefix(link, h.http.URL+"/#/owner?s=") || m["qr"] == nil || m["id"] == nil {
		t.Fatalf("reply %v", m)
	}
	exp, err := time.Parse(time.RFC3339Nano, m["expires_at"].(string))
	if err != nil || exp.Sub(time.Now()) > 10*time.Minute || exp.Sub(time.Now()) < 9*time.Minute {
		t.Errorf("expires_at %v (%v), want 10 minutes", m["expires_at"], err)
	}
	secret := ownerLinkSecret(t, link)
	row, err := h.st.PairCodeByID(context.Background(), m["id"].(string))
	if err != nil || row.Kind != "owner" || !strings.HasPrefix(row.CreatedBy, "owner:dev_") {
		t.Fatalf("row %+v %v", row, err)
	}

	// Consumed like `sy hub owner-link`: a new owner session.
	r = h.call("POST", "/v1/session/owner", map[string]string{"secret": secret, "name": "Laptop"}, same)
	if r.code != 204 {
		t.Fatalf("sign-in: %d %s", r.code, r.body)
	}
	other := sessionCookie(t, r).Value
	if other == owner || !strings.HasPrefix(other, "sy_o_") {
		t.Fatal("no new owner session")
	}
	me := h.call("GET", "/v1/me", nil, opts{cookie: other}).json(t)
	if me["owner"] != true || me["device"].(map[string]any)["owner_session"] != true {
		t.Errorf("me %v", me)
	}
	// Single use.
	r = h.call("POST", "/v1/session/owner", map[string]string{"secret": secret}, same)
	if r.code != 401 || r.json(t)["code"] != pair.LinkUsed {
		t.Errorf("replay: %d %s", r.code, r.body)
	}
	// The status shows the claim, as for a pairing code.
	if st := h.call("GET", "/v1/pair/codes/"+m["id"].(string), nil, opts{cookie: owner}).json(t); st["status"] != "claimed" {
		t.Errorf("status %v", st)
	}

	// Expiry: a link past its 10 minutes is refused as expired.
	r = h.call("POST", "/v1/owner-links", map[string]any{}, opts{cookie: owner, origin: h.http.URL})
	if r.code != 201 {
		t.Fatalf("mint 2: %d", r.code)
	}
	m = r.json(t)
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE pair_code SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).UnixMilli(), m["id"])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r = h.call("POST", "/v1/session/owner", map[string]string{"secret": ownerLinkSecret(t, m["url"].(string))}, same)
	if r.code != 401 || r.json(t)["code"] != pair.LinkExpired {
		t.Errorf("expired: %d %s", r.code, r.body)
	}

	// At most 3 board-made links are live: a fourth cancels the oldest.
	var secrets []string
	for i := range 4 {
		r := h.call("POST", "/v1/owner-links", map[string]any{}, opts{cookie: owner, origin: h.http.URL})
		if r.code != 201 {
			t.Fatalf("mint %d: %d", i, r.code)
		}
		secrets = append(secrets, ownerLinkSecret(t, r.json(t)["url"].(string)))
	}
	if r := h.call("POST", "/v1/session/owner", map[string]string{"secret": secrets[0]}, same); r.code != 401 || r.json(t)["code"] != pair.LinkCancelled {
		t.Errorf("oldest of four: %d %s", r.code, r.body)
	}
	if r := h.call("POST", "/v1/session/owner", map[string]string{"secret": secrets[3]}, same); r.code != 204 {
		t.Errorf("newest of four: %d %s", r.code, r.body)
	}
}

// Minting is rate limited per IP like claims (20 a minute).
func TestOwnerLinksRateLimited(t *testing.T) {
	h := newPairHub(t)
	owner := h.ownerCookie()
	var last int
	for range 25 {
		last = h.call("POST", "/v1/owner-links", map[string]any{}, opts{cookie: owner, xff: "203.0.113.60"}).code
	}
	if last != 429 {
		t.Errorf("mint flood: last status %d", last)
	}
}

// The hub log has one line per mint and sign-in, with the code id, never the secret.
func TestOwnerLinkLogHasNoSecret(t *testing.T) {
	h := newPairHub(t)
	var buf bytes.Buffer
	h.svc.Log = log.New(&buf, "", 0)
	ctx := context.Background()
	l0, err := h.svc.CreateOwnerLink(ctx, pair.OwnerLinkRequest{URL: pairHubURL})
	if err != nil {
		t.Fatal(err)
	}
	c, err := h.svc.OwnerSignIn(ctx, "192.0.2.1", l0.Secret, "x")
	if err != nil {
		t.Fatal(err)
	}
	l, err := h.svc.MintOwnerLink(ctx, "192.0.2.1", c.Device, pairHubURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.OwnerSignIn(ctx, "192.0.2.2", l.Secret, "y"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "pair owner-link made code="+l.ID+" by="+c.Device.ID) || !strings.Contains(out, "pair owner ok") {
		t.Errorf("log %q", out)
	}
	for _, s := range []string{l.Secret, l0.Secret, l.URL} {
		if strings.Contains(out, s) {
			t.Fatal("a secret reached the log")
		}
	}
	// Only an owner session mints.
	if _, err := h.svc.MintOwnerLink(ctx, "192.0.2.1", store.Device{ID: "dev_x", Scopes: []string{"read"}, CreatedBy: "pair:pc_1"}, pairHubURL); err == nil {
		t.Error("a non-owner device minted an owner link")
	}
}
