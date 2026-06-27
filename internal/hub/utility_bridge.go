// Utility bridge for ambient AI tasks.
//
// A dedicated long-lived kiro-cli bridge for cheap, stateless tasks
// (commit messages, chat rename, summaries, error explanations). Has
// its own ACP session so it never pollutes any chat's context. Uses
// CheapestModel(). Lazily started on first request, culled after 30
// minutes of inactivity (same as chat bridges).
//
// Callers serialize through a mutex; if the bridge is busy, the caller
// waits. This is acceptable because ambient tasks are not latency-
// critical enough to warrant parallelism.

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// utilityBridge wraps a dedicated kiro-cli bridge for ambient tasks.
// The bridge is recycled after maxUtilityPrompts to prevent context
// accumulation from bleeding between unrelated tasks.
type utilityBridge struct {
	lastActiveAt  time.Time
	bridge        api.ACPBridge
	shutdownCtx   context.Context
	bridgeFactory api.ACPBridgeFactory
	hubModels     func() []api.SessionModel
	responseCh    chan utilityChunkPayload
	forwardDone   chan struct{}
	promptCount   int
	mu            sync.Mutex
	started       bool
}

const maxUtilityPrompts = 20

// utilityUpdateBase extracts the sessionUpdate kind discriminator.
// Local to utility_bridge to avoid coupling to translate's wire types.
type utilityUpdateBase struct {
	Kind api.ACPUpdateKind `json:"sessionUpdate"`
}

// utilityChunkPayload is the minimal shape the utility bridge needs
// from an agent_message_chunk notification: just the text content.
type utilityChunkPayload struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// reset stops the bridge and clears the per-bridge prompt state so the
// next UtilityPrompt call starts a fresh bridge. Called from both
// mu-locked sites (UtilityPrompt recycle + error paths) and unlocked
// sites (drainResponse ctx.Done + timeout). Callers that need
// mutual exclusion must hold ub.mu themselves — reset does not lock.
func (ub *utilityBridge) reset() {
	ub.bridge.Stop()
	// Wait for forwardUtility to exit (it returns when NotifCh closes
	// after Stop). This ensures no goroutine leak on recycle.
	if ub.forwardDone != nil {
		<-ub.forwardDone
	}
	ub.started = false
	ub.promptCount = 0
	ub.responseCh = nil
	ub.forwardDone = nil
}

// newUtilityBridge constructs a utilityBridge with the initialization
// invariants explicit: started=false, promptCount=0, lastActiveAt=zero.
func newUtilityBridge(shutdownCtx context.Context, factory api.ACPBridgeFactory, hubModels func() []api.SessionModel) *utilityBridge {
	return &utilityBridge{
		bridgeFactory: factory,
		hubModels:     hubModels,
		shutdownCtx:   shutdownCtx,
	}
}

