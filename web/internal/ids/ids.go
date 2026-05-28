// Package ids provides random identifier generation for stored records.
// All callers share the same entropy-minting logic; encoding and length
// are parameterised so each domain keeps its existing format.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Encoding selects the base32 variant used for the output string.
type Encoding int

const (
	// HexUpper uses RFC 4648 base32hex (0-9 A-V), uppercase, no padding.
	HexUpper Encoding = iota
	// StdLower uses RFC 4648 base32 standard (a-z 2-7), lowercase, no padding.
	StdLower
)

// New returns a random identifier. byteLen controls entropy (output
// length is ceil(byteLen*8/5) characters). Panics if crypto/rand fails.
func New(byteLen int, enc Encoding) string {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("ids: crypto/rand.Read: %w", err))
	}
	switch enc {
	case HexUpper:
		return base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	case StdLower:
		s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
		return strings.ToLower(s)
	default:
		panic(fmt.Errorf("ids: unknown encoding %d", enc))
	}
}

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
