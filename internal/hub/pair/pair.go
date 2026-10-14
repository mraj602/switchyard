package pair

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mraj602/switchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/switchyard/internal/hub/keys"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
	"github.com/mraj602/switchyard/internal/paths"
)

// Lifetimes and caps of pairing.md 2.1.
const (
	DefaultTTL = 10 * time.Minute
	MinTTL     = 60 * time.Second
	MaxTTL     = time.Hour
	// MaxLive is how many typed codes can be live at once; a sixth cancels the oldest.
	MaxLive = 5
	// MaxNameLen is the longest device name.
	MaxNameLen = 60
	// PepperFile is the name of the HMAC key inside the state folder.
	PepperFile = "pair_pepper"
)

// Kinds a code can be made for.
var Kinds = []string{"machine", "desktop", "phone", "browser"}

func validKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// machinePattern is the envelope's id shape (record id pattern).
var machinePattern = regexp.MustCompile(`^[0-9A-Za-z_-]{16,64}$`)

// Errors a claim can end in. Handlers map them to 401, 409, 400 and 429.
var (
	// ErrInvalid is the one answer for an unknown, used, cancelled or expired code (401).
	ErrInvalid = errors.New("invalid or expired code")
	// ErrNotFound: no pairing code with that id.
	ErrNotFound = store.ErrNotFound
)

// ClaimError is a refusal that does not consume the code: 400 or 409.
type ClaimError struct {
	Status  int
	Code    string // api.yaml error code
	Message string
}

func (e *ClaimError) Error() string { return e.Message }

// LimitError means the caller is rate limited (429).
type LimitError struct{ RetryAfter time.Duration }

func (e *LimitError) Error() string {
	return fmt.Sprintf("too many attempts; try again in %d s", int(e.RetryAfter.Seconds())+1)
}

func badRequest(msg string) error { return &ClaimError{400, "bad_request", msg} }
func conflict(msg string) error   { return &ClaimError{409, "conflict", msg} }

// Service makes and claims pairing codes over the hub's store.
type Service struct {
	Store   *store.Store
	Limiter *Limiter
	// Log receives one line per claim, failure and lockout; never a code or a secret. Nil
	// discards.
	Log *log.Logger
	// Rand is the randomness source; nil = crypto/rand. Tests inject.
	Rand io.Reader

	// Keys, when set, registers device keys at claim time and adds h= and k= to the QR
	// (keys.md 7). Nil: pairing as before F1.
	Keys *hubkeys.Service

	pepperPath string
	mu         sync.Mutex
	pepper     []byte
}

// NewService returns a service over st. pepperPath is the file holding the HMAC key
// (made at first use, mode 0600); "" = <state>/pair_pepper (DefaultPepperPath). The store's
// clock is the service's clock.
func NewService(st *store.Store, pepperPath string) *Service {
	return &Service{Store: st, Limiter: NewLimiter(st.Now), pepperPath: pepperPath}
}

// DefaultPepperPath is <state>/pair_pepper (paths.md).
func DefaultPepperPath() (string, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return "", err
	}
	return filepath.Join(d.State.Path, PepperFile), nil
}

func (s *Service) logf(format string, a ...any) {
	if s.Log != nil {
		s.Log.Printf(format, a...)
	}
}

func (s *Service) rand() io.Reader {
	if s.Rand != nil {
		return s.Rand
	}
	return rand.Reader
}

// key returns the pepper, creating the file at first use. The file is not in the database
// on purpose: a copy of the database alone cannot test the 10^6 possible digits offline.
func (s *Service) key() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pepper != nil {
		return s.pepper, nil
	}
	path := s.pepperPath
	if path == "" {
		p, err := DefaultPepperPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	b, err := readOrCreatePepper(path)
	if err != nil {
		return nil, err
	}
	s.pepper = b
	return b, nil
}

func readOrCreatePepper(path string) ([]byte, error) {
	read := func() ([]byte, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("%s is damaged (%d bytes, want 32); delete it: only codes that are live now stop working", path, len(b))
		}
		return b, nil
	}
	if b, err := read(); err == nil || !errors.Is(err, os.ErrNotExist) {
		return b, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	var key [32]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return nil, err
	}
	// Write a private temp file and link it into place: a concurrent creator (the CLI and
	// the server at first use) cannot expose a half-written key, and the first link wins.
	tmp, err := os.CreateTemp(filepath.Dir(path), PepperFile+".tmp*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		// Windows has no modes; ignore.
		_ = err
	}
	if _, err := tmp.Write(key[:]); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if b, rerr := read(); rerr == nil {
			return b, nil
		}
		if rerr := os.Rename(tmp.Name(), path); rerr != nil {
			return nil, err
		}
	}
	return read()
}

