// Package liveness is the one statement of when a quiet SSE client reads gone.
//
// Three readers derive from it and none holds a literal of its own: the agent
// builds the hub's keepalive, write timeout and retry: from it; the push presence
// table sizes its absence window and its departure grace from it; the listener
// sets TCP_USER_TIMEOUT from it. A window that lived in one of them alone would
// drift from the cadence it is supposed to be a multiple of.
package liveness

import "time"

// Keepalive is the cadence of the named keepalive the hub writes on every
// connection. The client acknowledges each one it receives, so this is also the
// cadence of the acknowledgements the presence table reads.
const Keepalive = 15 * time.Second

// absenceBeats is how many keepalives may go unacknowledged before a client reads
// gone. Two, not one: a single missed beat is one lost request on a live tab.
const absenceBeats = 2

// AliveWindow is the absence threshold: a connected client whose last
// acknowledgement is older than this is gone, whatever its socket says. It is also
// the hub's write timeout and the listener's TCP_USER_TIMEOUT, so the socket's own
// account of a dead peer and the table's agree on the number.
const AliveWindow = absenceBeats * Keepalive

// ReconnectDelay is the stream's advertised retry: field, the delay the browser
// waits before reconnecting after a transient drop. Without it the delay is the
// browser default and the two disagree (3s Chrome, 5s Firefox). Not lower: a DOWN
// server retries on it. The presence table reads it as the grace a departure gets
// before it counts, since a reconnecting client comes back within it.
const ReconnectDelay = 1500 * time.Millisecond
