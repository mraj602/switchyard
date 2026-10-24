package pair

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"rsc.io/qr"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	t   *testing.T
	clk *clock
	st  *store.Store
	svc *Service
	dir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := &clock{t: t0}
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &fixture{t: t, clk: clk, st: st, svc: NewService(st, filepath.Join(dir, "state", PepperFile)), dir: dir}
}

func (f *fixture) code(kind string, mod ...func(*CreateRequest)) Code {
	f.t.Helper()
	req := CreateRequest{Kind: kind, URL: "https://hub.example.ts.net"}
	for _, m := range mod {
		m(&req)
	}
	c, err := f.svc.Create(context.Background(), req)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

const testMachine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"

// scripted returns the given chunks, one per Read, then real randomness.
type scripted struct{ chunks [][]byte }

func (s *scripted) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return io.ReadFull(randReader{}, p)
	}
	c := s.chunks[0]
	s.chunks = s.chunks[1:]
	if len(c) != len(p) {
		return 0, fmt.Errorf("scripted read of %d bytes, chunk has %d", len(p), len(c))
	}
	return copy(p, c), nil
}

type randReader struct{}

func (randReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i*37 + 11)
	}
	return len(p), nil
}

func draw(v uint32) []byte { return []byte{byte(v >> 16), byte(v >> 8), byte(v)} }

func TestDigitsRejectionBound(t *testing.T) {
	for v, want := range map[uint32]string{0: "000000", 1: "000001", 482913: "482913", 999999: "999999"} {
		if d, ok := DigitsFromDraw(v); !ok || d != want {
			t.Errorf("DigitsFromDraw(%d) = %q, %v", v, d, ok)
		}
	}
	for _, v := range []uint32{1_000_000, 1_048_575, 1<<20 - 1} {
		if _, ok := DigitsFromDraw(v); ok {
			t.Errorf("DigitsFromDraw(%d) accepted a value that would bias the digits", v)
		}
	}
	// Bits above the 20 drawn ones are ignored, not folded in.
	if d, ok := DigitsFromDraw(0xFF000000 | 7); !ok || d != "000007" {
		t.Errorf("high bits: %q %v", d, ok)
	}
	f := newFixture(t)
	f.svc.Rand = &scripted{chunks: [][]byte{draw(1_048_575), draw(1_000_000), draw(123456)}}
	if d, err := f.svc.NewDigits(); err != nil || d != "123456" {
		t.Errorf("NewDigits skipped rejected draws wrongly: %q %v", d, err)
	}
	// Every digit string is six characters of 0-9 and the first ten thousand are distinct draws.
	f.svc.Rand = nil
	seen := map[string]bool{}
	for range 2000 {
		d, err := f.svc.NewDigits()
		if err != nil || len(d) != 6 || strings.Trim(d, "0123456789") != "" {
			t.Fatalf("digits %q %v", d, err)
		}
		seen[d] = true
	}
	if len(seen) < 1900 {
		t.Errorf("only %d distinct digit strings in 2000 draws", len(seen))
	}
}

func TestSecretEntropyAndShape(t *testing.T) {
	f := newFixture(t)
	seen := map[string]bool{}
	for range 100 {
		s, err := f.svc.NewSecret()
		if err != nil || len(s) != 22 || strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			t.Fatalf("secret %q %v", s, err)
		}
		if seen[s] {
			t.Fatal("repeated secret")
		}
		seen[s] = true
	}
}

func TestNoTwoLiveCodesShareDigits(t *testing.T) {
	f := newFixture(t)
	// The second code's first draws collide with the first code's digits; generation retries.
	f.svc.Rand = &scripted{chunks: [][]byte{
		make([]byte, 16), draw(482913), // code 1: secret, digits
		append([]byte{1}, make([]byte, 15)...), draw(482913), draw(482913), draw(111222), // code 2
	}}
	a := f.code("phone")
	b := f.code("phone")
	if a.Digits != "482913" || b.Digits != "111222" {
		t.Fatalf("digits %s %s", a.Digits, b.Digits)
	}
	// An expired code's digits may be drawn again.
	f.clk.Add(11 * time.Minute)
	f.svc.Rand = &scripted{chunks: [][]byte{append([]byte{2}, make([]byte, 15)...), draw(482913)}}
	if c := f.code("phone"); c.Digits != "482913" {
		t.Errorf("digits of an expired code were not reusable: %s", c.Digits)
	}
}

