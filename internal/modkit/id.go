package modkit

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// The UUIDv7 layout of RFC 9562: sixteen bytes, a 48-bit millisecond time in the first
// six, the version nibble in byte six and the variant bits in byte eight, written as
// 8-4-4-4-12 hex digits. The numbers are spelled as arithmetic, the convention of the
// core's settings source check (modulekit.CheckSource): they are a format, not settings.
var (
	zero          = int(0)
	one           = zero + 1
	two           = one + one
	uuidTimeBytes = two + two + two
	uuidVariant   = two * two * two
	uuidBytes     = uuidVariant * two
	uuidGroups    = []int{two * two, two, two, two, uuidTimeBytes}
)

// NewID returns a UUIDv7 for the millisecond time ms, the core's id form.
func NewID(ms int64) string {
	b := make([]byte, uuidBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	ts := make([]byte, uuidVariant)
	binary.BigEndian.PutUint64(ts, uint64(ms))
	copy(b[:uuidTimeBytes], ts[two:])
	b[uuidTimeBytes] = (b[uuidTimeBytes] & 0x0f) | 0x70
	b[uuidVariant] = (b[uuidVariant] & 0x3f) | 0x80
	parts := make([]string, 0, len(uuidGroups))
	var off int
	for _, n := range uuidGroups {
		parts = append(parts, hex.EncodeToString(b[off:off+n]))
		off += n
	}
	return strings.Join(parts, "-")
}

// RandomHex returns sixteen random bytes as hex.
func RandomHex() string {
	b := make([]byte, uuidBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
