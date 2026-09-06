package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// summarizationInfo builds a session_info_update payload carrying a
// _meta.kiro.summarization block with the given status (and optional summary),
// matching the KAS v3 wire shape HandleSessionInfoUpdate decodes.
func summarizationInfo(t *testing.T, status, summary string) json.RawMessage {
	t.Helper()
	sum := map[string]any{"status": status}
	if summary != "" {
		sum["summary"] = map[string]any{"conversationSummary": summary}
	}
	return mustJSON(t, map[string]any{
		"_meta": map[string]any{"kiro": map[string]any{"summarization": sum}},
	})
}

// eventMsgsByKind returns the persisted RoleEvent messages on chatID whose
// EventKind matches.
func eventMsgsByKind(t *testing.T, store *testsupport.InMemoryChatStore, chatID vibekit.ChatID, kind vibekit.EventKind) []vibekit.Message {
	t.Helper()
	c, ok := store.Get(t.Context(), chatID)
	if !ok {
		return nil
	}
	var got []vibekit.Message
	for _, m := range c.Messages {
		if m.EventKind == kind {
			got = append(got, m)
		}
	}
	return got
}

// errorPayloads collects every EventError payload broadcast.
func errorPayloads(t *testing.T, events *[]vibekit.ServerEvent) []vibekit.ErrorPayload {
	t.Helper()
	var got []vibekit.ErrorPayload
	for _, e := range *events {
		if e.Type != vibekit.EventError {
			continue
		}
		p, ok := e.Payload.(vibekit.ErrorPayload)
		if !ok {
			t.Fatalf("EventError payload type = %T, want vibekit.ErrorPayload", e.Payload)
		}
		got = append(got, p)
	}
	return got
}

// countCompactionStarted counts compaction_started broadcasts.
func countCompactionStarted(events *[]vibekit.ServerEvent) int {
	n := 0
	for _, e := range *events {
		if e.Type == vibekit.EventCompactionStarted {
			n++
		}
	}
	return n
}

// TestHandleV3Summarization_CanceledIsBenign pins the fix for the MED finding:
// a KAS summarization "canceled"/"cancelled" reason is benign and must NOT
// surface a failed-compaction boundary (EventCompactFailed message) or an
// error banner (EventError broadcast). It is a quiet no-op — the turn simply
// continues uncompacted, so nothing reaches the client.
func TestHandleV3Summarization_CanceledIsBenign(t *testing.T) {
	for _, status := range []string{"canceled", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			deps, events, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, status, ""), FrameAttribution{})

			if msgs := eventMsgsByKind(t, store, "c1", vibekit.EventCompactFailed); len(msgs) != 0 {
				t.Errorf("EventCompactFailed messages = %d, want 0 (cancel is benign)", len(msgs))
			}
			if msgs := eventMsgsByKind(t, store, "c1", vibekit.EventCompacted); len(msgs) != 0 {
				t.Errorf("EventCompacted messages = %d, want 0 (cancel is not a completion)", len(msgs))
			}
			if p := errorPayloads(t, events); len(p) != 0 {
				t.Errorf("EventError broadcasts = %+v, want none (cancel must not banner)", p)
			}
			if len(*events) != 0 {
				t.Errorf("broadcasts = %d, want 0 (cancel is a silent no-op)", len(*events))
			}
			if c, _ := store.Get(t.Context(), "c1"); c.CompactionWatermark != "" {
				t.Errorf("CompactionWatermark = %q, want empty (no compaction occurred)", c.CompactionWatermark)
			}
		})
	}
}

// TestHandleV3Summarization_SuccessCompletes pins that a "success" reason still
// drives the completion path after injectContextRecovery was removed: exactly
// one EventCompacted message carrying the summary is persisted and the
// CompactionWatermark is set to that message's id — with no failure surface.
//
// It also pins the POSITION for the between-turns case: with no turn open there is
// nothing to seal, so the event is the only message the frame appends.
func TestHandleV3Summarization_SuccessCompletes(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "evt-1" }))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})

	msgs := eventMsgsByKind(t, store, "c1", vibekit.EventCompacted)
	if len(msgs) != 1 {
		t.Fatalf("EventCompacted messages = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "history summary" {
		t.Errorf("EventCompacted content = %q, want %q", msgs[0].Content, "history summary")
	}
	if got := roleOrder(t, store, "c1"); len(got) != 1 || got[0] != vibekit.RoleEvent {
		t.Errorf("persisted roles = %v, want just the event (nothing to seal between turns)", got)
	}
	c, _ := store.Get(t.Context(), "c1")
	if c.CompactionWatermark != "evt-1" {
		t.Errorf("CompactionWatermark = %q, want %q (must equal the compacted event id)", c.CompactionWatermark, "evt-1")
	}
	if failed := eventMsgsByKind(t, store, "c1", vibekit.EventCompactFailed); len(failed) != 0 {
		t.Errorf("EventCompactFailed messages = %d, want 0 on success", len(failed))
	}
	if p := errorPayloads(t, events); len(p) != 0 {
		t.Errorf("EventError broadcasts = %+v, want none on success", p)
	}
}

