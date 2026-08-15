package translate

import (
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// lineRec is a recording LineRecorder capturing RecordFromDiffs
// invocations so the diff gates in HandleToolCall / HandleToolCallUpdate
// are observable.
type lineRec struct {
	lastDiffs []api.ToolDiff
	calls     int
}

func (r *lineRec) RecordFromDiffs(_ api.ChatID, diffs []api.ToolDiff, _ int, _ string) {
	r.calls++
	r.lastDiffs = diffs
}

// lineDeps wraps baseDeps and swaps in the recording LineTracker.
type lineDeps struct {
	*baseDeps
	rec *lineRec
}

func (d *lineDeps) LineTracker() LineRecorder { return d.rec }

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
	_ Deps         = (*lineDeps)(nil)
	_ Deps         = (*workDirDeps)(nil)
	_ Deps         = (*hookStatusDeps)(nil)
)

// newLineCaptureDeps builds an event-capturing baseDeps with a recording
// LineTracker spliced in.
func newLineCaptureDeps() (*lineDeps, *lineRec, *[]api.ServerEvent) {
	base, events := newEventCaptureDeps()
	rec := &lineRec{}
	return &lineDeps{baseDeps: base, rec: rec}, rec, events
}

// primeToolCall builds an event-capturing translator with a recording
// LineTracker, registers one in-flight tool call "tc-1" (status pending,
// no diffs/locations/output, empty SubSessionID), then clears the
// captured events and recorder so the subsequent update is observed in
// isolation.
func primeToolCall(t *testing.T) (*Translator, *lineRec, *lineDeps, *[]api.ServerEvent, api.ChatID) {
	t.Helper()
	deps, rec, events := newLineCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "tc-mid" }))
	chatID := api.ChatID("c1")
	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"title":      "readFile",
		"kind":       "read",
		"status":     "pending",
	}), "")
	*events = nil
	rec.calls = 0
	rec.lastDiffs = nil
	return tr, rec, deps, events, chatID
}

// lastToolCallUpdate returns the ToolCall carried by the most recent
// tool_call_update event, or ok=false if none was emitted.
func lastToolCallUpdate(t *testing.T, events *[]api.ServerEvent) (api.ToolCall, bool) {
	t.Helper()
	for i := range slices.Backward(*events) {
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

func hasWorkingLabel(events *[]api.ServerEvent) bool {
	for _, e := range *events {
		if e.Type == api.EventWorkingLabel {
			return true
		}
	}
	return false
}

// hasToolCallEvent reports whether any tool_call event was broadcast.
func hasToolCallEvent(events *[]api.ServerEvent) bool {
	for _, e := range *events {
		if e.Type == api.EventToolCall {
			return true
		}
	}
	return false
}

// TestHandleToolCall_HookAskSuppression pins the M4 fix. On v3 (KAS) a
// pre-tool-use hook's ask-permission gate arrives as a kind:"other" tool
// call tagged _meta.kiro.hookAsk (there is no ToolKind "hook" in v3's
// zToolKind). When hooks.showStatus is off (IsHookStatusEnabled false)
// the hook-ask card is suppressed; when on, it renders. A normal tool
// call is never suppressed regardless of the setting.
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
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		chatID := api.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, hookAsk), "")
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
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), api.ChatID("c1"), mustJSON(t, hookAsk), "")
		if !hasToolCallEvent(events) {
			t.Error("hook-ask tool call suppressed while hooks.showStatus on; want shown")
		}
	})

	t.Run("NonHookAskShownWhenStatusDisabled", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &hookStatusDeps{baseDeps: base, enabled: false}
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), api.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
		}), "")
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
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), api.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-diff",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "x.go", "oldText": "a", "newText": "b"},
			},
		}), "")
		if rec.calls != 1 {
			t.Errorf("with diff: RecordFromDiffs calls = %d, want 1", rec.calls)
		}
	})
	t.Run("WithoutDiffSkipsLineTracker", func(t *testing.T) {
		deps, rec, _ := newLineCaptureDeps()
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), api.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-nodiff",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
		}), "")
		if rec.calls != 0 {
			t.Errorf("without diff: RecordFromDiffs calls = %d, want 0", rec.calls)
		}
	})
}

