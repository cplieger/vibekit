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
//
// Nothing here returns an error. Every mint draws from crypto/rand, whose Read
// has been documented since Go 1.24 never to fail — it crashes the program
// instead — so an error return would be a branch no test can reach and no
// caller can act on.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"uuid"
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
// length is ceil(byteLen*8/5) characters).
//
// It panics on an Encoding value that is not one of the constants above.
// That is not a failure a caller can handle: the argument is always a
// compile-time constant at the call site, so an unknown one means the
// program contradicts itself rather than that this call went wrong.
func New(byteLen int, enc Encoding) string {
	b := make([]byte, byteLen)
	rand.Read(b)
	switch enc {
	case HexUpper:
		return base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	case StdLower:
		s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
		return strings.ToLower(s)
	default:
		panic(fmt.Sprintf("ids: unknown encoding %d", enc))
	}
}

// NewMessageID returns a UUIDv7 (RFC 9562): time-ordered, globally unique,
// standard format. Consecutive ids sort in increasing order, sub-millisecond
// calls included, because the stdlib packs a 12-bit fraction of the
// millisecond beside the 48-bit timestamp.
//
// This is the one place vibekit decides which UUID version it mints, and it is
// the mint half of the rule ValidMessageID reads in the other direction.
func NewMessageID() string {
	return uuid.NewV7().String()
}
