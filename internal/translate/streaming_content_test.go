package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

var errBoom = errors.New("persist boom")

// recStore records AppendMessage/Mutate/UpsertTurnPlan calls and returns
// configurable errors so each write site's persist branches are observable.
type recStore struct {
	nopChatRecords
	appendErr   error
	mutateErr   error
	upsertErr   error
	appendCalls int
	mutateCalls int
	upsertCalls int
}

func (s *recStore) AppendMessage(_ context.Context, _ vibekit.ChatID, _ *vibekit.Message) error {
	s.appendCalls++
	return s.appendErr
}

func (s *recStore) UpsertTurnPlan(_ context.Context, _ vibekit.ChatID, _ *vibekit.Message) error {
	s.upsertCalls++
	return s.upsertErr
}

func (s *recStore) Mutate(_ context.Context, _ vibekit.ChatID, fn func(*vibekit.Chat, bool) bool) error {
	s.mutateCalls++
	if fn != nil {
		_ = fn(&vibekit.Chat{}, true)
	}
	return s.mutateErr
}

var _ ChatRecords = (*recStore)(nil)

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
	chatID := vibekit.ChatID("cap")
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
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "cap-mid" }))
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": vibekit.ContentTypeText, "text": text},
	}), false)
	// The chunk's OWN text, not merely "some chunk was broadcast": crossing the
	// cap now emits a one-off truncation notice, and counting that as processed
	// would make these cases pass whether the text was dropped or not.
	for _, e := range *events {
		if e.Type != vibekit.EventMessageChunk {
			continue
		}
		if p, ok := e.Payload.(vibekit.MessageChunkPayload); ok && p.Delta == text {
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
	const chatID vibekit.ChatID = "c1"
	deps, events, _ := depsWithStore(t, chatID)
	buf := deps.bufStore.GetOrInit(chatID)
	buf.Content.WriteString(strings.Repeat("a", contentLen))
	buf.Started = true
	buf.MessageID = "cap-mid"
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "cap-mid" }))
	for range chunks {
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": vibekit.ContentTypeText, "text": "a"},
		}), false)
	}
	n := 0
	for _, e := range *events {
		if e.Type != vibekit.EventMessageChunk {
			continue
		}
		if p, ok := e.Payload.(vibekit.MessageChunkPayload); ok && strings.Contains(p.Delta, "Reply truncated") {
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
// the plan JSON parses; malformed JSON is skipped without a write.
//
// It also pins that ONE store call carries a plan frame: the second write that
// used to maintain Chat.CurrentPlan is gone, so a frame costs one chat-file
// rewrite rather than two.
func TestHandlePlan_UnmarshalGuard(t *testing.T) {
	t.Run("ValidJSONUpsertsOnce", func(t *testing.T) {
		rec := &recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		tr.HandlePlan(t.Context(), vibekit.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if rec.upsertCalls != 1 {
			t.Errorf("valid plan JSON: UpsertTurnPlan calls = %d, want 1", rec.upsertCalls)
		}
		if rec.appendCalls != 0 || rec.mutateCalls != 0 {
			t.Errorf("a plan frame must cost one store call: AppendMessage = %d, Mutate = %d, want 0 and 0", rec.appendCalls, rec.mutateCalls)
		}
	})
	t.Run("InvalidJSONSkips", func(t *testing.T) {
		rec := &recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		tr.HandlePlan(t.Context(), vibekit.ChatID("c1"), json.RawMessage(`{`))
		if rec.upsertCalls != 0 {
			t.Errorf("invalid plan JSON: UpsertTurnPlan calls = %d, want 0", rec.upsertCalls)
		}
	})
}

// TestHandlePlan_LogsOnlyOnUpsertError pins that HandlePlan logs a
// persist-plan error only when UpsertTurnPlan fails, and stays silent on
// success.
func TestHandlePlan_LogsOnlyOnUpsertError(t *testing.T) {
	t.Run("ErrorLoggedWhenUpsertFails", func(t *testing.T) {
		var logbuf bytes.Buffer
		restore := captureSlog(&logbuf)
		defer restore()
		rec := &recStore{upsertErr: errBoom}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		tr.HandlePlan(t.Context(), vibekit.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if !strings.Contains(logbuf.String(), "persist plan") {
			t.Errorf("UpsertTurnPlan error not logged; log=%q, want it to contain %q", logbuf.String(), "persist plan")
		}
	})
	t.Run("NoLogWhenUpsertSucceeds", func(t *testing.T) {
		var logbuf bytes.Buffer
		restore := captureSlog(&logbuf)
		defer restore()
		rec := &recStore{} // every error field nil
		deps := newBaseDeps()
		deps.store = rec
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		tr.HandlePlan(t.Context(), vibekit.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		if strings.Contains(logbuf.String(), "persist plan") {
			t.Errorf("unexpected error log on UpsertTurnPlan success; log=%q", logbuf.String())
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
	chatID := vibekit.ChatID("c1")
	// Pre-create the chat so HandleModeUpdate's Mutate sees exists=true.
	_ = store.Mutate(t.Context(), chatID, func(_ *vibekit.Chat, _ bool) bool { return true })

	tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
	tr.HandleModeUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"currentModeId": "plan",
	}))

	// Persisted the new mode.
	got, ok := store.Get(t.Context(), chatID)
	if !ok {
		t.Fatal("chat missing after HandleModeUpdate")
	}
	if got.CurrentModeID != "plan" {
		t.Errorf("CurrentModeID = %q, want %q (current_mode_update must read currentModeId, not modeId)", got.CurrentModeID, "plan")
	}

	// Broadcast mode_changed carrying the new mode id.
	found := false
	for _, e := range *events {
		if e.Type != vibekit.EventModeChanged {
			continue
		}
		p, isModePayload := e.Payload.(vibekit.ModeChangedPayload)
		if !isModePayload {
			t.Fatalf("mode_changed payload type = %T, want vibekit.ModeChangedPayload", e.Payload)
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

// There is no TestHandlePlan_ContextErrGuard any more, and its absence is the
// point: it pinned a `ctx.Err()` check that sat BETWEEN HandlePlan's two writes,
// existing only to skip the second one when the frame's context had already
// expired. One write means there is no second write to skip, so the guard went
// with Chat.CurrentPlan and the test's whole subject went with it. Whether a
// cancelled context reaches disk is the store's contract, tested there.

// --- Model refusal (kiro-cli 2.13 _meta.kiro.refusal) ---

func TestHandleAssistantChunk_RefusalMeta(t *testing.T) {
	newChunk := func(meta map[string]any) map[string]any {
		c := map[string]any{
			"content": map[string]any{"type": vibekit.ContentTypeText, "text": "I can't continue."},
		}
		if meta != nil {
			c["_meta"] = map[string]any{"kiro": meta}
		}
		return c
	}

	t.Run("tagged text chunk stamps buffer and rides the chunk event", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := vibekit.ChatID("rf1")
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, newChunk(map[string]any{
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
		var chunkPayloads []vibekit.MessageChunkPayload
		for _, e := range *events {
			if e.Type == vibekit.EventMessageChunk {
				chunkPayloads = append(chunkPayloads, e.Payload.(vibekit.MessageChunkPayload))
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
		chatID := vibekit.ChatID("rf2")
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, newChunk(map[string]any{
			"refusal": map[string]any{"category": "first"},
		})), false)
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, newChunk(map[string]any{
			"refusal": map[string]any{"category": "second"},
		})), false)
		buf := deps.bufStore.Get(chatID)
		if buf == nil || buf.Refusal == nil || buf.Refusal.Category != "first" {
			t.Errorf("expected first refusal kept, got %+v", buf.Refusal)
		}
	})

	t.Run("reasoning chunk cannot mark the turn", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := vibekit.ChatID("rf3")
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, newChunk(map[string]any{
			"refusal": map[string]any{"category": "safety"},
		})), true)
		if buf := deps.bufStore.Get(chatID); buf != nil && buf.Refusal != nil {
			t.Error("reasoning chunk must not stamp refusal")
		}
		for _, e := range *events {
			if e.Type == vibekit.EventMessageChunk && e.Payload.(vibekit.MessageChunkPayload).Refusal != nil {
				t.Error("reasoning chunk must not carry refusal on the wire")
			}
		}
	})

	t.Run("untagged chunk stays clean", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := vibekit.ChatID("rf4")
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, newChunk(nil)), false)
		if buf := deps.bufStore.Get(chatID); buf != nil && buf.Refusal != nil {
			t.Error("untagged chunk must not stamp refusal")
		}
		for _, e := range *events {
			if e.Type == vibekit.EventMessageChunk && e.Payload.(vibekit.MessageChunkPayload).Refusal != nil {
				t.Error("untagged chunk must not carry refusal")
			}
		}
	})
}

// TestHandleAssistantChunk_AMutedTurnFoldsButPublishesNothing pins the FOLD half of
// the prime's source policy.
//
// A prime is a real session/prompt carrying the transcript, so its reply is real
// text on this wire. Revision 4 suppressed only the finalizer's persist and left
// three leaks, all reachable: the chunks were BROADCAST live and then vanished on
// the next reload, which is the vanishing-message class this codebase has already
// paid for. The frames still fold — a revised binding hands this buffer to the
// agent's own turn, whose content it then legitimately is, and that turn unmutes
// it — so the assertion is "folded AND silent", not "dropped".
func TestHandleAssistantChunk_AMutedTurnFoldsButPublishesNothing(t *testing.T) {
	d := newBaseDeps()
	var events []vibekit.EventType
	d.onBroadcast = func(_ context.Context, evt vibekit.ServerEvent) {
		events = append(events, evt.Type)
	}
	tr := New(rolesOf(d))
	d.bufStore.GetOrInit("c1").SetMuted(true)

	tr.HandleAssistantChunk(t.Context(), "c1", mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Caught up."},
	}), false)

	if len(events) != 0 {
		t.Errorf("a muted turn published %v; the priming preamble would render as conversation", events)
	}
	if got := d.bufStore.GetOrInit("c1").Content.String(); got != "Caught up." {
		t.Errorf("the frame did not fold: content = %q", got)
	}
}

