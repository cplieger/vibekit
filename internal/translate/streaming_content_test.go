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
	tr := New(deps, withIDGenerator(func() string { return "cap-mid" }))
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": api.ContentTypeText, "text": text},
	}), false)
	// The chunk's OWN text, not merely "some chunk was broadcast": crossing the
	// cap now emits a one-off truncation notice, and counting that as processed
	// would make these cases pass whether the text was dropped or not.
	for _, e := range *events {
		if e.Type != api.EventMessageChunk {
			continue
		}
		if p, ok := e.Payload.(api.MessageChunkPayload); ok && p.Delta == text {
			return true
		}
	}
	return false
}

// truncationNotice reports whether crossing the cap announced itself, and how
// many times. Silence here was the defect: the reply stopped mid-sentence with
// nothing in the transcript to say why.
func truncationNotices(t *testing.T, contentLen, chunks int) int {
	t.Helper()
	const chatID api.ChatID = "c1"
	deps, events, _ := depsWithStore(t, chatID)
	buf := deps.BufferStore().GetOrInit(chatID)
	buf.Content.WriteString(strings.Repeat("a", contentLen))
	buf.Started = true
	buf.MessageID = "cap-mid"
	tr := New(deps, withIDGenerator(func() string { return "cap-mid" }))
	for range chunks {
		tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": api.ContentTypeText, "text": "a"},
		}), false)
	}
	n := 0
	for _, e := range *events {
		if e.Type != api.EventMessageChunk {
			continue
		}
		if p, ok := e.Payload.(api.MessageChunkPayload); ok && strings.Contains(p.Delta, "Reply truncated") {
			n++
		}
	}
	return n
}

// Crossing the cap must SAY so — once. Frames keep arriving after the cap is hit,
// so a notice per frame would be a worse defect than the silence it replaced.
func TestBufferCap_AnnouncesTruncationExactlyOnce(t *testing.T) {
	if n := truncationNotices(t, maxBufferBytes, 5); n != 1 {
		t.Errorf("five over-cap chunks produced %d truncation notices, want exactly 1", n)
	}
}

// And it must stay silent while the turn is within the cap.
func TestBufferCap_NoNoticeUnderTheCap(t *testing.T) {
	if n := truncationNotices(t, 0, 3); n != 0 {
		t.Errorf("three in-budget chunks produced %d truncation notices, want 0", n)
	}
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
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if rec.appendCalls != 1 {
			t.Errorf("valid plan JSON: AppendMessage calls = %d, want 1", rec.appendCalls)
		}
	})
	t.Run("InvalidJSONSkips", func(t *testing.T) {
		rec := &recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, withIDGenerator(func() string { return "id" }))
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
		tr := New(deps, withIDGenerator(func() string { return "id" }))
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
		tr := New(deps, withIDGenerator(func() string { return "id" }))
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

	tr := New(deps, withIDGenerator(func() string { return "id" }))
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
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if rec.mutateCalls != 1 {
			t.Errorf("active ctx: Mutate calls = %d, want 1", rec.mutateCalls)
		}
	})
	t.Run("CancelledContextSkipsMutate", func(t *testing.T) {
		rec := &recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tr.HandlePlan(ctx, api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if rec.mutateCalls != 0 {
			t.Errorf("cancelled ctx: Mutate calls = %d, want 0", rec.mutateCalls)
		}
	})
}

// --- Model refusal (kiro-cli 2.13 _meta.kiro.refusal) ---

func TestHandleAssistantChunk_RefusalMeta(t *testing.T) {
	newChunk := func(meta map[string]any) map[string]any {
		c := map[string]any{
			"content": map[string]any{"type": api.ContentTypeText, "text": "I can't continue."},
		}
		if meta != nil {
			c["_meta"] = map[string]any{"kiro": meta}
		}
		return c
	}

	t.Run("tagged text chunk stamps buffer and rides the chunk event", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := api.ChatID("rf1")
		tr := New(deps, withIDGenerator(func() string { return "m1" }))
		tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, newChunk(map[string]any{
			"refusal": map[string]any{
				"category":         "safety",
				"explanation":      "dup of the text",
				"recommendedModel": "model-x",
			},
		})), false)

		buf := deps.bufStore.Get(chatID)
		if buf == nil || buf.Refusal == nil {
			t.Fatal("buffer refusal not stamped")
		}
		if buf.Refusal.Category != "safety" || buf.Refusal.RecommendedModel != "model-x" {
			t.Errorf("refusal fields: %+v", buf.Refusal)
		}
		var chunkPayloads []api.MessageChunkPayload
		for _, e := range *events {
			if e.Type == api.EventMessageChunk {
				chunkPayloads = append(chunkPayloads, e.Payload.(api.MessageChunkPayload))
			}
		}
		if len(chunkPayloads) != 1 || chunkPayloads[0].Refusal == nil {
			t.Fatalf("expected one refusal-tagged chunk event, got %+v", chunkPayloads)
		}
		if chunkPayloads[0].Refusal.Category != "safety" {
			t.Errorf("chunk payload refusal: %+v", chunkPayloads[0].Refusal)
		}
	})

	t.Run("first refusal wins", func(t *testing.T) {
		deps, _ := newEventCaptureDeps()
		chatID := api.ChatID("rf2")
		tr := New(deps, withIDGenerator(func() string { return "m1" }))
		tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, newChunk(map[string]any{
			"refusal": map[string]any{"category": "first"},
		})), false)
		tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, newChunk(map[string]any{
			"refusal": map[string]any{"category": "second"},
		})), false)
		buf := deps.bufStore.Get(chatID)
		if buf == nil || buf.Refusal == nil || buf.Refusal.Category != "first" {
			t.Errorf("expected first refusal kept, got %+v", buf.Refusal)
		}
	})

	t.Run("reasoning chunk cannot mark the turn", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := api.ChatID("rf3")
		tr := New(deps, withIDGenerator(func() string { return "m1" }))
		tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, newChunk(map[string]any{
			"refusal": map[string]any{"category": "safety"},
		})), true)
		if buf := deps.bufStore.Get(chatID); buf != nil && buf.Refusal != nil {
			t.Error("reasoning chunk must not stamp refusal")
		}
		for _, e := range *events {
			if e.Type == api.EventMessageChunk && e.Payload.(api.MessageChunkPayload).Refusal != nil {
				t.Error("reasoning chunk must not carry refusal on the wire")
			}
		}
	})

	t.Run("untagged chunk stays clean", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := api.ChatID("rf4")
		tr := New(deps, withIDGenerator(func() string { return "m1" }))
		tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, newChunk(nil)), false)
		if buf := deps.bufStore.Get(chatID); buf != nil && buf.Refusal != nil {
			t.Error("untagged chunk must not stamp refusal")
		}
		for _, e := range *events {
			if e.Type == api.EventMessageChunk && e.Payload.(api.MessageChunkPayload).Refusal != nil {
				t.Error("untagged chunk must not carry refusal")
			}
		}
	})
}
