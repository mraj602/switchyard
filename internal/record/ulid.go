package record

import (
	"crypto/rand"
	"errors"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID (26 characters, Crockford base32, upper case) for time t: 48 bits of
// milliseconds followed by 80 random bits from crypto/rand. It panics only if the system
// random source fails, which leaves no safe way to make a record id.
func NewID(t time.Time) string {
	id, err := newID(t)
	if err != nil {
		panic("record: no random source: " + err.Error())
	}
	return id
}

func newID(t time.Time) (string, error) {
	ms := uint64(t.UnixMilli())
	var rnd [10]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	var out [26]byte
	// 10 characters of time: 50 bits, the top two are always zero for 48-bit times.
	for i := 9; i >= 0; i-- {
		out[i] = crockford[ms&31]
		ms >>= 5
	}
	// 16 characters of randomness: 80 bits.
	var acc uint32
	var bits uint
	j := 10
	for _, b := range rnd {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[j] = crockford[(acc>>bits)&31]
			j++
		}
	}
	return string(out[:]), nil
}

// ULIDTime returns the time encoded in a ULID made by NewID.
func ULIDTime(id string) (time.Time, error) {
	if len(id) != 26 {
		return time.Time{}, errors.New("record: not a ULID")
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		v := -1
		for k := 0; k < 32; k++ {
			if crockford[k] == id[i] {
				v = k
				break
			}
		}
		if v < 0 {
			return time.Time{}, errors.New("record: not a ULID")
		}
		ms = ms<<5 | uint64(v)
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}
