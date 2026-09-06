package translate

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// lineRec is a recording LineRecorder capturing RecordFromDiffs
// invocations so the diff gates in HandleToolCall / HandleToolCallUpdate
// are observable.
type lineRec struct {
	lastDiffs []vibekit.ToolDiff
	calls     int
	// lastTurn is the turn number the tracker was handed. Recorded because it
	// is the tracker's eviction key and the number the editor's gutter groups
	// by: a call that records the right ranges under the wrong turn is a
	// silent corruption the diff assertions cannot see.
	lastTurn int
}

func (r *lineRec) RecordFromDiffs(_ vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, _ string) {
	r.calls++
	r.lastDiffs = diffs
	r.lastTurn = turn
}

// lineDeps wraps baseDeps and records the line-tracking calls. It overrides
// RecordFromDiffs directly; it used to override a LineTracker() getter, and that
// getter is gone with the role composites.
type lineDeps struct {
	*baseDeps
	rec *lineRec
	// primed is the tool call primeToolCall created, kept because it clears the
	// event stream afterwards: the delta oracle needs the value the deltas fold
	// ONTO, and the create frame that carried it is gone by then.
	primed vibekit.ToolCall
}

func (d *lineDeps) RecordFromDiffs(chatID vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, kind string) {
	d.rec.RecordFromDiffs(chatID, diffs, turn, kind)
}

// workDirDeps wraps baseDeps and overrides WorkDir for relPath tests.
type workDirDeps struct {
	*baseDeps
	workDir string
}

func (d *workDirDeps) WorkDir() string { return d.workDir }

// hookStatusDeps wraps baseDeps and overrides IsHookStatusEnabled so the
// hook-ask suppression path is exercisable in both states (baseDeps hard-
// codes false).
type hookStatusDeps struct {
	*baseDeps
	enabled bool
}

func (d *hookStatusDeps) IsHookStatusEnabled() bool { return d.enabled }

var (
	_ LineRecorder = (*lineRec)(nil)
	_ hostDouble   = (*lineDeps)(nil)
	_ hostDouble   = (*workDirDeps)(nil)
	_ hostDouble   = (*hookStatusDeps)(nil)
)

// newLineCaptureDeps builds an event-capturing baseDeps with a recording
// LineTracker spliced in.
func newLineCaptureDeps() (*lineDeps, *lineRec, *[]vibekit.ServerEvent) {
	base, events := newEventCaptureDeps()
	rec := &lineRec{}
	return &lineDeps{baseDeps: base, rec: rec}, rec, events
}

// primeToolCall registers one in-flight tool call "tc-1" on an event-capturing
// translator, then clears the captured events so the next update is seen in isolation.
func primeToolCall(t *testing.T) (*Translator, *lineRec, *lineDeps, *[]vibekit.ServerEvent, vibekit.ChatID) {
	t.Helper()
	deps, rec, events := newLineCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "tc-mid" }))
	chatID := vibekit.ChatID("c1")
	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"title":      "readFile",
		"kind":       "read",
		"status":     "pending",
	}), FrameAttribution{})
	stashCreatedThenClear(t, deps, events)
	rec.calls = 0
	rec.lastDiffs = nil
	rec.lastTurn = 0
	return tr, rec, deps, events, chatID
}

// stashCreatedThenClear records the tool call the create frames built and then
// empties the stream, so a following update is observed in isolation while the
// delta oracle still knows the value those deltas fold onto.
func stashCreatedThenClear(t *testing.T, deps *lineDeps, events *[]vibekit.ServerEvent) {
	t.Helper()
	deps.primed, _ = foldToolCallUpdates(t, vibekit.ToolCall{}, events)
	*events = nil
}

// lastToolCallUpdate folds every tool_call_update DELTA onto the tool call the buffer
// started from and returns the reconstructed whole, because no single event carries it.
// The fold is also the ORACLE for the delta shape — it is toolCallDelta's inverse, and
// the cross-check against the buffer's own accumulated value is what keeps it from
// being the emitter's rules agreeing with themselves.
func lastToolCallUpdate(t *testing.T, deps *lineDeps, events *[]vibekit.ServerEvent) (vibekit.ToolCall, bool) {
	t.Helper()
	// Seeded from the create primeToolCall consumed. A create frame still in the
	// stream overrides it, so a test that primes its own call needs no seed.
	folded, ok := foldToolCallUpdates(t, deps.primed, events)
	if !ok {
		return vibekit.ToolCall{}, false
	}
	held, _, found := deps.bufStore.GetOrInit("c1").ToolCall(folded.ID)
	if !found {
		t.Fatalf("the buffer holds no tool call %q, so the delta stream cannot be checked", folded.ID)
	}
	if !reflect.DeepEqual(folded, held) {
		t.Fatalf("the delta stream reconstructs\n  %+v\nbut the buffer holds\n  %+v\n"+
			"— a field the fold changed is missing from the wire", folded, held)
	}
	return folded, true
}

// foldToolCallUpdates replays the delta stream: the create frame's whole ToolCall
// plus every later delta for that id, in order.
func foldToolCallUpdates(t *testing.T, seed vibekit.ToolCall, events *[]vibekit.ServerEvent) (vibekit.ToolCall, bool) {
	t.Helper()
	out := seed
	sawUpdate := false
	for _, e := range *events {
		switch p := e.Payload.(type) {
		case vibekit.ToolCallPayload:
			out = p.ToolCall
		case vibekit.ToolCallUpdatePayload:
			sawUpdate = true
			applyToolCallDelta(&out, p)
		}
	}
	return out, sawUpdate
}

