package hub

import (
	"fmt"
	"sync"
	"time"

	"vibekit/internal/api"
)

// bridgeState represents the lifecycle state of a sharedBridge.
type bridgeState int

const (
	bridgeIdle      bridgeState = iota // ready for a new prompt
	bridgeStarting                     // bridge.Start in progress (held during getOrCreateBridge)
	bridgePrompting                    // prompt in flight
)

// Compile-time assertion: update String() when adding variants.
var _ [bridgePrompting + 1]struct{} = [3]struct{}{}

// String returns a human-readable name for the bridge lifecycle state.
func (s bridgeState) String() string {
	switch s {
	case bridgeIdle:
		return "idle"
	case bridgeStarting:
		return "starting"
	case bridgePrompting:
		return "prompting"
	default:
		return fmt.Sprintf("bridgeState(%d)", int(s))
	}
}

// sharedBridge wraps an ACP bridge with the hub's per-chat state.
// The mu mutex protects field access; the state field encodes the
// lifecycle phase so callers can check "busy" via a readable state
// comparison rather than relying on TryLock as a signal.
type sharedBridge struct {
	lastActiveAt time.Time
	bridge       api.ACPBridge
	primeReason  primeReason
	mu           sync.Mutex
	state        bridgeState
	primed       bool
}

// tryAcquireForPrompt attempts to transition from idle to prompting.
// Returns true if the transition succeeded (caller owns the prompt slot).
func (sb *sharedBridge) tryAcquireForPrompt() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.state != bridgeIdle {
		return false
	}
	sb.state = bridgePrompting
	return true
}

// releaseAfterPrompt transitions from prompting back to idle.
func (sb *sharedBridge) releaseAfterPrompt() {
	sb.mu.Lock()
	sb.state = bridgeIdle
	sb.mu.Unlock()
}
