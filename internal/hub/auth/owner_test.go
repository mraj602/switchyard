package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

var testPepper = []byte("0123456789abcdef0123456789abcdef")

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newAuth(t *testing.T) (*Authenticator, *store.Store, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: t0}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := New(st)
	a.Now = clk.Now
	a.Pepper = func() ([]byte, error) { return testPepper, nil }
	return a, st, clk
}

func ownerSession(t *testing.T, st *store.Store) Created {
	t.Helper()
	c, err := Create(context.Background(), st, CreateRequest{Kind: "browser", Name: "owner", Owner: true, CreatedBy: "owner-link:pc_x", Pepper: testPepper})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOwnerSessionShapeScopesAndExpiry(t *testing.T) {
	a, st, clk := newAuth(t)
	c := ownerSession(t, st)
	if !strings.HasPrefix(c.Token, "sy_o_") || !LooksLikeToken(c.Token) {
		t.Fatalf("token %q", c.Token[:6])
	}
	if got := strings.Join(c.Device.Scopes, ","); got != "owner,read,push" || c.Device.Kind != "browser" {
		t.Errorf("device %+v", c.Device)
	}
	ctx := context.Background()
	d, err := a.LookupDetail(ctx, c.Token)
	if err != nil || d.ExpiresAt != t0.Add(30*24*time.Hour).UnixMilli() || !IsOwnerSession(d.Device) || !d.HasScope("owner") {
		t.Fatalf("lookup %+v %v", d, err)
	}
	clk.Add(30*24*time.Hour - time.Millisecond)
	if _, err := a.Lookup(ctx, c.Token); err != nil {
		t.Errorf("1 ms before expiry: %v", err)
	}
	if !a.Alive(ctx, c.Device) {
		t.Error("stream would close 1 ms before expiry")
	}
	clk.Add(time.Millisecond)
	if _, err := a.Lookup(ctx, c.Token); err != ErrUnauthorized {
		t.Errorf("at expires_at: %v", err)
	}
	if a.Alive(ctx, c.Device) {
		t.Error("an expired session keeps its stream")
	}
	// Other devices never expire; making one with the owner flag needs a browser.
	if _, err := Create(context.Background(), st, CreateRequest{Kind: "phone", Name: "x", Owner: true}); err == nil {
		t.Error("owner session of kind phone")
	}
	p, _ := Create(ctx, st, CreateRequest{Kind: "phone", Name: "phone"})
	clk.Add(3000 * 24 * time.Hour)
	if _, err := a.Lookup(ctx, p.Token); err != nil {
		t.Errorf("phone devices must not expire: %v", err)
	}
	if _, err := NewToken("owner"); err != nil {
		t.Error(err)
	}
	for _, k := range Kinds() {
		if k == "owner" {
			t.Error("wy hub token create must not offer --kind owner")
		}
	}
	if _, err := Create(ctx, st, CreateRequest{Kind: "owner", Name: "x"}); err == nil {
		t.Error("kind owner accepted by Create")
	}
}

// Sign out revokes the device behind the cookie: an owner session, and a phone or browser
// paired in cookie mode (pairing.md 6). A token made with `wy hub token create` (pasted, used
// as a bearer elsewhere) is not revoked by a cookie that happens to hold it, and no other
// device is touched: the owner signing out of one browser leaves the phone it paired alone.
func TestLogoutRevokesTheCookiesDeviceOnly(t *testing.T) {
	a, st, _ := newAuth(t)
	ctx := context.Background()
	o := ownerSession(t, st)
	phone, _ := Create(ctx, st, CreateRequest{Kind: "phone", Name: "phone", CreatedBy: "pair:pc_y"})
	browser, _ := Create(ctx, st, CreateRequest{Kind: "browser", Name: "laptop", CreatedBy: "pair:pc_z"})
	cli, _ := Create(ctx, st, CreateRequest{Kind: "browser", Name: "kiosk"})
	bystander, _ := Create(ctx, st, CreateRequest{Kind: "phone", Name: "other", CreatedBy: "pair:pc_w"})
	for _, tc := range []struct {
		name   string
		c      Created
		revoke bool
	}{{"owner", o, true}, {"phone", phone, true}, {"browser", browser, true}, {"cli", cli, false}} {
		name := tc.name
		req := httptest.NewRequest("POST", "/v1/session/logout", nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: tc.c.Token})
		rec := httptest.NewRecorder()
		a.LogoutHandler().ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("%s: %d", name, rec.Code)
		}
		_, err := a.Lookup(ctx, tc.c.Token)
		if (err == ErrUnauthorized) != tc.revoke {
			t.Errorf("%s: revoked=%v, want %v", name, err == ErrUnauthorized, tc.revoke)
		}
		if ck := rec.Result().Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
			t.Errorf("%s: cookie not cleared", name)
		}
	}
	if _, err := a.Lookup(ctx, bystander.Token); err != nil {
		t.Errorf("a sign-out revoked another device: %v", err)
	}
	// A foreign Origin only clears the cookie.
	other, _ := Create(ctx, st, CreateRequest{Kind: "phone", Name: "x", CreatedBy: "pair:pc_v"})
	req := httptest.NewRequest("POST", "/v1/session/logout", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(&http.Cookie{Name: CookieName, Value: other.Token})
	a.LogoutHandler().ServeHTTP(httptest.NewRecorder(), req)
	if _, err := a.Lookup(ctx, other.Token); err != nil {
		t.Errorf("a cross-origin sign-out revoked the device: %v", err)
	}
}