// applyToolCallDelta is the client's fold, in Go: the inverse of toolCallDelta.
// An absent field means unchanged.
func applyToolCallDelta(tc *vibekit.ToolCall, d vibekit.ToolCallUpdatePayload) {
	tc.ID = d.ToolCallID
	if d.Title != "" {
		tc.Title = d.Title
	}
	if d.Kind != "" {
		tc.Kind = d.Kind
	}
	if d.Status != "" {
		tc.Status = d.Status
	}
	switch {
	case d.OutputReplace:
		tc.Output = d.OutputDelta
	case d.OutputDelta != "":
		tc.Output += d.OutputDelta
	}
	if d.OutputSpans != nil {
		tc.OutputSpans = d.OutputSpans
	}
	if len(d.DiffsAppended) > 0 {
		tc.Diffs = append(tc.Diffs, d.DiffsAppended...)
	}
	if d.Locations != nil {
		tc.Locations = d.Locations
	}
	if d.DurationMs != 0 {
		tc.DurationMs = d.DurationMs
	}
	if d.TerminalID != "" {
		tc.TerminalID = d.TerminalID
	}
	if d.SubSessionID != "" {
		tc.SubSessionID = d.SubSessionID
	}
	if d.AgentSubtaskID != "" {
		tc.AgentSubtaskID = d.AgentSubtaskID
	}
	if d.WorkflowID != "" {
		tc.WorkflowID = d.WorkflowID
	}
	if d.Checkpoint != nil {
		tc.Checkpoint = d.Checkpoint
	}
	if d.Disclosed != nil {
		tc.Disclosed = d.Disclosed
	}
	if d.Denial != nil {
		tc.Denial = d.Denial
	}
}

func hasWorkingLabel(events *[]vibekit.ServerEvent) bool {
	for _, e := range *events {
		if e.Type == vibekit.EventWorkingLabel {
			return true
		}
	}
	return false
}

// hasToolCallEvent reports whether any tool_call event was broadcast.
func hasToolCallEvent(events *[]vibekit.ServerEvent) bool {
	for _, e := range *events {
		if e.Type == vibekit.EventToolCall {
			return true
		}
	}
	return false
}

// TestHandleToolCall_HookAskSuppression: a pre-tool-use hook's ask-permission gate
// arrives as a kind:"other" call tagged _meta.kiro.hookAsk, because v3's zToolKind has
// no "hook". Its card follows hooks.showStatus; a normal call never does.
func TestHandleToolCall_HookAskSuppression(t *testing.T) {
	hookAsk := map[string]any{
		"toolCallId": "hook-ask-1",
		"title":      "Run hook",
		"kind":       "other",
		"status":     "pending",
		"_meta": map[string]any{"kiro": map[string]any{
			"hookAsk": map[string]any{"kind": "pre-tool-use", "toolName": "fs_write", "reason": "guard"},
		}},
	}

	t.Run("SuppressedWhenStatusDisabled", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &hookStatusDeps{baseDeps: base, enabled: false}
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		chatID := vibekit.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, hookAsk), FrameAttribution{})
		if hasToolCallEvent(events) {
			t.Error("hook-ask tool call broadcast a tool_call event; want suppressed (hooks.showStatus off)")
		}
		if n := len(base.bufStore.GetOrInit(chatID).ToolCalls); n != 0 {
			t.Errorf("buffered tool calls = %d, want 0 (hook-ask must not be buffered)", n)
		}
	})

	t.Run("ShownWhenStatusEnabled", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &hookStatusDeps{baseDeps: base, enabled: true}
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), vibekit.ChatID("c1"), mustJSON(t, hookAsk), FrameAttribution{})
		if !hasToolCallEvent(events) {
			t.Error("hook-ask tool call suppressed while hooks.showStatus on; want shown")
		}
	})

	t.Run("NonHookAskShownWhenStatusDisabled", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &hookStatusDeps{baseDeps: base, enabled: false}
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), vibekit.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
		}), FrameAttribution{})
		if !hasToolCallEvent(events) {
			t.Error("normal tool call suppressed with hooks.showStatus off; want shown (only hook-ask cards are gated)")
		}
	})
}

// TestHandleToolCall_DiffGate pins that HandleToolCall records line
// changes through the LineTracker only when the call carries at least
// one diff; a diff-free call never touches the tracker.
func TestHandleToolCall_DiffGate(t *testing.T) {
	t.Run("WithDiffRecordsLineChanges", func(t *testing.T) {
		deps, rec, _ := newLineCaptureDeps()
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), vibekit.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-diff",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "x.go", "oldText": "a", "newText": "b"},
			},
		}), FrameAttribution{})
		if rec.calls != 1 {
			t.Errorf("with diff: RecordFromDiffs calls = %d, want 1", rec.calls)
		}
		// The first tool call of a chat is turn 1, not turn 0 and not a negative:
		// the number is the tracker's eviction key and what the editor's
		// changed-line gutter groups by, so an off-by-one files every range
		// under a turn no card claims.
		if rec.lastTurn != 1 {
			t.Errorf("with diff: RecordFromDiffs turn = %d, want 1 (the chat's first tool call)", rec.lastTurn)
		}
		// A second diffed call in the same chat advances to 2, which is what
		// makes the number a turn rather than a constant.
		tr.HandleToolCall(t.Context(), vibekit.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-diff-2",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "y.go", "oldText": "a", "newText": "b"},
			},
		}), FrameAttribution{})
		if rec.lastTurn != 2 {
			t.Errorf("second diffed call: RecordFromDiffs turn = %d, want 2", rec.lastTurn)
		}
	})
	t.Run("WithoutDiffSkipsLineTracker", func(t *testing.T) {
		deps, rec, _ := newLineCaptureDeps()
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), vibekit.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-nodiff",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
		}), FrameAttribution{})
		if rec.calls != 0 {
			t.Errorf("without diff: RecordFromDiffs calls = %d, want 0", rec.calls)
		}
	})
}

// TestToolCallUpdate_StatusApplied pins that a non-empty status in an
// update overwrites the in-flight tool call's status.
func TestToolCallUpdate_StatusApplied(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
	}), FrameAttribution{})
	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if tc.Status != vibekit.ToolCompleted {
		t.Errorf("ToolCall.Status = %q, want %q (non-empty status must be applied)", tc.Status, vibekit.ToolCompleted)
	}
}

