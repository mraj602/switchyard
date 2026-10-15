package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/pair"
	"github.com/mraj602/switchyard/internal/hub/store"
)

const pairHubURL = "https://hub.example.ts.net"

// pairHub is a hub whose pairing pepper lives in a temp folder, plus a service on the same
// pepper to make codes the way `sy hub pair` does.
type pairHub struct {
	*hub
	svc *pair.Service
}

func newPairHub(t *testing.T) *pairHub {
	t.Helper()
	old := api.PairPepperPath
	api.PairPepperPath = filepath.Join(t.TempDir(), "pair_pepper")
	t.Cleanup(func() { api.PairPepperPath = old })
	h := startHub(t, "")
	return &pairHub{hub: h, svc: pair.NewService(h.st, api.PairPepperPath)}
}

type resp struct {
	code    int
	body    []byte
	cookies []*http.Cookie
	header  http.Header
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("not JSON (%d): %s", r.code, r.body)
	}
	return m
}

// opts of pcall.
type opts struct {
	token  string // bearer
	cookie string // sy_session
	ctype  string // default application/json when there is a body
	origin string
	xff    string
}

func (h *pairHub) call(method, path string, body any, o opts) resp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.http.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if o.ctype != "" {
		req.Header.Set("Content-Type", o.ctype)
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if o.token != "" {
		req.Header.Set("Authorization", "Bearer "+o.token)
	}
	if o.cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: o.cookie})
	}
	if o.origin != "" {
		req.Header.Set("Origin", o.origin)
	}
	if o.xff != "" {
		req.Header.Set("X-Forwarded-For", o.xff)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{code: res.StatusCode, body: b, cookies: res.Cookies(), header: res.Header}
}

func sessionCookie(t *testing.T, r resp) *http.Cookie {
	t.Helper()
	for _, c := range r.cookies {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %d %s", auth.CookieName, r.code, r.body)
	return nil
}

// ownerCookie signs in with a fresh owner link and returns the session token.
func (h *pairHub) ownerCookie() string {
	h.t.Helper()
	l, err := h.svc.CreateOwnerLink(context.Background(), pair.OwnerLinkRequest{URL: pairHubURL})
	if err != nil {
		h.t.Fatal(err)
	}
	r := h.call("POST", "/v1/session/owner", map[string]string{"secret": l.Secret, "name": "Firefox on Linux"}, opts{origin: h.http.URL})
	if r.code != 204 {
		h.t.Fatalf("owner sign-in: %d %s", r.code, r.body)
	}
	return sessionCookie(h.t, r).Value
}

// phoneCookie pairs a phone in cookie mode and returns its session token and device id.
func (h *pairHub) phoneCookie(name string) (cookie, device string) {
	h.t.Helper()
	c, err := h.svc.Create(context.Background(), pair.CreateRequest{Kind: "phone", URL: pairHubURL})
	if err != nil {
		h.t.Fatal(err)
	}
	r := h.call("POST", "/v1/pair/claim", map[string]any{"secret": c.Secret, "name": name, "cookie": true, "expect_kind": "phone"}, opts{origin: h.http.URL})
	if r.code != 200 {
		h.t.Fatalf("claim: %d %s", r.code, r.body)
	}
	return sessionCookie(h.t, r).Value, r.json(h.t)["device"].(string)
}

func TestOwnerSignInFlow(t *testing.T) {
	h := newPairHub(t)
	l, err := h.svc.CreateOwnerLink(context.Background(), pair.OwnerLinkRequest{URL: pairHubURL})
	if err != nil {
		t.Fatal(err)
	}
	same := opts{origin: h.http.URL}

	if r := h.call("POST", "/v1/session/owner", map[string]string{"secret": l.Secret}, opts{ctype: "text/plain"}); r.code != 403 {
		t.Errorf("text/plain sign-in: %d", r.code)
	}
	if r := h.call("POST", "/v1/session/owner", map[string]string{"secret": l.Secret}, opts{origin: "https://evil.example"}); r.code != 403 {
		t.Errorf("cross-origin sign-in: %d", r.code)
	}
	if r := h.call("POST", "/v1/session/owner", map[string]string{"secret": "short"}, same); r.code != 400 {
		t.Errorf("short secret: %d", r.code)
	}
	if r := h.call("POST", "/v1/session/owner", map[string]string{"secret": strings.Repeat("A", 22)}, same); r.code != 401 {
		t.Errorf("unknown secret: %d", r.code)
	}

	r := h.call("POST", "/v1/session/owner", map[string]string{"secret": l.Secret}, same)
	if r.code != 204 {
		t.Fatalf("sign-in: %d %s", r.code, r.body)
	}
	c := sessionCookie(t, r)
	if !strings.HasPrefix(c.Value, "sy_o_") || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 30*24*3600 {
		t.Errorf("cookie %+v", c)
	}
	if c.Secure {
		t.Error("Secure on a plain http test server")
	}
	if r2 := h.call("POST", "/v1/session/owner", map[string]string{"secret": l.Secret}, same); r2.code != 401 {
		t.Errorf("replay: %d", r2.code)
	}

	me := h.call("GET", "/v1/me", nil, opts{cookie: c.Value}).json(t)
	dev := me["device"].(map[string]any)
	if me["owner"] != true || dev["kind"] != "browser" || dev["owner_session"] != true || dev["current"] != true || dev["expires_at"] == nil {
		t.Errorf("me %v", me)
	}
	if fmt.Sprint(dev["scopes"]) != "[owner read push]" {
		t.Errorf("scopes %v", dev["scopes"])
	}
	if me["hub"].(map[string]any)["version"] == "" {
		t.Errorf("hub %v", me["hub"])
	}

	// The hub expires it at expires_at, whatever the cookie says.
	id := dev["id"].(string)
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE device SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).UnixMilli(), id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := h.call("GET", "/v1/me", nil, opts{cookie: c.Value}); r.code != 401 {
		t.Errorf("expired owner session: %d", r.code)
	}
}