func (s *Service) mac(prefix, value string) ([]byte, error) {
	k, err := s.key()
	if err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, k)
	h.Write([]byte(prefix))
	h.Write([]byte(value))
	return h.Sum(nil), nil
}

// Pepper returns the pairing pepper (made at first use). The authenticator keys the hash
// of sy_o_ tokens with it (auth.OwnerHash).
func (s *Service) Pepper() ([]byte, error) { return s.key() }

// SealKey is the QR's k: HMAC-SHA256(pepper, "seal:" + secret)[0:16] (keys.md 7). It is
// recomputed from the secret at claim time; nothing is stored.
func (s *Service) SealKey(secret string) ([]byte, error) {
	m, err := s.mac("seal:", secret)
	if err != nil {
		return nil, err
	}
	return m[:keys.SealKeyLen], nil
}

// CodeHash is HMAC-SHA256(pepper, "code:" + 6 digits).
func (s *Service) CodeHash(digits string) ([]byte, error) { return s.mac("code:", digits) }

// SecretHash is HMAC-SHA256(pepper, "secret:" + secret).
func (s *Service) SecretHash(secret string) ([]byte, error) { return s.mac("secret:", secret) }

// DigitsFromDraw turns a 20-bit draw into six digits. Values of 1 000 000 and above are
// rejected (ok false), so every code is equally likely: no modulo bias.
func DigitsFromDraw(v uint32) (digits string, ok bool) {
	v &= 1<<20 - 1
	if v >= 1_000_000 {
		return "", false
	}
	return fmt.Sprintf("%06d", v), true
}

// NewDigits draws six uniformly random digits.
func (s *Service) NewDigits() (string, error) {
	for {
		var b [3]byte
		if _, err := io.ReadFull(s.rand(), b[:]); err != nil {
			return "", err
		}
		if d, ok := DigitsFromDraw(uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])); ok {
			return d, nil
		}
	}
}

// NewSecret makes a 128-bit secret: 16 random bytes, base64url without padding (22 chars).
func (s *Service) NewSecret() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(s.rand(), b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// FormatCode shows six digits as 482-913.
func FormatCode(digits string) string {
	if len(digits) != 6 {
		return digits
	}
	return digits[:3] + "-" + digits[3:]
}

// NormalizeCode accepts 482913, 482-913 and 482 913 and returns the six digits.
func NormalizeCode(s string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ':
		default:
			return "", false
		}
	}
	d := b.String()
	return d, len(d) == 6
}