// TestToolCallUpdate_TerminalStatusEmitsWorkingLabel pins that reaching
// a terminal status (completed or failed) broadcasts a working_label
// (Thinking) event.
func TestToolCallUpdate_TerminalStatusEmitsWorkingLabel(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "Completed", status: "completed"},
		{name: "Failed", status: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, _, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     tt.status,
			}), FrameAttribution{})
			if !hasWorkingLabel(events) {
				t.Errorf("status=%q: no working_label event emitted, want one (terminal status must emit it)", tt.status)
			}
		})
	}
}

// TestToolCallUpdate_OutputAppendedWhenContentPresent pins that content
// text in an update is sanitized, newline-terminated, and appended to
// the tool call's Output.
func TestToolCallUpdate_OutputAppendedWhenContentPresent(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"text": "hello"}},
		},
	}), FrameAttribution{})
	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if tc.Output != "hello\n" {
		t.Errorf("ToolCall.Output = %q, want %q (output must be appended when content present)", tc.Output, "hello\n")
	}
}

// TestToolCallUpdate_LocationsGate pins that Locations are replaced only
// when the update carries a non-empty list; an empty list leaves the
// existing Locations (nil here) untouched.
func TestToolCallUpdate_LocationsGate(t *testing.T) {
	t.Run("LocationsSetWhenPresent", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"locations":  []map[string]any{{"path": "f.go", "line": 5}},
		}), FrameAttribution{})
		tc, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if len(tc.Locations) != 1 || tc.Locations[0].Path != "f.go" {
			t.Errorf("ToolCall.Locations = %+v, want one location path=f.go", tc.Locations)
		}
	})
	t.Run("EmptyLocationsNotAssigned", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"locations":  []map[string]any{}, // decodes to a non-nil empty slice
		}), FrameAttribution{})
		tc, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if tc.Locations != nil {
			t.Errorf("ToolCall.Locations = %+v (len %d), want nil (empty locations must not be assigned)", tc.Locations, len(tc.Locations))
		}
	})
}

// TestToolCallUpdate_NoDiffSkipsLineTracker pins that an update carrying
// no diffs does not invoke the LineTracker.
func TestToolCallUpdate_NoDiffSkipsLineTracker(t *testing.T) {
	tr, rec, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
	}), FrameAttribution{})
	if _, ok := lastToolCallUpdate(t, deps, events); !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if rec.calls != 0 {
		t.Errorf("update without diffs: RecordFromDiffs calls = %d, want 0", rec.calls)
	}
}

// TestToolCallUpdate_IndexBoundaryGuard pins that an index equal to
// len(ToolCalls) is treated as out of range: the update returns early
// and broadcasts nothing, guarding against an out-of-bounds index.
func TestToolCallUpdate_IndexBoundaryGuard(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	buf := deps.bufStore.GetOrInit(chatID)
	buf.ToolCallIndex["tc-1"] = len(buf.ToolCalls)
	*events = nil
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
	}), FrameAttribution{})
	for _, e := range *events {
		if e.Type == vibekit.EventToolCallUpdate {
			t.Error("idx==len: tool_call_update emitted, want early return (no event)")
		}
	}
}

// TestToolCallUpdate_DiffPresentRecordsAndAppends pins the ledger gate: a diff
// always lands on the card, and only a `completed` tool feeds the changed-file
// ledger and the line tracker. Dropping the status check makes the in-progress
// case record, which is what counted a streaming write's partial diffs.
func TestToolCallUpdate_DiffPresentRecordsAndAppends(t *testing.T) {
	t.Run("InProgressAppendsWithoutRecording", func(t *testing.T) {
		tr, rec, deps, _, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"content": []map[string]any{
				{"type": "diff", "path": "a.go", "oldText": "x", "newText": "y\n"},
			},
		}), FrameAttribution{})
		buf := deps.bufStore.GetOrInit(chatID)
		idx := buf.ToolCallIndex["tc-1"]
		if got := len(buf.ToolCalls[idx].Diffs); got != 1 {
			t.Fatalf("in_progress: ToolCalls[%d].Diffs len = %d, want 1 (diff must be appended when present)", idx, got)
		}
		if got := buf.ToolCalls[idx].Diffs[0].Path; got != "a.go" {
			t.Errorf("in_progress: Diffs[0].Path = %q, want %q", got, "a.go")
		}
		if rec.calls != 0 {
			t.Errorf("in_progress: RecordFromDiffs calls = %d, want 0 (only a completed tool changed a file)", rec.calls)
		}
		if _, ok := buf.ChangedFiles["a.go"]; ok {
			t.Error("in_progress: ChangedFiles[a.go] present, want absent (nothing has reached disk yet)")
		}
	})
	t.Run("CompletedRecordsOnce", func(t *testing.T) {
		tr, rec, deps, _, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"content": []map[string]any{
				{"type": "diff", "path": "a.go", "oldText": "x\n", "newText": "y\n"},
			},
		}), FrameAttribution{})
		buf := deps.bufStore.GetOrInit(chatID)
		idx := buf.ToolCallIndex["tc-1"]
		if got := len(buf.ToolCalls[idx].Diffs); got != 1 {
			t.Fatalf("completed: ToolCalls[%d].Diffs len = %d, want 1", idx, got)
		}
		if rec.calls != 1 {
			t.Errorf("completed: RecordFromDiffs calls = %d, want 1 (must record when the tool succeeded)", rec.calls)
		}
		if got := len(rec.lastDiffs); got != 1 {
			t.Errorf("completed: RecordFromDiffs received %d diffs, want 1", got)
		}
		fc, ok := buf.ChangedFiles["a.go"]
		if !ok {
			t.Fatal("completed: ChangedFiles[a.go] missing; the diff was not tracked")
		}
		if fc.LinesAdded != 1 {
			t.Errorf("completed: LinesAdded = %d, want 1 (counted once, not once per frame)", fc.LinesAdded)
		}
	})
	t.Run("FailedDoesNotEnterTheLedger", func(t *testing.T) {
		// The write tool's catch emits a diff with a real path and the text it meant
		// to write, so a failed write claimed a file changed when nothing did.
		tr, rec, deps, _, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "failed",
			"content": []map[string]any{
				{"type": "diff", "path": "locked.go", "oldText": "", "newText": "package x\n"},
			},
		}), FrameAttribution{})
		buf := deps.bufStore.GetOrInit(chatID)
		idx := buf.ToolCallIndex["tc-1"]
		if got := len(buf.ToolCalls[idx].Diffs); got != 1 {
			t.Fatalf("failed: ToolCalls[%d].Diffs len = %d, want 1 (the card still shows what it tried)", idx, got)
		}
		if rec.calls != 0 {
			t.Errorf("failed: RecordFromDiffs calls = %d, want 0", rec.calls)
		}
		if _, ok := buf.ChangedFiles["locked.go"]; ok {
			t.Error("failed: ChangedFiles[locked.go] present, want absent (the write failed)")
		}
	})
}

