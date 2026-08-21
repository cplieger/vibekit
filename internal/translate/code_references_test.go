package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// codeRefsPayload extracts the CodeReferencesPayload from the single
// EventCodeReferences broadcast, or fails if there isn't exactly one.
func codeRefsPayload(t *testing.T, events *[]vibekit.ServerEvent) vibekit.CodeReferencesPayload {
	t.Helper()
	var got []vibekit.CodeReferencesPayload
	for _, e := range *events {
		if e.Type != vibekit.EventCodeReferences {
			continue
		}
		p, ok := e.Payload.(vibekit.CodeReferencesPayload)
		if !ok {
			t.Fatalf("EventCodeReferences payload type = %T, want vibekit.CodeReferencesPayload", e.Payload)
		}
		got = append(got, p)
	}
	if len(got) != 1 {
		t.Fatalf("EventCodeReferences broadcast count = %d, want 1", len(got))
	}
	return got[0]
}

func countCodeRefEvents(events *[]vibekit.ServerEvent) int {
	n := 0
	for _, e := range *events {
		if e.Type == vibekit.EventCodeReferences {
			n++
		}
	}
	return n
}

// startedBuf returns an in-flight assistant buffer for chatID with a
// message id set (mirrors ensureTurnStarted).
func startedBuf(deps *baseDeps, chatID vibekit.ChatID, msgID string) {
	buf := deps.bufStore.GetOrInit(chatID)
	buf.Started = true
	buf.MessageID = msgID
}

func codeRefMsg(t *testing.T, sessionID string, refs []map[string]any) *vibekit.RPCResponse {
	t.Helper()
	return &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"sessionId":  sessionID,
		"references": refs,
	})}
}

// TestHandleCodeReferences_HappyPath pins that a well-formed notification on
// an in-flight turn accumulates the references onto the buffer and broadcasts
// exactly one code_references event carrying the turn's message id and the
// full list.
func TestHandleCodeReferences_HappyPath(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := vibekit.ChatID("c1")
	startedBuf(deps, chatID, "m-1")

	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", []map[string]any{
		{"licenseName": "MIT", "repository": "github.com/foo/bar", "url": "https://github.com/foo/bar"},
	}))

	p := codeRefsPayload(t, events)
	if p.MessageID != "m-1" {
		t.Errorf("payload MessageID = %q, want %q", p.MessageID, "m-1")
	}
	if len(p.References) != 1 || p.References[0].LicenseName != "MIT" ||
		p.References[0].Repository != "github.com/foo/bar" ||
		p.References[0].URL != "https://github.com/foo/bar" {
		t.Errorf("payload References = %+v, want one MIT/foo/bar reference", p.References)
	}
	buf := deps.bufStore.GetOrInit(chatID)
	if len(buf.CodeReferences) != 1 {
		t.Errorf("buffer CodeReferences = %+v, want 1 accumulated", buf.CodeReferences)
	}
}

// TestHandleCodeReferences_DropsEmptyLicense pins that references with no
// license name are filtered (matching KAS's own filter); a notification with
// only such entries produces no broadcast and no accumulation.
func TestHandleCodeReferences_DropsEmptyLicense(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := vibekit.ChatID("c1")
	startedBuf(deps, chatID, "m-1")

	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", []map[string]any{
		{"licenseName": "", "repository": "github.com/x/y", "url": "https://github.com/x/y"},
	}))

	if n := countCodeRefEvents(events); n != 0 {
		t.Errorf("broadcast count = %d, want 0 (all references had empty license)", n)
	}
	if buf := deps.bufStore.GetOrInit(chatID); len(buf.CodeReferences) != 0 {
		t.Errorf("buffer CodeReferences = %+v, want none", buf.CodeReferences)
	}
}

