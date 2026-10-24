package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The WYXE container (machines.md 8.1 step 5): a passphrase-encrypted export, standard
// library only.
//
//	"WYXE" | 0x01 version | 0x01 KDF id | uint32 iterations | 16-byte salt | 8-byte nonce prefix
//	then chunks: uint32 length | AES-256-GCM ciphertext (plaintext up to 64 KiB, plus a 16-byte tag)
//
// Key: PBKDF2-HMAC-SHA256 (crypto/pbkdf2, standard library since Go 1.24) over the passphrase,
// 600 000 iterations, 32 bytes. scrypt and argon2 live in golang.org/x/crypto, which this
// project does not depend on, so PBKDF2 is the strongest
// stock choice; the iteration count is in the header, so a later release can raise it.
// Each chunk's nonce is the prefix plus its index (uint32, big endian) and its additional data
// is "wyxe1" plus the index plus a last-chunk flag, so chunks cannot be reordered, dropped or
// duplicated and a truncated file fails for want of its last chunk.
const (
	wyxeMagic     = "WYXE"
	wyxeVersion   = 1
	wyxeKDFPBKDF2 = 1
	wyxeIter      = 600_000
	wyxeMinIter   = 100_000
	wyxeMaxIter   = 20_000_000
	wyxeChunk     = 64 << 10
	wyxeTag       = 16
)

// ErrWrongPassphrase is what a failed chunk reports: a wrong passphrase and a damaged file
// are indistinguishable by design.
var ErrWrongPassphrase = errors.New("cannot decrypt the export: wrong passphrase, or the file is damaged or truncated")

func wyxeAAD(index uint32, last bool) []byte {
	aad := make([]byte, 0, 10)
	aad = append(aad, "wyxe1"...)
	aad = binary.BigEndian.AppendUint32(aad, index)
	if last {
		return append(aad, 1)
	}
	return append(aad, 0)
}

func wyxeNonce(prefix []byte, index uint32) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[8:], index)
	return n
}

func wyxeKey(pass string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, salt, iter, 32)
}

func wyxeAEAD(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// wyxeWriter encrypts what is written to it. Close writes the last chunk and must be called.
type wyxeWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	prefix []byte
	index  uint32
	buf    []byte // pending plaintext, at most wyxeChunk
	done   bool
}

func newWyxeWriter(w io.Writer, pass string) (*wyxeWriter, error) {
	salt := randBytes(16)
	prefix := randBytes(8)
	key, err := wyxeKey(pass, salt, wyxeIter)
	if err != nil {
		return nil, err
	}
	aead, err := wyxeAEAD(key)
	if err != nil {
		return nil, err
	}
	hdr := []byte(wyxeMagic)
	hdr = append(hdr, wyxeVersion, wyxeKDFPBKDF2)
	hdr = binary.BigEndian.AppendUint32(hdr, wyxeIter)
	hdr = append(hdr, salt...)
	hdr = append(hdr, prefix...)
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return &wyxeWriter{w: w, aead: aead, prefix: prefix}, nil
}

func (e *wyxeWriter) seal(last bool) error {
	ct := e.aead.Seal(nil, wyxeNonce(e.prefix, e.index), e.buf, wyxeAAD(e.index, last))
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct)))
	if _, err := e.w.Write(l[:]); err != nil {
		return err
	}
	if _, err := e.w.Write(ct); err != nil {
		return err
	}
	e.index++
	e.buf = e.buf[:0]
	return nil
}

func (e *wyxeWriter) Write(p []byte) (int, error) {
	if e.done {
		return 0, errors.New("wyxe: write after close")
	}
	n := len(p)
	for len(p) > 0 {
		// A full buffer is sealed only when more data follows, so Close can mark the last chunk.
		if len(e.buf) == wyxeChunk {
			if err := e.seal(false); err != nil {
				return 0, err
			}
		}
		k := min(wyxeChunk-len(e.buf), len(p))
		e.buf = append(e.buf, p[:k]...)
		p = p[k:]
	}
	return n, nil
}

// Close seals the last chunk (possibly empty).
func (e *wyxeWriter) Close() error {
	if e.done {
		return nil
	}
	e.done = true
	return e.seal(true)
}

// wyxeReader decrypts a WYXE stream.
type wyxeReader struct {
	r      io.Reader
	aead   cipher.AEAD
	prefix []byte
	index  uint32
	buf    []byte
	peek   []byte // one byte read ahead to know whether a chunk is the last
	have   bool
	eof    bool
}

func newWyxeReader(r io.Reader, pass string) (*wyxeReader, error) {
	hdr := make([]byte, 4+1+1+4+16+8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, errors.New("the encrypted export is truncated")
	}
	if string(hdr[:4]) != wyxeMagic {
		return nil, errors.New("not an encrypted export")
	}
	if hdr[4] != wyxeVersion {
		return nil, fmt.Errorf("encrypted export version %d is not supported by this wy", hdr[4])
	}
	if hdr[5] != wyxeKDFPBKDF2 {
		return nil, fmt.Errorf("key derivation %d is not supported by this wy", hdr[5])
	}
	iter := int(binary.BigEndian.Uint32(hdr[6:10]))
	if iter < wyxeMinIter || iter > wyxeMaxIter {
		return nil, fmt.Errorf("encrypted export asks for %d key iterations: refusing", iter)
	}
	key, err := wyxeKey(pass, hdr[10:26], iter)
	if err != nil {
		return nil, err
	}
	aead, err := wyxeAEAD(key)
	if err != nil {
		return nil, err
	}
	return &wyxeReader{r: r, aead: aead, prefix: append([]byte(nil), hdr[26:34]...)}, nil
}

// more reports whether any bytes follow the current position.
func (d *wyxeReader) more() (bool, error) {
	if d.have {
		return true, nil
	}
	var b [1]byte
	n, err := d.r.Read(b[:])
	for n == 0 && err == nil {
		n, err = d.r.Read(b[:])
	}
	if n == 1 {
		d.peek, d.have = []byte{b[0]}, true
		return true, nil
	}
	if err == io.EOF {
		return false, nil
	}
	return false, err
}

func (d *wyxeReader) full(p []byte) error {
	off := 0
	if d.have {
		p[0] = d.peek[0]
		d.have = false
		off = 1
	}
	_, err := io.ReadFull(d.r, p[off:])
	return err
}

func (d *wyxeReader) next() error {
	more, err := d.more()
	if err != nil {
		return err
	}
	if !more {
		return ErrWrongPassphrase // the last chunk never came: truncated
	}
	var l [4]byte
	if err := d.full(l[:]); err != nil {
		return ErrWrongPassphrase
	}
	n := binary.BigEndian.Uint32(l[:])
	if n < wyxeTag || n > wyxeChunk+wyxeTag {
		return ErrWrongPassphrase
	}
	ct := make([]byte, n)
	if err := d.full(ct); err != nil {
		return ErrWrongPassphrase
	}
	after, err := d.more()
	if err != nil {
		return err
	}
	pt, err := d.aead.Open(nil, wyxeNonce(d.prefix, d.index), ct, wyxeAAD(d.index, !after))
	if err != nil {
		return ErrWrongPassphrase
	}
	d.index++
	d.buf = pt
	if !after {
		d.eof = true
	}
	return nil
}

func (d *wyxeReader) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.eof {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}