// pairing.md 4: an owner session used in its last 7 days slides to 30 days from now, and its
// cookie is set again; earlier it is left alone.
func TestOwnerSessionRenewsInItsLastWeek(t *testing.T) {
	a, st, clk := newAuth(t)
	ctx := context.Background()
	o := ownerSession(t, st)
	h := a.Require(Options{}, "owner")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v1/devices", nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: o.Token})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	exp := func() int64 {
		d, err := st.DeviceDetailByID(ctx, o.Device.ID)
		if err != nil {
			t.Fatal(err)
		}
		return d.ExpiresAt
	}
	first := exp()
	clk.Add(22 * 24 * time.Hour) // 8 days left: not yet
	if rec := call(); rec.Code != 204 || len(rec.Result().Cookies()) != 0 || exp() != first {
		t.Fatalf("renewed too early: %d, %d cookies, expiry moved %v", rec.Code, len(rec.Result().Cookies()), exp() != first)
	}
	clk.Add(2 * 24 * time.Hour) // 6 days left
	rec := call()
	if rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	if want := clk.Now().Add(OwnerSessionAge).UnixMilli(); exp() != want {
		t.Errorf("expiry %d, want %d (30 days from now)", exp(), want)
	}
	ck := rec.Result().Cookies()
	if len(ck) != 1 || ck[0].Value != o.Token || ck[0].MaxAge != int(OwnerSessionAge.Seconds()) {
		t.Errorf("cookie not set again with 30 days: %+v", ck)
	}
	// Unused for 30 days: it ends, and the 401 says why.
	clk.Add(OwnerSessionAge + time.Minute)
	rec = call()
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"reason":"owner_expired"`) {
		t.Errorf("expired owner session: %d %s", rec.Code, rec.Body.String())
	}
}

// A 401 says why when the token belongs to a device: revoked, or expired; never for an
// unknown token.
func TestUnauthorizedReason(t *testing.T) {
	a, st, _ := newAuth(t)
	ctx := context.Background()
	h := a.Require(Options{}, "read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	phone, _ := Create(ctx, st, CreateRequest{Kind: "phone", Name: "phone", CreatedBy: "pair:pc_y"})
	if err := Revoke(ctx, st, phone.Device.ID); err != nil {
		t.Fatal(err)
	}
	unknown, _ := NewToken("phone")
	for tok, want := range map[string]string{phone.Token: `"reason":"revoked"`, unknown: ""} {
		req := httptest.NewRequest("GET", "/v1/overview", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code != 401 || (want != "" && !strings.Contains(body, want)) || (want == "" && strings.Contains(body, "reason")) {
			t.Errorf("%d %s, want reason %q", rec.Code, body, want)
		}
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func TestCookieRequestsNeedJSONAndSameOrigin(t *testing.T) {
	a, st, _ := newAuth(t)
	o := ownerSession(t, st)
	h := a.Require(Options{}, "owner")(okHandler())
	do := func(method, ctype, origin string, cookie bool) int {
		req := httptest.NewRequest(method, "http://hub.example.ts.net/v1/x", nil)
		req.Host = "hub.example.ts.net"
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: o.Token})
		} else {
			req.Header.Set("Authorization", "Bearer "+o.Token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, c := range []struct {
		name                  string
		method, ctype, origin string
		cookie                bool
		want                  int
	}{
		{"cookie POST json same origin", "POST", "application/json", "https://hub.example.ts.net", true, 204},
		{"cookie POST json charset", "POST", "application/json; charset=utf-8", "", true, 204},
		{"cookie POST json no origin", "POST", "application/json", "", true, 204},
		{"cookie POST text/plain", "POST", "text/plain", "", true, 403},
		{"cookie POST form", "DELETE", "application/x-www-form-urlencoded", "", true, 403},
		{"cookie POST no content type", "PATCH", "", "", true, 403},
		{"cookie POST wrong origin", "POST", "application/json", "https://evil.example", true, 403},
		{"cookie POST null origin", "POST", "application/json", "null", true, 403},
		{"cookie POST lookalike origin", "POST", "application/json", "https://hub.example.ts.net.evil.example", true, 403},
		{"cookie GET needs nothing", "GET", "", "https://evil.example", true, 204},
		{"bearer POST text/plain", "POST", "text/plain", "https://evil.example", false, 204},
		{"bearer DELETE no content type", "DELETE", "", "", false, 204},
	} {
		if got := do(c.method, c.ctype, c.origin, c.cookie); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	// A proxy that rewrites Host is covered by the configured public URL.
	a.PublicURL = func(context.Context) string { return "https://public.example.ts.net" }
	req := httptest.NewRequest("POST", "http://127.0.0.1:8787/v1/x", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://public.example.ts.net")
	req.AddCookie(&http.Cookie{Name: CookieName, Value: o.Token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Errorf("origin of the public URL refused: %d", rec.Code)
	}
}

func TestClaimedCookieIsRefreshedOncePerDay(t *testing.T) {
	a, st, clk := newAuth(t)
	phone, _ := Create(context.Background(), st, CreateRequest{Kind: "phone", Name: "phone", CreatedBy: "pair:pc_z"})
	login, _ := Create(context.Background(), st, CreateRequest{Kind: "browser", Name: "board", CreatedBy: "login:dev_x", Scopes: []string{"read"}})
	h := a.Require(Options{}, "read")(okHandler())
	get := func(tok string) []*http.Cookie {
		req := httptest.NewRequest("GET", "https://hub.example.ts.net/v1/x", nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result().Cookies()
	}
	ck := get(phone.Token)
	if len(ck) != 1 || ck[0].Value != phone.Token || ck[0].MaxAge != int(ClaimedCookieAge.Seconds()) || !ck[0].HttpOnly ||
		ck[0].SameSite != http.SameSiteStrictMode || ck[0].Path != "/" || !ck[0].Secure {
		t.Fatalf("first refresh %+v", ck)
	}
	if ClaimedCookieAge != 400*24*time.Hour {
		t.Error("400 days")
	}
	if ck := get(phone.Token); len(ck) != 0 {
		t.Error("refreshed twice in a minute")
	}
	clk.Add(23 * time.Hour)
	if ck := get(phone.Token); len(ck) != 0 {
		t.Error("refreshed within a day")
	}
	clk.Add(2 * time.Hour)
	if ck := get(phone.Token); len(ck) != 1 {
		t.Error("not refreshed after a day")
	}
	if ck := get(login.Token); len(ck) != 0 {
		t.Error("a board login session must not be refreshed (30 days, fixed)")
	}
}

func TestClientIPRightMostForwardedOnlyFromLoopback(t *testing.T) {
	mk := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	for _, c := range []struct {
		name string
		r    *http.Request
		want string
	}{
		{"loopback proxy, one entry", mk("127.0.0.1:5555", "203.0.113.9"), "203.0.113.9"},
		{"right-most wins over spoofed left entries", mk("127.0.0.1:5555", "6.6.6.6, 203.0.113.9"), "203.0.113.9"},
		{"several header lines", mk("[::1]:5555", "6.6.6.6", "203.0.113.9"), "203.0.113.9"},
		{"remote peer cannot spoof", mk("198.51.100.4:5555", "6.6.6.6"), "198.51.100.4"},
		{"loopback without header", mk("127.0.0.1:5555"), "127.0.0.1"},
		{"garbage entry falls back", mk("127.0.0.1:5555", "not-an-ip"), "127.0.0.1"},
		{"ipv6 client", mk("127.0.0.1:1", "2001:db8::1"), "2001:db8::1"},
	} {
		if got := ClientIP(c.r); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSessionCookieSecureOnlyOverHTTPS(t *testing.T) {
	r := httptest.NewRequest("GET", "http://127.0.0.1:8787/", nil)
	if SessionCookie(r, "t", time.Hour).Secure {
		t.Error("Secure on plain http: the browser would drop the cookie")
	}
	r.Header.Set("X-Forwarded-Proto", "https") // loopback proxy says it terminated TLS
	r.RemoteAddr = "127.0.0.1:1"
	if c := SessionCookie(r, "t", time.Hour); !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie %+v", c)
	}
	r.RemoteAddr = "198.51.100.4:1" // a remote peer's header is not believed
	if SessionCookie(r, "t", time.Hour).Secure {
		t.Error("X-Forwarded-Proto trusted from a remote peer")
	}
}

func TestRevokeCascadeDeletesTargetsAndCancelsDeliveries(t *testing.T) {
	_, st, _ := newAuth(t)
	ctx := context.Background()
	phone, _ := Create(ctx, st, CreateRequest{Kind: "phone", Name: "phone"})
	other, _ := Create(ctx, st, CreateRequest{Kind: "phone", Name: "ipad"})
	sess, _ := Create(ctx, st, CreateRequest{Kind: "browser", Name: "board", CreatedBy: "login:" + phone.Device.ID, Parent: phone.Device.ID, Scopes: []string{"read"}})
	err := st.Write(ctx, func(tx *store.Tx) error {
		for _, q := range []string{
			`INSERT INTO alert (id, episode, trigger, status, created_at, facts, updated_at) VALUES ('al_1', 'e1', 'asked', 'fired', 1, '{}', 1)`,
			`INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at) VALUES ('pt_a', 'webpush', '` + phone.Device.ID + `', 'https://push.example/a', 1, 1)`,
			`INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at) VALUES ('pt_b', 'webpush', '` + other.Device.ID + `', 'https://push.example/b', 1, 1)`,
			`INSERT INTO push_target (id, kind, device_id, endpoint, created_at, updated_at) VALUES ('pt_c', 'ntfy', NULL, 'https://ntfy.sh/sy-x', 1, 1)`,
			`INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, next_at, created_at, updated_at) VALUES ('al_1', 'pt_a', 'webpush', 1, 'pending', 5, 1, 1)`,
			`INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, next_at, created_at, updated_at) VALUES ('al_1', 'pt_a', 'webpush', 2, 'held', 6, 1, 1)`,
			`INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, created_at, updated_at) VALUES ('al_1', 'pt_a', 'webpush', 3, 'sent', 1, 1)`,
			`INSERT INTO delivery (alert_id, target_id, target_kind, fire, status, next_at, created_at, updated_at) VALUES ('al_1', 'pt_b', 'webpush', 1, 'pending', 5, 1, 1)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Revoke(ctx, st, phone.Device.ID); err != nil {
		t.Fatal(err)
	}
	count := func(q string) (n int) {
		if err := st.R().QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM push_target WHERE device_id = '` + phone.Device.ID + `'`); n != 0 {
		t.Errorf("%d targets of the revoked device remain", n)
	}
	if n := count(`SELECT count(*) FROM push_target`); n != 2 {
		t.Errorf("other targets touched: %d left, want 2", n)
	}
	if n := count(`SELECT count(*) FROM delivery WHERE target_id = 'pt_a' AND status IN ('pending', 'held') OR next_at IS NOT NULL AND target_id = 'pt_a'`); n != 0 {
		t.Errorf("%d pending deliveries of the revoked device remain", n)
	}
	if n := count(`SELECT count(*) FROM delivery WHERE target_id = 'pt_a' AND status = 'skipped' AND reason = 'revoked'`); n != 2 {
		t.Errorf("%d deliveries skipped, want 2", n)
	}
	if n := count(`SELECT count(*) FROM delivery WHERE target_id = 'pt_a' AND status = 'sent'`); n != 1 {
		t.Error("a sent delivery was rewritten")
	}
	if n := count(`SELECT count(*) FROM delivery WHERE target_id = 'pt_b' AND status = 'pending'`); n != 1 {
		t.Error("another device's delivery was cancelled")
	}
	if d, _ := st.DeviceByID(ctx, sess.Device.ID); d.RevokedAt == 0 {
		t.Error("the board session made from the device stayed valid")
	}
	if d, _ := st.DeviceByID(ctx, other.Device.ID); d.RevokedAt != 0 {
		t.Error("another device revoked")
	}
	if err := Revoke(ctx, st, phone.Device.ID); err == nil {
		t.Error("revoking twice should report not found")
	}
}

// A database writer without the pepper file cannot make an owner session (keys.md 5.4).
func TestOwnerTokenHashNeedsPepper(t *testing.T) {
	a, st, _ := newAuth(t)
	ctx := context.Background()
	o := ownerSession(t, st)
	if _, err := Create(ctx, st, CreateRequest{Kind: "browser", Name: "o", Owner: true, CreatedBy: "owner-link:pc_q"}); err == nil {
		t.Fatal("owner session made without the pepper")
	}
	// The stored hash is not plain SHA-256.
	if _, err := st.DeviceDetailByHash(ctx, Hash(o.Token)); err == nil {
		t.Fatal("owner token stored as plain SHA-256")
	}
	// Rows a writer inserts: a sy_o_ token with a plain hash, and a sy_b_ token on a device
	// with the owner scope.
	tokO, _ := NewToken("owner")
	tokB, _ := NewToken("browser")
	for i, c := range []struct{ tok string }{{tokO}, {tokB}} {
		d := store.Device{ID: store.NewID("dev_", t0.Add(time.Duration(i)*time.Millisecond)), Kind: "browser", Name: "evil",
			TokenHint: Hint(c.tok), Scopes: []string{"owner", "read"}, CreatedAt: t0.UnixMilli(), CreatedBy: "owner-link:pc_evil"}
		if err := st.Write(ctx, func(tx *store.Tx) error { return tx.InsertDevice(d, Hash(c.tok)) }); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Lookup(ctx, c.tok); err != ErrUnauthorized {
			t.Errorf("forged row %d accepted: %v", i, err)
		}
	}
	// Without a pepper function no owner session authenticates.
	a.Pepper = nil
	if _, err := a.Lookup(ctx, o.Token); err != ErrUnauthorized {
		t.Fatalf("no pepper: %v", err)
	}
}