// TestHandlePlan_AMutedTurnPersistsNothing is the persistence half of the prime's
// fold-time policy, and the leak the emit funnel could not reach.
//
// The funnel covers BROADCAST. A plan is different in kind: HandlePlan writes
// straight to the chat store, so a prime that emitted a plan wrote a durable
// transcript row while every other frame of that same turn was suppressed — an
// invisible turn leaving user-visible data behind, which is exactly what "neither
// broadcast nor served nor persisted" rules out.
//
// Both directions are asserted, because a gate that swallowed every plan would
// pass a one-sided test while deleting the feature.
func TestHandlePlan_AMutedTurnPersistsNothing(t *testing.T) {
	t.Run("MutedSkipsThePersist", func(t *testing.T) {
		rec := &recStore{}
		d := newBaseDeps()
		d.store = rec
		tr := New(rolesOf(d), withIDGenerator(func() string { return "id" }))
		d.bufStore.GetOrInit("c1").SetMuted(true)

		tr.HandlePlan(t.Context(), "c1", json.RawMessage(`{"entries":[{"content":"step"}]}`))

		if rec.upsertCalls != 0 {
			t.Errorf("a muted turn's plan wrote %d transcript rows, want 0", rec.upsertCalls)
		}
	})
	t.Run("AnOrdinaryTurnStillPersists", func(t *testing.T) {
		rec := &recStore{}
		d := newBaseDeps()
		d.store = rec
		tr := New(rolesOf(d), withIDGenerator(func() string { return "id" }))

		tr.HandlePlan(t.Context(), "c1", json.RawMessage(`{"entries":[{"content":"step"}]}`))

		if rec.upsertCalls != 1 {
			t.Errorf("UpsertTurnPlan calls = %d, want 1: the gate must not swallow every plan", rec.upsertCalls)
		}
	})
}