// TestToolCallUpdate_SubSessionGate pins that SubSessionID is set from
// the incoming value only when the stored one is empty; a non-empty
// stored SubSessionID is never overwritten.
func TestToolCallUpdate_SubSessionGate(t *testing.T) {
	t.Run("SetWhenEmptyAndIncomingNonEmpty", func(t *testing.T) {
		tr, _, deps, _, chatID := primeToolCall(t)
		buf := deps.bufStore.GetOrInit(chatID)
		idx := buf.ToolCallIndex["tc-1"]
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
		}), FrameAttribution{SubSessionID: "sub-9"})
		if got := buf.ToolCalls[idx].SubSessionID; got != "sub-9" {
			t.Errorf("SubSessionID = %q, want %q (empty existing + non-empty incoming must set it)", got, "sub-9")
		}
	})
	t.Run("NotOverwrittenWhenExistingNonEmpty", func(t *testing.T) {
		tr, _, deps, _, chatID := primeToolCall(t)
		buf := deps.bufStore.GetOrInit(chatID)
		idx := buf.ToolCallIndex["tc-1"]
		buf.ToolCalls[idx].SubSessionID = "existing"
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
		}), FrameAttribution{SubSessionID: "sub-9"})
		if got := buf.ToolCalls[idx].SubSessionID; got != "existing" {
			t.Errorf("SubSessionID = %q, want %q (non-empty existing must not be overwritten)", got, "existing")
		}
	})
}

// TestRelPath pins relPath's workspace-root stripping: paths inside the
// workdir become workdir-relative slash paths, while an empty workdir or
// a path that escapes the root returns the absolute path unchanged.
func TestRelPath(t *testing.T) {
	tests := []struct {
		name    string
		workDir string
		abs     string
		want    string
	}{
		{name: "StripsRootPrefix", workDir: "/work", abs: "/work/sub/file.go", want: "sub/file.go"},
		{name: "OutsideWorkDirReturnsAbs", workDir: "/work", abs: "/elsewhere/x.go", want: "/elsewhere/x.go"},
		{name: "EmptyWorkDirReturnsAbs", workDir: "", abs: "/a/b.go", want: "/a/b.go"},
		// A first component that merely BEGINS with two dots is a directory
		// name, not a traversal: the escape test is separator-precise
		// (pathinside.RelEscapes), so this stays relative instead of leaking
		// the absolute path to the client.
		{name: "DotDotPrefixedDirIsRelative", workDir: "/work", abs: "/work/..drafts/x.go", want: "..drafts/x.go"},
		{name: "ParentEscapeReturnsAbs", workDir: "/work", abs: "/x.go", want: "/x.go"},
		// KAS sends some tool-call paths as file:// URIs, and every consumer treats
		// the value as a path, so the URI must be gone by the time it leaves here.
		// filepath.Clean turns "file:///work/x.go" into the RELATIVE "file:/work/x.go",
		// which makes filepath.Rel error and passes the raw URI through to consumers.
		{name: "FileURIBecomesRelative", workDir: "/work", abs: "file:///work/sub/file.go", want: "sub/file.go"},
		{name: "FileURIIsPercentDecoded", workDir: "/work", abs: "file:///work/hello%20world.sh", want: "hello world.sh"},
		// Normalising FIRST is what keeps the outside-the-workspace branch from
		// returning the spelling this function exists to remove.
		{name: "FileURIOutsideWorkDirIsStillAPath", workDir: "/work", abs: "file:///elsewhere/x.go", want: "/elsewhere/x.go"},
		{name: "FileURIWithEmptyWorkDirIsStillAPath", workDir: "", abs: "file:///a/b.go", want: "/a/b.go"},
		{name: "LocalhostAuthorityIsAccepted", workDir: "/work", abs: "file://localhost/work/x.go", want: "x.go"},
		// A remote authority names a file this process cannot open, so it is
		// left alone rather than rewritten into a local path that would then be
		// resolved against the local filesystem.
		{name: "RemoteAuthorityIsLeftAlone", workDir: "/work", abs: "file://host/share/x.go", want: "file://host/share/x.go"},
		{name: "NonFileSchemeIsLeftAlone", workDir: "/work", abs: "https://example.com/x.go", want: "https://example.com/x.go"},
		// A filename may legitimately contain "://", which trips the cheap gate but
		// parses to NO scheme, so it comes back through as a path; the duplicate
		// slashes collapse because filepath.Clean does that to every path here.
		{name: "PathContainingSchemeSeparator", workDir: "/work", abs: "/work/weird:///name.go", want: "weird:/name.go"},
		// An unparseable reference is returned as-is rather than mangled.
		{name: "MalformedURIIsLeftAlone", workDir: "/work", abs: "file://%zz/x.go", want: "file://%zz/x.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &workDirDeps{baseDeps: newBaseDeps(), workDir: tt.workDir}
			tr := New(rolesOf(deps), withIDGenerator(func() string { return "mid" }))
			if got := tr.relPath(tt.abs); got != tt.want {
				t.Errorf("relPath(%q) [workDir=%q] = %q, want %q", tt.abs, tt.workDir, got, tt.want)
			}
		})
	}
}

