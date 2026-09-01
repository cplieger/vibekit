package agent

import "github.com/cplieger/vibekit/internal/ids"

// newMessageID returns a UUIDv7 (RFC 9562): time-ordered, globally unique.
func newMessageID() string {
	return ids.NewMessageID()
}
