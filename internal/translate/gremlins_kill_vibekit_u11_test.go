package translate

// Mutation-killing tests for unit vibekit-u11 (package internal/translate).
//
// Targets surviving gremlins mutants in streaming_tools.go and wire.go.
// Tests add no production code; every assertion's expected value depends on
// the exact operator at the mutated site. No target is equivalent — all
// seven are killed by an observable behavioral difference.
//
// Target → killing test:
//   streaming_tools.go:109:16 NEGATION (`len(diffs) > 0` in HandleToolCallUpdate)
//     -> TestGk_vibekit_u11_ToolCallUpdate_DiffPresentRecordsAndAppends
//   streaming_tools.go:115:37 NEGATION (`SubSessionID == ""`)
//   streaming_tools.go:115:59 NEGATION (`subSessionID != ""`)
//     -> TestGk_vibekit_u11_ToolCallUpdate_SubSessionGate
//   streaming_tools.go:160:13 NEGATION (`workDir == ""` in relPath)
//   streaming_tools.go:166:9  NEGATION (`err != nil` in relPath)
//     -> TestGk_vibekit_u11_RelPath/StripsRootPrefix
//   wire.go:163:26 BOUNDARY + NEGATION (`len(p.PendingStages) > 0` in crewFromWire)
//     -> TestGk_vibekit_u11_CrewFromWire_PendingStagesGate
//
// All new identifiers are prefixed gk_vibekit_u11_ to avoid colliding with
// sibling units sharing this package.

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// --- prefixed test fixtures ---

// gk_vibekit_u11_lineRec records RecordFromDiffs invocations so the diff
// gate in HandleToolCallUpdate is observable.
type gk_vibekit_u11_lineRec struct {
	lastDiffs []api.ToolDiff
	calls     int
}

func (r *gk_vibekit_u11_lineRec) RecordFromDiffs(_ api.ChatID, diffs []api.ToolDiff, _ int, _ string) {
	r.calls++
	r.lastDiffs = diffs
}

// gk_vibekit_u11_lineDeps wraps baseDeps and swaps in the recording LineTracker.
type gk_vibekit_u11_lineDeps struct {
	*baseDeps
	rec *gk_vibekit_u11_lineRec
}

func (d *gk_vibekit_u11_lineDeps) LineTracker() LineRecorder { return d.rec }

// gk_vibekit_u11_workDirDeps wraps baseDeps and overrides WorkDir for relPath tests.
type gk_vibekit_u11_workDirDeps struct {
	*baseDeps
	workDir string
}

func (d *gk_vibekit_u11_workDirDeps) WorkDir() string { return d.workDir }

// Compile-time interface assertions.
var (
	_ LineRecorder = (*gk_vibekit_u11_lineRec)(nil)
	_ Deps         = (*gk_vibekit_u11_lineDeps)(nil)
	_ Deps         = (*gk_vibekit_u11_workDirDeps)(nil)
)

// gk_vibekit_u11_primeUpdate builds a translator with a recording line tracker
// and registers one in-flight tool call "tc-1" (status pending, no
// diffs/locations/output, empty SubSessionID), then resets the recorder so the
// subsequent HandleToolCallUpdate is observed in isolation.
func gk_vibekit_u11_primeUpdate(t *testing.T) (*Translator, *gk_vibekit_u11_lineRec, *gk_vibekit_u11_lineDeps, api.ChatID) {
	t.Helper()
	rec := &gk_vibekit_u11_lineRec{}
	deps := &gk_vibekit_u11_lineDeps{baseDeps: newBaseDeps(), rec: rec}
	tr := New(deps, "/tmp", WithIDGenerator(func() string { return "gk-vibekit-u11-mid" }))
	chatID := api.ChatID("gk-vibekit-u11-c1")
	tr.HandleToolCall(context.Background(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"title":      "readFile",
		"kind":       "read",
		"status":     "pending",
	}), "")
	rec.calls = 0
	rec.lastDiffs = nil
	return tr, rec, deps, chatID
}

// =====================================================================
// streaming_tools.go:109:16 NEGATION  (`if len(diffs) > 0` in HandleToolCallUpdate)
// =====================================================================

func TestGk_vibekit_u11_ToolCallUpdate_DiffPresentRecordsAndAppends(t *testing.T) {
	tr, rec, deps, chatID := gk_vibekit_u11_primeUpdate(t)
	tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"content": []map[string]any{
			{"type": "diff", "path": "a.go", "oldText": "x", "newText": "y"},
		},
	}), "")
	buf := deps.bufStore.GetOrInit(chatID)
	idx := buf.ToolCallIndex["tc-1"]
	// len(diffs)==1 -> `1 > 0` true -> diff appended AND RecordFromDiffs called.
	// The `<= 0` (negation) mutant: `1 <= 0` false -> skip -> no append, no record.
	if got := len(buf.ToolCalls[idx].Diffs); got != 1 {
		t.Fatalf("HandleToolCallUpdate(diff): ToolCalls[%d].Diffs len = %d, want 1 (diff must be appended when len(diffs) > 0)", idx, got)
	}
	if got := buf.ToolCalls[idx].Diffs[0].Path; got != "a.go" {
		t.Errorf("HandleToolCallUpdate(diff): Diffs[0].Path = %q, want %q", got, "a.go")
	}
	if rec.calls != 1 {
		t.Errorf("HandleToolCallUpdate(diff): RecordFromDiffs calls = %d, want 1 (must record when len(diffs) > 0)", rec.calls)
	}
	if got := len(rec.lastDiffs); got != 1 {
		t.Errorf("HandleToolCallUpdate(diff): RecordFromDiffs received %d diffs, want 1", got)
	}
}

