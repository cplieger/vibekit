package translate

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestHandlersDoNotRaceBufferSnapshot pins the reason every write this package
// makes to a buffer.Buffer goes through a guarded method rather than an exported
// field.
//
// Three goroutines touch a live buffer: this package's handlers on the per-chat
// dispatch loop, the prompt handler latching the turn's model at dispatch, and
// the SSE connect handler calling Snapshot for a client joining mid-turn
// (agent/sse.go replayTurnState). Snapshot holds the buffer's mutex; a field
// write here does not, so the pair raced. Measured with -race on go1.27.0 before
// the fix: Buffer.MessageID written by ensureTurnStarted against Snapshot's read
// of it.
//
// The assertion is the race detector itself, which is why there is no t.Errorf
// below: CI runs `go test -race`, so reintroducing a direct field write in any
// handler on this path turns this test red. Iteration counts are what make it
// reliable rather than probabilistic — the pre-fix run reported on the first
// pass at these numbers.
func TestHandlersDoNotRaceBufferSnapshot(t *testing.T) {
	const iterations = 500

	deps := newBaseDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
	chatID := vibekit.ChatID("c1")
	buf := deps.bufStore.GetOrInit(chatID)
	ctx := t.Context()

	toolCall := json.RawMessage(`{"toolCallId":"tc-1","title":"probe","kind":"execute",
		"status":"pending","content":[{"type":"content","content":{"text":"out"}}]}`)
	toolUpdate := json.RawMessage(`{"toolCallId":"tc-1","status":"completed",
		"content":[{"type":"content","content":{"text":"more"}}]}`)
	textChunk := json.RawMessage(`{"content":{"type":"text","text":"hello"}}`)
	thoughtChunk := json.RawMessage(`{"content":{"type":"text","text":"pondering"}}`)

	var wg sync.WaitGroup
	// The per-chat dispatch loop: every handler that writes the buffer.
	wg.Go(func() {
		for range iterations {
			tr.HandleToolCall(ctx, chatID, toolCall, "")
			tr.HandleToolCallUpdate(ctx, chatID, toolUpdate, "")
			tr.HandleAssistantChunk(ctx, chatID, textChunk, false)
			tr.HandleAssistantChunk(ctx, chatID, thoughtChunk, true)
			FlushSteerCarry(buf)
		}
	})
	// The SSE connect handler, on its own goroutine.
	wg.Go(func() {
		for range iterations {
			_, _, _ = buf.Snapshot()
		}
	})
	wg.Wait()
}
