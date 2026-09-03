// Package agent runs the utility text-generation agent (the first of the
// utility runtime's two roles; see utility_session.go for the split).
// UtilityPrompt serves ambient AI tasks, one text turn at a time (turnMu),
// which does not extend to the session's stateless RPC reads.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

const maxUtilityPrompts = 20

// maxUtilityPromptBytes is the cumulative prompt-size budget per session.
// 64 KB ≈ a handful of capped commit diffs (8 KB) / PR diffs (12 KB); past
// it the accumulated context costs more per turn than a session recycle.
const maxUtilityPromptBytes = 64 * 1024

// utilityAgent runs text-generation turns on the shared utility session.
// Its per-session bookkeeping (prompt counters, applied effort) is keyed
// to the session's generation: whenever the session restarts underneath
// it (recycle, error reset, idle cull), the next turn observes the new
// generation and starts fresh counters.
type utilityAgent struct {
	session *utilitySession
	// currentEffort is the reasoning-effort level last applied to the live
	// session (empty = model default). Per-task levels: cheap tasks run
	// low, diff-reading tasks run medium. Only re-applied when it differs.
	currentEffort vibekit.EffortLevel

	// turnMu serializes text-generation turns. Ambient tasks are not
	// latency-critical enough to warrant parallelism, and one session
	// cannot interleave two prompt streams anyway.
	turnMu sync.Mutex
	// counterGen is the session generation the counter/effort fields
	// belong to.
	counterGen  uint64
	promptCount int
	// promptBytes accumulates the byte size of every prompt sent on the
	// current session. Each turn re-sends the whole prior conversation, so
	// recycling on a byte budget bounds the re-billed dead context.
	promptBytes int
	// effortUnsupported latches after a failed effortLevel
	// set_config_option (the cheapest model may expose no reasoning-effort
	// config). Cleared with the rest of the per-session state on a
	// generation change, so a recycle onto a different model re-probes
	// once.
	effortUnsupported bool
}

// newUtilityAgent binds an agent to its session.
func newUtilityAgent(session *utilitySession) *utilityAgent {
	return &utilityAgent{session: session}
}

// UtilityPrompt sends a prompt on the utility session and returns the
// text response. Lazily starts the session on first call. Thread-safe;
// concurrent text turns serialize on turnMu. The session is recycled
// after maxUtilityPrompts prompts or maxUtilityPromptBytes of cumulative
// prompt input, whichever comes first, to bound both context bleed and
// the re-billed dead context each turn drags along. effort is the
// per-task reasoning-effort level ("" keeps the session's current level).
func (ua *utilityAgent) UtilityPrompt(ctx context.Context, prompt string, effort vibekit.EffortLevel) (string, error) {
	ua.turnMu.Lock()
	defer ua.turnMu.Unlock()

	lease, err := ua.session.acquire(ctx)
	if err != nil {
		return "", err
	}
	ua.syncCounters(lease.gen)

	if ua.promptCount >= maxUtilityPrompts || ua.promptBytes >= maxUtilityPromptBytes {
		slog.Info("utility bridge recycled", "prompts", ua.promptCount, "prompt_bytes", ua.promptBytes)
		ua.session.resetIf(lease.gen)
		if lease, err = ua.session.acquire(ctx); err != nil {
			return "", err
		}
		ua.syncCounters(lease.gen)
	}
	ua.promptCount++
	ua.promptBytes += len(prompt)

	ua.applyEffort(ctx, lease, effort)

	drainLeftoverChunks(lease.chunks)

	resp, err := lease.bridge.Call(ctx, vibekit.MethodPrompt, utilitySessionParams(lease.bridge, map[string]any{
		vibekit.KeyPrompt: []map[string]any{vibekit.TextBlock(utilitySystemPrompt + prompt)},
	}))
	if err != nil {
		ua.session.resetIf(lease.gen)
		return "", fmt.Errorf("utility prompt: %w", err)
	}

	return ua.drainResponse(ctx, lease, resp)
}

// syncCounters resets the per-session bookkeeping when the session
// generation changed underneath the agent (recycle, error reset, idle
// cull + restart). This is what guarantees a culled-then-restarted
// session never inherits a stale prompt count or effort latch.
func (ua *utilityAgent) syncCounters(gen uint64) {
	if ua.counterGen == gen {
		return
	}
	ua.counterGen = gen
	ua.promptCount = 0
	ua.promptBytes = 0
	ua.currentEffort = ""
	ua.effortUnsupported = false
}

// applyEffort sets the session's reasoning-effort level via
// session/set_config_option when the requested level differs from the one
// already applied. Best-effort: the cheapest model may expose no
// effortLevel config option, in which case the failure is latched
// (effortUnsupported) so subsequent tasks don't re-pay the round-trip
// until the next session start. Caller holds turnMu.
func (ua *utilityAgent) applyEffort(ctx context.Context, lease sessionLease, effort vibekit.EffortLevel) {
	if effort == "" || effort == ua.currentEffort || ua.effortUnsupported || !effort.Valid() {
		return
	}
	_, err := lease.bridge.Call(ctx, vibekit.MethodSetConfigOption, utilitySessionParams(lease.bridge, map[string]any{
		"configId": vibekit.ConfigOptionEffort,
		"value":    string(effort),
	}))
	if err != nil {
		slog.Debug("utility bridge: effortLevel unsupported on this session", "effort", effort, "error", err)
		ua.effortUnsupported = true
		return
	}
	ua.currentEffort = effort
}

// drainLeftoverChunks non-blockingly empties the chunk channel of anything
// a prior turn left behind. drainResponse returns on a short idle
// debounce, so a late chunk can land after it returns and sit in the
// buffer; the success path never clears the channel. A nil channel (a
// session preset by tests, or acquired before any start) is a no-op.
func drainLeftoverChunks(chunks <-chan utilityChunkPayload) {
	if chunks == nil {
		return
	}
	for {
		select {
		case <-chunks:
		default:
			return
		}
	}
}

// drainResponse reads the prompt response and collects assistant text from
// the forwarded response channel.
//
// kiro-cli does NOT emit a `session/update` sessionUpdate=="end_turn"
// notification: per ACP, the turn-end signal is the JSON-RPC RESPONSE to
// session/prompt, already awaited by Call before this runs. So this only
// drains chunks that arrived before the response landed, until a short idle
// debounce elapses or ctx / the 60s hard ceiling fire.
func (ua *utilityAgent) drainResponse(ctx context.Context, lease sessionLease, resp *vibekit.RPCResponse) (string, error) {
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
			// Only reset if cancellation came from the request context, not
			// shutdownCtx — during shutdown, stopUtilityBridge handles
			// cleanup and an extra reset here would race Stop().
			if !ua.session.shuttingDown() {
				ua.session.resetIf(lease.gen)
			}
			return text.String(), ctx.Err()
		case chunk, ok := <-lease.chunks:
			if !ok {
				return text.String(), nil
			}
			text.WriteString(chunk.Content.Text)
			// Reset alone is safe: a chan-based timer's channel is
			// unbuffered since Go 1.23, so no stale receive can land.
			idle.Reset(idleDebounce)
		case <-idle.C:
			return text.String(), nil
		case <-timeout.C:
			// Reset so a wedged turn doesn't interleave leftover chunks
			// into the next caller's stream.
			ua.session.resetIf(lease.gen)
			return text.String(), errors.New("utility prompt timeout")
		}
	}
}