// cleanName removes control characters and trims.
func cleanName(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

// CreateRequest makes a pairing code (pairing.md 2.4).
type CreateRequest struct {
	Kind      string        // machine, desktop, phone or browser
	Name      string        // suggested device name, optional
	Machine   string        // optional, machine and desktop codes
	URL       string        // the hub's address (already resolved, see ResolveURL)
	TTL       time.Duration // 0 = the pair.ttl_s setting, else 10 minutes
	CreatedBy string        // "cli" or "owner:<device id>"
	Insecure  bool          // allow an http address for phone and browser codes
}

// Code is a freshly made pairing code. Code, Secret and URL are shown only now: the hub
// keeps only their hashes.
type Code struct {
	ID        string
	Kind      string
	Name      string
	Machine   string
	Digits    string // 482913
	Code      string // 482-913
	Secret    string
	Hub       string // the origin
	URL       string // the QR content
	ExpiresAt time.Time
	QR        Matrix
	Command   string // machine and desktop: sy connect <hub> --code 482-913
	// PlainHTTP is true when a phone or browser code points at http: it pairs, but the web
	// app cannot install or receive pushes.
	PlainHTTP bool
}

func (s *Service) ttl(ctx context.Context, d time.Duration) (time.Duration, error) {
	if d == 0 {
		if v, ok, err := s.Store.PairSettingInt(ctx, "pair.ttl_s"); err == nil && ok {
			d = time.Duration(v) * time.Second
		} else {
			d = DefaultTTL
		}
		d = min(max(d, MinTTL), MaxTTL)
	}
	if d < MinTTL || d > MaxTTL {
		return 0, fmt.Errorf("ttl must be between %s and %s", MinTTL, MaxTTL)
	}
	return d, nil
}

// Create makes a code: 6 digits and a 128-bit secret, stored as HMACs. It cancels the
// oldest live code when five are live, and retries until no live code shares the digits.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Code, error) {
	if !validKind(req.Kind) {
		return Code{}, badRequest(fmt.Sprintf("kind must be one of %s", strings.Join(Kinds, ", ")))
	}
	name := cleanName(req.Name)
	if len([]rune(name)) > MaxNameLen {
		return Code{}, badRequest(fmt.Sprintf("name is longer than %d characters", MaxNameLen))
	}
	if req.Machine != "" {
		if !auth.NeedsMachine(req.Kind) {
			return Code{}, badRequest("a machine id is only for machine and desktop codes")
		}
		if !machinePattern.MatchString(req.Machine) {
			return Code{}, badRequest("that does not look like a machine id (m_…; see `sy machine` on that machine)")
		}
	}
	origin, plain, err := NormalizeURL(req.URL, req.Kind, req.Insecure)
	if err != nil {
		return Code{}, badRequest(err.Error())
	}
	ttl, err := s.ttl(ctx, req.TTL)
	if err != nil {
		return Code{}, badRequest(err.Error())
	}
	secret, err := s.NewSecret()
	if err != nil {
		return Code{}, err
	}
	sh, err := s.SecretHash(secret)
	if err != nil {
		return Code{}, err
	}
	var seal []byte
	if s.Keys != nil {
		if seal, err = s.SealKey(secret); err != nil {
			return Code{}, err
		}
	}
	by := req.CreatedBy
	if by == "" {
		by = "cli"
	}
	var out Code
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.NowMS()
		var digits string
		var ch []byte
		for range 50 {
			d, err := s.NewDigits()
			if err != nil {
				return err
			}
			h, err := s.CodeHash(d)
			if err != nil {
				return err
			}
			taken, err := tx.LiveCodeHashExists(h, now)
			if err != nil {
				return err
			}
			if !taken {
				digits, ch = d, h
				break
			}
		}
		if ch == nil {
			return errors.New("could not find free digits")
		}
		if _, err := tx.CancelOldestLiveCodes(MaxLive-1, now); err != nil {
			return err
		}
		id := store.NewID("pc_", s.Store.Now())
		exp := now + ttl.Milliseconds()
		if err := tx.InsertPairCode(store.PairCodeRow{ID: id, Kind: req.Kind, CodeHash: ch, SecretHash: sh, Name: name,
			MachineID: req.Machine, URL: origin, CreatedAt: now, CreatedBy: by, ExpiresAt: exp}); err != nil {
			return err
		}
		u := PairURL(origin, secret, digits)
		if seal != nil {
			u = PairURLKeys(origin, secret, digits, s.Keys.Key.FP, keys.B64(seal))
		}
		out = Code{ID: id, Kind: req.Kind, Name: name, Machine: req.Machine, Digits: digits, Code: FormatCode(digits),
			Secret: secret, Hub: origin, URL: u, ExpiresAt: time.UnixMilli(exp).UTC(), PlainHTTP: plain}
		return nil
	})
	if err != nil {
		return Code{}, err
	}
	if out.QR, err = NewMatrix(out.URL); err != nil {
		return Code{}, err
	}
	if auth.NeedsMachine(out.Kind) {
		out.Command = "sy connect " + out.Hub + " --code " + out.Code
	}
	return out, nil
}

// OwnerLinkRequest makes an owner sign-in link (pairing.md 4).
type OwnerLinkRequest struct {
	URL       string
	TTL       time.Duration
	CreatedBy string
	Insecure  bool
}

// OwnerLink is a one-time owner sign-in link; Secret and URL are shown only now.
type OwnerLink struct {
	ID        string
	Secret    string
	Hub       string
	URL       string
	ExpiresAt time.Time
	PlainHTTP bool
}