// TestHandleToolCall_IsNewFileFlag pins the isNew computation feeding TrackFileChanges:
// a call counts as a new-file creation only when it is BOTH an edit kind AND pending,
// observable on buf.ChangedFiles[path].IsNewFile.
func TestHandleToolCall_IsNewFileFlag(t *testing.T) {
	t.Run("PendingEditMarksNewFile", func(t *testing.T) {
		deps, _, _ := newLineCaptureDeps()
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		chatID := vibekit.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-new",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "new.go", "oldText": "", "newText": "package x\n"},
			},
		}), FrameAttribution{})
		buf := deps.bufStore.GetOrInit(chatID)
		fc, ok := buf.ChangedFiles["new.go"]
		if !ok {
			t.Fatal("ChangedFiles[new.go] missing; the diff was not tracked")
		}
		if !fc.IsNewFile {
			t.Errorf("IsNewFile = false, want true (a pending edit must be marked a new-file creation)")
		}
	})
	t.Run("CompletedEditIsNotNewFile", func(t *testing.T) {
		deps, _, _ := newLineCaptureDeps()
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
		chatID := vibekit.ChatID("c2")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-existing",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "completed",
			"content": []map[string]any{
				{"type": "diff", "path": "existing.go", "oldText": "a\n", "newText": "b\n"},
			},
		}), FrameAttribution{})
		buf := deps.bufStore.GetOrInit(chatID)
		fc, ok := buf.ChangedFiles["existing.go"]
		if !ok {
			t.Fatal("ChangedFiles[existing.go] missing; the diff was not tracked")
		}
		if fc.IsNewFile {
			t.Errorf("IsNewFile = true, want false (a completed edit is not a new-file creation)")
		}
	})
}

// --- _meta.kiro.checkpoint: KAS's snapshot mapping ---

// TestToolCallUpdate_CheckpointFromWire drives two shapes a real kiro-cli emits through
// the actual JSON decode, so the `_meta.kiro.checkpoint` NESTING is pinned and not just
// the merge logic: a misplaced struct tag compiles cleanly, yields nothing, and the
// symptom is "Rewind shows no diff" with nothing in any log. The create case is why the
// table exists — KAS sends NO `original` for a file it just created.
func TestToolCallUpdate_CheckpointFromWire(t *testing.T) {
	const (
		origURI = "kiro-snapshot-v2://sess_51d58124:5c1bae6d/?originalPath%3Dexisting.txt"
		modURI  = "kiro-snapshot-v2://sess_51d58124:952e8e1f/?originalPath%3Dexisting.txt"
		local   = "file:///tmp/ws/existing.txt"
	)
	tests := []struct {
		name       string
		checkpoint map[string]any
		want       vibekit.ToolCheckpoint
	}{
		{
			name:       "overwriting an existing file carries all three",
			checkpoint: map[string]any{"original": origURI, "modified": modURI, "local": local},
			want:       vibekit.ToolCheckpoint{Original: origURI, Modified: modURI, Local: local},
		},
		{
			name:       "creating a file has no pre-image",
			checkpoint: map[string]any{"modified": modURI, "local": local},
			want:       vibekit.ToolCheckpoint{Modified: modURI, Local: local},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, deps, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "completed",
				"_meta":      map[string]any{"kiro": map[string]any{"checkpoint": tt.checkpoint}},
			}), FrameAttribution{})
			tc, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_call_update event emitted")
			}
			if tc.Checkpoint == nil {
				t.Fatalf("ToolCall.Checkpoint = nil, want %+v (is the _meta.kiro.checkpoint tag path right?)", tt.want)
			}
			if *tc.Checkpoint != tt.want {
				t.Errorf("ToolCall.Checkpoint = %+v, want %+v", *tc.Checkpoint, tt.want)
			}
		})
	}
}

// TestToolCallUpdate_CheckpointMergeIsPerField pins that a later frame with a narrower
// key set cannot erase a value an earlier one supplied. The key set genuinely varies
// frame to frame for one tool call, so a wholesale struct replacement drops `original`
// and takes the pre-image — the only thing a diff needs — with it.
func TestToolCallUpdate_CheckpointMergeIsPerField(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	send := func(cp map[string]any) {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"_meta":      map[string]any{"kiro": map[string]any{"checkpoint": cp}},
		}), FrameAttribution{})
	}
	send(map[string]any{"original": "orig-uri", "modified": "mod-uri", "local": "local-uri"})
	send(map[string]any{"modified": "mod-uri-2"})

	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	want := vibekit.ToolCheckpoint{Original: "orig-uri", Modified: "mod-uri-2", Local: "local-uri"}
	if tc.Checkpoint == nil || *tc.Checkpoint != want {
		t.Errorf("ToolCall.Checkpoint = %+v, want %+v (a narrower frame must refine, not replace)", tc.Checkpoint, want)
	}
}

// TestToolCallUpdate_CheckpointAbsentStaysNil pins that a tool call which
// touched no file grows no checkpoint. ~95% of tool calls are in this case,
// so allocating an empty struct here would put a useless `"checkpoint":{}`
// on almost every tool call in every chat file on disk.
func TestToolCallUpdate_CheckpointAbsentStaysNil(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
	}{
		{name: "no _meta at all", meta: nil},
		{name: "kiro meta without a checkpoint", meta: map[string]any{"kiro": map[string]any{"kind": "other"}}},
		{name: "an explicitly empty checkpoint", meta: map[string]any{"kiro": map[string]any{"checkpoint": map[string]any{}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, deps, events, chatID := primeToolCall(t)
			frame := map[string]any{"toolCallId": "tc-1", "status": "completed"}
			if tt.meta != nil {
				frame["_meta"] = tt.meta
			}
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, frame), FrameAttribution{})
			tc, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_call_update event emitted")
			}
			if tc.Checkpoint != nil {
				t.Errorf("ToolCall.Checkpoint = %+v, want nil (no file was written)", *tc.Checkpoint)
			}
		})
	}
}

