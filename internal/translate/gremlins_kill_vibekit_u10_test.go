package translate

// Mutation-killing tests for unit vibekit-u10 (package internal/translate).
//
// Targets surviving gremlins mutants in commands_handler.go, crew.go,
// deps.go, streaming_content.go, and streaming_tools.go. Tests add no
// production code; every assertion's expected value depends on the exact
// operator at the mutated site. Two mutants are genuinely equivalent and
// documented as such (no test can distinguish them):
//   - commands_handler.go:70:16 CONDITIONALS_BOUNDARY (assigning a nil
//     meta is indistinguishable from skipping the assignment).
//   - streaming_tools.go:103:23 CONDITIONALS_BOUNDARY (appending an empty
//     output string is a no-op).
//
// All new identifiers are prefixed gk_vibekit_u10_ to avoid colliding
// with sibling units sharing this package.

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

// --- prefixed test fixtures ---

var gk_vibekit_u10_errBoom = errors.New("gk-vibekit-u10 boom")

// gk_vibekit_u10_lineRec is a recording LineRecorder counting RecordFromDiffs calls.
type gk_vibekit_u10_lineRec struct {
	calls int
}

func (r *gk_vibekit_u10_lineRec) RecordFromDiffs(_ api.ChatID, _ []api.ToolDiff, _ int, _ string) {
	r.calls++
}

// gk_vibekit_u10_lineDeps wraps baseDeps and swaps in the recording LineTracker.
type gk_vibekit_u10_lineDeps struct {
	*baseDeps
	rec *gk_vibekit_u10_lineRec
}

func (d *gk_vibekit_u10_lineDeps) LineTracker() LineRecorder { return d.rec }

// gk_vibekit_u10_mcpRec is a recording MCPRecorder counting SetKnownTools calls.
type gk_vibekit_u10_mcpRec struct {
	setKnownCalls int
}

func (*gk_vibekit_u10_mcpRec) RecordConnected(context.Context, string)         {}
func (*gk_vibekit_u10_mcpRec) RecordOAuth(context.Context, string, string)     {}
func (*gk_vibekit_u10_mcpRec) RecordInitFailure(context.Context, string, string) {}
func (*gk_vibekit_u10_mcpRec) SignalReady()                                    {}
func (r *gk_vibekit_u10_mcpRec) SetKnownTools(_ context.Context, _ string, _ []string) {
	r.setKnownCalls++
}

// gk_vibekit_u10_mcpDeps wraps baseDeps and swaps in the recording MCPRecorder.
type gk_vibekit_u10_mcpDeps struct {
	*baseDeps
	rec *gk_vibekit_u10_mcpRec
}

func (d *gk_vibekit_u10_mcpDeps) MCPRecorder() MCPRecorder { return d.rec }

// gk_vibekit_u10_recStore records AppendMessage/Mutate calls and returns
// configurable errors, so HandlePlan's persist branches are observable.
type gk_vibekit_u10_recStore struct {
	testsupport.NopChatStore
	appendCalls int
	mutateCalls int
	appendErr   error
	mutateErr   error
}

func (s *gk_vibekit_u10_recStore) AppendMessage(_ context.Context, _ api.ChatID, _ *api.Message) error {
	s.appendCalls++
	return s.appendErr
}

func (s *gk_vibekit_u10_recStore) Mutate(_ context.Context, _ api.ChatID, fn func(*api.Chat, bool) bool) error {
	s.mutateCalls++
	if fn != nil {
		_ = fn(&api.Chat{}, true)
	}
	return s.mutateErr
}

// Compile-time interface assertions.
var (
	_ api.ChatStore = (*gk_vibekit_u10_recStore)(nil)
	_ LineRecorder  = (*gk_vibekit_u10_lineRec)(nil)
	_ MCPRecorder   = (*gk_vibekit_u10_mcpRec)(nil)
	_ Deps          = (*gk_vibekit_u10_lineDeps)(nil)
	_ Deps          = (*gk_vibekit_u10_mcpDeps)(nil)
)