// roleOrder is the persisted messages' roles, in order — the shape a mid-turn
// compaction is judged on, since where the summary sits is array position and
// nothing else.
func roleOrder(t *testing.T, store *testsupport.InMemoryChatStore, chatID vibekit.ChatID) []vibekit.Role {
	t.Helper()
	c, ok := store.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q not found", chatID)
	}
	roles := make([]vibekit.Role, 0, len(c.Messages))
	for _, m := range c.Messages {
		roles = append(roles, m.Role)
	}
	return roles
}

// A compaction that lands MID-TURN seals the turn first, so the summary sits
// between what the model said before it and what it says after — the position the
// compaction actually happened at.
//
// Without the seal the event is a sibling of the whole turn, and the two paths
// disagree: live it appends AFTER the reply, because the assistant message is only
// persisted at turn end, while a replay projects it BEFORE.
func TestHandleV3Summarization_SealsTheSegmentBeforeTheEvent(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "evt-1" }))
	// A turn in flight with two blocks buffered, as a reply mid-stream is.
	buf := deps.bufStore.GetOrInit("c1")
	buf.StartTurn("m-pre")
	buf.AppendTextDelta("before the compaction", "")
	buf.AppendThinkingDelta("thinking about it", "")

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})

	got := roleOrder(t, store, "c1")
	want := []vibekit.Role{vibekit.RoleAssistant, vibekit.RoleEvent}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("persisted roles = %v, want %v (the sealed segment must precede the summary)", got, want)
	}

	c, _ := store.Get(t.Context(), "c1")
	seg := c.Messages[0]
	if seg.ID != "m-pre" {
		t.Errorf("sealed segment id = %q, want the streamed message's %q", seg.ID, "m-pre")
	}
	if seg.Content != "before the compaction" || seg.Reasoning != "thinking about it" {
		t.Errorf("sealed segment content = %q / reasoning = %q, want the pre-compaction pair",
			seg.Content, seg.Reasoning)
	}
	if len(seg.Blocks) != 2 {
		t.Errorf("sealed segment carried %d blocks, want the 2 buffered before the compaction", len(seg.Blocks))
	}
	// The TURN is not over, so the segment carries none of the turn's own facts:
	// exactly one message per turn may claim the outcome, and a second carrier
	// opens a spurious segment for both projections.
	if seg.TurnOutcome != "" {
		t.Errorf("sealed segment TurnOutcome = %q, want empty (the turn has not ended)", seg.TurnOutcome)
	}
	if c.CompactionWatermark != "evt-1" {
		t.Errorf("CompactionWatermark = %q, want the event's id %q", c.CompactionWatermark, "evt-1")
	}

	// And the rest of the turn accumulates into a fresh message rather than
	// re-extending the one already on disk.
	if after := deps.bufStore.Get("c1").TakeTurn(); after.Content != "" || after.Started {
		t.Errorf("after the seal the buffer holds %q with Started = %t, want empty and false",
			after.Content, after.Started)
	}
}

// A turn holding a tool call still in flight is NOT split, so the summary lands
// after the turn instead.
//
// The alternative is worse than the wrong position: an update resolves its call
// against the current buffer, so a call sealed mid-flight can never be written
// back and its card renders as a permanent spinner in a message nothing rewrites.
func TestHandleV3Summarization_DoesNotSealWithAToolInFlight(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	deps.sealRefusals = map[vibekit.ChatID]bool{"c1": true}
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "evt-1" }))
	buf := deps.bufStore.GetOrInit("c1")
	buf.StartTurn("m-pre")
	buf.AppendTextDelta("before the compaction", "")
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t-1", Status: vibekit.ToolInProgress})

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})

	if got := roleOrder(t, store, "c1"); len(got) != 1 || got[0] != vibekit.RoleEvent {
		t.Errorf("persisted roles = %v, want just the event (a declined split appends nothing else)", got)
	}
	// The turn keeps its content, so its closer still persists the whole reply.
	if snap := deps.bufStore.Get("c1").TakeTurn(); snap.Content != "before the compaction" {
		t.Errorf("after a declined split the buffer holds %q, want the turn's content intact", snap.Content)
	}
}

// A compaction between turns appends the event and nothing else: there is no
// position inside a turn to seal at.
func TestHandleV3Summarization_NoTurnOpenAppendsOnly(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "evt-1" }))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})

	if got := roleOrder(t, store, "c1"); len(got) != 1 || got[0] != vibekit.RoleEvent {
		t.Errorf("persisted roles = %v, want just the event", got)
	}
}