// A mid-flight update may refine the card's title and kind, and KAS sends both
// nullish on most updates. So a value the update carries is applied and a value
// it omits keeps what the initial tool_call set — treating absence as an
// instruction blanks the label of a card the user is watching.
func TestToolCallUpdate_TitleAndKindAppliedOnlyWhenPresent(t *testing.T) {
	tests := []struct {
		name      string
		update    map[string]any
		wantTitle string
		wantKind  vibekit.ToolKind
	}{
		{
			name:      "the_update_refines_both",
			update:    map[string]any{"title": "readFile(config.yaml)", "kind": "edit"},
			wantTitle: "readFile(config.yaml)",
			wantKind:  vibekit.ToolKind("edit"),
		},
		{
			name:      "the_update_omits_both",
			update:    map[string]any{},
			wantTitle: "readFile",
			wantKind:  vibekit.ToolKind("read"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr, _, deps, events, chatID := primeToolCall(t)
			update := map[string]any{"toolCallId": "tc-1", "status": "completed"}
			maps.Copy(update, tc.update)

			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, update), FrameAttribution{})

			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_call_update event emitted")
			}
			if got.Title != tc.wantTitle {
				t.Errorf("ToolCall.Title after an update %v = %q, want %q", tc.update, got.Title, tc.wantTitle)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("ToolCall.Kind after an update %v = %q, want %q", tc.update, got.Kind, tc.wantKind)
			}
		})
	}
}

// A subtask attribution can arrive on an UPDATE, so it is adopted late — into an empty
// slot only, and workflow identity outranks the plain id. All three rules keep a step's
// card under the step: adopting over a held value re-parents it mid-flight, refusing to
// adopt leaves it in the parent's block, and the plain id files it under a uuid the
// workflow view cannot address.
func TestToolCallUpdate_SubtaskAdoptedLateIntoAnEmptySlot(t *testing.T) {
	workflowMeta := map[string]any{
		"workflow": map[string]any{"workflowId": "wf_1", "nodeId": "build"},
	}

	t.Run("workflow_identity_wins_over_the_plain_id", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)
		meta := map[string]any{"agentSubtaskId": "uuid-plain"}
		maps.Copy(meta, workflowMeta)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta":      map[string]any{"kiro": meta},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if got.AgentSubtaskID != "wf:wf_1:build" {
			t.Errorf("ToolCall.AgentSubtaskID after an update carrying both ids = %q, want %q",
				got.AgentSubtaskID, "wf:wf_1:build")
		}
	})

	t.Run("the_plain_id_is_adopted_when_no_workflow_rides_along", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta":      map[string]any{"kiro": map[string]any{"agentSubtaskId": "uuid-plain"}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if got.AgentSubtaskID != "uuid-plain" {
			t.Errorf("ToolCall.AgentSubtaskID after an update carrying only the plain id = %q, want %q",
				got.AgentSubtaskID, "uuid-plain")
		}
	})

	t.Run("a_held_id_survives_a_later_frame", func(t *testing.T) {
		deps, _, events := newLineCaptureDeps()
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "tc-mid" }))
		chatID := vibekit.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
			"_meta":      map[string]any{"kiro": map[string]any{"agentSubtaskId": "uuid-first"}},
		}), FrameAttribution{})
		stashCreatedThenClear(t, deps, events)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta":      map[string]any{"kiro": map[string]any{"agentSubtaskId": "uuid-second"}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if got.AgentSubtaskID != "uuid-first" {
			t.Errorf("ToolCall.AgentSubtaskID after a second id arrived = %q, want %q (late adoption never overwrites)",
				got.AgentSubtaskID, "uuid-first")
		}
	})
}

// TestToolCallUpdate_WorkflowIDFromRawOutput pins the one field this client reads out of
// `rawOutput`, which KAS types as `unknown`. `run_workflow` reports the id of the run it
// created there, and it is the ONLY structural link from the invocation to its run, so
// without it the transcript cannot render a run's steps inside the call that launched
// them. Every case is a shape KAS really sends, and none may panic or contaminate it.
func TestToolCallUpdate_WorkflowIDFromRawOutput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  any
		want string
	}{
		{"run_workflow's own shape", map[string]any{
			"message":    "Workflow 'wf_9' started successfully. Status: running.",
			"workflowId": "wf_9",
			"status":     "running",
		}, "wf_9"},
		{"an object carrying no id", map[string]any{"message": "done"}, ""},
		{"a bare string, which most tools send", "some output", ""},
		{"a number", 42, ""},
		{"an array", []any{"a", "b"}, ""},
		{"null", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			tr, _, deps, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "completed",
				"rawOutput":  c.raw,
			}), FrameAttribution{})

			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_call_update event emitted")
			}
			if got.WorkflowID != c.want {
				t.Errorf("ToolCall.WorkflowID from rawOutput %v = %q, want %q", c.raw, got.WorkflowID, c.want)
			}
		})
	}
}

// TestToolCallUpdate_WorkflowIDIsAdoptedOnce mirrors the late-adoption rule the
// subtask id follows: KAS reports the run on the terminal update, and no later
// frame for the same call can name a different run.
func TestToolCallUpdate_WorkflowIDIsAdoptedOnce(t *testing.T) {
	t.Parallel()
	tr, _, deps, events, chatID := primeToolCall(t)

	for _, id := range []string{"wf_first", "wf_second"} {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"rawOutput":  map[string]any{"workflowId": id},
		}), FrameAttribution{})
	}

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if got.WorkflowID != "wf_first" {
		t.Errorf("ToolCall.WorkflowID after a second id arrived = %q, want %q", got.WorkflowID, "wf_first")
	}
}

