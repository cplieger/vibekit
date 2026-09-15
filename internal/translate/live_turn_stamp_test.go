package translate

import (
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// wantLiveTurnStamp asserts evt carries {live_turn, chatID, version}.
func wantLiveTurnStamp(t *testing.T, evt vibekit.ServerEvent, chatID vibekit.ChatID, version string) {
	t.Helper()
	if evt.Subject == nil {
		t.Fatalf("%s: Subject = nil, want {live_turn %s %s}", evt.Type, chatID, version)
	}
	got := *evt.Subject
	want := vibekit.SubjectStamp{Kind: string(subject.KindLiveTurn), Ref: string(chatID), Version: version}
	if got != want {
		t.Errorf("%s: Subject = %+v, want %+v", evt.Type, got, want)
	}
}

// lastOfType returns the last captured event of type et.
func lastOfType(t *testing.T, events []vibekit.ServerEvent, et vibekit.EventType) vibekit.ServerEvent {
	t.Helper()
	for _, evt := range slices.Backward(events) {
		if evt.Type == et {
			return evt
		}
	}
	t.Fatalf("no %s among %v", et, eventTypes(events))
	return vibekit.ServerEvent{}
}

func TestHandleAssistantChunk_StampsLiveTurnFromTheAppend(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reasoning bool
	}{
		{name: "text", reasoning: false},
		{name: "thinking", reasoning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
			chatID := vibekit.ChatID("c1")
			tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
				"content": map[string]any{"type": "text", "text": "Hello"},
			}), tc.reasoning)
			chunk := lastOfType(t, *events, vibekit.EventMessageChunk)
			wantLiveTurnStamp(t, chunk, chatID, deps.bufStore.GetOrInit(chatID).Version())
			if created := lastOfType(t, *events, vibekit.EventMessageCreated); created.Subject != nil {
				t.Errorf("message_created carries %+v, want no stamp: it completes no projection", *created.Subject)
			}
		})
	}
}

// A refusal chunk writes SetRefusal AND the append; the frame must carry the
// append's version, which is the buffer's current one only when the append ran last.
func TestHandleAssistantChunk_RefusalWriteIsNotTheLast(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
	chatID := vibekit.ChatID("c1")
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "I cannot help with that."},
		"_meta":   map[string]any{"kiro": map[string]any{"refusal": map[string]any{"category": "harmful"}}},
	}), false)
	chunk := lastOfType(t, *events, vibekit.EventMessageChunk)
	payload, ok := chunk.Payload.(vibekit.MessageChunkPayload)
	if !ok || payload.Refusal == nil {
		t.Fatalf("message_chunk payload = %#v, want a refusal-carrying chunk", chunk.Payload)
	}
	wantLiveTurnStamp(t, chunk, chatID, deps.bufStore.GetOrInit(chatID).Version())
}

func TestAnnounceTruncation_StampsLiveTurn(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
	chatID := vibekit.ChatID("c1")
	buf := deps.bufStore.GetOrInit(chatID)
	tr.announceTruncation(t.Context(), chatID, buf, "", maxBufferBytes)
	chunk := lastOfType(t, *events, vibekit.EventMessageChunk)
	wantLiveTurnStamp(t, chunk, chatID, buf.Version())
}

func TestHandleToolCall_StampsLiveTurnFromTheLastWrite(t *testing.T) {
	t.Run("NoDiffs", func(t *testing.T) {
		deps, _, events := newLineCaptureDeps()
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
		chatID := vibekit.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1", "title": "readFile", "kind": "read", "status": "pending",
		}), FrameAttribution{})
		call := lastOfType(t, *events, vibekit.EventToolCall)
		wantLiveTurnStamp(t, call, chatID, deps.bufStore.GetOrInit(chatID).Version())
		if label := lastOfType(t, *events, vibekit.EventWorkingLabel); label.Subject != nil {
			t.Errorf("working_label carries %+v, want no stamp", *label.Subject)
		}
	})
	t.Run("WithDiffsTheFileTrackingIsLast", func(t *testing.T) {
		deps, _, events := newLineCaptureDeps()
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
		chatID := vibekit.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-diff", "title": "writeFile", "kind": "edit", "status": "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "x.go", "oldText": "a", "newText": "b"},
			},
		}), FrameAttribution{})
		call := lastOfType(t, *events, vibekit.EventToolCall)
		// Equal to the buffer's version AFTER TrackFileChanges: a stamp taken from
		// RecordToolStart (one write earlier) would read one rev behind.
		wantLiveTurnStamp(t, call, chatID, deps.bufStore.GetOrInit(chatID).Version())
	})
}

func TestHandleToolCallUpdate_StampsLiveTurnFromSetToolCall(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
	}), FrameAttribution{})
	upd := lastOfType(t, *events, vibekit.EventToolCallUpdate)
	wantLiveTurnStamp(t, upd, chatID, deps.bufStore.GetOrInit(chatID).Version())
}

// The hook card is an emitter like any other tool call: two buffer writes, one frame,
// stamped with the LAST write's version. An unstamped frame leaves the client's map
// one write behind on a turn whose next digest then names it stale.
func TestHandleHookUpdate_StampsLiveTurnFromTheLastWrite(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &hookStatusDeps{baseDeps: base, enabled: true}
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
	chatID := vibekit.ChatID("c1")
	tr.HandleSessionInfoUpdate(t.Context(), chatID,
		mustJSON(t, hookUpdateFrame(t, "probe-save", hookStatusCompleted)), FrameAttribution{})
	call := lastOfType(t, *events, vibekit.EventToolCall)
	wantLiveTurnStamp(t, call, chatID, base.bufStore.GetOrInit(chatID).Version())
}

func TestHandleCodeReferences_StampsLiveTurn(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
	chatID := vibekit.ChatID("c1")
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Hello"},
	}), false)
	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", []map[string]any{
		{"licenseName": "MIT", "repository": "r", "url": "https://x"},
	}))
	refs := lastOfType(t, *events, vibekit.EventCodeReferences)
	wantLiveTurnStamp(t, refs, chatID, deps.bufStore.GetOrInit(chatID).Version())
}