func TestSixthCodeCancelsTheOldest(t *testing.T) {
	f := newFixture(t)
	var codes []Code
	for range 6 {
		codes = append(codes, f.code("phone"))
		f.clk.Add(time.Second)
	}
	ctx := context.Background()
	for i, c := range codes {
		st, err := f.svc.Status(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := StatusLive
		if i == 0 {
			want = StatusCancelled
		}
		if st.State != want {
			t.Errorf("code %d is %s, want %s", i, st.State, want)
		}
	}
	// The oldest cannot be claimed any more.
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Secret: codes[0].Secret, Name: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("cancelled code: %v", err)
	}
	// Owner links do not count towards the five.
	for range 3 {
		if _, err := f.svc.CreateOwnerLink(ctx, OwnerLinkRequest{URL: "https://hub.example.ts.net"}); err != nil {
			t.Fatal(err)
		}
	}
	f.code("phone")
	if st, _ := f.svc.Status(ctx, codes[1].ID); st.State != StatusCancelled {
		t.Errorf("seventh code should cancel the next oldest, owner links must not take slots: %s", st.State)
	}
	if st, _ := f.svc.Status(ctx, codes[5].ID); st.State != StatusLive {
		t.Errorf("newest of the earlier codes: %s", st.State)
	}
}

func TestTTLBoundaries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.code("phone")
	if !a.ExpiresAt.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("default ttl: %v", a.ExpiresAt)
	}
	f.clk.Add(10*time.Minute - time.Millisecond)
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Secret: a.Secret}); err != nil {
		t.Fatalf("claim 1 ms before expiry: %v", err)
	}
	b := f.code("phone") // at t0+10m-1ms; expires 10 minutes later
	f.clk.Add(10 * time.Minute)
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Secret: b.Secret}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("claim at expires_at must fail: %v", err)
	}
	if st, _ := f.svc.Status(ctx, b.ID); st.State != StatusExpired {
		t.Errorf("status %s", st.State)
	}
	// ttl bounds
	for _, ttl := range []time.Duration{59 * time.Second, 61 * time.Minute} {
		if _, err := f.svc.Create(ctx, CreateRequest{Kind: "phone", URL: "https://h.example.ts.net", TTL: ttl}); err == nil {
			t.Errorf("ttl %v accepted", ttl)
		}
	}
	c, err := f.svc.Create(ctx, CreateRequest{Kind: "phone", URL: "https://h.example.ts.net", TTL: time.Minute})
	if err != nil || !c.ExpiresAt.Equal(f.clk.Now().Add(time.Minute)) {
		t.Errorf("1 minute ttl: %v %v", c.ExpiresAt, err)
	}
	// pair.ttl_s setting
	if err := f.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO setting (key, value, updated_at) VALUES ('pair.ttl_s', '120', 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if c := f.code("phone"); !c.ExpiresAt.Equal(f.clk.Now().Add(2 * time.Minute)) {
		t.Errorf("pair.ttl_s ignored: %v", c.ExpiresAt)
	}
}