// TestHandleCodeReferences_NoTurnInFlight pins that a notification arriving
// with no started turn is dropped: no broadcast, and nothing accumulated onto
// the (freshly GetOrInit'd) buffer so it can't contaminate the next turn.
func TestHandleCodeReferences_NoTurnInFlight(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := vibekit.ChatID("c1")
	// No startedBuf: buffer is absent / not started.

	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", []map[string]any{
		{"licenseName": "Apache-2.0", "repository": "github.com/a/b", "url": "https://github.com/a/b"},
	}))

	if n := countCodeRefEvents(events); n != 0 {
		t.Errorf("broadcast count = %d, want 0 (no in-flight turn)", n)
	}
	if buf := deps.bufStore.GetOrInit(chatID); len(buf.CodeReferences) != 0 {
		t.Errorf("buffer CodeReferences = %+v, want none (must not attach to a not-started turn)", buf.CodeReferences)
	}
}

// TestHandleCodeReferences_SkipsSubagentFanout pins the KAS fan-out dedup:
// KAS broadcasts the same references under every live session id, so a copy
// keyed to a subagent session (differing from the parent) is skipped; the
// parent-session copy is processed.
func TestHandleCodeReferences_SkipsSubagentFanout(t *testing.T) {
	t.Run("SubagentCopySkipped", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		deps.parent = "sess-parent"
		tr := New(rolesOf(deps))
		chatID := vibekit.ChatID("c1")
		startedBuf(deps, chatID, "m-1")

		tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "sess-sub", []map[string]any{
			{"licenseName": "MIT", "repository": "r", "url": "https://example.com"},
		}))
		if n := countCodeRefEvents(events); n != 0 {
			t.Errorf("broadcast count = %d, want 0 (subagent-keyed copy must be skipped)", n)
		}
	})
	t.Run("ParentCopyProcessed", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		deps.parent = "sess-parent"
		tr := New(rolesOf(deps))
		chatID := vibekit.ChatID("c1")
		startedBuf(deps, chatID, "m-1")

		tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "sess-parent", []map[string]any{
			{"licenseName": "MIT", "repository": "r", "url": "https://example.com"},
		}))
		if n := countCodeRefEvents(events); n != 1 {
			t.Errorf("broadcast count = %d, want 1 (parent-keyed copy must be processed)", n)
		}
	})
}

// TestHandleCodeReferences_DedupAcrossNotifications pins that the same
// reference delivered twice (e.g. a completion reproducing the same snippet
// again) accumulates once; the second broadcast still carries a single entry.
func TestHandleCodeReferences_DedupAcrossNotifications(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := vibekit.ChatID("c1")
	startedBuf(deps, chatID, "m-1")

	ref := []map[string]any{{"licenseName": "MIT", "repository": "r", "url": "https://example.com"}}
	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", ref))
	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", ref))

	if n := countCodeRefEvents(events); n != 2 {
		t.Fatalf("broadcast count = %d, want 2 (one per notification)", n)
	}
	last := (*events)[len(*events)-1]
	p, ok := last.Payload.(vibekit.CodeReferencesPayload)
	if !ok {
		t.Fatalf("last payload type = %T", last.Payload)
	}
	if len(p.References) != 1 {
		t.Errorf("deduped References = %+v, want 1 (identical reference must not duplicate)", p.References)
	}
	if buf := deps.bufStore.GetOrInit(chatID); len(buf.CodeReferences) != 1 {
		t.Errorf("buffer CodeReferences = %+v, want 1 after dedup", buf.CodeReferences)
	}
}

// TestHandleCodeReferences_MalformedParamsNoop pins that malformed params are
// dropped without a broadcast (defensive decode).
func TestHandleCodeReferences_MalformedParamsNoop(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := vibekit.ChatID("c1")
	startedBuf(deps, chatID, "m-1")

	tr.HandleCodeReferences(t.Context(), chatID, &vibekit.RPCResponse{Params: []byte("{")})
	if n := countCodeRefEvents(events); n != 0 {
		t.Errorf("broadcast count = %d, want 0 (malformed params)", n)
	}
}
