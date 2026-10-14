package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// RFC 8291 (Web Push message encryption) over RFC 8188 (aes128gcm), single record only.

const (
	recordSize = 4096 // rs written in the header; one record
	headerLen  = 16 + 4 + 1 + 65
	tagLen     = 16
	// MaxPlaintext is the most a single record can carry: 4096 - 86 header - 16 tag - 1
	// delimiter byte.
	MaxPlaintext = recordSize - headerLen - tagLen - 1
)

// PadMultiple is the default padding unit (push.md 6.3): the record is padded so the push
// service learns only a coarse size.
const PadMultiple = 128

// ErrTooLarge is returned when the payload does not fit one record.
var ErrTooLarge = errors.New("webpush: payload too large for one record")

// Encrypt makes the body of a Web Push request: header (salt, rs, idlen, as_public) and one
// aes128gcm record. uaPublic is the subscription's p256dh (65 bytes, uncompressed) and
// authSecret its auth (16 bytes). rnd supplies the ephemeral private key (32 bytes, read
// first) and the salt (16 bytes, read second), so a fixed reader reproduces the RFC 8291
// example. padMultiple > 0 pads the record with zero bytes up to the next multiple of that
// size (never beyond one record); 0 adds no padding.
func Encrypt(rnd io.Reader, plaintext, uaPublic, authSecret []byte, padMultiple int) ([]byte, error) {
	if len(authSecret) != 16 {
		return nil, errors.New("webpush: auth secret must be 16 bytes")
	}
	if len(plaintext) > MaxPlaintext {
		return nil, ErrTooLarge
	}
	curve := ecdh.P256()
	uaPub, err := curve.NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("webpush: bad p256dh: %w", err)
	}
	var scalar [32]byte
	var asPriv *ecdh.PrivateKey
	for tries := 0; ; tries++ {
		if _, err := io.ReadFull(rnd, scalar[:]); err != nil {
			return nil, fmt.Errorf("webpush: random: %w", err)
		}
		if asPriv, err = curve.NewPrivateKey(scalar[:]); err == nil {
			break
		}
		if tries > 8 { // a scalar out of range: astronomically unlikely, so a stuck reader
			return nil, errors.New("webpush: no valid ephemeral key from the random source")
		}
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rnd, salt); err != nil {
		return nil, fmt.Errorf("webpush: random: %w", err)
	}
	return encrypt(plaintext, uaPub, authSecret, asPriv, salt, padMultiple)
}

func encrypt(plaintext []byte, uaPub *ecdh.PublicKey, authSecret []byte, asPriv *ecdh.PrivateKey, salt []byte, padMultiple int) ([]byte, error) {
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: ecdh: %w", err)
	}
	asPub := asPriv.PublicKey().Bytes()
	keyInfo := make([]byte, 0, 14+65+65)
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPub.Bytes()...)
	keyInfo = append(keyInfo, asPub...)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	record := append(append(make([]byte, 0, MaxPlaintext+1), plaintext...), 0x02) // 0x02: last record
	if padMultiple > 0 {
		target := (len(record) + padMultiple - 1) / padMultiple * padMultiple
		if target > MaxPlaintext+1 {
			target = MaxPlaintext + 1
		}
		record = append(record, make([]byte, target-len(record))...)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	body := make([]byte, 0, headerLen+len(record)+tagLen)
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, recordSize)
	body = append(body, byte(len(asPub)))
	body = append(body, asPub...)
	return gcm.Seal(body, nonce, record, nil), nil
}

// ValidateKeys checks a subscription's keys as registration does (push.md 6.3): p256dh is
// base64url (padding tolerated) for a point on P-256, auth for 16 bytes. It returns the
// decoded values.
func ValidateKeys(p256dh, auth string) (pub, authSecret []byte, err error) {
	if pub, err = decodeB64(p256dh); err != nil {
		return nil, nil, errors.New("p256dh is not base64url")
	}
	if _, err = ecdh.P256().NewPublicKey(pub); err != nil {
		return nil, nil, errors.New("p256dh is not a P-256 point")
	}
	if authSecret, err = decodeB64(auth); err != nil {
		return nil, nil, errors.New("auth is not base64url")
	}
	if len(authSecret) != 16 {
		return nil, nil, errors.New("auth must be 16 bytes")
	}
	return pub, authSecret, nil
}

func decodeB64(s string) ([]byte, error) {
	for len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}
	return base64.RawURLEncoding.DecodeString(s)
}