// gk_vibekit_u10_captureSlog redirects the default slog logger to buf and
// returns a restore function. Not parallel-safe (global slog default).
func gk_vibekit_u10_captureSlog(buf *bytes.Buffer) func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(prev) }
}

// =====================================================================
// crew.go:46:9 CONDITIONALS_NEGATION  (`if err != nil` in MarshalCrew)
// =====================================================================

func TestGk_vibekit_u10_MarshalCrew_ReturnsNonNilForValidCrew(t *testing.T) {
	crew := &api.Crew{
		Group:     "g1",
		Subagents: []api.CrewSubagent{{SessionID: "s1", Status: api.CrewWorking, Group: "g1"}},
	}
	got := MarshalCrew(crew)
	// json.Marshal succeeds for a valid crew, so err==nil and the
	// function returns the bytes. The `err == nil` mutant would take the
	// `return nil` branch and yield nil for this valid input.
	if got == nil {
		t.Fatal("MarshalCrew(validCrew) = nil, want non-nil JSON bytes")
	}
	if len(got) == 0 {
		t.Fatalf("MarshalCrew(validCrew) len = %d, want > 0", len(got))
	}
	var back api.Crew
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("MarshalCrew output is not valid JSON: %v", err)
	}
	if back.Group != "g1" {
		t.Errorf("MarshalCrew round-trip Group = %q, want %q", back.Group, "g1")
	}
}

// =====================================================================
// deps.go:67:16 CONDITIONALS_NEGATION  (`if t.newMsgID == nil` in New)
// =====================================================================

func TestGk_vibekit_u10_New_InstallsDefaultIDWhenUnset(t *testing.T) {
	tr := New(newBaseDeps(), "/tmp")
	// No WithIDGenerator: the `== nil` check is true so the default
	// generator is installed. The `!= nil` mutant would leave it nil.
	if tr.newMsgID == nil {
		t.Fatal("New (no WithIDGenerator): newMsgID is nil, want default generator installed")
	}
	if id := tr.newMsgID(); id == "" {
		t.Errorf("default newMsgID() = %q, want non-empty", id)
	}
}

func TestGk_vibekit_u10_New_KeepsCustomIDGenerator(t *testing.T) {
	tr := New(newBaseDeps(), "/tmp", WithIDGenerator(func() string { return "gk-vibekit-u10-custom" }))
	// newMsgID is non-nil (custom), so the `== nil` check is false and the
	// custom generator is preserved. The `!= nil` mutant would overwrite
	// it with the default generator.
	if got := tr.newMsgID(); got != "gk-vibekit-u10-custom" {
		t.Errorf("newMsgID() = %q, want %q (custom generator must not be overwritten)", got, "gk-vibekit-u10-custom")
	}
}

// =====================================================================
// commands_handler.go:37:19  (`if len(s.Tools) > 0`)  BOUNDARY + NEGATION
// =====================================================================

func TestGk_vibekit_u10_CommandsAvailable_SetKnownToolsGate(t *testing.T) {
	tests := []struct {
		name      string
		tools     []string
		wantCalls int
	}{
		{name: "NonEmptyToolsPersisted", tools: []string{"a", "b"}, wantCalls: 1},
		{name: "EmptyToolsSkipped", tools: []string{}, wantCalls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &gk_vibekit_u10_mcpRec{}
			deps := &gk_vibekit_u10_mcpDeps{baseDeps: newBaseDeps(), rec: rec}
			tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
			tr.HandleCommandsAvailable(context.Background(), api.ChatID("c1"), &api.RPCResponse{
				Params: mustJSON(t, map[string]any{
					"mcpServers": []map[string]any{
						{"name": "srv", "status": "running", "tools": tt.tools},
					},
				}),
			})
			// Non-empty tools (len 2): `2>0` true -> persisted; the `<=` (negation)
			// mutant skips. Empty tools (len 0): `0>0` false -> skipped; the `>=`
			// (boundary) mutant persists empty tools, the `<=` (negation) mutant
			// also persists. Together both mutants are killed.
			if rec.setKnownCalls != tt.wantCalls {
				t.Errorf("SetKnownTools calls = %d, want %d (tools=%v)", rec.setKnownCalls, tt.wantCalls, tt.tools)
			}
		})
	}
}