// CreateOwnerLink makes a link: a pair_code of kind owner with a secret and no digits.
func (s *Service) CreateOwnerLink(ctx context.Context, req OwnerLinkRequest) (OwnerLink, error) {
	origin, plain, err := NormalizeURL(req.URL, "owner", req.Insecure)
	if err != nil {
		return OwnerLink{}, badRequest(err.Error())
	}
	ttl, err := s.ttl(ctx, req.TTL)
	if err != nil {
		return OwnerLink{}, badRequest(err.Error())
	}
	secret, err := s.NewSecret()
	if err != nil {
		return OwnerLink{}, err
	}
	sh, err := s.SecretHash(secret)
	if err != nil {
		return OwnerLink{}, err
	}
	by := req.CreatedBy
	if by == "" {
		by = "cli"
	}
	var out OwnerLink
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.NowMS()
		id := store.NewID("pc_", s.Store.Now())
		exp := now + ttl.Milliseconds()
		if err := tx.InsertPairCode(store.PairCodeRow{ID: id, Kind: "owner", SecretHash: sh, URL: origin, CreatedAt: now,
			CreatedBy: by, ExpiresAt: exp}); err != nil {
			return err
		}
		out = OwnerLink{ID: id, Secret: secret, Hub: origin, URL: OwnerURL(origin, secret), ExpiresAt: time.UnixMilli(exp).UTC(), PlainHTTP: plain}
		return nil
	})
	return out, err
}

// ClaimRequest is POST /v1/pair/claim (pairing.md 3).
type ClaimRequest struct {
	Secret     string
	Code       string
	Name       string
	Machine    string
	ExpectKind string
	Cookie     bool
	Client     string
	// Keys are the device's public keys in the clear (typed codes and `sy connect --code`,
	// reg tofu); KeysSealed the same JSON sealed with the QR's k (secret claims, reg qr).
	// At most one; keys.md 7.1.
	Keys       *keys.DeviceKeysNew
	KeysSealed string
}

// ClaimResult is a new device. Token is empty in cookie mode: the caller sets the cookie.
type ClaimResult struct {
	Device  store.Device
	Token   string
	PairID  string
	HubURL  string
	Machine string
	// Cert is the device's hub-signed certificate and Reg how its keys were registered,
	// when the claim carried keys (keys.md 7.3).
	Cert *keys.Doc
	Reg  string
}