// A FAILED compaction is a notice rather than a boundary — nothing about the
// context changed, so there is no point for it to sit at and the turn is left
// whole.
func TestHandleV3Summarization_FailureDoesNotSealTheTurn(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	buf := deps.bufStore.GetOrInit("c1")
	buf.StartTurn("m-pre")
	buf.AppendTextDelta("mid-reply", "")

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "error", ""), FrameAttribution{})

	if got := roleOrder(t, store, "c1"); len(got) != 1 || got[0] != vibekit.RoleEvent {
		t.Errorf("persisted roles = %v, want just the failed-compaction event", got)
	}
	if snap := deps.bufStore.Get("c1").TakeTurn(); snap.Content != "mid-reply" {
		t.Errorf("after a failed compaction the buffer holds %q, want the turn's content intact", snap.Content)
	}
}

// TestHandleV3Summarization_GenuineErrorFails is the regression guard that the
// canceled special-case did not swallow real failures: a genuine "error"
// reason still persists an EventCompactFailed boundary AND broadcasts a
// compaction_failed error banner.
func TestHandleV3Summarization_GenuineErrorFails(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "error", ""), FrameAttribution{})

	msgs := eventMsgsByKind(t, store, "c1", vibekit.EventCompactFailed)
	if len(msgs) != 1 {
		t.Fatalf("EventCompactFailed messages = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "error" {
		t.Errorf("EventCompactFailed content = %q, want %q", msgs[0].Content, "error")
	}
	p := errorPayloads(t, events)
	if len(p) != 1 {
		t.Fatalf("EventError broadcasts = %d, want 1", len(p))
	}
	if p[0].Code != vibekit.ErrCodeCompactionFailed {
		t.Errorf("error code = %q, want %q", p[0].Code, vibekit.ErrCodeCompactionFailed)
	}
	if p[0].Message != "error" {
		t.Errorf("error message = %q, want %q", p[0].Message, "error")
	}
}

// TestHandleV3Summarization_RunningStarts pins that a "running" reason
// broadcasts exactly one compaction_started signal and persists no terminal
// (compacted/failed) event.
func TestHandleV3Summarization_RunningStarts(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "running", ""), FrameAttribution{})

	if n := countCompactionStarted(events); n != 1 {
		t.Errorf("compaction_started broadcasts = %d, want 1", n)
	}
	if p := errorPayloads(t, events); len(p) != 0 {
		t.Errorf("EventError broadcasts = %+v, want none while running", p)
	}
	if msgs := eventMsgsByKind(t, store, "c1", vibekit.EventCompacted); len(msgs) != 0 {
		t.Errorf("EventCompacted messages = %d, want 0 while running", len(msgs))
	}
	if msgs := eventMsgsByKind(t, store, "c1", vibekit.EventCompactFailed); len(msgs) != 0 {
		t.Errorf("EventCompactFailed messages = %d, want 0 while running", len(msgs))
	}
}

// The compacted-event append logs an error when it FAILS and says nothing when
// it succeeds. The quiet half is the half worth pinning: compaction runs on
// every long conversation, so an error line on the ordinary path trains an
// operator to scroll past the one that means the breadcrumb was really lost.
func TestHandleV3Summarization_CompactedAppendSpeaksOnlyOnFailure(t *testing.T) {
	tests := []struct {
		appendErr  error
		name       string
		wantLogged bool
	}{
		{name: "a_successful_append_is_silent", appendErr: nil, wantLogged: false},
		{name: "a_failed_append_reports_itself", appendErr: errBoom, wantLogged: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			t.Cleanup(captureSlog(&logs))
			deps, _ := newEventCaptureDeps()
			deps.store = &recStore{appendErr: tc.appendErr}
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})

			got := strings.Contains(logs.String(), `msg="compaction: append event"`)
			if got != tc.wantLogged {
				t.Errorf("HandleSessionInfoUpdate(success, appendErr=%v) logged the append error = %t, want %t; logs = %q",
					tc.appendErr, got, tc.wantLogged, logs.String())
			}
		})
	}
}

// The compaction-FAILED event's own append follows the same rule: the error
// line belongs to the persist failing, not to a compaction failure being
// recorded successfully. The error banner reaches the client either way, so a
// line here would be the second report of a failure already surfaced.
func TestHandleV3Summarization_FailedEventAppendSpeaksOnlyOnFailure(t *testing.T) {
	tests := []struct {
		appendErr  error
		name       string
		wantLogged bool
	}{
		{name: "a_successful_append_is_silent", appendErr: nil, wantLogged: false},
		{name: "a_failed_append_reports_itself", appendErr: errBoom, wantLogged: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			t.Cleanup(captureSlog(&logs))
			deps, _ := newEventCaptureDeps()
			deps.store = &recStore{appendErr: tc.appendErr}
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "error", ""), FrameAttribution{})

			got := strings.Contains(logs.String(), `msg="compaction: append failed event"`)
			if got != tc.wantLogged {
				t.Errorf("HandleSessionInfoUpdate(error, appendErr=%v) logged the append error = %t, want %t; logs = %q",
					tc.appendErr, got, tc.wantLogged, logs.String())
			}
		})
	}
}