// The message KAS's write tool throws when a remote-$schema JSON write is refused
// outside Autopilot. Verbatim from the 2.20.1 bundle, because the reason reaching
// the card unaltered is the property under test.
const remoteJSONSchemaReason = "Cannot use this tool to write a Remote JSON Schema in Supervised mode. " +
	"Switch to Autopilot mode to allow this write."

// TestHandleToolCallUpdate_FailedTakesReasonFromRawOutput drives the frame the guard
// really produces: status failed, the reason as a bare JSON string in rawOutput, and a
// diff block with an empty path because the throw beat resolveFile. KAS's edit arm puts
// the reason in no content block, so rawOutput is the only channel it travels on.
func TestHandleToolCallUpdate_FailedTakesReasonFromRawOutput(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "failed",
		"rawOutput":  remoteJSONSchemaReason,
		"content": []map[string]any{
			{"type": "diff", "path": "", "newText": "{\"$schema\":\"https://example.test/s.json\"}\n"},
		},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if got.Output != remoteJSONSchemaReason {
		t.Errorf("ToolCall.Output on a failed edit = %q, want the reason %q", got.Output, remoteJSONSchemaReason)
	}
	// The same frame pins that a path-less diff still contributes nothing: it is
	// the whole content set here, so if it rendered the card would claim a change.
	if len(got.Diffs) != 0 {
		t.Errorf("ToolCall.Diffs = %+v, want none (a diff with no path names no file)", got.Diffs)
	}
	buf := deps.bufStore.GetOrInit(chatID)
	if len(buf.ChangedFiles) != 0 {
		t.Errorf("ChangedFiles = %v, want empty (the write threw before it reached a path)", buf.ChangedFiles)
	}
}

// TestHandleToolCallUpdate_FailedKeepsExistingOutput pins the `Output == ""` half
// of the gate. A failed execute tool has printed its own output, and that is what
// the reader needs; dropping the guard would append KAS's error text to it or
// overwrite it.
func TestHandleToolCallUpdate_FailedKeepsExistingOutput(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "failed",
		"rawOutput":  "Command exited with status 1",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"type": "text", "text": "FAIL: 2 tests failed"}},
		},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if got.Output != "FAIL: 2 tests failed\n" {
		t.Errorf("ToolCall.Output = %q, want the command's own output alone", got.Output)
	}
}

// TestHandleToolCallUpdate_CompletedIgnoresRawOutput pins the status half of the
// gate, which is what keeps this a failure-reason reader rather than the general
// structured-output channel the content blocks own. `run_workflow` succeeds with
// an OBJECT in rawOutput, so a dropped gate puts its JSON on the card.
func TestHandleToolCallUpdate_CompletedIgnoresRawOutput(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"rawOutput": map[string]any{
			"message":    "Workflow 'wf_9' started successfully. Status: running.",
			"workflowId": "wf_9",
			"status":     "running",
		},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if got.Output != "" {
		t.Errorf("ToolCall.Output on a completed tool = %q, want empty (nothing is read from a success)", got.Output)
	}
	if strings.Contains(got.Output, "workflowId") {
		t.Errorf("ToolCall.Output = %q, want no run_workflow payload in it", got.Output)
	}
}

// TestRawOutputFailureText is the decode on its own: a bare string, then an
// object's error or message, and "" for everything else. The negative rows are
// what keep the read narrow — each is a shape KAS really sends on some tool, and
// none of them may become a card's output.
func TestRawOutputFailureText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"a bare string, which is what the write tool sends", `"lock is held by another process"`, "lock is held by another process"},
		{"an object with error", `{"error":"ENOSPC: no space left on device"}`, "ENOSPC: no space left on device"},
		{"an object with message", `{"message":"policy refused the write"}`, "policy refused the write"},
		{"error wins over message", `{"error":"the cause","message":"the summary"}`, "the cause"},
		{"a whitespace-only string", `"   "`, ""},
		{"an empty string", `""`, ""},
		{"a number", `42`, ""},
		{"an array", `["a","b"]`, ""},
		{"an object with a non-string error", `{"error":{"code":13}}`, ""},
		{"run_workflow's success object", `{"workflowId":"wf_9","status":"running"}`, ""},
		{"malformed json", `{`, ""},
		{"absent", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := rawOutputFailureText(json.RawMessage(c.raw)); got != c.want {
				t.Errorf("rawOutputFailureText(%s) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// A disclosure and a policy denial are both decided when the call is ATTEMPTED,
// so either can arrive on an update rather than the create. Each is adopted
// into an empty slot only: overwriting would let a later frame replace the
// refusal a user is reading with a narrower one.
func TestToolCallUpdate_DisclosureAndDenialAdoptedLate(t *testing.T) {
	t.Run("a_disclosure_on_the_update_is_adopted", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta": map[string]any{"kiro": map[string]any{
				"disclosedContext": map[string]any{
					"type": "skill", "displayName": "deploy-app", "uri": "file:///skills/deploy-app.md",
				},
			}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if got.Disclosed == nil {
			t.Fatalf("ToolCall.Disclosed after an update carrying disclosedContext = nil, want the skill")
		}
		if got.Disclosed.DisplayName != "deploy-app" || got.Disclosed.Type != "skill" {
			t.Errorf("ToolCall.Disclosed = %+v, want type %q name %q", *got.Disclosed, "skill", "deploy-app")
		}
	})

	t.Run("a_denial_on_the_update_is_adopted", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "failed",
			"_meta": map[string]any{"kiro": map[string]any{
				"policyDenial": map[string]any{
					"capability": "fs.write", "resource": "/etc/passwd", "scope": "workspace", "source": "policy.json",
				},
			}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if got.Denial == nil {
			t.Fatalf("ToolCall.Denial after an update carrying policyDenial = nil, want the refusal")
		}
		if got.Denial.Capability != "fs.write" || got.Denial.Resource != "/etc/passwd" {
			t.Errorf("ToolCall.Denial = %+v, want capability %q resource %q", *got.Denial, "fs.write", "/etc/passwd")
		}
	})

	t.Run("values_held_from_the_create_survive_a_later_frame", func(t *testing.T) {
		deps, _, events := newLineCaptureDeps()
		tr := New(rolesOf(deps), withIDGenerator(func() string { return "tc-mid" }))
		chatID := vibekit.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"title":      "disclose_context",
			"kind":       "other",
			"status":     "pending",
			"_meta": map[string]any{"kiro": map[string]any{
				"disclosedContext": map[string]any{"type": "skill", "displayName": "first", "uri": "file:///a.md"},
				"policyDenial":     map[string]any{"capability": "fs.write", "resource": "/first"},
			}},
		}), FrameAttribution{})
		stashCreatedThenClear(t, deps, events)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta": map[string]any{"kiro": map[string]any{
				"disclosedContext": map[string]any{"type": "steering", "displayName": "second", "uri": "file:///b.md"},
				"policyDenial":     map[string]any{"capability": "shell.exec", "resource": "/second"},
			}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if got.Disclosed == nil || got.Disclosed.DisplayName != "first" {
			t.Errorf("ToolCall.Disclosed after a second disclosure arrived = %+v, want the one held from the create (%q)",
				got.Disclosed, "first")
		}
		if got.Denial == nil || got.Denial.Resource != "/first" {
			t.Errorf("ToolCall.Denial after a second denial arrived = %+v, want the one held from the create (%q)",
				got.Denial, "/first")
		}
	})
}

