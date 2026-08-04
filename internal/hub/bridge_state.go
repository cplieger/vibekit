package hub

import (
	"context"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
)

// bridgeState represents the lifecycle state of a sharedBridge.
type bridgeState int

const (
	bridgeIdle      bridgeState = iota // ready for a new prompt
	bridgeStarting                     // bridge.Start in progress (held during getOrCreateBridge)
	bridgePrompting                    // prompt in flight
)

// Compile-time assertion: sharedBridge satisfies api.CommandBridge.
var _ api.CommandBridge = (*sharedBridge)(nil)

// sharedBridge wraps an ACP bridge with the hub's per-chat state.
// The mu mutex protects field access; the state field encodes the
// lifecycle phase so callers can check "busy" via a readable state
// comparison rather than relying on TryLock as a signal.
type sharedBridge struct {
	bridge      api.ACPBridge
	primeReason primeReason
	mu          sync.Mutex
	state       bridgeState
	primed      bool
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

// --- api.CommandBridge implementation on sharedBridge ---

func (sb *sharedBridge) Call(ctx context.Context, method string, params any) (*api.RPCResponse, error) {
	return sb.bridge.Call(ctx, method, params)
}

func (sb *sharedBridge) Notify(ctx context.Context, method string, params any) error {
	return sb.bridge.Notify(ctx, method, params)
}

func (sb *sharedBridge) Respond(ctx context.Context, requestID int64, result any, err error) error {
	return sb.bridge.Respond(ctx, requestID, result, err)
}

func (sb *sharedBridge) SessionID() api.SessionID {
	return sb.bridge.SessionID()
}

func (sb *sharedBridge) TryAcquireForPrompt() bool {
	return sb.tryAcquireForPrompt()
}

func (sb *sharedBridge) ReleaseAfterPrompt() {
	sb.releaseAfterPrompt()
}

func (sb *sharedBridge) SetPrompting() {
	sb.mu.Lock()
	sb.state = bridgePrompting
	sb.mu.Unlock()
}

func (sb *sharedBridge) IsPrimed() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.primed
}

func (sb *sharedBridge) SetPrimed() {
	sb.mu.Lock()
	sb.primed = true
	sb.mu.Unlock()
}
