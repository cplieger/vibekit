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
	"strings"
	"sync"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/models"
	"vibekit/internal/translate"
)

// utilityBridge wraps a dedicated kiro-cli bridge for ambient tasks.
// The bridge is recycled after maxUtilityPrompts to prevent context
// accumulation from bleeding between unrelated tasks.
type utilityBridge struct {
	bridge        api.ACPBridge
	bridgeFactory api.ACPBridgeFactory
	hubModels     func() []api.SessionModel
	lastActiveAt  time.Time
	mu            sync.Mutex
	promptCount   int
	started       bool
}

const maxUtilityPrompts = 20

// reset stops the bridge and clears the per-bridge prompt state so the
// next UtilityPrompt call starts a fresh bridge. Called from both
// mu-locked sites (UtilityPrompt recycle + error paths) and unlocked
// sites (drainResponse ctx.Done + timeout). Callers that need
// mutual exclusion must hold ub.mu themselves — reset does not lock.
func (ub *utilityBridge) reset() {
	ub.bridge.Stop()
	ub.started = false
	ub.promptCount = 0
}

// newUtilityBridge constructs a utilityBridge with the initialization
// invariants explicit: started=false, promptCount=0, lastActiveAt=zero.
func newUtilityBridge(factory api.ACPBridgeFactory, hubModels func() []api.SessionModel) *utilityBridge {
	return &utilityBridge{
		bridgeFactory: factory,
		hubModels:     hubModels,
	}
}

// UtilityPrompt sends a prompt to the utility bridge and returns the
// text response. Lazily starts the bridge on first call. Thread-safe;
// concurrent callers serialize. The bridge is recycled after
// maxUtilityPrompts to prevent context accumulation.
func (h *Hub) UtilityPrompt(ctx context.Context, prompt string) (string, error) {
	h.bridge.utilityOnce.Do(func() {
		h.bridge.utility = newUtilityBridge(h.bridge.factory, h.Models)
	})
	ub := h.bridge.utility

	ub.mu.Lock()
	defer ub.mu.Unlock()

	// Recycle the bridge if it has served too many prompts. This
	// prevents context from prior tasks bleeding into new ones.
	if ub.started && ub.promptCount >= maxUtilityPrompts {
		slog.Info("utility bridge recycled", "prompts", ub.promptCount)
		ub.reset()
	}

	if !ub.started {
		if err := h.startUtilityBridge(ctx, ub); err != nil {
			return "", fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()
	ub.promptCount++

	resp, err := ub.bridge.Call(ctx, methodPrompt, map[string]any{
		"sessionId": ub.bridge.SessionID(),
		"prompt":    []map[string]any{{"type": contentTypeText, contentTypeText: utilitySystemPrompt + prompt}},
	})
	if err != nil {
		// Bridge may be dead; reset so next call restarts.
		ub.reset()
		return "", fmt.Errorf("utility prompt: %w", err)
	}

	// Drain the notification channel to consume the response chunks.
	// The utility bridge has no forward goroutine; we read directly.
	return ub.drainResponse(ctx, resp)
}

func (h *Hub) startUtilityBridge(ctx context.Context, ub *utilityBridge) error {
	bridge := ub.bridgeFactory()
	model := models.CheapestModel(ctx, ub.hubModels())

	// Start with the hub's shutdown context as the subprocess lifecycle
	// context. The per-request ctx is only used for CheapestModel above;
	// the bridge subprocess must outlive individual requests.
	if err := bridge.Start(h.lifecycle.shutdownCtx, &api.StartOpts{Model: model}); err != nil {
		return err
	}
	ub.bridge = bridge
	ub.started = true
	ub.lastActiveAt = time.Now()
	slog.Info("utility bridge started", "model", model)
	return nil
}

// drainResponse reads the prompt response and collects assistant
// text from the notification channel. Returns the concatenated text.
//
// kiro-cli does NOT emit a `session/update` sessionUpdate=="end_turn"
// notification: per ACP, the turn-end signal is the JSON-RPC RESPONSE
// to session/prompt (which ub.bridge.Call already awaited before this
// function runs). By the time we get here, the turn is already over;
// all we need to do is drain whatever chunks are still buffered in
// notifCh from before the response landed.
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
			// Caller cancelled; drop the bridge so the abandoned
			// turn doesn't pollute subsequent calls.
			ub.reset()
			return text.String(), ctx.Err()
		case msg, ok := <-ub.bridge.NotifCh():
			if !ok {
				return text.String(), nil
			}
			// Peer-initiated requests (fs/*, permission, extensions)
			// are carried as notifications with an ID on NotifCh.
			// The utility bridge has no tools and no permission
			// dialog, so an ID-bearing notification here is unexpected
			// — log and ignore rather than silently truncating the
			// accumulated text (the old behaviour) or acking with
			// success (the old early-return).
			if msg.ID != nil {
				slog.Warn("utility bridge: unexpected peer request, ignoring",
					"method", msg.Method, "id", *msg.ID)
				continue
			}
			if msg.Method == methodUpdate && msg.Params != nil {
				var base translate.ACPSessionUpdateBase
				if json.Unmarshal(msg.Params, &base) == nil && base.Kind == api.ACPUpdateAgentChunk {
					var chunk translate.ACPChunkWire
					if json.Unmarshal(msg.Params, &chunk) == nil {
						text.WriteString(chunk.Content.Text)
					}
				}
			}
			// Reset the idle timer on every chunk we accepted.
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(idleDebounce)
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
	ub.mu.Lock()
	started := ub.started
	ub.started = false
	ub.mu.Unlock()
	if started {
		ub.bridge.Stop()
		slog.Info("utility bridge stopped")
	}
}