// =====================================================================
// streaming_tools.go:115:37 and 115:59 NEGATION
//   (`if buf.ToolCalls[idx].SubSessionID == "" && subSessionID != ""`)
// =====================================================================

func TestGk_vibekit_u11_ToolCallUpdate_SubSessionGate(t *testing.T) {
	t.Run("SetWhenEmptyAndIncomingNonEmpty", func(t *testing.T) {
		tr, _, deps, chatID := gk_vibekit_u11_primeUpdate(t)
		buf := deps.bufStore.GetOrInit(chatID)
		idx := buf.ToolCallIndex["tc-1"]
		// Existing SubSessionID is "" (set by HandleToolCall with "").
		tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
		}), "sub-9")
		// `"" == "" && "sub-9" != ""` -> true -> SubSessionID set to "sub-9".
		// 115:37 (`==`->`!=`): `"" != ""` false -> AND false -> not set.
		// 115:59 (`!=`->`==`): `"sub-9" == ""` false -> AND false -> not set.
		if got := buf.ToolCalls[idx].SubSessionID; got != "sub-9" {
			t.Errorf("SubSessionID = %q, want %q (empty existing + non-empty incoming must set it)", got, "sub-9")
		}
	})
	t.Run("NotOverwrittenWhenExistingNonEmpty", func(t *testing.T) {
		tr, _, deps, chatID := gk_vibekit_u11_primeUpdate(t)
		buf := deps.bufStore.GetOrInit(chatID)
		idx := buf.ToolCallIndex["tc-1"]
		buf.ToolCalls[idx].SubSessionID = "existing"
		tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
		}), "sub-9")
		// `"existing" == "" && ...` -> false -> unchanged.
		// 115:37 (`==`->`!=`): `"existing" != "" && "sub-9" != ""` -> true -> overwritten to "sub-9".
		if got := buf.ToolCalls[idx].SubSessionID; got != "existing" {
			t.Errorf("SubSessionID = %q, want %q (non-empty existing must not be overwritten)", got, "existing")
		}
	})
}

// =====================================================================
// streaming_tools.go:160:13 NEGATION  (`if workDir == ""` in relPath)
// streaming_tools.go:166:9  NEGATION  (`if err != nil || ...` in relPath)
// =====================================================================

func TestGk_vibekit_u11_RelPath(t *testing.T) {
	tests := []struct {
		name    string
		workDir string
		abs     string
		want    string
	}{
		// Kills 160:13 (mutant `workDir != ""` returns abs early) AND
		// 166:9 (mutant `err == nil` returns abs instead of the stripped rel).
		{name: "StripsRootPrefix", workDir: "/work", abs: "/work/sub/file.go", want: "sub/file.go"},
		// Escaping path: err==nil but rel starts with ".." -> returns abs.
		{name: "OutsideWorkDirReturnsAbs", workDir: "/work", abs: "/elsewhere/x.go", want: "/elsewhere/x.go"},
		// Empty workDir early-return path.
		{name: "EmptyWorkDirReturnsAbs", workDir: "", abs: "/a/b.go", want: "/a/b.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &gk_vibekit_u11_workDirDeps{baseDeps: newBaseDeps(), workDir: tt.workDir}
			tr := New(deps, "/tmp", WithIDGenerator(func() string { return "gk-vibekit-u11-mid" }))
			if got := tr.relPath(tt.abs); got != tt.want {
				t.Errorf("relPath(%q) [workDir=%q] = %q, want %q", tt.abs, tt.workDir, got, tt.want)
			}
		})
	}
}

// =====================================================================
// wire.go:163:26 BOUNDARY + NEGATION  (`if len(p.PendingStages) > 0` in crewFromWire)
// =====================================================================

func TestGk_vibekit_u11_CrewFromWire_PendingStagesGate(t *testing.T) {
	t.Run("NoPendingStagesLeavesNil", func(t *testing.T) {
		p := &CrewNotifPayload{
			Subagents: []CrewNotifSubagent{{SessionID: "s1", Group: "g1"}},
		}
		crew := crewFromWire(p)
		// len(PendingStages)==0 -> `0 > 0` false -> PendingStages stays nil.
		// BOUNDARY (`>=`): `0 >= 0` true -> make([]..., 0) -> non-nil empty slice.
		// NEGATION (`<=`): `0 <= 0` true -> make([]..., 0) -> non-nil empty slice.
		if crew.PendingStages != nil {
			t.Errorf("crew.PendingStages = %+v (len %d), want nil when there are no pending stages", crew.PendingStages, len(crew.PendingStages))
		}
	})
	t.Run("PendingStagesPopulated", func(t *testing.T) {
		p := &CrewNotifPayload{
			Subagents: []CrewNotifSubagent{{SessionID: "s1", Group: "g1"}},
			PendingStages: []CrewNotifPendingStage{
				{Name: "stage1", AgentName: "coder", Role: "impl", DependsOn: []string{"x"}},
			},
		}
		crew := crewFromWire(p)
		// len(PendingStages)==1 -> `1 > 0` true -> populated (len 1).
		// NEGATION (`<=`): `1 <= 0` false -> skip -> PendingStages nil.
		if got := len(crew.PendingStages); got != 1 {
			t.Fatalf("crew.PendingStages len = %d, want 1 (pending stages must be mapped when present)", got)
		}
		if got := crew.PendingStages[0].Name; got != "stage1" {
			t.Errorf("crew.PendingStages[0].Name = %q, want %q", got, "stage1")
		}
		if got := crew.PendingStages[0].AgentName; got != "coder" {
			t.Errorf("crew.PendingStages[0].AgentName = %q, want %q", got, "coder")
		}
	})
}