// Claim swaps a code or secret for a device (pairing.md 3, steps 1 to 6). ip is the
// client address (auth.ClientIP). Errors: *LimitError (429), ErrInvalid (401, the same for
// unknown, used, cancelled and expired), *ClaimError (400 or 409; the code is untouched).
func (s *Service) Claim(ctx context.Context, ip string, req ClaimRequest) (ClaimResult, error) {
	typed := req.Code != ""
	if (req.Code == "") == (req.Secret == "") {
		return ClaimResult{}, badRequest("send exactly one of code and secret")
	}
	var digits string
	if typed {
		d, ok := NormalizeCode(req.Code)
		if !ok {
			return ClaimResult{}, badRequest("the code is six digits, like 482-913")
		}
		digits = d
	} else if len(req.Secret) > 128 {
		return ClaimResult{}, badRequest("secret is too long")
	}
	kindLabel := map[bool]string{true: "code", false: "secret"}[typed]
	if ok, retry := s.Limiter.Allow(ip, typed); !ok {
		s.logf("pair claim limited ip=%s kind=%s retry=%ds", ip, kindLabel, int(retry.Seconds()))
		return ClaimResult{}, &LimitError{RetryAfter: retry}
	}
	failed := false
	if typed { // release the failure slot Allow reserved, recording a failure if the guess was wrong
		defer func() { s.Limiter.Settle(ip, failed) }()
	}
	var h []byte
	var err error
	if typed {
		h, err = s.CodeHash(digits)
	} else {
		h, err = s.SecretHash(req.Secret)
	}
	if err != nil {
		return ClaimResult{}, err
	}
	name := cleanName(req.Name)
	if len([]rune(name)) > MaxNameLen {
		return ClaimResult{}, badRequest(fmt.Sprintf("name is longer than %d characters", MaxNameLen))
	}
	if req.ExpectKind != "" && !validKind(req.ExpectKind) {
		return ClaimResult{}, badRequest("expect_kind must be machine, desktop, phone or browser")
	}
	// keys.md 7.1: clear keys only with a typed code (no downgrade of the QR path), sealed
	// keys only with a secret. Refused before the code is looked at, so it stays live.
	if req.Keys != nil && req.KeysSealed != "" {
		return ClaimResult{}, badRequest("send at most one of keys and keys_sealed")
	}
	if (req.Keys != nil && !typed) || (req.KeysSealed != "" && typed) {
		return ClaimResult{}, badRequest(keys.ErrSeal.Error())
	}
	if (req.Keys != nil || req.KeysSealed != "") && s.Keys == nil {
		return ClaimResult{}, badRequest("this hub has no signing key yet")
	}
	var devKeys *keys.DeviceKeysNew
	reg := ""
	var sealKey []byte
	switch {
	case req.Keys != nil:
		devKeys, reg = req.Keys, keys.RegTOFU
	case req.KeysSealed != "":
		if sealKey, err = s.SealKey(req.Secret); err != nil {
			return ClaimResult{}, err
		}
		reg = keys.RegQR
	}

	var res ClaimResult
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.NowMS()
		var row store.PairCodeRow
		var err error
		if typed {
			row, err = tx.LivePairCodeByCodeHash(h, now)
		} else {
			row, err = tx.LivePairCodeBySecretHash(h, now)
		}
		if errors.Is(err, store.ErrNotFound) {
			return ErrInvalid
		}
		if err != nil {
			return err
		}
		// Belt and braces: the row was found by this hash; make the equality explicit and
		// constant-time.
		stored := row.SecretHash
		if typed {
			stored = row.CodeHash
		}
		if !hmac.Equal(stored, h) {
			return ErrInvalid
		}
		if row.Kind == "owner" {
			return conflict("this is an owner sign-in link; open it in the board instead")
		}
		if req.ExpectKind != "" && req.ExpectKind != row.Kind {
			return conflict(fmt.Sprintf("this code is for a %s, not a %s", row.Kind, req.ExpectKind))
		}
		machine := ""
		if auth.NeedsMachine(row.Kind) {
			machine = req.Machine
			if machine == "" {
				machine = row.MachineID
			}
			if machine == "" {
				return badRequest("machine is required")
			}
			if !machinePattern.MatchString(machine) {
				return badRequest("machine is not a valid machine id")
			}
			if row.MachineID != "" && row.MachineID != machine {
				return badRequest("this code is for a different machine")
			}
		}
		if req.Cookie && auth.NeedsMachine(row.Kind) {
			return badRequest("cookie mode is only for phone and browser codes")
		}
		if name == "" {
			name = row.Name
		}
		if name == "" {
			name = row.Kind + " " + time.UnixMilli(now).UTC().Format("2006-01-02")
		}
		p, err := auth.Prepare(auth.CreateRequest{Kind: row.Kind, Name: name, Machine: machine, CreatedBy: "pair:" + row.ID}, time.UnixMilli(now))
		if err != nil {
			return badRequest(err.Error())
		}
		if err := p.Insert(tx); err != nil {
			return err
		}
		if sealKey != nil {
			// Opened only once the secret matched a live code: an unknown secret is still a
			// 401 that the limiter counts. A seal that does not open is 400 and the
			// transaction rolls back, so a tampered request cannot burn the phone's code.
			plain, err := keys.OpenKeys(sealKey, req.Secret, req.KeysSealed)
			if err != nil {
				return badRequest(keys.ErrSeal.Error())
			}
			var dk keys.DeviceKeysNew
			dec := json.NewDecoder(bytes.NewReader(plain))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&dk); err != nil {
				return badRequest(keys.ErrSeal.Error())
			}
			devKeys = &dk
		}
		var cert *keys.Doc
		if devKeys != nil {
			// The keys must match the code's kind; a failure rolls everything back and the
			// code stays live (keys.md 7.1 steps 2 and 3).
			if err := keys.CheckKeys(row.Kind, *devKeys, row.Kind == "phone"); err != nil {
				return badRequest(keys.ErrSeal.Error() + ": " + err.Error())
			}
			d, err := s.Keys.RegisterTx(tx, p.Device, *devKeys, reg)
			if err != nil {
				var ke *hubkeys.Error
				if errors.As(err, &ke) {
					return badRequest(ke.Msg)
				}
				return err
			}
			cert = &d
		}
		won, err := tx.ClaimPairCode(row.ID, p.Device.ID, ip, now)
		if err != nil {
			return err
		}
		if !won {
			return ErrInvalid // a concurrent claim won: roll the device back
		}
		res = ClaimResult{Device: p.Device, Token: p.Token, PairID: row.ID, HubURL: row.URL, Machine: machine, Cert: cert}
		if cert != nil {
			res.Reg = reg
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			if typed {
				failed = true
			} else {
				s.Limiter.Fail(ip, false)
			}
			s.logf("pair claim fail ip=%s kind=%s", ip, kindLabel)
		}
		return ClaimResult{}, err
	}
	s.logf("pair claim ok device=%s kind=%s code=%s", res.Device.ID, res.Device.Kind, res.PairID)
	return res, nil
}

