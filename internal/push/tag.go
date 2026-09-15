package push

import (
	"crypto/sha256"
	"encoding/base64"
)

// TagLen is the length of a client tag: 22 base64url characters carry 132 bits,
// inside the [A-Za-z0-9_-]{1,64} grammar of the SSE-Client header.
const TagLen = 22

// TagOf derives the presence tag of a push subscription from its endpoint:
// base64url(sha256(endpoint)) truncated to TagLen. The page computes the same
// value from registration.pushManager.getSubscription() and presents it as
// SSE-Client, so a presence row and the subscription it silences share a key with
// no persisted field. The endpoint is a capability URL, so the tag is unforgeable
// without it and safe to log where the endpoint is not.
func TagOf(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:TagLen]
}
