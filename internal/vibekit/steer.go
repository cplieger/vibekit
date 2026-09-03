package vibekit

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
