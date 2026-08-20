package translate

import (
	"encoding/json"
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

			tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, status, ""), "")

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
func TestHandleV3Summarization_SuccessCompletes(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "evt-1" }))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), "")

	msgs := eventMsgsByKind(t, store, "c1", vibekit.EventCompacted)
	if len(msgs) != 1 {
		t.Fatalf("EventCompacted messages = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "history summary" {
		t.Errorf("EventCompacted content = %q, want %q", msgs[0].Content, "history summary")
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

// TestHandleV3Summarization_GenuineErrorFails is the regression guard that the
// canceled special-case did not swallow real failures: a genuine "error"
// reason still persists an EventCompactFailed boundary AND broadcasts a
// compaction_failed error banner.
func TestHandleV3Summarization_GenuineErrorFails(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "error", ""), "")

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

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "running", ""), "")

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