func TestLogoutRevokesTheOwnerSession(t *testing.T) {
	h := newPairHub(t)
	c := h.ownerCookie()
	if r := h.call("GET", "/v1/devices", nil, opts{cookie: c}); r.code != 200 {
		t.Fatalf("devices: %d", r.code)
	}
	if r := h.call("POST", "/v1/session/logout", nil, opts{cookie: c}); r.code != 204 {
		t.Fatalf("logout %d", r.code)
	}
	if r := h.call("GET", "/v1/me", nil, opts{cookie: c}); r.code != 401 {
		t.Errorf("the owner session survived logout: %d", r.code)
	}
}

func TestCookieCSRFRules(t *testing.T) {
	h := newPairHub(t)
	c := h.ownerCookie()
	body := map[string]any{"kind": "phone", "url": pairHubURL}
	if r := h.call("POST", "/v1/pair/codes", body, opts{cookie: c, ctype: "text/plain"}); r.code != 403 {
		t.Errorf("cookie + text/plain: %d", r.code)
	}
	if r := h.call("POST", "/v1/pair/codes", body, opts{cookie: c, origin: "https://evil.example"}); r.code != 403 {
		t.Errorf("cookie + foreign Origin: %d", r.code)
	}
	if r := h.call("POST", "/v1/pair/codes", body, opts{cookie: c, origin: h.http.URL}); r.code != 201 {
		t.Errorf("cookie + same origin JSON: %d %s", r.code, r.body)
	}
	if r := h.call("POST", "/v1/pair/codes", body, opts{cookie: c}); r.code != 201 {
		t.Errorf("cookie + JSON, no Origin: %d", r.code)
	}
	// A bearer request is not affected, even with a foreign Origin and text/plain.
	if r := h.call("POST", "/v1/pair/codes", body, opts{token: c, ctype: "text/plain", origin: "https://evil.example"}); r.code != 201 {
		t.Errorf("bearer + text/plain + foreign Origin: %d %s", r.code, r.body)
	}
	// A GET with a cookie needs nothing.
	if r := h.call("GET", "/v1/me", nil, opts{cookie: c, origin: "https://evil.example"}); r.code != 200 {
		t.Errorf("cookie GET: %d", r.code)
	}
}

