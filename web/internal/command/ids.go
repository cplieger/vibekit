package command

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewMessageID returns a UUIDv7 (RFC 9562): time-ordered, globally
// unique, standard format.
func NewMessageID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40) //nolint:gosec // G115: ID encoding
	b[1] = byte(ms >> 32) //nolint:gosec // G115: ID encoding
	b[2] = byte(ms >> 24) //nolint:gosec // G115: ID encoding
	b[3] = byte(ms >> 16) //nolint:gosec // G115: ID encoding
	b[4] = byte(ms >> 8) //nolint:gosec // G115: ID encoding
	b[5] = byte(ms) //nolint:gosec // G115: ID encoding
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}
