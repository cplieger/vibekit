package mcp

import (
	"errors"
	"fmt"

	"github.com/cplieger/vibekit/internal/ids"
)

// ServerID is a validated identifier for an MCP server record. Values
// are generated at Create time via newID() and are base32-encoded random
// values. The type prevents accidental Name-as-ID confusion at compile
// time, mirroring vibekit.ChatID and vibekit.SessionID.
type ServerID string

// IDMaxLen is the byte bound on a server id. Generated ids are 10 chars; the
// headroom is for an id minted by an older or a future encoding.
const IDMaxLen = 32

// ParseServerID validates a raw string as a server ID.
//
// The CHARSET is the shared one (mcp.NameAllowedRune), which is the half
// that has to agree with every other admission door. Two rules
// deliberately do NOT come from ValidateName: the BOUND is IDMaxLen
// (32), not mcp.NameMaxLen (64), and the LEADING-letter rule does not
// apply, since newID() is base32 lowercase and can open with a digit.
//
// TestNameDoorsAgree pins the shared half and the two stated
// differences together.
func ParseServerID(raw string) (ServerID, error) {
	if raw == "" {
		return "", errors.New("empty server id")
	}
	if len(raw) > IDMaxLen {
		return "", fmt.Errorf("server id too long: %d chars (max %d)", len(raw), IDMaxLen)
	}
	for _, r := range raw {
		if !NameAllowedRune(r) {
			return "", fmt.Errorf("server id has an illegal character: %q", raw)
		}
	}
	return ServerID(raw), nil
}

// String implements fmt.Stringer.
func (id ServerID) String() string { return string(id) }

// newID returns a short random identifier for a server record. 10 chars
// of base32 lowercase is ~48 bits of entropy (6 bytes of randomness;
// ample for a single user's configured set).
func newID() ServerID {
	return ServerID(ids.New(6, ids.StdLower))
}