func TestHashingUsesThePepperAndNeverStoresSecrets(t *testing.T) {
	f := newFixture(t)
	c := f.code("phone")
	pepper, err := os.ReadFile(filepath.Join(f.dir, "state", PepperFile))
	if err != nil || len(pepper) != 32 {
		t.Fatalf("pepper: %d bytes, %v", len(pepper), err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(f.dir, "state", PepperFile)); fi.Mode().Perm() != 0o600 {
			t.Errorf("pepper mode %v", fi.Mode().Perm())
		}
	}
	mac := func(key []byte, s string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(s))
		return h.Sum(nil)
	}
	row, err := f.st.PairCodeByID(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hmac.Equal(row.CodeHash, mac(pepper, "code:"+c.Digits)) || !hmac.Equal(row.SecretHash, mac(pepper, "secret:"+c.Secret)) {
		t.Error("stored hashes are not HMAC(pepper, prefix+value)")
	}
	plain := sha256.Sum256([]byte("code:" + c.Digits))
	if hmac.Equal(row.CodeHash, plain[:]) {
		t.Error("the code hash is an unkeyed hash: the 10^6 digits could be tested offline")
	}
	// Another pepper (a hub restored without its state folder) cannot claim the old code.
	other := NewService(f.st, filepath.Join(f.dir, "other", PepperFile))
	if _, err := other.Claim(context.Background(), "1.1.1.1", ClaimRequest{Secret: c.Secret}); !errors.Is(err, ErrInvalid) {
		t.Errorf("claim with another pepper: %v", err)
	}
	// The same file gives the same key (a second process at first use).
	again := NewService(f.st, filepath.Join(f.dir, "state", PepperFile))
	if h, _ := again.CodeHash(c.Digits); !hmac.Equal(h, row.CodeHash) {
		t.Error("pepper not stable across services")
	}
	// A damaged pepper is an error, not a silent new key.
	bad := filepath.Join(f.dir, "bad", PepperFile)
	_ = os.MkdirAll(filepath.Dir(bad), 0o700)
	_ = os.WriteFile(bad, []byte("short"), 0o600)
	if _, err := NewService(f.st, bad).CodeHash("123456"); err == nil {
		t.Error("damaged pepper accepted")
	}
}

func TestConcurrentPepperCreationAgrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s", PepperFile)
	var wg sync.WaitGroup
	keys := make([][]byte, 16)
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := readOrCreatePepper(path)
			if err != nil {
				t.Error(err)
			}
			keys[i] = k
		}()
	}
	wg.Wait()
	for _, k := range keys[1:] {
		if string(k) != string(keys[0]) {
			t.Fatal("concurrent creators ended with different keys")
		}
	}
}

func claimOK(t *testing.T, f *fixture, ip string, req ClaimRequest) ClaimResult {
	t.Helper()
	r, err := f.svc.Claim(context.Background(), ip, req)
	if err != nil {
		t.Fatalf("claim %+v: %v", req, err)
	}
	return r
}