// TestParseToolUpdateContent_UnmodelledType pins the BEHAVIOUR for a content block
// vibekit does not decode — nothing rendered — and the two Debug lines that make the
// drop findable, since the symptom is otherwise a claim-only card with an empty details
// region and no signal anywhere. This is the surface kiro-cli's structuredContent lands
// on, deliberately not adopted while there is no renderer behind it.
// Serial (no t.Parallel): captureSlog swaps the process-wide slog default.
func TestParseToolUpdateContent_UnmodelledType(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	t.Run("an unknown type renders nothing and logs both lines", func(t *testing.T) {
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))

		got := tr.parseToolUpdateContent("tc-1", []ACPToolCallContentBlock{
			{Type: "structuredContent"},
		})

		// Behaviour first: the block is dropped, which is what it did before the
		// logging and what this test would catch a silent adoption of.
		if got.output != "" || got.diffs != nil || got.terminalID != "" {
			t.Errorf("an unmodelled block produced output: %#v", got)
		}
		line := logs.String()
		for _, want := range []string{"unmodelled type", "structuredContent", "tc-1"} {
			if !strings.Contains(line, want) {
				t.Errorf("log %q missing %q", line, want)
			}
		}
		// The second line is the observable SYMPTOM rather than the cause, and it
		// is what a reader searches for after seeing an empty card.
		if !strings.Contains(line, "produced nothing to render") {
			t.Errorf("log %q missing the empty-render line", line)
		}
	})

	t.Run("a claim-only tool logs nothing", func(t *testing.T) {
		// No content blocks at all is the ~95% case (read, delete, think). Logging
		// here would put a line on almost every tool call and bury the real one.
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		tr.parseToolUpdateContent("tc-2", nil)
		if logs.Len() != 0 {
			t.Errorf("a claim-only tool logged %q", logs.String())
		}
	})

	t.Run("a known type with an unmatched payload is not called unmodelled", func(t *testing.T) {
		// An empty-text content block and a diff with no path are NORMAL frames,
		// not gaps in what vibekit decodes. A bare `default` arm would report both
		// as unmodelled types, which is the noise that would make the real line
		// unfindable — so the guard is on the TYPE, not on whether an arm matched.
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		tr.parseToolUpdateContent("tc-3", []ACPToolCallContentBlock{
			{Type: ContentTypeContent},
			{Type: ContentTypeDiff},
			{Type: ContentTypeTerminal},
		})
		line := logs.String()
		if strings.Contains(line, "unmodelled type") {
			t.Errorf("log %q calls a known type unmodelled", line)
		}
		// The empty-render line DOES fire, and should: content arrived and none of
		// it reached the card, which is the state worth knowing about however the
		// blocks were shaped.
		if !strings.Contains(line, "produced nothing to render") {
			t.Errorf("log %q missing the empty-render line", line)
		}
	})

	t.Run("a block that renders logs nothing", func(t *testing.T) {
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		// Field assignment rather than a composite literal: ACPToolCallContentBlock
		// declares Content as an ANONYMOUS struct, so a literal would have to
		// restate its type inline.
		blk := ACPToolCallContentBlock{Type: ContentTypeContent}
		blk.Content.Text = "hello"
		got := tr.parseToolUpdateContent("tc-4", []ACPToolCallContentBlock{blk})
		if got.output == "" {
			t.Fatal("a content block with text produced no output")
		}
		if logs.Len() != 0 {
			t.Errorf("a rendering block logged %q", logs.String())
		}
	})
}

// TestKnownToolContentType is the closed set the diagnostic above keys on, listed
// rather than derived: a derived expectation would agree with any switch,
// including one that had quietly stopped covering a member.
func TestKnownToolContentType(t *testing.T) {
	for _, known := range []string{ContentTypeContent, ContentTypeDiff, ContentTypeTerminal} {
		if !knownToolContentType(known) {
			t.Errorf("knownToolContentType(%q) = false, want true", known)
		}
	}
	for _, unknown := range []string{"structuredContent", "", "Content", "resource_link"} {
		if knownToolContentType(unknown) {
			t.Errorf("knownToolContentType(%q) = true, want false", unknown)
		}
	}
}