// UtilityPrompt sends a prompt to the utility bridge and returns the
// text response. Lazily starts the bridge on first call. Thread-safe;
// concurrent callers serialize. The bridge is recycled after
// maxUtilityPrompts to prevent context accumulation.
func (ub *utilityBridge) UtilityPrompt(ctx context.Context, prompt string) (string, error) {
	ub.mu.Lock()
	defer ub.mu.Unlock()

	// Recycle the bridge if it has served too many prompts. This
	// prevents context from prior tasks bleeding into new ones.
	if ub.started && ub.promptCount >= maxUtilityPrompts {
		slog.Info("utility bridge recycled", "prompts", ub.promptCount)
		ub.reset()
	}

	if !ub.started {
		if err := ub.start(ctx); err != nil {
			return "", fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()
	ub.promptCount++

	resp, err := ub.bridge.Call(ctx, api.MethodPrompt, ub.sessionParams(map[string]any{
		"prompt": []map[string]any{api.TextBlock(utilitySystemPrompt + prompt)},
	}))
	if err != nil {
		// Bridge may be dead; reset so next call restarts.
		ub.reset()
		return "", fmt.Errorf("utility prompt: %w", err)
	}

	// Drain the forwarded response channel to consume the response chunks.
	return ub.drainResponse(ctx, resp)
}

// sessionParams builds the ACP parameter map with the session ID
// injected. Mirrors command.SessionParams but works with the raw
// ACPBridge interface (which doesn't satisfy command.Bridge).
func (ub *utilityBridge) sessionParams(extra map[string]any) map[string]any {
	m := map[string]any{api.KeySessionID: ub.bridge.SessionID()}
	maps.Copy(m, extra)
	return m
}

func (ub *utilityBridge) start(ctx context.Context) error {
	bridge := ub.bridgeFactory()
	model := cheapestModel(ctx, ub.hubModels())

	// Start with the hub's shutdown context as the subprocess lifecycle
	// context. The per-request ctx is only used for CheapestModel above;
	// the bridge subprocess must outlive individual requests.
	if err := bridge.Start(ub.shutdownCtx, &api.StartOpts{Model: model}); err != nil {
		return err
	}
	ub.bridge = bridge
	ub.started = true
	ub.lastActiveAt = time.Now()

	// Start the forward goroutine to continuously drain NotifCh.
	// This prevents the channel from filling up between prompts.
	ub.responseCh = make(chan utilityChunkPayload, 64)
	ub.forwardDone = make(chan struct{})
	go forwardUtility(bridge.NotifCh(), ub.responseCh, ub.forwardDone)

	slog.Info("utility bridge started", "model", model)
	return nil
}

// Stop stops the utility bridge if it is running. Thread-safe.
func (ub *utilityBridge) Stop() {
	ub.mu.Lock()
	started := ub.started
	forwardDone := ub.forwardDone
	ub.started = false
	ub.mu.Unlock()
	if started {
		ub.bridge.Stop()
		if forwardDone != nil {
			<-forwardDone
		}
		slog.Info("utility bridge stopped")
	}
}

// drainAndResetTimer stops t, drains its channel if it had already fired
// (so a stale tick can't fire spuriously), then rearms it to d.
func drainAndResetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// drainResponse reads the prompt response and collects assistant
// text from the forwarded response channel. Returns the concatenated text.
//
// kiro-cli does NOT emit a `session/update` sessionUpdate=="end_turn"
// notification: per ACP, the turn-end signal is the JSON-RPC RESPONSE
// to session/prompt (which ub.bridge.Call already awaited before this
// function runs). By the time we get here, the turn is already over;
// all we need to do is drain whatever chunks are still buffered in
// responseCh from before the response landed.
//
// Strategy: keep reading chunks until a short idle period elapses or
// ctx / the 60 s hard ceiling fire. The idle debounce handles the race
// between Call returning and the last chunks landing in the channel
// buffer. Caller-supplied ctx lets HTTP handlers cancel the operation
// without waiting for the ceiling.
func (ub *utilityBridge) drainResponse(ctx context.Context, resp *api.RPCResponse) (string, error) {
	if resp == nil {
		return "", errors.New("nil response")
	}

	const (
		idleDebounce  = 50 * time.Millisecond
		totalDeadline = 60 * time.Second
	)

	var text strings.Builder
	idle := time.NewTimer(idleDebounce)
	defer idle.Stop()
	timeout := time.NewTimer(totalDeadline)
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			// Caller cancelled. Only reset the bridge if the
			// cancellation came from the request context (user
			// navigated away), not from shutdownCtx (graceful
			// shutdown). During shutdown, stopUtilityBridge handles
			// cleanup; an extra reset here would race with Stop().
			if ub.shutdownCtx.Err() == nil {
				ub.reset()
			}
			return text.String(), ctx.Err()
		case chunk, ok := <-ub.responseCh:
			if !ok {
				return text.String(), nil
			}
			text.WriteString(chunk.Content.Text)
			// Reset the idle timer on every chunk we accepted.
			drainAndResetTimer(idle, idleDebounce)
		case <-idle.C:
			// No chunks for idleDebounce → all chunks drained.
			return text.String(), nil
		case <-timeout.C:
			// Hard ceiling hit. Stop the bridge so a wedged turn
			// doesn't pollute the next UtilityPrompt caller with
			// leftover chunks interleaving with their own stream.
			ub.reset()
			return text.String(), errors.New("utility prompt timeout")
		}
	}
}

// forwardUtility is a dedicated goroutine that continuously drains
// the bridge's NotifCh, forwarding agent_chunk text to responseCh.
// All other notifications (usage stats, peer requests, stale chunks
// from prior turns) are discarded. This prevents NotifCh from filling
// up between UtilityPrompt calls, which would block readLoop and
// deadlock all pending Call waiters on the bridge.
func forwardUtility(notifCh <-chan *api.RPCResponse, responseCh chan<- utilityChunkPayload, done chan<- struct{}) {
	defer close(done)
	defer close(responseCh)
	for msg := range notifCh {
		if msg.ID != nil {
			slog.Warn("utility bridge: unexpected peer request, ignoring",
				"method", msg.Method, "id", *msg.ID)
			continue
		}
		if msg.Method != api.MethodSessionUpdate || msg.Params == nil {
			continue
		}
		var base utilityUpdateBase
		if json.Unmarshal(msg.Params, &base) != nil || base.Kind != api.ACPUpdateAgentChunk {
			continue
		}
		var chunk utilityChunkPayload
		if json.Unmarshal(msg.Params, &chunk) == nil {
			responseCh <- chunk
		}
	}
}

// stopUtilityBridge stops the utility bridge if it exists.
//
// Only safe to call from Shutdown, where inflight.Wait() has already
// returned: no concurrent UtilityPrompt can be holding ub.mu. Under
// that invariant, acquiring ub.mu here serialises with the cull path
// (which also takes ub.mu to read/write started) and closes the data
// race the race detector would otherwise flag on the `started` read.
func (h *Hub) stopUtilityBridge() {
	ub := h.bridge.utility
	h.bridge.utility = nil
	if ub == nil {
		return
	}
	ub.Stop()
}

// --- Model selection (inlined from internal/models) ---