func TestClaimFields(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// code accepts three spellings; secret works; exactly one of them.
	for _, spell := range []func(string) string{
		func(d string) string { return d },
		func(d string) string { return d[:3] + "-" + d[3:] },
		func(d string) string { return d[:3] + " " + d[3:] },
	} {
		c := f.code("phone")
		r := claimOK(t, f, "1.1.1.1", ClaimRequest{Code: spell(c.Digits), ExpectKind: "phone"})
		if r.Device.Kind != "phone" || !strings.HasPrefix(r.Token, "wy_p_") || r.Device.CreatedBy != "pair:"+c.ID {
			t.Errorf("claim result %+v", r.Device)
		}
		if got := strings.Join(r.Device.Scopes, ","); got != "read,push" {
			t.Errorf("scopes %s", got)
		}
	}
	c := f.code("phone")
	for _, req := range []ClaimRequest{{}, {Code: c.Digits, Secret: c.Secret}, {Code: "12345"}, {Code: "abcdef"}, {Code: "123456789"}} {
		var ce *ClaimError
		if _, err := f.svc.Claim(ctx, "1.1.1.1", req); !errors.As(err, &ce) || ce.Status != 400 {
			t.Errorf("claim %+v: %v, want 400", req, err)
		}
	}
	// Malformed requests do not count as failed guesses.
	for range 10 {
		_, _ = f.svc.Claim(ctx, "9.9.9.9", ClaimRequest{Code: "12"})
	}
	if ok, _ := f.svc.Limiter.Allow("9.9.9.9", true); !ok {
		t.Error("malformed codes locked the IP out")
	}

	// name: the code's when it has one, else the claimer's, else "<kind> <date>"; trimmed; control characters removed;
	// longer than 60 is a 400 that leaves the code live.
	named := f.code("phone", func(r *CreateRequest) { r.Name = "Suggested" })
	var ce *ClaimError
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Secret: named.Secret, Name: strings.Repeat("x", 61)}); !errors.As(err, &ce) || ce.Status != 400 {
		t.Errorf("long name: %v", err)
	}
	if r := claimOK(t, f, "1.1.1.1", ClaimRequest{Secret: named.Secret}); r.Device.Name != "Suggested" {
		t.Errorf("default name %q", r.Device.Name)
	}
	// The code's name (wy hub pair --name) wins over the claimer's (wy connect sends the host).
	nm := f.code("machine", func(r *CreateRequest) { r.Name = "lab-machine" })
	if r := claimOK(t, f, "1.1.1.1", ClaimRequest{Code: nm.Digits, Machine: testMachine, Name: "lab-box"}); r.Device.Name != "lab-machine" {
		t.Errorf("code name lost to the claimer's: %q", r.Device.Name)
	}
	if r := claimOK(t, f, "1.1.1.1", ClaimRequest{Secret: c.Secret, Name: "  iPhone\x00 · \x07Safari \n"}); r.Device.Name != "iPhone · Safari" {
		t.Errorf("cleaned name %q", r.Device.Name)
	}
	d := f.code("browser")
	if r := claimOK(t, f, "1.1.1.1", ClaimRequest{Secret: d.Secret}); r.Device.Name != "browser 2026-10-12" {
		t.Errorf("fallback name %q", r.Device.Name)
	}

	// machine: required for machine and desktop, without consuming the code; ignored for others.
	m := f.code("machine")
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Code: m.Digits}); !errors.As(err, &ce) || ce.Status != 400 || !strings.Contains(ce.Message, "machine is required") {
		t.Fatalf("machine code without machine: %v", err)
	}
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Code: m.Digits, Machine: "bad id"}); !errors.As(err, &ce) || ce.Status != 400 {
		t.Fatalf("bad machine id: %v", err)
	}
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Code: m.Digits, Machine: testMachine, Cookie: true}); !errors.As(err, &ce) || ce.Status != 400 {
		t.Fatalf("cookie for a machine code: %v", err)
	}
	r := claimOK(t, f, "1.1.1.1", ClaimRequest{Code: m.Digits, Machine: testMachine, Name: "gpu-box", ExpectKind: "machine"})
	if r.Device.MachineID != testMachine || r.Device.Kind != "machine" || strings.Join(r.Device.Scopes, ",") != "write" {
		t.Errorf("machine device %+v", r.Device)
	}
	p := f.code("phone")
	if r := claimOK(t, f, "1.1.1.1", ClaimRequest{Secret: p.Secret, Machine: testMachine}); r.Device.MachineID != "" {
		t.Errorf("machine on a phone device: %q", r.Device.MachineID)
	}
	// A code made for one machine refuses another.
	for_ := f.code("desktop", func(r *CreateRequest) { r.Machine = testMachine })
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Code: for_.Digits, Machine: "m_01JZZZZZZZZZZZZZZZZZZZZZZZ"}); !errors.As(err, &ce) || ce.Status != 400 {
		t.Errorf("other machine: %v", err)
	}
	if r := claimOK(t, f, "1.1.1.1", ClaimRequest{Code: for_.Digits}); r.Device.MachineID != testMachine || r.Device.Kind != "desktop" {
		t.Errorf("machine taken from the code: %+v", r.Device)
	}

	// expect_kind mismatch: 409 and the code stays live.
	e := f.code("machine")
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Code: e.Digits, Machine: testMachine, ExpectKind: "phone"}); !errors.As(err, &ce) || ce.Status != 409 {
		t.Fatalf("expect_kind mismatch: %v", err)
	}
	if st, _ := f.svc.Status(ctx, e.ID); st.State != StatusLive {
		t.Errorf("a mismatch consumed the code: %s", st.State)
	}
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Code: e.Digits, Machine: testMachine, ExpectKind: "gadget"}); !errors.As(err, &ce) || ce.Status != 400 {
		t.Errorf("unknown expect_kind: %v", err)
	}
	claimOK(t, f, "1.1.1.1", ClaimRequest{Code: e.Digits, Machine: testMachine, ExpectKind: "machine"})

	// owner links are refused here (409) and stay live for /v1/session/owner.
	ol, err := f.svc.CreateOwnerLink(ctx, OwnerLinkRequest{URL: "https://hub.example.ts.net"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Secret: ol.Secret}); !errors.As(err, &ce) || ce.Status != 409 {
		t.Errorf("owner link through claim: %v", err)
	}
	if st, _ := f.svc.Status(ctx, ol.ID); st.State != StatusLive {
		t.Errorf("owner link consumed by claim: %s", st.State)
	}
	// ... and a phone secret does not sign anyone in as owner.
	q := f.code("phone")
	if _, err := f.svc.OwnerSignIn(ctx, "1.1.1.1", q.Secret, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("phone secret at owner sign-in: %v", err)
	}
	if st, _ := f.svc.Status(ctx, q.ID); st.State != StatusLive {
		t.Errorf("phone code consumed by owner sign-in: %s", st.State)
	}
}

