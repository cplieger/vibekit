// Package ids is vibekit's identifier vocabulary: it mints the ids the app
// owns and validates every id the app accepts.
//
// The two halves belong together because they are one rule read in two
// directions. NewMessageID emits a UUIDv7 and ValidMessageID says what a
// message id may be; a caller changing either without the other in view is
// how a minted id becomes one its own boundary rejects. The validators cover
// ids vibekit does NOT mint as well — an ACP session id, an agent or model
// identifier, a client-generated chat id — because the question they answer is
// about the identifier, not about who made it.
//
// Three of the five validators are filesystem gates rather than format
// preferences: a chat id names the chat's own JSON file, and a session id is
// concatenated into a path under $KIRO_HOME/sessions/.
//
// The validators used to live in internal/vibekit beside the wire and domain
// types, which put a path-traversal gate inside the package the code generator
// walks for the cross-language type contract.
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
	s, err := NewE(byteLen, enc)
	if err != nil {
		panic(err)
	}
	return s
}

// NewE is like New but returns an error instead of panicking when
// crypto/rand fails. Prefer this in library code where the caller
// can handle the failure gracefully.
func NewE(byteLen int, enc Encoding) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ids: crypto/rand.Read: %w", err)
	}
	switch enc {
	case HexUpper:
		return base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
	case StdLower:
		s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
		return strings.ToLower(s), nil
	default:
		return "", fmt.Errorf("ids: unknown encoding %d", enc)
	}
}

// NewMessageID returns a UUIDv7 (RFC 9562): time-ordered, globally
// unique, standard format.
func NewMessageID() string {
	s, err := NewMessageIDE()
	if err != nil {
		panic(fmt.Errorf("ids: NewMessageIDE: %w", err))
	}
	return s
}

// NewMessageIDE is like NewMessageID but returns an error instead of
// panicking when crypto/rand fails.
func NewMessageIDE() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ids: crypto/rand.Read: %w", err)
	}
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40) //nolint:gosec // G115: ID encoding
	b[1] = byte(ms >> 32) //nolint:gosec // G115: ID encoding
	b[2] = byte(ms >> 24) //nolint:gosec // G115: ID encoding
	b[3] = byte(ms >> 16) //nolint:gosec // G115: ID encoding
	b[4] = byte(ms >> 8)  //nolint:gosec // G115: ID encoding
	b[5] = byte(ms)       //nolint:gosec // G115: ID encoding
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
	return string(buf[:]), nil
}
