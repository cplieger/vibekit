package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/testsupport"
)

var errBoom = errors.New("persist boom")

// recStore records AppendMessage/Mutate calls and returns configurable
// errors so HandlePlan's persist branches are observable.
type recStore struct {
	testsupport.NopChatStore
	appendErr   error
	mutateErr   error
	appendCalls int
	mutateCalls int
}

func (s *recStore) AppendMessage(_ context.Context, _ api.ChatID, _ *api.Message) error {
	s.appendCalls++
	return s.appendErr
}

func (s *recStore) Mutate(_ context.Context, _ api.ChatID, fn func(*api.Chat, bool) bool) error {
	s.mutateCalls++
	if fn != nil {
		_ = fn(&api.Chat{}, true)
	}
	return s.mutateErr
}

var _ api.ChatStore = (*recStore)(nil)

// captureSlog redirects the default slog logger to buf and returns a
// restore function. Not parallel-safe (mutates the global slog default).
func captureSlog(buf *bytes.Buffer) func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(prev) }
}

// chunkProcessed pre-fills the content/reasoning builders to the given
// byte lengths, sends one text chunk, and reports whether the chunk was
// processed (a message_chunk event was broadcast) versus dropped by the
// per-turn maxBufferBytes guard.
func chunkProcessed(t *testing.T, contentLen, reasoningLen int, text string) bool {
	t.Helper()
	deps, events := newEventCaptureDeps()
	chatID := api.ChatID("cap")
	buf := deps.bufStore.GetOrInit(chatID)
	if contentLen > 0 {
		buf.Content.WriteString(strings.Repeat("a", contentLen))
	}
	if reasoningLen > 0 {
		buf.Reasoning.WriteString(strings.Repeat("b", reasoningLen))
	}
	// Pre-start the turn so ensureTurnStarted does not emit message_created;
	// the only possible broadcast is then the message_chunk on process.
	buf.Started = true
	buf.MessageID = "cap-mid"
	tr := New(deps, "/tmp", WithIDGenerator(func() string { return "cap-mid" }))
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": api.ContentTypeText, "text": text},
	}), false)
	for _, e := range *events {
		if e.Type == api.EventMessageChunk {
			return true
		}
	}
	return false
}

// TestBufferCap_BoundaryExactMaxIsProcessed pins that a chunk whose
// running total exactly equals maxBufferBytes is still processed: the
// cap is an exclusive upper bound, so equality does not drop the chunk.
func TestBufferCap_BoundaryExactMaxIsProcessed(t *testing.T) {
	if !chunkProcessed(t, maxBufferBytes-1, 0, "a") {
		t.Error("chunk at total==maxBufferBytes: processed=false, want true (cap must not fire at equality)")
	}
}

// TestBufferCap_OverByOneIsDropped pins that a chunk pushing the running
// total one byte over the cap is dropped (no message_chunk broadcast).
func TestBufferCap_OverByOneIsDropped(t *testing.T) {
	if chunkProcessed(t, maxBufferBytes, 0, "a") {
		t.Error("chunk at total==maxBufferBytes+1: processed=true, want false (one byte over the cap must drop)")
	}
}

// TestBufferCap_ReasoningCountsTowardTotal pins that reasoning bytes
// count toward the same per-turn cap as content bytes: content=max-1 +
// reasoning=1 + a 1-byte chunk crosses the cap and drops.
func TestBufferCap_ReasoningCountsTowardTotal(t *testing.T) {
	if chunkProcessed(t, maxBufferBytes-1, 1, "a") {
		t.Error("chunk with content=max-1, reasoning=1: processed=true, want false (reasoning must add to total)")
	}
}

