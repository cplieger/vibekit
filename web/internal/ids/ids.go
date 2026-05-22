// Package ids provides random identifier generation for stored records.
// All callers share the same entropy-minting logic; encoding and length
// are parameterised so each domain keeps its existing format.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
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