// TestToolCallUpdate_StatusApplied pins that a non-empty status in an
// update overwrites the in-flight tool call's status.
func TestToolCallUpdate_StatusApplied(t *testing.T) {
	tr, _, _, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
	}), "")
	tc, ok := lastToolCallUpdate(t, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if tc.Status != api.ToolCompleted {
		t.Errorf("ToolCall.Status = %q, want %q (non-empty status must be applied)", tc.Status, api.ToolCompleted)
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
			}), "")
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
	tr, _, _, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"text": "hello"}},
		},
	}), "")
	tc, ok := lastToolCallUpdate(t, events)
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
		tr, _, _, events, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"locations":  []map[string]any{{"path": "f.go", "line": 5}},
		}), "")
		tc, ok := lastToolCallUpdate(t, events)
		if !ok {
			t.Fatal("no tool_call_update event emitted")
		}
		if len(tc.Locations) != 1 || tc.Locations[0].Path != "f.go" {
			t.Errorf("ToolCall.Locations = %+v, want one location path=f.go", tc.Locations)
		}
	})
	t.Run("EmptyLocationsNotAssigned", func(t *testing.T) {
		tr, _, _, events, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"locations":  []map[string]any{}, // decodes to a non-nil empty slice
		}), "")
		tc, ok := lastToolCallUpdate(t, events)
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
	tr, rec, _, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
	}), "")
	if _, ok := lastToolCallUpdate(t, events); !ok {
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
	}), "")
	for _, e := range *events {
		if e.Type == api.EventToolCallUpdate {
			t.Error("idx==len: tool_call_update emitted, want early return (no event)")
		}
	}
}

// TestToolCallUpdate_DiffPresentRecordsAndAppends pins that an update
// carrying a diff appends it to the tool call's Diffs and records it
// through the LineTracker.
func TestToolCallUpdate_DiffPresentRecordsAndAppends(t *testing.T) {
	tr, rec, deps, _, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"content": []map[string]any{
			{"type": "diff", "path": "a.go", "oldText": "x", "newText": "y"},
		},
	}), "")
	buf := deps.bufStore.GetOrInit(chatID)
	idx := buf.ToolCallIndex["tc-1"]
	if got := len(buf.ToolCalls[idx].Diffs); got != 1 {
		t.Fatalf("ToolCalls[%d].Diffs len = %d, want 1 (diff must be appended when present)", idx, got)
	}
	if got := buf.ToolCalls[idx].Diffs[0].Path; got != "a.go" {
		t.Errorf("Diffs[0].Path = %q, want %q", got, "a.go")
	}
	if rec.calls != 1 {
		t.Errorf("RecordFromDiffs calls = %d, want 1 (must record when a diff is present)", rec.calls)
	}
	if got := len(rec.lastDiffs); got != 1 {
		t.Errorf("RecordFromDiffs received %d diffs, want 1", got)
	}
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
		}), "sub-9")
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
		}), "sub-9")
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &workDirDeps{baseDeps: newBaseDeps(), workDir: tt.workDir}
			tr := New(deps, withIDGenerator(func() string { return "mid" }))
			if got := tr.relPath(tt.abs); got != tt.want {
				t.Errorf("relPath(%q) [workDir=%q] = %q, want %q", tt.abs, tt.workDir, got, tt.want)
			}
		})
	}
}