// TestHandlePlan_UnmarshalGuard pins that HandlePlan persists only when
// the plan JSON parses; malformed JSON is skipped without appending.
func TestHandlePlan_UnmarshalGuard(t *testing.T) {
	t.Run("ValidJSONAppendsMessage", func(t *testing.T) {
		rec := &recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if rec.appendCalls != 1 {
			t.Errorf("valid plan JSON: AppendMessage calls = %d, want 1", rec.appendCalls)
		}
	})
	t.Run("InvalidJSONSkips", func(t *testing.T) {
		rec := &recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{`))
		if rec.appendCalls != 0 {
			t.Errorf("invalid plan JSON: AppendMessage calls = %d, want 0", rec.appendCalls)
		}
	})
}

// TestHandlePlan_LogsOnlyOnAppendError pins that HandlePlan logs a
// persist-plan error only when AppendMessage fails, and stays silent on
// success.
func TestHandlePlan_LogsOnlyOnAppendError(t *testing.T) {
	t.Run("ErrorLoggedWhenAppendFails", func(t *testing.T) {
		var logbuf bytes.Buffer
		restore := captureSlog(&logbuf)
		defer restore()
		rec := &recStore{appendErr: errBoom}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if !strings.Contains(logbuf.String(), "persist plan") {
			t.Errorf("AppendMessage error not logged; log=%q, want it to contain %q", logbuf.String(), "persist plan")
		}
	})
	t.Run("NoLogWhenAppendSucceeds", func(t *testing.T) {
		var logbuf bytes.Buffer
		restore := captureSlog(&logbuf)
		defer restore()
		rec := &recStore{} // appendErr nil, mutateErr nil
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if strings.Contains(logbuf.String(), "persist plan") {
			t.Errorf("unexpected error log on AppendMessage success; log=%q", logbuf.String())
		}
	})
}

// TestHandleModeUpdate_CurrentModeIDPersistsAndBroadcasts pins the H3
// fix: KAS's current_mode_update sub-kind carries the new mode under
// `currentModeId` (the bundle's zCurrentModeUpdate object), not `modeId`
// (which is the outbound session/set_mode request's field). HandleModeUpdate
// must read that key, persist CurrentModeID, and broadcast mode_changed.
// Fails against the old `json:"modeId"` tag: the payload then decodes to
// an empty ModeID, so HandleModeUpdate early-returns with no persist and
// no broadcast.
func TestHandleModeUpdate_CurrentModeIDPersistsAndBroadcasts(t *testing.T) {
	deps, events := newEventCaptureDeps()
	store := testsupport.NewRecordingChatStore()
	deps.store = store
	chatID := api.ChatID("c1")
	// Pre-create the chat so HandleModeUpdate's Mutate sees exists=true.
	_ = store.Mutate(context.Background(), chatID, func(_ *api.Chat, _ bool) bool { return true })

	tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
	tr.HandleModeUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"currentModeId": "plan",
	}))

	// Persisted the new mode.
	got, ok := store.Get(context.Background(), chatID)
	if !ok {
		t.Fatal("chat missing after HandleModeUpdate")
	}
	if got.CurrentModeID != "plan" {
		t.Errorf("CurrentModeID = %q, want %q (current_mode_update must read currentModeId, not modeId)", got.CurrentModeID, "plan")
	}

	// Broadcast mode_changed carrying the new mode id.
	found := false
	for _, e := range *events {
		if e.Type != api.EventModeChanged {
			continue
		}
		p, isModePayload := e.Payload.(api.ModeChangedPayload)
		if !isModePayload {
			t.Fatalf("mode_changed payload type = %T, want api.ModeChangedPayload", e.Payload)
		}
		if p.ModeID != "plan" {
			t.Errorf("mode_changed ModeID = %q, want %q", p.ModeID, "plan")
		}
		found = true
	}
	if !found {
		t.Errorf("no mode_changed event broadcast; got %v", eventTypes(*events))
	}
}

// TestHandlePlan_ContextErrGuard pins that HandlePlan proceeds to Mutate
// under an active context but skips it when the context is already
// cancelled.
func TestHandlePlan_ContextErrGuard(t *testing.T) {
	t.Run("ActiveContextProceedsToMutate", func(t *testing.T) {
		rec := &recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if rec.mutateCalls != 1 {
			t.Errorf("active ctx: Mutate calls = %d, want 1", rec.mutateCalls)
		}
	})
	t.Run("CancelledContextSkipsMutate", func(t *testing.T) {
		rec := &recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tr.HandlePlan(ctx, api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if rec.mutateCalls != 0 {
			t.Errorf("cancelled ctx: Mutate calls = %d, want 0", rec.mutateCalls)
		}
	})
}