// =====================================================================
// commands_handler.go:47:13  (`if len(in) == 0`)  NEGATION
// =====================================================================

func TestGk_vibekit_u10_ToAvailableCommands_EmptyVsNonEmpty(t *testing.T) {
	// len(in)==0 -> returns nil (NOT a non-nil empty slice). The `!=`
	// mutant skips the early return and builds a non-nil empty slice.
	if got := toAvailableCommands(nil); got != nil {
		t.Errorf("toAvailableCommands(nil) len=%d = %v, want nil", len(got), got)
	}
	if got := toAvailableCommands([]map[string]any{}); got != nil {
		t.Errorf("toAvailableCommands(empty) len=%d = %v, want nil", len(got), got)
	}
	// len(in)>0 -> processes. The `!=` mutant returns nil for non-empty input.
	got := toAvailableCommands([]map[string]any{{"name": "x"}})
	if len(got) != 1 {
		t.Fatalf("toAvailableCommands(1 elem) len = %d, want 1", len(got))
	}
	if got[0].Name != "x" {
		t.Errorf("toAvailableCommands(1 elem)[0].Name = %q, want %q", got[0].Name, "x")
	}
}

// =====================================================================
// commands_handler.go:62:9 and 62:33  (the two `k == ...` skip checks)  NEGATION
// =====================================================================

