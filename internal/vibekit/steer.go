package vibekit

// SteerIDPrefix is what KAS puts in front of a steer's message id. It also keeps
// a steer's id space clear of an agent notice's, which takes `notify-` instead.
const SteerIDPrefix = "steer-"

// SteerIDFor mints the id a steer will be known by: `steer-<messageID>`.
//
// It is DERIVABLE rather than only server-assigned, and that is load-bearing twice
// over. KAS's own `handleSessionSteer` prefixes the `messageId` the caller sent and
// stamps that same value on both the response and the `steering_queued`
// notification, so a caller knows the id BEFORE the call returns — which is what
// lets the steer ledger be written ahead of the RPC instead of racing the
// notification (internal/command/steer.go). The client mints the same id at submit
// to key its optimistic row, so `static-src/store.ts` steerIDFor is the twin and
// must agree with this on the prefix.
func SteerIDFor(messageID string) string {
	return SteerIDPrefix + messageID
}

// SteerOrigin says WHOSE words a mid-turn steer carries.
//
// KAS's steering buffer is the only inbound channel into a live turn, so it
// carries the user's own correction AND a workflow reporting into the chat that
// launched it. Measured on the live store (2026-09-03, KAS 2.21.0), all three
// producers persist identically and `notificationSeverity` cannot separate them:
// it is set only when the TEXT carries a `[notification/<sev>]` prefix. So the
// server records the steers IT sent, and everything else is the agent's.
type SteerOrigin string

// The two origins. Each string is the wire value AND the client's SteerOrigin
// union member, so a rename here is a cross-language change.
//
// There is deliberately no "unknown": the ledger answers for every id, and a
// third value would put a label the client has no wording for on the wire.
const (
	// SteerOriginUser is a steer this server sent on the user's behalf.
	SteerOriginUser SteerOrigin = "user"
	// SteerOriginAgent is a steer that arrived from KAS's own buffer: a
	// workflow step's report, or a run-completion nudge.
	SteerOriginAgent SteerOrigin = "agent"
)

// SteerState says whether the model ever READ a mid-turn steer. Rendering an
// undelivered correction like a delivered one is a false statement about the
// reader's own message.
//
// ABSENT means NOT KNOWN, a third answer rather than a missing value: neither the
// legacy population nor a session/load replay row carries one, because KAS's log
// records a steer without saying whether the model consumed it. Unknown renders as
// the NEUTRAL note; dropped would claim non-delivery for a steer that may have landed.
type SteerState string

// The two states vibekit can observe, each from its own frame. There is
// deliberately no "unknown" member: absence carries that, and a third value
// would put on the wire a state the note has no wording for.
const (
	// SteerStateRead is a steer the model read, from steering_injected.
	SteerStateRead SteerState = "read"
	// SteerStateDropped is a steer a turn boundary cleared unread, from
	// steering_cleared for an id KAS's buffer still held. The wire word is the
	// buffer's; the reader sees "Not delivered".
	SteerStateDropped SteerState = "dropped"
)
