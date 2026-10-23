package appmirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// Signature scheme block ids in the APK signing block (v3 first, then v2).
const (
	idV3 = 0xf05368c0
	idV2 = 0x7109871a
)

var sigBlockMagic = []byte("APK Sig Block 42")

// APKSignerCerts returns the SHA-256 fingerprint of the first certificate of every signer in the
// APK's v3 (or else v2) signing block. It reads what the file claims; it does not verify the
// signature (Android does that at install, and the phone also compares the certificate with the
// installed app's). The mirror uses it so the manifest's cert_sha256 cannot disagree with the
// file it describes.
func APKSignerCerts(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	// End of central directory: the last 22 bytes plus a comment of up to 65535 bytes.
	tail := int64(22 + 65535)
	if tail > size {
		tail = size
	}
	buf := make([]byte, tail)
	if _, err := f.ReadAt(buf, size-tail); err != nil && err != io.EOF {
		return nil, err
	}
	eocd := bytes.LastIndex(buf, []byte{'P', 'K', 5, 6})
	if eocd < 0 || eocd+22 > len(buf) {
		return nil, errors.New("apk: no central directory")
	}
	cd := int64(binary.LittleEndian.Uint32(buf[eocd+16:]))
	if cd < 32 || cd > size {
		return nil, errors.New("apk: bad central directory offset")
	}
	foot := make([]byte, 24)
	if _, err := f.ReadAt(foot, cd-24); err != nil {
		return nil, errors.New("apk: no signing block")
	}
	if !bytes.Equal(foot[8:], sigBlockMagic) {
		return nil, errors.New("apk: no v2 or v3 signing block")
	}
	blockSize := binary.LittleEndian.Uint64(foot[:8]) // excludes the leading size field
	if blockSize < 24 || blockSize > uint64(cd)-8 || blockSize > 64<<20 {
		return nil, errors.New("apk: bad signing block size")
	}
	pairs := make([]byte, blockSize-24)
	start := cd - int64(blockSize) // the block begins 8 bytes earlier, with its size field
	if _, err := f.ReadAt(pairs, start); err != nil {
		return nil, fmt.Errorf("apk: signing block: %w", err)
	}
	var v2, v3 []byte
	for len(pairs) > 0 {
		if len(pairs) < 12 {
			return nil, errors.New("apk: bad signing block pair")
		}
		n := binary.LittleEndian.Uint64(pairs[:8])
		if n < 4 || n > uint64(len(pairs))-8 {
			return nil, errors.New("apk: bad signing block pair")
		}
		id := binary.LittleEndian.Uint32(pairs[8:12])
		val := pairs[12 : 8+n]
		switch id {
		case idV3:
			v3 = val
		case idV2:
			v2 = val
		}
		pairs = pairs[8+n:]
	}
	val := v3
	if val == nil {
		val = v2
	}
	if val == nil {
		return nil, errors.New("apk: no v2 or v3 signature")
	}
	return signerCerts(val)
}

// lenPrefixed splits one uint32-length-prefixed field off b.
func lenPrefixed(b []byte) (field, rest []byte, err error) {
	if len(b) < 4 {
		return nil, nil, errors.New("apk: truncated signature data")
	}
	n := binary.LittleEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, errors.New("apk: truncated signature data")
	}
	return b[4 : 4+n], b[4+n:], nil
}

// signerCerts reads a v2/v3 value: signers { signer { signedData { digests, certificates, ... }, ... } }.
func signerCerts(val []byte) ([]string, error) {
	signers, _, err := lenPrefixed(val)
	if err != nil {
		return nil, err
	}
	var out []string
	for len(signers) > 0 {
		var signer []byte
		if signer, signers, err = lenPrefixed(signers); err != nil {
			return nil, err
		}
		signed, _, err := lenPrefixed(signer)
		if err != nil {
			return nil, err
		}
		_, afterDigests, err := lenPrefixed(signed)
		if err != nil {
			return nil, err
		}
		certs, _, err := lenPrefixed(afterDigests)
		if err != nil {
			return nil, err
		}
		cert, _, err := lenPrefixed(certs)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(cert)
		out = append(out, hex.EncodeToString(sum[:]))
	}
	if len(out) == 0 {
		return nil, errors.New("apk: no signer")
	}
	return out, nil
}