func TestReplayAndConcurrentClaims(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.code("phone")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, invalid := 0, 0
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := ClaimRequest{Secret: c.Secret}
			if i%2 == 1 {
				req = ClaimRequest{Code: c.Digits}
			}
			_, err := f.svc.Claim(ctx, fmt.Sprintf("10.0.0.%d", i), req)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrInvalid):
				invalid++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || invalid != 19 {
		t.Fatalf("%d claims won, %d refused; want exactly one winner", ok, invalid)
	}
	var n int
	if err := f.st.R().QueryRow(`SELECT count(*) FROM device WHERE created_by = ?`, "pair:"+c.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d devices made from one code (%v)", n, err)
	}
	// Replay: the same request gets the same 401 as a code that never existed.
	_, e1 := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Secret: c.Secret})
	_, e2 := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Secret: "AAAAAAAAAAAAAAAAAAAAAA"})
	if !errors.Is(e1, ErrInvalid) || e1.Error() != e2.Error() {
		t.Errorf("used vs unknown differ: %v / %v", e1, e2)
	}
	// A cancelled code gives the same answer too.
	d := f.code("phone")
	if err := f.svc.Cancel(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, e3 := f.svc.Claim(ctx, "1.1.1.1", ClaimRequest{Secret: d.Secret}); e3 == nil || e3.Error() != e1.Error() {
		t.Errorf("cancelled: %v", e3)
	}
	if err := f.svc.Cancel(ctx, "pc_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel unknown: %v", err)
	}
	if err := f.svc.Cancel(ctx, c.ID); err != nil { // claimed: no error, no change
		t.Errorf("cancel claimed: %v", err)
	}
	if st, _ := f.svc.Status(ctx, c.ID); st.State != StatusClaimed || st.Device == nil {
		t.Errorf("claimed status %+v", st)
	}
}