func TestPairCodesAPI(t *testing.T) {
	h := newPairHub(t)
	owner := h.ownerCookie()
	phone, _ := h.phoneCookie("Pixel 9 · Chrome")

	if r := h.call("POST", "/v1/pair/codes", map[string]any{"kind": "phone", "url": pairHubURL}, opts{cookie: phone}); r.code != 403 {
		t.Errorf("a phone made a code: %d", r.code)
	}
	if r := h.call("POST", "/v1/pair/codes", map[string]any{"kind": "phone", "url": pairHubURL}, opts{}); r.code != 401 {
		t.Errorf("no token: %d", r.code)
	}
	mt := h.token("machine")
	if r := h.call("POST", "/v1/pair/codes", map[string]any{"kind": "phone", "url": pairHubURL}, opts{token: mt}); r.code != 403 {
		t.Errorf("a machine token made a code: %d", r.code)
	}

	// No url: the origin of the request is http, which a phone cannot use.
	r := h.call("POST", "/v1/pair/codes", map[string]any{"kind": "phone"}, opts{cookie: owner})
	// The fix names the hub's own port (the test server's), not a fixed 8787.
	if _, port, _ := strings.Cut(strings.TrimPrefix(h.http.URL, "http://"), ":"); r.code != 400 || !strings.Contains(string(r.body), "tailscale serve --bg "+port+"`") {
		t.Errorf("http origin for a phone: %d %s", r.code, r.body)
	}
	// With hub.public_url set it is used.
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO setting (key, value, updated_at) VALUES ('hub.public_url', '"https://pub.example.ts.net"', 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r = h.call("POST", "/v1/pair/codes", map[string]any{"kind": "phone", "name": "Anna's phone"}, opts{cookie: owner})
	if r.code != 201 {
		t.Fatalf("make code: %d %s", r.code, r.body)
	}
	c := r.json(t)
	code, secret := c["code"].(string), c["secret"].(string)
	if len(code) != 7 || code[3] != '-' || len(secret) != 22 || c["kind"] != "phone" || c["name"] != "Anna's phone" || c["command"] != nil {
		t.Errorf("code reply %v", c)
	}
	if want := "https://pub.example.ts.net/#/pair?s=" + secret + "&c=" + strings.Replace(code, "-", "", 1); c["url"] != want {
		t.Errorf("url %v, want %s", c["url"], want)
	}
	qr := c["qr"].(map[string]any)
	rows := qr["rows"].([]any)
	if int(qr["size"].(float64)) != len(rows) || len(rows) < 29 {
		t.Errorf("qr size %v rows %d", qr["size"], len(rows))
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Error("code reply is cacheable")
	}
	id := c["id"].(string)

	// The explicit url wins over the setting; machine codes carry the connect command.
	r = h.call("POST", "/v1/pair/codes", map[string]any{"kind": "machine", "url": "https://other.example.ts.net", "ttl_s": 60}, opts{cookie: owner})
	m := r.json(t)
	if r.code != 201 || !strings.HasPrefix(m["command"].(string), "sy connect https://other.example.ts.net --code ") {
		t.Errorf("machine code: %d %v", r.code, m)
	}
	for _, bad := range []map[string]any{{"kind": "owner", "url": pairHubURL}, {"kind": "phone", "url": pairHubURL, "ttl_s": 5},
		{"kind": "phone", "url": pairHubURL, "ttl_s": 99999}, {"kind": "phone", "url": "ftp://x"}, {"kind": "phone", "url": pairHubURL, "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"}} {
		if r := h.call("POST", "/v1/pair/codes", bad, opts{cookie: owner}); r.code != 400 {
			t.Errorf("%v: %d %s", bad, r.code, r.body)
		}
	}

	// Status never shows the code or secret.
	r = h.call("GET", "/v1/pair/codes/"+id, nil, opts{cookie: owner})
	st := r.json(t)
	if r.code != 200 || st["status"] != "live" || st["claimed_at"] != nil || st["device"] != nil || strings.Contains(string(r.body), secret) {
		t.Errorf("status %d %s", r.code, r.body)
	}
	// Claim it by the typed code, from "another" address, and watch the status change.
	cl := h.call("POST", "/v1/pair/claim", map[string]any{"code": code, "name": "Anna's phone"}, opts{xff: "198.51.100.7"})
	if cl.code != 200 {
		t.Fatalf("claim: %d %s", cl.code, cl.body)
	}
	st = h.call("GET", "/v1/pair/codes/"+id, nil, opts{cookie: owner}).json(t)
	if st["status"] != "claimed" || st["claimed_at"] == nil || st["device"].(map[string]any)["id"] != cl.json(t)["device"] {
		t.Errorf("claimed status %v", st)
	}
	var ip string
	_ = h.st.R().QueryRow(`SELECT claim_ip FROM pair_code WHERE id = ?`, id).Scan(&ip)
	if ip != "198.51.100.7" {
		t.Errorf("claim_ip %q, want the right-most X-Forwarded-For entry", ip)
	}

	// Cancel.
	r = h.call("POST", "/v1/pair/codes", map[string]any{"kind": "browser", "url": pairHubURL}, opts{cookie: owner})
	cid := r.json(t)["id"].(string)
	if r := h.call("DELETE", "/v1/pair/codes/"+cid, nil, opts{cookie: owner, ctype: "application/json"}); r.code != 204 {
		t.Errorf("cancel: %d", r.code)
	}
	if st := h.call("GET", "/v1/pair/codes/"+cid, nil, opts{cookie: owner}).json(t); st["status"] != "cancelled" {
		t.Errorf("status after cancel %v", st)
	}
	if r := h.call("DELETE", "/v1/pair/codes/"+id, nil, opts{cookie: owner, ctype: "application/json"}); r.code != 204 {
		t.Errorf("cancel a claimed code: %d", r.code)
	}
	for _, m := range []string{"GET", "DELETE"} {
		if r := h.call(m, "/v1/pair/codes/pc_nope", nil, opts{cookie: owner, ctype: "application/json"}); r.code != 404 {
			t.Errorf("%s unknown code: %d", m, r.code)
		}
	}
}

func TestClaimAPI(t *testing.T) {
	h := newPairHub(t)
	ctx := context.Background()
	mk := func(kind string) pair.Code {
		c, err := h.svc.Create(ctx, pair.CreateRequest{Kind: kind, URL: pairHubURL})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	same := opts{origin: h.http.URL}

	// Cookie mode: the token is only in the cookie.
	p := mk("phone")
	r := h.call("POST", "/v1/pair/claim", map[string]any{"secret": p.Secret, "name": "iPhone · Safari", "cookie": true, "expect_kind": "phone", "client": "pwa/3f9c01ab"}, same)
	if r.code != 200 {
		t.Fatalf("claim: %d %s", r.code, r.body)
	}
	c := sessionCookie(t, r)
	b := r.json(t)
	if _, has := b["token"]; has || strings.Contains(string(r.body), c.Value) {
		t.Errorf("cookie mode leaked the token in the body: %s", r.body)
	}
	if !strings.HasPrefix(c.Value, "sy_p_") || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 400*24*3600 {
		t.Errorf("cookie %+v", c)
	}
	if fmt.Sprint(b["scopes"]) != "[read push]" || b["kind"] != "phone" || b["name"] != "iPhone · Safari" || b["hub"].(map[string]any)["url"] != pairHubURL {
		t.Errorf("reply %v", b)
	}
	me := h.call("GET", "/v1/me", nil, opts{cookie: c.Value}).json(t)
	if me["owner"] != false || me["device"].(map[string]any)["kind"] != "phone" {
		t.Errorf("me %v", me)
	}
	// Replay.
	if r := h.call("POST", "/v1/pair/claim", map[string]any{"secret": p.Secret, "cookie": true}, same); r.code != 401 || !strings.Contains(string(r.body), "invalid or expired code") {
		t.Errorf("replay: %d %s", r.code, r.body)
	}

	// Cookie mode needs a same-origin JSON request; a refusal leaves the code live.
	q := mk("phone")
	if r := h.call("POST", "/v1/pair/claim", map[string]any{"secret": q.Secret, "cookie": true}, opts{origin: "https://evil.example"}); r.code != 403 {
		t.Errorf("cross-site cookie claim: %d", r.code)
	}
	if r := h.call("POST", "/v1/pair/claim", map[string]any{"secret": q.Secret, "cookie": true}, opts{ctype: "text/plain"}); r.code != 403 {
		t.Errorf("text/plain cookie claim: %d", r.code)
	}
	// A claim with the owner's cookie replaces it: the owner session is revoked, as on logout.
	owner := h.ownerCookie()
	r = h.call("POST", "/v1/pair/claim", map[string]any{"secret": q.Secret, "cookie": true, "expect_kind": "phone"}, opts{origin: h.http.URL, cookie: owner})
	if r.code != 200 {
		t.Fatalf("claim after refusals: %d %s", r.code, r.body)
	}
	if r := h.call("GET", "/v1/me", nil, opts{cookie: owner}); r.code != 401 {
		t.Errorf("the replaced owner session stayed valid: %d", r.code)
	}

	// Token mode (CLI, phase 8): the token is in the body, the reply is not cacheable.
	m := mk("machine")
	mid := "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"
	if r := h.call("POST", "/v1/pair/claim", map[string]any{"code": m.Digits, "expect_kind": "phone", "machine": mid}, opts{}); r.code != 409 {
		t.Errorf("expect_kind mismatch: %d %s", r.code, r.body)
	}
	if r := h.call("POST", "/v1/pair/claim", map[string]any{"code": m.Code}, opts{}); r.code != 400 || !strings.Contains(string(r.body), "machine is required") {
		t.Errorf("machine required: %d %s", r.code, r.body)
	}
	r = h.call("POST", "/v1/pair/claim", map[string]any{"code": m.Code, "machine": mid, "name": "gpu-box", "expect_kind": "machine"}, opts{})
	if r.code != 200 || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("machine claim: %d %v %s", r.code, r.header, r.body)
	}
	b = r.json(t)
	tok, _ := b["token"].(string)
	if !strings.HasPrefix(tok, "sy_m_") || b["machine"] != mid || fmt.Sprint(b["scopes"]) != "[write]" {
		t.Errorf("machine reply %v", b)
	}
	if code, _ := h.do("GET", "/v1/overview", tok, nil); code != 403 {
		t.Errorf("a machine token read: %d", code)
	}
	// Cookie mode for a machine code is refused.
	m2 := mk("machine")
	if r := h.call("POST", "/v1/pair/claim", map[string]any{"code": m2.Digits, "machine": mid, "cookie": true}, same); r.code != 400 {
		t.Errorf("cookie for a machine: %d", r.code)
	}
	// Owner links are not claimable here.
	l, _ := h.svc.CreateOwnerLink(ctx, pair.OwnerLinkRequest{URL: pairHubURL})
	if r := h.call("POST", "/v1/pair/claim", map[string]any{"secret": l.Secret}, opts{}); r.code != 409 {
		t.Errorf("owner link at claim: %d", r.code)
	}
	for _, bad := range []any{map[string]any{}, map[string]any{"code": "123456", "secret": "x"}, "not an object"} {
		if r := h.call("POST", "/v1/pair/claim", bad, opts{}); r.code != 400 {
			t.Errorf("%v: %d", bad, r.code)
		}
	}
}

func TestClaimRateLimitsAPI(t *testing.T) {
	h := newPairHub(t)
	ctx := context.Background()
	live, err := h.svc.Create(ctx, pair.CreateRequest{Kind: "phone", URL: pairHubURL})
	if err != nil {
		t.Fatal(err)
	}
	wrong := func(xff string) resp {
		return h.call("POST", "/v1/pair/claim", map[string]any{"code": "000000"}, opts{xff: xff})
	}
	// Per IP: five wrong guesses, then 429 with Retry-After, even for the right code. A
	// forged left-most X-Forwarded-For entry does not make a new address.
	for i := range 5 {
		if r := wrong(fmt.Sprintf("1.1.1.%d, 203.0.113.9", i)); r.code != 401 {
			t.Fatalf("guess %d: %d", i, r.code)
		}
	}
	r := h.call("POST", "/v1/pair/claim", map[string]any{"code": live.Digits}, opts{xff: "9.9.9.9, 203.0.113.9"})
	if r.code != 429 || r.header.Get("Retry-After") == "" {
		t.Fatalf("locked IP: %d %v", r.code, r.header)
	}
	// ...but the QR secret from that IP works.
	// (Checked last: it consumes the code.)

	// Hub-wide: many addresses, each with a few wrong guesses, lock typed claims for all.
	for i := range 20 {
		wrong(fmt.Sprintf("198.51.100.%d", i+1))
	}
	if r := h.call("POST", "/v1/pair/claim", map[string]any{"code": live.Digits}, opts{xff: "192.0.2.77"}); r.code != 429 {
		t.Fatalf("hub-wide lockout: %d", r.code)
	}
	// The QR path still works during the hub-wide lockout, and from the locked IP.
	r = h.call("POST", "/v1/pair/claim", map[string]any{"secret": live.Secret, "name": "x"}, opts{xff: "203.0.113.9"})
	if r.code != 200 {
		t.Fatalf("QR claim during a lockout: %d %s", r.code, r.body)
	}
	// Owner sign-in is rate limited too: 20 requests a minute per IP.
	var last int
	for range 25 {
		last = h.call("POST", "/v1/session/owner", map[string]string{"secret": strings.Repeat("B", 22)}, opts{xff: "203.0.113.50"}).code
	}
	if last != 429 {
		t.Errorf("owner sign-in flood: last status %d", last)
	}
}

func (h *pairHub) insertTargets(t *testing.T, device string, ids ...string) {
	t.Helper()
	err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO alert (id, episode, trigger, status, created_at, facts, updated_at) VALUES ('al_1', 'e1', 'asked', 'fired', 1, '{}', 1)`); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec(`INSERT INTO push_target (id, kind, device_id, name, endpoint, last_ok_at, created_at, updated_at) VALUES (?, 'webpush', ?, 'Safari', ?, 5, 1, 1)`,
				id, device, "https://web.push.apple.com/"+id); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, next_at, created_at, updated_at) VALUES ('al_1', ?, 'webpush', 1, 'pending', 9, 1, 1)`, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDevicesAPI(t *testing.T) {
	h := newPairHub(t)
	owner := h.ownerCookie()
	phone, phoneID := h.phoneCookie("iPhone · Safari")
	phone2, phone2ID := h.phoneCookie("Pixel 9 · Chrome")
	h.insertTargets(t, phoneID, "pt_one")

	// List: owner only.
	if r := h.call("GET", "/v1/devices", nil, opts{cookie: phone}); r.code != 403 {
		t.Errorf("phone listed devices: %d", r.code)
	}
	r := h.call("GET", "/v1/devices", nil, opts{cookie: owner})
	if r.code != 200 {
		t.Fatalf("list: %d %s", r.code, r.body)
	}
	var list struct{ Devices []map[string]any }
	_ = json.Unmarshal(r.body, &list)
	byID := map[string]map[string]any{}
	for _, d := range list.Devices {
		byID[d["id"].(string)] = d
	}
	if len(list.Devices) != 3 || byID[phoneID] == nil || byID[phone2ID] == nil {
		t.Fatalf("devices %s", r.body)
	}
	var cur, ownerSess int
	for _, d := range list.Devices {
		if d["current"] == true {
			cur++
		}
		if d["owner_session"] == true {
			ownerSess++
		}
	}
	if cur != 1 || ownerSess != 1 {
		t.Errorf("current %d owner_session %d", cur, ownerSess)
	}
	tg := byID[phoneID]["targets"].([]any)
	if len(tg) != 1 || tg[0].(map[string]any)["status"] != "ok" || tg[0].(map[string]any)["endpoint_host"] != "web.push.apple.com" || tg[0].(map[string]any)["device"] != phoneID {
		t.Errorf("targets %v", tg)
	}
	if p := byID[phoneID]["prefs"].(map[string]any); p["tap"] != "auto" || fmt.Sprint(p["mute"]) != "[]" {
		t.Errorf("default prefs %v", p)
	}
	if byID[phoneID]["created_by"] == nil || !strings.HasPrefix(byID[phoneID]["created_by"].(string), "pair:pc_") {
		t.Errorf("created_by %v", byID[phoneID]["created_by"])
	}

	// self.
	own := h.call("GET", "/v1/devices/self", nil, opts{cookie: phone}).json(t)
	if own["id"] != phoneID || own["current"] != true || len(own["targets"].([]any)) != 1 {
		t.Errorf("self %v", own)
	}
	if r := h.call("GET", "/v1/devices/"+phoneID, nil, opts{cookie: phone}); r.code != 200 {
		t.Errorf("own id: %d", r.code)
	}
	if r := h.call("GET", "/v1/devices/"+phone2ID, nil, opts{cookie: phone}); r.code != 403 {
		t.Errorf("another device as a phone: %d", r.code)
	}
	if r := h.call("GET", "/v1/devices/"+phone2ID, nil, opts{cookie: owner}); r.code != 200 {
		t.Errorf("another device as owner: %d", r.code)
	}
	if r := h.call("GET", "/v1/devices/dev_nope", nil, opts{cookie: owner}); r.code != 404 {
		t.Errorf("unknown: %d", r.code)
	}

	// PATCH name and prefs.
	r = h.call("PATCH", "/v1/devices/self", map[string]any{"name": "  Anna's iPhone ", "prefs": map[string]any{
		"mute": []string{"limit_80"}, "private": true, "tap": "claude", "tz": "Asia/Kolkata",
		"quiet_hours": map[string]any{"enabled": true, "start": "22:30", "end": "07:00", "critical": false}}}, opts{cookie: phone})
	got := r.json(t)
	if r.code != 200 || got["name"] != "Anna's iPhone" {
		t.Fatalf("patch: %d %s", r.code, r.body)
	}
	p := got["prefs"].(map[string]any)
	if fmt.Sprint(p["mute"]) != "[limit_80]" || p["private"] != true || p["tap"] != "claude" || p["tz"] != "Asia/Kolkata" ||
		p["quiet_hours"].(map[string]any)["start"] != "22:30" {
		t.Errorf("prefs %v", p)
	}
	var raw string
	_ = h.st.R().QueryRow(`SELECT prefs FROM device WHERE id = ?`, phoneID).Scan(&raw)
	if !strings.Contains(raw, `"limit_80"`) {
		t.Errorf("stored prefs %s", raw)
	}
	// prefs replaces the whole object.
	r = h.call("PATCH", "/v1/devices/self", map[string]any{"prefs": map[string]any{"tap": "session"}}, opts{cookie: phone})
	if p := r.json(t)["prefs"].(map[string]any); p["tap"] != "session" || fmt.Sprint(p["mute"]) != "[]" || p["private"] != nil || p["quiet_hours"] != nil {
		t.Errorf("replaced prefs %v", p)
	}
	for _, bad := range []map[string]any{
		{}, {"name": ""}, {"name": strings.Repeat("n", 61)}, {"prefs": map[string]any{"tap": "loud"}},
		{"prefs": map[string]any{"mute": []string{"nonsense"}}}, {"prefs": map[string]any{"quiet_hours": map[string]any{"start": "25:00", "end": "07:00"}}},
		{"prefs": map[string]any{"tz": "Not A Zone!"}}, {"prefs": "text"},
		// Review phase 4 finding 7: well-formed but not a real zone.
		{"prefs": map[string]any{"tz": "Mars/Olympus"}},
		{"prefs": map[string]any{"quiet_hours": map[string]any{"start": "22:00", "end": "07:00", "tz": "Mars/Olympus"}}},
	} {
		if r := h.call("PATCH", "/v1/devices/self", bad, opts{cookie: phone}); r.code != 400 {
			t.Errorf("%v: %d %s", bad, r.code, r.body)
		}
	}
	if r := h.call("PATCH", "/v1/devices/"+phone2ID, map[string]any{"name": "mine now"}, opts{cookie: phone}); r.code != 403 {
		t.Errorf("a phone renamed another device: %d", r.code)
	}
	if r := h.call("PATCH", "/v1/devices/"+phone2ID, map[string]any{"name": "Renamed by owner"}, opts{cookie: owner}); r.code != 200 {
		t.Errorf("owner rename: %d", r.code)
	}
	if r := h.call("PATCH", "/v1/devices/dev_nope", map[string]any{"name": "x"}, opts{cookie: owner}); r.code != 404 {
		t.Errorf("patch unknown: %d", r.code)
	}
	// A phone's cookie PATCH must be JSON.
	if r := h.call("PATCH", "/v1/devices/self", map[string]any{"name": "x"}, opts{cookie: phone, ctype: "text/plain"}); r.code != 403 {
		t.Errorf("text/plain patch: %d", r.code)
	}

	// The owner cannot revoke the session making the request, by id or self.
	ownerID := ""
	for id, d := range byID {
		if d["current"] == true {
			ownerID = id
		}
	}
	for _, id := range []string{ownerID, "self"} {
		if r := h.call("DELETE", "/v1/devices/"+id, nil, opts{cookie: owner, ctype: "application/json"}); r.code != 409 {
			t.Errorf("owner revoked itself via %s: %d", id, r.code)
		}
	}
	if r := h.call("GET", "/v1/me", nil, opts{cookie: owner}); r.code != 200 {
		t.Errorf("the owner session died: %d", r.code)
	}

	// The owner revokes the first phone: token, targets and pending deliveries go.
	r = h.call("DELETE", "/v1/devices/"+phoneID, nil, opts{cookie: owner, ctype: "application/json", origin: h.http.URL})
	if r.code != 204 {
		t.Fatalf("revoke: %d %s", r.code, r.body)
	}
	if r := h.call("GET", "/v1/me", nil, opts{cookie: phone}); r.code != 401 {
		t.Errorf("revoked phone: %d", r.code)
	}
	if n := h.count(`SELECT count(*) FROM push_target WHERE device_id = ?`, phoneID); n != 0 {
		t.Errorf("%d targets left", n)
	}
	if n := h.count(`SELECT count(*) FROM delivery WHERE target_id = 'pt_one' AND status = 'pending'`); n != 0 {
		t.Errorf("%d pending deliveries left", n)
	}
	if r := h.call("DELETE", "/v1/devices/"+phoneID, nil, opts{cookie: owner, ctype: "application/json"}); r.code != 204 {
		t.Errorf("revoke again: %d", r.code)
	}
	if r := h.call("DELETE", "/v1/devices/dev_nope", nil, opts{cookie: owner, ctype: "application/json"}); r.code != 404 {
		t.Errorf("revoke unknown: %d", r.code)
	}
	if r := h.call("GET", "/v1/devices", nil, opts{cookie: owner}); strings.Contains(string(r.body), phoneID) {
		t.Error("a revoked device is listed without all=true")
	}
	if r := h.call("GET", "/v1/devices?all=true", nil, opts{cookie: owner}); !strings.Contains(string(r.body), phoneID) {
		t.Error("all=true hides the revoked device")
	}

	// A phone unpairs itself: 204, the cookie is cleared, the token stops working.
	h.insertTargets(t, phone2ID, "pt_two")
	r = h.call("DELETE", "/v1/devices/self", nil, opts{cookie: phone2, ctype: "application/json"})
	if r.code != 204 {
		t.Fatalf("unpair: %d %s", r.code, r.body)
	}
	if c := sessionCookie(t, r); c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("cookie not cleared: %+v", c)
	}
	if r := h.call("GET", "/v1/me", nil, opts{cookie: phone2}); r.code != 401 {
		t.Errorf("unpaired phone: %d", r.code)
	}
	if n := h.count(`SELECT count(*) FROM push_target`); n != 0 {
		t.Errorf("%d targets left after unpair", n)
	}
	// An owner revoking someone else does not clear its own cookie.
	phone3, p3 := h.phoneCookie("iPad")
	_ = phone3
	r = h.call("DELETE", "/v1/devices/"+p3, nil, opts{cookie: owner, ctype: "application/json"})
	for _, c := range r.cookies {
		if c.Name == auth.CookieName {
			t.Error("the owner's cookie was cleared when revoking another device")
		}
	}
}

func TestMeForEveryKind(t *testing.T) {
	h := newPairHub(t)
	for _, kind := range []string{"machine", "desktop", "phone", "browser"} {
		me := h.call("GET", "/v1/me", nil, opts{token: h.token(kind)}).json(t)
		if me["owner"] != false || me["device"].(map[string]any)["kind"] != kind {
			t.Errorf("%s: %v", kind, me)
		}
	}
	if r := h.call("GET", "/v1/me", nil, opts{}); r.code != 401 {
		t.Errorf("anonymous: %d", r.code)
	}
	if me := h.call("GET", "/v1/me", nil, opts{token: h.token("phone")}).json(t); me["hub"].(map[string]any)["public_url"] != nil {
		t.Errorf("public_url %v", me["hub"])
	}
}

func TestStreamEndsForADeviceRevokedFromTheList(t *testing.T) {
	old := api.PairPepperPath
	api.PairPepperPath = filepath.Join(t.TempDir(), "pair_pepper")
	t.Cleanup(func() { api.PairPepperPath = old })
	h := &pairHub{hub: startHubPing(t, "", 50*time.Millisecond)}
	h.svc = pair.NewService(h.st, api.PairPepperPath)
	owner := h.ownerCookie()
	phone, phoneID := h.phoneCookie("iPhone · Safari")
	req, _ := http.NewRequest("GET", h.http.URL+"/v1/stream", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: phone})
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stream: %v %v", res, err)
	}
	defer res.Body.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, res.Body); close(done) }()
	time.Sleep(120 * time.Millisecond)
	if r := h.call("DELETE", "/v1/devices/"+phoneID, nil, opts{cookie: owner, ctype: "application/json"}); r.code != 204 {
		t.Fatalf("revoke: %d", r.code)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream of a revoked device stayed open")
	}
}

// The revoke itself ends the stream, not the next ping: with an hour between pings, a revoked
// board must not keep getting live updates (the confirm says "It stops working at once").
func TestStreamEndsAtOnceOnRevoke(t *testing.T) {
	old := api.PairPepperPath
	api.PairPepperPath = filepath.Join(t.TempDir(), "pair_pepper")
	t.Cleanup(func() { api.PairPepperPath = old })
	h := &pairHub{hub: startHubPing(t, "", time.Hour)}
	h.svc = pair.NewService(h.st, api.PairPepperPath)
	owner := h.ownerCookie()
	phone, phoneID := h.phoneCookie("Pixel 9 · Chrome")
	req, _ := http.NewRequest("GET", h.http.URL+"/v1/stream", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: phone})
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stream: %v %v", res, err)
	}
	defer res.Body.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, res.Body); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if r := h.call("DELETE", "/v1/devices/"+phoneID, nil, opts{cookie: owner, ctype: "application/json"}); r.code != 204 {
		t.Fatalf("revoke: %d", r.code)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the stream of a revoked device stayed open until the next ping")
	}
}
