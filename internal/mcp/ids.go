package mcp

import (
	"errors"
	"fmt"

	"github.com/cplieger/vibekit/internal/ids"
)

// ServerID is a validated identifier for an MCP server record. Values
// are generated at Create time via newID() and are base32-encoded random
// values. The type prevents accidental Name-as-ID confusion at compile
// time, mirroring api.ChatID and api.SessionID.
type ServerID string

// ParseServerID validates a raw string as a server ID. Returns an error
// if the value is empty or exceeds 32 characters.
func ParseServerID(raw string) (ServerID, error) {
	if raw == "" {
		return "", errors.New("empty server id")
	}
	if len(raw) > 32 {
		return "", fmt.Errorf("server id too long: %d chars (max 32)", len(raw))
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