// OwnerSignIn swaps an owner link's secret for an owner session: a browser device with
// scopes owner, read and push, a sy_o_ token and a 30 day expiry (pairing.md 4). The
// caller sets the cookie. Errors as Claim; a secret of any other kind of code is ErrInvalid
// and the code is left alone.
func (s *Service) OwnerSignIn(ctx context.Context, ip, secret, name string) (auth.Created, error) {
	if len(secret) < 22 || len(secret) > 64 {
		return auth.Created{}, badRequest("secret must be 22 to 64 characters")
	}
	name = cleanName(name)
	if len([]rune(name)) > MaxNameLen {
		return auth.Created{}, badRequest(fmt.Sprintf("name is longer than %d characters", MaxNameLen))
	}
	if ok, retry := s.Limiter.Allow(ip, false); !ok {
		s.logf("pair owner limited ip=%s retry=%ds", ip, int(retry.Seconds()))
		return auth.Created{}, &LimitError{RetryAfter: retry}
	}
	pepper, err := s.key()
	if err != nil {
		return auth.Created{}, err
	}
	h, err := s.SecretHash(secret)
	if err != nil {
		return auth.Created{}, err
	}
	if name == "" {
		name = "Owner session"
	}
	var out auth.Created
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.NowMS()
		row, err := tx.LivePairCodeBySecretHash(h, now)
		if errors.Is(err, store.ErrNotFound) {
			return ErrInvalid
		}
		if err != nil {
			return err
		}
		if !hmac.Equal(row.SecretHash, h) || row.Kind != "owner" {
			return ErrInvalid
		}
		p, err := auth.Prepare(auth.CreateRequest{Kind: "browser", Name: name, CreatedBy: "owner-link:" + row.ID, Owner: true, Pepper: pepper}, time.UnixMilli(now))
		if err != nil {
			return err
		}
		if err := p.Insert(tx); err != nil {
			return err
		}
		won, err := tx.ClaimPairCode(row.ID, p.Device.ID, ip, now)
		if err != nil {
			return err
		}
		if !won {
			return ErrInvalid
		}
		out = p.Created
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			s.Limiter.Fail(ip, false)
			s.logf("pair owner fail ip=%s", ip)
		}
		return auth.Created{}, err
	}
	s.logf("pair owner ok device=%s", out.Device.ID)
	return out, nil
}

// Status values of a pairing code.
const (
	StatusLive      = "live"
	StatusClaimed   = "claimed"
	StatusCancelled = "cancelled"
	StatusExpired   = "expired"
)

// Status is what the board and the CLI poll: never the code or the secret.
type Status struct {
	ID, Kind, State string
	ExpiresAt       time.Time
	ClaimedAt       time.Time // zero unless claimed
	Device          *store.Device
}

// Status reports a code's state (ErrNotFound if there is no such code).
func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	row, err := s.Store.PairCodeByID(ctx, id)
	if err != nil {
		return Status{}, err
	}
	st := Status{ID: row.ID, Kind: row.Kind, ExpiresAt: time.UnixMilli(row.ExpiresAt).UTC()}
	switch {
	case row.ClaimedAt != 0:
		st.State = StatusClaimed
		st.ClaimedAt = time.UnixMilli(row.ClaimedAt).UTC()
		if d, err := s.Store.DeviceByID(ctx, row.DeviceID); err == nil {
			st.Device = &d
		}
	case row.CancelledAt != 0:
		st.State = StatusCancelled
	case row.ExpiresAt <= s.Store.Now().UnixMilli():
		st.State = StatusExpired
	default:
		st.State = StatusLive
	}
	return st, nil
}

// Cancel cancels a code that is still live; a claimed, cancelled or expired one is left as
// it is (no error). ErrNotFound when there is no such code.
func (s *Service) Cancel(ctx context.Context, id string) error {
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		found, err := tx.CancelPairCode(id, tx.NowMS())
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		return nil
	})
}