// TestHandleToolCall_IsNewFileFlag pins the isNew computation that feeds
// TrackFileChanges: a tool call is treated as a new-file creation only
// when it is BOTH an edit kind AND pending status
// (tc.Kind == edit && tc.Status == pending). The verdict is observable
// on buf.ChangedFiles[path].IsNewFile. A pending edit marks the diffed
// file new; a completed edit (same kind, different status) does not.
func TestHandleToolCall_IsNewFileFlag(t *testing.T) {
	t.Run("PendingEditMarksNewFile", func(t *testing.T) {
		deps, _, _ := newLineCaptureDeps()
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		chatID := api.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-new",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "new.go", "oldText": "", "newText": "package x\n"},
			},
		}), "")
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
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		chatID := api.ChatID("c2")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-existing",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "completed",
			"content": []map[string]any{
				{"type": "diff", "path": "existing.go", "oldText": "a\n", "newText": "b\n"},
			},
		}), "")
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

// TestToolCallUpdate_CheckpointFromWire drives the two shapes a real
// kiro-cli emits (probed 2026-08-02 against 2.16.0) through the actual JSON
// decode, so the `_meta.kiro.checkpoint` NESTING is pinned and not just the
// merge logic. A misplaced struct tag compiles cleanly and silently yields
// nothing — the same trap that bit `_meta.title` — and here the symptom
// would be "Rewind shows no diff", with nothing in any log.
//
// The create case is the one worth the table: KAS sends NO `original` for a
// file it just created, so a consumer that requires all three keys breaks on
// the first file the agent writes.
func TestToolCallUpdate_CheckpointFromWire(t *testing.T) {
	const (
		origURI = "kiro-snapshot-v2://sess_51d58124:5c1bae6d/?originalPath%3Dexisting.txt"
		modURI  = "kiro-snapshot-v2://sess_51d58124:952e8e1f/?originalPath%3Dexisting.txt"
		local   = "file:///tmp/ws/existing.txt"
	)
	tests := []struct {
		name       string
		checkpoint map[string]any
		want       api.ToolCheckpoint
	}{
		{
			name:       "overwriting an existing file carries all three",
			checkpoint: map[string]any{"original": origURI, "modified": modURI, "local": local},
			want:       api.ToolCheckpoint{Original: origURI, Modified: modURI, Local: local},
		},
		{
			name:       "creating a file has no pre-image",
			checkpoint: map[string]any{"modified": modURI, "local": local},
			want:       api.ToolCheckpoint{Modified: modURI, Local: local},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, _, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "completed",
				"_meta":      map[string]any{"kiro": map[string]any{"checkpoint": tt.checkpoint}},
			}), "")
			tc, ok := lastToolCallUpdate(t, events)
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

// TestToolCallUpdate_CheckpointMergeIsPerField pins that a later frame with
// a narrower key set cannot erase a value an earlier frame supplied.
//
// Not hypothetical: the key set genuinely varies frame to frame for one tool
// call, so a wholesale struct replacement would drop `original` and take the
// pre-image — the only thing a diff actually needs — with it.
func TestToolCallUpdate_CheckpointMergeIsPerField(t *testing.T) {
	tr, _, _, events, chatID := primeToolCall(t)
	send := func(cp map[string]any) {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"_meta":      map[string]any{"kiro": map[string]any{"checkpoint": cp}},
		}), "")
	}
	send(map[string]any{"original": "orig-uri", "modified": "mod-uri", "local": "local-uri"})
	send(map[string]any{"modified": "mod-uri-2"})

	tc, ok := lastToolCallUpdate(t, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	want := api.ToolCheckpoint{Original: "orig-uri", Modified: "mod-uri-2", Local: "local-uri"}
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
			tr, _, _, events, chatID := primeToolCall(t)
			frame := map[string]any{"toolCallId": "tc-1", "status": "completed"}
			if tt.meta != nil {
				frame["_meta"] = tt.meta
			}
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, frame), "")
			tc, ok := lastToolCallUpdate(t, events)
			if !ok {
				t.Fatal("no tool_call_update event emitted")
			}
			if tc.Checkpoint != nil {
				t.Errorf("ToolCall.Checkpoint = %+v, want nil (no file was written)", *tc.Checkpoint)
			}
		})
	}
}