func TestGk_vibekit_u10_ToAvailableCommands_MetaExcludesNameAndDescription(t *testing.T) {
	got := toAvailableCommands([]map[string]any{
		{"name": "cmd-x", "description": "desc-d", "extra": "val-e"},
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	ac := got[0]
	if ac.Name != "cmd-x" {
		t.Errorf("Name = %q, want %q", ac.Name, "cmd-x")
	}
	if ac.Description != "desc-d" {
		t.Errorf("Description = %q, want %q", ac.Description, "desc-d")
	}
	// Original Meta = {extra:val-e}. The `k != name` (62:9) mutant adds
	// "name" and drops "extra"; the `k != "description"` (62:33) mutant
	// adds "description" and drops "extra". Asserting Meta is exactly
	// {extra:val-e} kills both.
	if v, ok := ac.Meta["extra"]; !ok || v != "val-e" {
		t.Errorf("Meta[extra] = %v (ok=%v), want %q", v, ok, "val-e")
	}
	if _, ok := ac.Meta["name"]; ok {
		t.Errorf("Meta must not contain key %q (it is extracted to Name)", "name")
	}
	if _, ok := ac.Meta["description"]; ok {
		t.Errorf("Meta must not contain key %q (it is extracted to Description)", "description")
	}
	if len(ac.Meta) != 1 {
		t.Errorf("len(Meta) = %d, want 1 (only the extra key)", len(ac.Meta))
	}
}

// =====================================================================
// commands_handler.go:70:16  (`if len(meta) > 0`)
//   NEGATION killed below; BOUNDARY is equivalent (meta is nil at len 0,
//   so assigning it vs skipping leaves ac.Meta nil either way).
// =====================================================================

func TestGk_vibekit_u10_ToAvailableCommands_MetaAssignedOnlyWhenNonEmpty(t *testing.T) {
	// Extra key present -> meta has 1 entry -> `1>0` true -> ac.Meta set.
	// The `<=` (negation) mutant leaves ac.Meta nil.
	got := toAvailableCommands([]map[string]any{{"name": "n", "extra": "e"}})
	if got[0].Meta == nil {
		t.Fatal("ac.Meta = nil, want non-nil when an extra key is present")
	}
	if got[0].Meta["extra"] != "e" {
		t.Errorf("Meta[extra] = %v, want %q", got[0].Meta["extra"], "e")
	}
	// No extra key -> meta stays nil -> ac.Meta nil (boundary mutant would
	// assign nil here, which is indistinguishable: documented equivalent).
	got2 := toAvailableCommands([]map[string]any{{"name": "n", "description": "d"}})
	if got2[0].Meta != nil {
		t.Errorf("ac.Meta = %v, want nil when there are no extra keys", got2[0].Meta)
	}
}

// =====================================================================
// streaming_content.go:31:32, 32:13 (ARITHMETIC_BASE) and 32:38 (BOUNDARY)
//   the per-turn buffer cap guard.
// =====================================================================

// gk_vibekit_u10_chunkProcessed pre-fills the content/reasoning builders to
// the given byte lengths, sends one text chunk, and reports whether the
// chunk was processed (a message_chunk event was broadcast) vs dropped by
// the maxBufferBytes guard.
func gk_vibekit_u10_chunkProcessed(t *testing.T, contentLen, reasoningLen int, text string) bool {
	t.Helper()
	deps, events := newEventCaptureDeps()
	chatID := api.ChatID("gk-vibekit-u10-cap")
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
	buf.MessageID = "gk-vibekit-u10-mid"
	tr := New(deps, "/tmp", WithIDGenerator(func() string { return "gk-vibekit-u10-mid" }))
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

func TestGk_vibekit_u10_BufferCap_BoundaryExactMaxIsProcessed(t *testing.T) {
	// 32:38 BOUNDARY: total == maxBufferBytes. `total > max` is false so
	// the chunk is processed; the `total >= max` mutant would drop it.
	if !gk_vibekit_u10_chunkProcessed(t, maxBufferBytes-1, 0, "a") {
		t.Error("chunk at total==maxBufferBytes: processed=false, want true (`>` must not fire at equality)")
	}
}

func TestGk_vibekit_u10_BufferCap_OverByOneIsDropped(t *testing.T) {
	// 32:13 ARITHMETIC_BASE: total==max, +1 pushes over the cap -> dropped.
	// The `totalLen - len(text)` mutant lands under the cap -> processed.
	if gk_vibekit_u10_chunkProcessed(t, maxBufferBytes, 0, "a") {
		t.Error("chunk at total==maxBufferBytes+1: processed=true, want false (`+ len(text)` must push over the cap)")
	}
}

func TestGk_vibekit_u10_BufferCap_ReasoningCountsTowardTotal(t *testing.T) {
	// 31:32 ARITHMETIC_BASE: reasoning bytes must ADD to content bytes.
	// content=max-1, reasoning=1, text=1 -> total max -> +1 over cap -> drop.
	// The `Content.Len() - Reasoning.Len()` mutant -> (max-1-1)+1=max-1 -> process.
	if gk_vibekit_u10_chunkProcessed(t, maxBufferBytes-1, 1, "a") {
		t.Error("chunk with content=max-1, reasoning=1: processed=true, want false (reasoning must add to total)")
	}
}

// =====================================================================
// streaming_content.go:64:29 NEGATION  (HandlePlan unmarshal guard)
// =====================================================================

func TestGk_vibekit_u10_HandlePlan_UnmarshalGuard(t *testing.T) {
	t.Run("ValidJSONAppendsMessage", func(t *testing.T) {
		rec := &gk_vibekit_u10_recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		// Valid JSON: `err != nil` false -> proceeds -> AppendMessage.
		// The `err == nil` mutant returns before appending.
		if rec.appendCalls != 1 {
			t.Errorf("valid plan JSON: AppendMessage calls = %d, want 1", rec.appendCalls)
		}
	})
	t.Run("InvalidJSONSkips", func(t *testing.T) {
		rec := &gk_vibekit_u10_recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{`))
		// Invalid JSON: `err != nil` true -> returns. The `err == nil`
		// mutant proceeds and appends a message.
		if rec.appendCalls != 0 {
			t.Errorf("invalid plan JSON: AppendMessage calls = %d, want 0", rec.appendCalls)
		}
	})
}

// =====================================================================
// streaming_content.go:73:69 NEGATION  (AppendMessage error log branch)
// =====================================================================

func TestGk_vibekit_u10_HandlePlan_LogsOnlyOnAppendError(t *testing.T) {
	t.Run("ErrorLoggedWhenAppendFails", func(t *testing.T) {
		var logbuf bytes.Buffer
		restore := gk_vibekit_u10_captureSlog(&logbuf)
		defer restore()
		rec := &gk_vibekit_u10_recStore{appendErr: gk_vibekit_u10_errBoom}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		// AppendMessage returns an error: `err != nil` true -> logs.
		// The `err == nil` mutant would NOT log on this error.
		if !strings.Contains(logbuf.String(), "persist plan") {
			t.Errorf("AppendMessage error not logged; log=%q, want it to contain %q", logbuf.String(), "persist plan")
		}
	})
	t.Run("NoLogWhenAppendSucceeds", func(t *testing.T) {
		var logbuf bytes.Buffer
		restore := gk_vibekit_u10_captureSlog(&logbuf)
		defer restore()
		rec := &gk_vibekit_u10_recStore{} // appendErr nil, mutateErr nil
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		// AppendMessage succeeds: `err != nil` false -> no log. The
		// `err == nil` mutant would log on success.
		if strings.Contains(logbuf.String(), "persist plan") {
			t.Errorf("unexpected error log on AppendMessage success; log=%q", logbuf.String())
		}
	})
}

// =====================================================================
// streaming_content.go:76:15 NEGATION  (ctx.Err() guard before Mutate)
// =====================================================================

func TestGk_vibekit_u10_HandlePlan_ContextErrGuard(t *testing.T) {
	t.Run("ActiveContextProceedsToMutate", func(t *testing.T) {
		rec := &gk_vibekit_u10_recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandlePlan(context.Background(), api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		// Active ctx: `ctx.Err() != nil` false -> proceeds to Mutate.
		// The `== nil` mutant would return before Mutate.
		if rec.mutateCalls != 1 {
			t.Errorf("active ctx: Mutate calls = %d, want 1", rec.mutateCalls)
		}
	})
	t.Run("CancelledContextSkipsMutate", func(t *testing.T) {
		rec := &gk_vibekit_u10_recStore{}
		deps := newBaseDeps()
		deps.store = rec
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tr.HandlePlan(ctx, api.ChatID("c1"), json.RawMessage(`{"entries":[]}`))
		// Cancelled ctx: `ctx.Err() != nil` true -> returns before Mutate.
		// The `== nil` mutant would proceed and call Mutate.
		if rec.mutateCalls != 0 {
			t.Errorf("cancelled ctx: Mutate calls = %d, want 0", rec.mutateCalls)
		}
	})
}

// =====================================================================
// streaming_tools.go:59:16  (`if len(diffs) > 0` in HandleToolCall)  BOUNDARY + NEGATION
// =====================================================================

func gk_vibekit_u10_lineDepsWith() (*gk_vibekit_u10_lineDeps, *gk_vibekit_u10_lineRec, *[]api.ServerEvent) {
	base, events := newEventCaptureDeps()
	rec := &gk_vibekit_u10_lineRec{}
	return &gk_vibekit_u10_lineDeps{baseDeps: base, rec: rec}, rec, events
}

func TestGk_vibekit_u10_HandleToolCall_DiffGate(t *testing.T) {
	t.Run("WithDiffRecordsLineChanges", func(t *testing.T) {
		deps, rec, _ := gk_vibekit_u10_lineDepsWith()
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(context.Background(), api.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-diff",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "x.go", "oldText": "a", "newText": "b"},
			},
		}), "")
		// len(diffs)==1: `1>0` true -> RecordFromDiffs. The `<=` (negation)
		// mutant skips the call.
		if rec.calls != 1 {
			t.Errorf("with diff: RecordFromDiffs calls = %d, want 1", rec.calls)
		}
	})
	t.Run("WithoutDiffSkipsLineTracker", func(t *testing.T) {
		deps, rec, _ := gk_vibekit_u10_lineDepsWith()
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(context.Background(), api.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-nodiff",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
		}), "")
		// len(diffs)==0: `0>0` false -> skipped. The `>=` (boundary) and
		// `<=` (negation) mutants both call RecordFromDiffs.
		if rec.calls != 0 {
			t.Errorf("without diff: RecordFromDiffs calls = %d, want 0", rec.calls)
		}
	})
}

// =====================================================================
// streaming_tools.go HandleToolCallUpdate mutants (92, 95, 97, 103, 106, 109)
// =====================================================================

// gk_vibekit_u10_primeToolCall builds an event-capturing translator with a
// recording LineTracker, registers one in-flight tool call "tc-1" (status
// pending, no diffs/locations), then clears captured state so the
// subsequent update is observed in isolation.
func gk_vibekit_u10_primeToolCall(t *testing.T) (*Translator, *gk_vibekit_u10_lineRec, *[]api.ServerEvent, api.ChatID) {
	t.Helper()
	deps, rec, events := gk_vibekit_u10_lineDepsWith()
	tr := New(deps, "/tmp", WithIDGenerator(func() string { return "gk-vibekit-u10-mid" }))
	chatID := api.ChatID("c1")
	tr.HandleToolCall(context.Background(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"title":      "readFile",
		"kind":       "read",
		"status":     "pending",
	}), "")
	*events = nil
	rec.calls = 0
	return tr, rec, events, chatID
}

func gk_vibekit_u10_lastUpdate(t *testing.T, events *[]api.ServerEvent) (api.ToolCall, bool) {
	t.Helper()
	for i := len(*events) - 1; i >= 0; i-- {
		if (*events)[i].Type == api.EventToolCallUpdate {
			p, ok := (*events)[i].Payload.(api.ToolCallUpdatePayload)
			if !ok {
				t.Fatalf("tool_call_update payload type = %T, want api.ToolCallUpdatePayload", (*events)[i].Payload)
			}
			return p.ToolCall, true
		}
	}
	return api.ToolCall{}, false
}

func gk_vibekit_u10_hasWorkingLabel(events *[]api.ServerEvent) bool {
	for _, e := range *events {
		if e.Type == api.EventWorkingLabel {
			return true
		}
	}
	return false
}

// 95:15 NEGATION  (`if tu.Status != ""`)
func TestGk_vibekit_u10_ToolCallUpdate_StatusApplied(t *testing.T) {
	tr, _, events, chatID := gk_vibekit_u10_primeToolCall(t)
	tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
	}), "")
	tc, ok := gk_vibekit_u10_lastUpdate(t, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	// Non-empty status: `!= ""` true -> status applied. The `== ""` mutant
	// leaves the status at its prior "pending" value.
	if tc.Status != api.ToolCompleted {
		t.Errorf("ToolCall.Status = %q, want %q (non-empty status must be applied)", tc.Status, api.ToolCompleted)
	}
}

// 97:16 and 97:50 NEGATION  (`status == Completed || status == Failed`)
func TestGk_vibekit_u10_ToolCallUpdate_TerminalStatusEmitsWorkingLabel(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "Completed", status: "completed"}, // kills 97:16
		{name: "Failed", status: "failed"},       // kills 97:50
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, events, chatID := gk_vibekit_u10_primeToolCall(t)
			tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     tt.status,
			}), "")
			// Terminal status -> inner branch runs -> a working_label
			// (Thinking) event is broadcast. The `!=` mutant on either
			// operand drops the matching terminal status out of the branch.
			if !gk_vibekit_u10_hasWorkingLabel(events) {
				t.Errorf("status=%q: no working_label event emitted, want one (terminal status must emit it)", tt.status)
			}
		})
	}
}

// 103:23 NEGATION  (`if outputDelta.Len() > 0`); boundary is equivalent.
func TestGk_vibekit_u10_ToolCallUpdate_OutputAppendedWhenContentPresent(t *testing.T) {
	tr, _, events, chatID := gk_vibekit_u10_primeToolCall(t)
	tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"text": "hello"}},
		},
	}), "")
	tc, ok := gk_vibekit_u10_lastUpdate(t, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	// outputDelta.Len()>0 -> output appended. The `<=` (negation) mutant
	// skips the append, leaving Output empty.
	if tc.Output != "hello\n" {
		t.Errorf("ToolCall.Output = %q, want %q (output must be appended when content present)", tc.Output, "hello\n")
	}
}

// 106:23  (`if len(tu.Locations) > 0`)  BOUNDARY + NEGATION
func TestGk_vibekit_u10_ToolCallUpdate_LocationsGate(t *testing.T) {
	t.Run("LocationsSetWhenPresent", func(t *testing.T) {
		tr, _, events, chatID := gk_vibekit_u10_primeToolCall(t)
		tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"locations":  []map[string]any{{"path": "f.go", "line": 5}},
		}), "")
		tc, ok := gk_vibekit_u10_lastUpdate(t, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		// len==1: `1>0` true -> assigned. The `<=` (negation) mutant skips,
		// leaving Locations nil.
		if len(tc.Locations) != 1 || tc.Locations[0].Path != "f.go" {
			t.Errorf("ToolCall.Locations = %+v, want one location path=f.go", tc.Locations)
		}
	})
	t.Run("EmptyLocationsNotAssigned", func(t *testing.T) {
		tr, _, events, chatID := gk_vibekit_u10_primeToolCall(t)
		tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"locations":  []map[string]any{}, // "[]" decodes to a non-nil empty slice
		}), "")
		tc, ok := gk_vibekit_u10_lastUpdate(t, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		// len==0: `0>0` false -> NOT assigned -> Locations stays nil. The
		// `>=` (boundary) mutant assigns the non-nil empty slice, and the
		// `<=` (negation) mutant assigns too. Either makes Locations non-nil.
		if tc.Locations != nil {
			t.Errorf("ToolCall.Locations = %+v (len %d), want nil (empty locations must not be assigned)", tc.Locations, len(tc.Locations))
		}
	})
}

// 109:16  (`if len(diffs) > 0` in HandleToolCallUpdate)  BOUNDARY
func TestGk_vibekit_u10_ToolCallUpdate_NoDiffSkipsLineTracker(t *testing.T) {
	tr, rec, events, chatID := gk_vibekit_u10_primeToolCall(t)
	tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
	}), "")
	if _, ok := gk_vibekit_u10_lastUpdate(t, events); !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	// len(diffs)==0: `0>0` false -> RecordFromDiffs NOT called. The `>=`
	// (boundary) mutant enters the block and calls RecordFromDiffs.
	if rec.calls != 0 {
		t.Errorf("update without diffs: RecordFromDiffs calls = %d, want 0", rec.calls)
	}
}

// 92:16 BOUNDARY  (`if !ok || idx >= len(buf.ToolCalls)`)
func TestGk_vibekit_u10_ToolCallUpdate_IndexBoundaryGuard(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp", WithIDGenerator(func() string { return "gk-vibekit-u10-mid" }))
	chatID := api.ChatID("c1")
	tr.HandleToolCall(context.Background(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"title":      "readFile",
		"kind":       "read",
		"status":     "pending",
	}), "")
	buf := deps.bufStore.GetOrInit(chatID)
	// Force idx == len(ToolCalls): the boundary guard `idx >= len` must
	// return early. The `idx > len` mutant would index buf.ToolCalls[len]
	// (out of range) and panic.
	buf.ToolCallIndex["tc-1"] = len(buf.ToolCalls)
	*events = nil
	tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
	}), "")
	for _, e := range *events {
		if e.Type == api.EventToolCallUpdate {
			t.Error("idx==len: tool_call_update emitted, want early return (no event)")
		}
	}
}
