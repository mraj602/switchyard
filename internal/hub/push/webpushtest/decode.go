package webpushtest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Decode is an independent RFC 8188 / RFC 8291 receiver. It shares no code with the
// webpush package (not even the HKDF: it is spelled out with HMAC here), so a mistake in
// the encoder cannot cancel out in the decoder.
//
// It returns the plaintext and the number of padding bytes that followed it.
func Decode(body []byte, uaPrivate *ecdh.PrivateKey, authSecret []byte) (plain []byte, padding int, err error) {
	if len(body) < 16+4+1 {
		return nil, 0, errors.New("body shorter than the aes128gcm header")
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idlen := int(body[20])
	if len(body) < 21+idlen {
		return nil, 0, errors.New("body shorter than its key id")
	}
	asPubBytes := body[21 : 21+idlen]
	cipherText := body[21+idlen:]
	if rs < 18 || rs > 4096 {
		return nil, 0, fmt.Errorf("record size %d outside 18..4096", rs)
	}
	if uint32(len(cipherText)) > rs {
		return nil, 0, errors.New("more than one record (not produced by the hub)")
	}
	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("sender key: %w", err)
	}
	secret, err := uaPrivate.ECDH(asPub)
	if err != nil {
		return nil, 0, err
	}
	keyInfo := bytes.Join([][]byte{[]byte("WebPush: info\x00"), uaPrivate.PublicKey().Bytes(), asPubBytes}, nil)
	ikm := hkdf(authSecret, secret, keyInfo, 32)
	cek := hkdf(salt, ikm, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdf(salt, ikm, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, err
	}
	record, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("decrypt: %w", err)
	}
	// Strip padding: zeros, then the delimiter 0x02 (last record) from the end.
	end := len(record)
	for end > 0 && record[end-1] == 0 {
		end--
	}
	if end == 0 || record[end-1] != 0x02 {
		return nil, 0, errors.New("missing last-record delimiter 0x02")
	}
	return record[:end-1], len(record) - end, nil
}

// hkdf is RFC 5869 with SHA-256: extract with salt, expand to n <= 32 bytes (one block).
func hkdf(salt, ikm, info []byte, n int) []byte {
	ext := hmac.New(sha256.New, salt)
	ext.Write(ikm)
	prk := ext.Sum(nil)
	exp := hmac.New(sha256.New, prk)
	exp.Write(info)
	exp.Write([]byte{1})
	return exp.Sum(nil)[:n]
}
