package api

// Push types: Web Push subscription shapes and push notification kind
// constants. Consumed by the push package and the hub's push-notification
// path.

// PushSubscription is a Web Push subscription from the browser (RFC 8030).
type PushSubscription struct {
	Endpoint string               `json:"endpoint"`
	Keys     PushSubscriptionKeys `json:"keys"`
}

// PushSubscriptionKeys holds the client-side encryption keys.
type PushSubscriptionKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// PushKind identifies a push notification category. The underlying
// string is the wire value stored in settings and matched by the
// client's service worker.
type PushKind string

// Push notification kind constants. These live in the api package so
// hub callers can reference them without importing the push package
// (eliminating the hub→push import that existed solely for constant
// access).
const (
	PushKindAgentFinished PushKind = "agent_finished"
	PushKindPermission    PushKind = "permission"
)

// Valid reports whether k is a known push notification kind.
// Used at the settings-load boundary to reject corrupted keys
// at load time rather than at send time.
func (k PushKind) Valid() bool {
	switch k {
	case PushKindAgentFinished, PushKindPermission:
		return true
	}
	return false
}