func TestLimiterWithFakeClock(t *testing.T) {
	clk := &clock{t: t0}
	l := NewLimiter(clk.Now)

	// Per IP: 5 failed typed claims lock that IP for 10 minutes; other IPs are fine.
	for range TypedFailsPerIP {
		if ok, _ := l.Allow("1.1.1.1", true); !ok {
			t.Fatal("blocked too early")
		}
		l.Settle("1.1.1.1", true)
	}
	if ok, retry := l.Allow("1.1.1.1", true); ok || retry <= 0 || retry > Lockout {
		t.Errorf("sixth typed claim: ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allow("2.2.2.2", true); !ok {
		t.Error("another IP was blocked by the first one's failures")
	}
	// The per-IP typed lock does not block that IP's QR claims.
	if ok, _ := l.Allow("1.1.1.1", false); !ok {
		t.Error("typed lock blocked a secret claim")
	}
	clk.Add(Lockout - time.Second)
	if ok, _ := l.Allow("1.1.1.1", true); ok {
		t.Error("lock ended early")
	}
	clk.Add(2 * time.Second)
	if ok, _ := l.Allow("1.1.1.1", true); !ok {
		t.Error("lock did not end after 10 minutes")
	}

	// Failures older than the window do not add up.
	l = NewLimiter(clk.Now)
	for range TypedFailsPerIP - 1 {
		l.Fail("3.3.3.3", true)
	}
	clk.Add(FailWindow + time.Second)
	l.Fail("3.3.3.3", true)
	if ok, _ := l.Allow("3.3.3.3", true); !ok {
		t.Error("old failures counted")
	}

	// One address locking itself out never blocks another.
	l = NewLimiter(clk.Now)
	for range TypedFailsPerIP {
		l.Fail("5.5.5.5", true)
	}
	if ok, _ := l.Allow("5.5.5.5", true); ok {
		t.Error("per-IP lock missing")
	}
	if ok, _ := l.Allow("5.5.5.6", true); !ok {
		t.Error("one address's lockout blocked another")
	}
	// Failures from fewer addresses than the ceiling need never lock the hub.
	for i := range TypedFailsHub/TypedFailsPerIP - 2 {
		for range TypedFailsPerIP {
			l.Fail(fmt.Sprintf("10.1.%d.1", i), true)
		}
	}
	if ok, _ := l.Allow("10.9.9.9", true); !ok {
		t.Error("hub locked below the ceiling")
	}

	// Hub-wide ceiling: TypedFailsHub failures from many IPs lock typed claims for everyone, QR claims still work.
	l = NewLimiter(clk.Now)
	for i := range TypedFailsHub {
		ip := fmt.Sprintf("10.0.%d.1", i) // one failure per IP: no per-IP lock
		l.Fail(ip, true)
	}
	if ok, retry := l.Allow("10.9.9.9", true); ok || retry <= 0 {
		t.Errorf("hub-wide lockout missing: %v %v", ok, retry)
	}
	if ok, _ := l.Allow("10.9.9.9", false); !ok {
		t.Error("hub-wide typed lockout blocked a QR claim")
	}
	clk.Add(Lockout + time.Second)
	if ok, _ := l.Allow("10.9.9.9", true); !ok {
		t.Error("hub-wide lock did not end")
	}

	// Secret failures: 20 per IP, then that IP only.
	l = NewLimiter(clk.Now)
	for range SecretFailsPerIP {
		l.Fail("4.4.4.4", false)
	}
	if ok, _ := l.Allow("4.4.4.4", false); ok {
		t.Error("secret failures not limited")
	}
	if ok, _ := l.Allow("5.5.5.5", false); !ok {
		t.Error("other IP blocked")
	}

	// Requests per minute.
	l = NewLimiter(clk.Now)
	for i := range MaxRequestsPerMinute {
		if ok, _ := l.Allow("6.6.6.6", false); !ok {
			t.Fatalf("request %d blocked", i)
		}
	}
	if ok, retry := l.Allow("6.6.6.6", false); ok || retry <= 0 || retry > time.Minute {
		t.Errorf("21st request: %v %v", ok, retry)
	}
	clk.Add(61 * time.Second)
	if ok, _ := l.Allow("6.6.6.6", false); !ok {
		t.Error("window did not slide")
	}
}

func TestClaimIsRateLimitedAndLogsNoSecrets(t *testing.T) {
	f := newFixture(t)
	var logs strings.Builder
	f.svc.Log = newLogger(&logs)
	ctx := context.Background()
	c := f.code("phone")
	for range TypedFailsPerIP {
		if _, err := f.svc.Claim(ctx, "7.7.7.7", ClaimRequest{Code: "000001"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("wrong code: %v", err)
		}
	}
	var le *LimitError
	if _, err := f.svc.Claim(ctx, "7.7.7.7", ClaimRequest{Code: c.Digits}); !errors.As(err, &le) {
		t.Fatalf("locked IP with the right code: %v (a lockout must hold even for the right guess)", err)
	}
	// The right secret from the same IP still works.
	claimOK(t, f, "7.7.7.7", ClaimRequest{Secret: c.Secret})
	out := logs.String()
	for _, secret := range []string{c.Secret, c.Digits, c.Code, "000001"} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains a code or secret %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "pair claim fail ip=7.7.7.7 kind=code") || !strings.Contains(out, "pair claim ok device=dev_") {
		t.Errorf("log lines missing:\n%s", out)
	}
}

func TestOwnerSignIn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l, err := f.svc.CreateOwnerLink(ctx, OwnerLinkRequest{URL: "https://hub.example.ts.net"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://hub.example.ts.net/#/owner?s=" + l.Secret; l.URL != want {
		t.Errorf("url %s", l.URL)
	}
	c, err := f.svc.OwnerSignIn(ctx, "1.1.1.1", l.Secret, "")
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.st.DeviceDetailByID(ctx, c.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Token, "wy_o_") || !auth.LooksLikeToken(c.Token) || d.Kind != "browser" ||
		strings.Join(d.Scopes, ",") != "owner,read,push" || d.CreatedBy != "owner-link:"+l.ID ||
		d.ExpiresAt != t0.Add(30*24*time.Hour).UnixMilli() {
		t.Errorf("owner session %+v (token %s)", d, c.Token[:5])
	}
	if _, err := f.svc.OwnerSignIn(ctx, "1.1.1.1", l.Secret, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("replay: %v", err)
	}
	var ce *ClaimError
	if _, err := f.svc.OwnerSignIn(ctx, "1.1.1.1", "short", ""); !errors.As(err, &ce) || ce.Status != 400 {
		t.Errorf("short secret: %v", err)
	}
	// Expired link.
	l2, _ := f.svc.CreateOwnerLink(ctx, OwnerLinkRequest{URL: "https://hub.example.ts.net", TTL: time.Minute})
	f.clk.Add(time.Minute)
	if _, err := f.svc.OwnerSignIn(ctx, "1.1.1.1", l2.Secret, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("expired link: %v", err)
	}
	// http only on loopback (or --insecure).
	if _, err := f.svc.CreateOwnerLink(ctx, OwnerLinkRequest{URL: "http://hub.example.com"}); err == nil {
		t.Error("owner link over plain http to a public name")
	}
	if _, err := f.svc.CreateOwnerLink(ctx, OwnerLinkRequest{URL: "http://127.0.0.1:8787"}); err != nil {
		t.Errorf("owner link on loopback: %v", err)
	}
}

func TestURLResolutionAndRules(t *testing.T) {
	if u, err := ResolveURL("https://a", "https://b", "https://c"); err != nil || u != "https://a" {
		t.Errorf("explicit first: %q %v", u, err)
	}
	if u, _ := ResolveURL("", "https://b", "https://c"); u != "https://b" {
		t.Errorf("setting second: %q", u)
	}
	if u, _ := ResolveURL(" ", "", "https://c"); u != "https://c" {
		t.Errorf("request origin third: %q", u)
	}
	if _, err := ResolveURL("", "", ""); !errors.Is(err, ErrNoURL) || !strings.Contains(err.Error(), "--url https://") {
		t.Errorf("nothing: %v", err)
	}
	cases := []struct {
		raw, kind string
		insecure  bool
		want      string
		plain     bool
		fail      bool
	}{
		{"https://hub.example.ts.net/", "phone", false, "https://hub.example.ts.net", false, false},
		{"https://hub:8443", "browser", false, "https://hub:8443", false, false},
		{"http://hub.example.ts.net", "phone", false, "", false, true},
		{"http://hub.example.ts.net", "phone", true, "http://hub.example.ts.net", true, false},
		{"http://100.64.0.5:8787", "machine", false, "http://100.64.0.5:8787", false, false},
		{"http://127.0.0.1:8787", "owner", false, "http://127.0.0.1:8787", false, false},
		{"http://hub.example.com", "owner", false, "", false, true},
		{"ftp://x", "phone", false, "", false, true},
		{"https://hub/sub", "phone", false, "", false, true},
		{"https://u:p@hub", "phone", false, "", false, true},
		{"https://hub?x=1", "phone", false, "", false, true},
		{"hub.example.ts.net", "phone", false, "", false, true},
	}
	for _, c := range cases {
		got, plain, err := NormalizeURL(c.raw, c.kind, c.insecure)
		if (err != nil) != c.fail || got != c.want || plain != c.plain {
			t.Errorf("NormalizeURL(%q, %s, %v) = %q, %v, %v", c.raw, c.kind, c.insecure, got, plain, err)
		}
	}
	if _, _, err := NormalizeURL("http://x.example.com:19200", "phone", false); !errors.Is(err, ErrNeedsHTTPS) || !strings.Contains(err.Error(), "tailscale serve --bg 19200") {
		t.Errorf("http refusal must say how to fix it: %v", err)
	}
	if got := PairURL("https://h", "Q2xhdWRlIGlzIG5vdCBoZXJl", "482913"); got != "https://h/#/pair?s=Q2xhdWRlIGlzIG5vdCBoZXJl&c=482913" {
		t.Errorf("pair url %s", got)
	}
}

func TestCreateValidationAndCommand(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, req := range []CreateRequest{
		{Kind: "satellite", URL: "https://h.example.ts.net"},
		{Kind: "owner", URL: "https://h.example.ts.net"},
		{Kind: "phone", URL: "https://h.example.ts.net", Machine: testMachine},
		{Kind: "machine", URL: "https://h.example.ts.net", Machine: "nope"},
		{Kind: "phone", URL: "https://h.example.ts.net", Name: strings.Repeat("n", 61)},
		{Kind: "phone", URL: ""},
	} {
		var ce *ClaimError
		if _, err := f.svc.Create(ctx, req); err == nil {
			t.Errorf("%+v accepted", req)
		} else if !errors.As(err, &ce) {
			t.Errorf("%+v: validation errors must be *ClaimError (400), got %T", req, err)
		}
	}
	c := f.code("machine")
	if c.Command != "wy connect https://hub.example.ts.net --code "+c.Code {
		t.Errorf("command %q", c.Command)
	}
	if p := f.code("phone"); p.Command != "" {
		t.Errorf("phone command %q", p.Command)
	}
	// The QR matrix is the pairing URL.
	p := f.code("phone")
	want, err := NewMatrix(p.URL)
	if err != nil || fmt.Sprint(want) != fmt.Sprint(p.QR) {
		t.Errorf("QR is not the matrix of the URL")
	}
	// Prune keeps unclaimed rows for 30 days.
	f.clk.Add(31 * 24 * time.Hour)
	n, err := f.st.PrunePairCodes(ctx, f.clk.Now().Add(-30*24*time.Hour).UnixMilli())
	if err != nil || n == 0 {
		t.Errorf("prune: %d %v", n, err)
	}
}

func TestMatrixTerminalDecodesBack(t *testing.T) {
	url := "https://hub.example.ts.net/#/pair?s=Q2xhdWRlIGlzIG5vdCBoZXJl&c=482913"
	m, err := NewMatrix(url)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := qr.Encode(url, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	if m.Size != ref.Size+8 || len(m.Rows) != m.Size {
		t.Fatalf("size %d (encoder %d), want a 4-module quiet zone", m.Size, ref.Size)
	}
	for y := range m.Size {
		if len(m.Rows[y]) != m.Size {
			t.Fatalf("row %d length", y)
		}
		for x := range m.Size {
			inner := x >= 4 && y >= 4 && x < 4+ref.Size && y < 4+ref.Size
			want := inner && ref.Black(x-4, y-4)
			if m.Black(x, y) != want {
				t.Fatalf("module (%d,%d) differs from the encoder's", x, y)
			}
			if !inner && m.Black(x, y) {
				t.Fatalf("dark module in the quiet zone at (%d,%d)", x, y)
			}
		}
	}
	term := m.Terminal()
	if !strings.Contains(term, "\x1b[30;47m") || !strings.Contains(term, "▀") || !strings.Contains(term, "▄") {
		t.Error("rendering lacks the black-on-white wrapper or half blocks")
	}
	got, err := ParseTerminal(term)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(m) {
		t.Error("the terminal rendering does not decode to the encoder's matrix")
	}
}

func newLogger(w io.Writer) *log.Logger { return log.New(w, "", 0) }
