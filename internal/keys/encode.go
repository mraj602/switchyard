package keys

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// B64 is base64url without padding, the form of every binary value in JSON (keys.md 2).
func B64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// UnB64 decodes base64url without padding. Padding or the standard alphabet is an error.
func UnB64(s string) ([]byte, error) { return base64.RawURLEncoding.Strict().DecodeString(s) }

// unB64Len decodes s and checks it is exactly n bytes.
func unB64Len(s string, n int, what string) ([]byte, error) {
	b, err := UnB64(s)
	if err != nil || len(b) != n {
		return nil, errors.New(what + ": want " + strconv.Itoa(n) + " bytes of base64url")
	}
	return b, nil
}

// b32 is RFC 4648 base32 without padding.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// FPLen is the length of a fingerprint: 15 bytes of SHA-256 in base32, 24 characters.
const FPLen = 24

// Fingerprint is lower(base32(SHA-256(pub)[0:15])), exactly 24 characters (keys.md 2).
func Fingerprint(pub []byte) string {
	h := sha256.Sum256(pub)
	return strings.ToLower(b32.EncodeToString(h[:15]))
}

// ValidFP reports whether s has the shape of a fingerprint (24 of a-z, 2-7).
func ValidFP(s string) bool {
	if len(s) != FPLen {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// GroupFP shows a fingerprint in 6 groups of 4: k7q2-m4xa-pz3c-ufh6-2wbe-q5no.
func GroupFP(fp string) string {
	if len(fp) != FPLen {
		return fp
	}
	var parts []string
	for i := 0; i < FPLen; i += 4 {
		parts = append(parts, fp[i:i+4])
	}
	return strings.Join(parts, "-")
}

// NormalizeFP accepts a fingerprint with or without the group dashes or spaces, any case.
func NormalizeFP(s string) (string, bool) {
	s = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
	return s, ValidFP(s)
}

// TimeFormat is the time form in document bodies and key files: UTC, milliseconds, "Z".
const TimeFormat = "2006-01-02T15:04:05.000Z"

// FormatTime renders t in TimeFormat.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }
