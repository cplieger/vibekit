package translate

// Tests for a PARENTLESS run's step projection.
//
// The rule under test in every case is the one the chat path does not have to
// state: a run has no buffer, so anything that needs accumulating has to be
// accumulated HERE or it cannot be rendered at all. Text and reasoning need none
// (the client appends deltas into a live block), and a tool call needs all of it
// (an update carries a status and an output delta and nothing that says what ran).

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// stepFrame builds a run bridge's `session/update` params for one step frame.
func stepFrame(kind, workflowID string, nodePath []string, update map[string]any) json.RawMessage {
	u := map[string]any{"sessionUpdate": kind}
	maps.Copy(u, update)
	u["_meta"] = map[string]any{"kiro": map[string]any{"workflow": map[string]any{
		"workflowId": workflowID,
		"nodeId":     nodePath[len(nodePath)-1],
		"nodePath":   nodePath,
		"type":       "step",
	}}}
	raw, err := json.Marshal(map[string]any{
		"sessionId": "sess_step",
		"update":    u,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// runSteps decodes every run_step payload out of a captured event slice.
func runSteps(t *testing.T, events []vibekit.ServerEvent) []vibekit.RunStepPayload {
	t.Helper()
	var out []vibekit.RunStepPayload
	for _, evt := range events {
		if evt.Type != vibekit.EventRunStep {
			continue
		}
		raw, err := json.Marshal(evt.Payload)
		if err != nil {
			t.Fatalf("Setup: re-marshalling a run_step payload: %s", err)
		}
		var p vibekit.RunStepPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("Setup: decoding a run_step payload: %s", err)
		}
		out = append(out, p)
	}
	return out
}

// TestHandleRunStepFrame_ForwardsContent pins the two delta kinds and the address
// they carry. The NODE PATH is the one that matters: a repeat's iterations share a
// node id, so an id addresses a node in the plan rather than one execution of it,
// and two passes of a loop body would stream into each other's rows.
func TestHandleRunStepFrame_ForwardsContent(t *testing.T) {
	t.Parallel()
	var events []vibekit.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("agent_message_chunk", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"content": map[string]any{"type": "text", "text": "all green"},
		}))
	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("agent_thought_chunk", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"content": map[string]any{"type": "text", "text": "let me check"},
		}))

	got := runSteps(t, events)
	if len(got) != 2 {
		t.Fatalf("HandleRunStepFrame produced %d run_step events, want 2", len(got))
	}
	if got[0].Kind != vibekit.RunStepText || got[0].Delta != "all green" {
		t.Errorf("first frame = (%q, %q), want (text, %q)", got[0].Kind, got[0].Delta, "all green")
	}
	if got[1].Kind != vibekit.RunStepThinking || got[1].Delta != "let me check" {
		t.Errorf("second frame = (%q, %q), want (thinking, %q)", got[1].Kind, got[1].Delta, "let me check")
	}
	for i, p := range got {
		if p.NodePath != "seq/coder" {
			t.Errorf("frame %d node_path = %q, want seq/coder", i, p.NodePath)
		}
		if p.WorkflowID != "wf_1" {
			t.Errorf("frame %d workflow_id = %q, want wf_1", i, p.WorkflowID)
		}
	}
}

// TestHandleRunStepFrame_FoldsAToolUpdate is the case the accumulator exists for.
//
// A `tool_call_update` carries a status and an output delta and nothing that says
// what ran, so a client sent the update alone would render a card with no title, no
// kind and no input. The projection folds it into the create and sends the whole
// call, which is what lets the client render the last frame it received and be
// right.
func TestHandleRunStepFrame_FoldsAToolUpdate(t *testing.T) {
	t.Parallel()
	var events []vibekit.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("tool_call", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"toolCallId": "t1",
			"title":      "Run ci-local.sh",
			"kind":       "execute",
			"status":     "in_progress",
		}))
	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("tool_call_update", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"toolCallId": "t1",
			"status":     "completed",
			"content": []any{map[string]any{
				"type":    "content",
				"content": map[string]any{"type": "text", "text": "RESULT: PASS"},
			}},
		}))

	got := runSteps(t, events)
	if len(got) != 2 {
		t.Fatalf("got %d run_step events, want 2 (a create and a folded update)", len(got))
	}
	final := got[1].ToolCall
	if final == nil {
		t.Fatal("the update carried no tool call")
	}
	// The create's facts survive the update that did not restate them. This is the
	// whole point: KAS sends title and kind nullish on an update.
	if final.Title != "Run ci-local.sh" {
		t.Errorf("title = %q, want it kept from the create", final.Title)
	}
	if final.Kind != vibekit.ToolKind("execute") {
		t.Errorf("kind = %q, want it kept from the create", final.Kind)
	}
	if final.Status != vibekit.ToolCompleted {
		t.Errorf("status = %q, want completed", final.Status)
	}
	// The trailing newline is `sanitize.Output`'s, which is the point: the output
	// went through the SAME content parser the chat path uses rather than a second
	// copy of it. Asserted exactly for that reason.
	if final.Output != "RESULT: PASS\n" {
		t.Errorf("output = %q, want the update's text through the shared parser", final.Output)
	}
	// The update frame's own address is not trusted for the row: KAS is not
	// guaranteed to repeat the workflow meta, so the path comes from what the
	// create recorded.
	if got[1].NodePath != "seq/coder" {
		t.Errorf("node_path = %q, want seq/coder", got[1].NodePath)
	}
}

// TestHandleRunStepFrame_ReportsRunProgress is the progress signal for the
// population that has no other one, and it is here rather than only on the chat
// path because that asymmetry is the whole reason this call site exists.
//
// A manual or scheduled run's step frames arrive on its own bridge and reach
// forwardRunToolCall, never the chat path's countStepTurn site. So without this,
// an unattended run's only progress signal would be `node_complete` — and one
// legitimately long step, which is exactly the shape a fifteen-minute idle window
// exists to tolerate, would read as a stall and be cancelled. That population is
// the one the watchdog exists for.
//
// The UPDATE deliberately reports nothing: a status change on a call already
// counted is not new evidence the run is producing frames, and crediting it would
// let one wedged tool call refill the window forever by re-reporting itself.
func TestHandleRunStepFrame_ReportsRunProgress(t *testing.T) {
	t.Parallel()
	deps := newBaseDeps()
	tr := New(rolesOf(deps))

	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("tool_call", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"toolCallId": "t1",
			"title":      "Read a file",
			"kind":       "read",
			"status":     "pending",
		}))
	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("tool_call_update", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"toolCallId": "t1",
			"status":     "completed",
		}))

	if want := []string{"wf_1"}; !slices.Equal(deps.runProgress, want) {
		t.Errorf("progress reported = %v, want %v: the create is the evidence, the update is "+
			"not — a parentless run's tool calls are its only progress signal besides a "+
			"completed node, and a re-reported call is not a new frame", deps.runProgress, want)
	}
}

// TestHandleRunStepFrame_ReportsProgressAboveTheRenderGuards is the report's
// PLACEMENT on the population that has no other progress signal, which is what makes
// it the sharper half of the same property.
//
// Both guards below it are rendering decisions — do not draw a hook card, and there
// is no row to put content in — and neither says anything about whether KAS is
// producing frames, which is the only question the idle window asks. Reported under
// them, hooks.showStatus off cancels an unattended run whose step is asking a hook
// gate, as stalled, while it works.
func TestHandleRunStepFrame_ReportsProgressAboveTheRenderGuards(t *testing.T) {
	t.Parallel()
	for name, meta := range map[string]map[string]any{
		// A hook ask with hooks.showStatus off: the card is dropped, the run is not.
		"a hook ask whose card is suppressed": {
			"workflow": map[string]any{
				"workflowId": "wf_1",
				"nodeId":     "coder",
				"nodePath":   []string{"seq", "coder"},
				"type":       "step",
			},
			"hookAsk": map[string]any{"kind": "pre-tool-use", "toolName": "fs_write"},
		},
		// A workflow block naming the run but no node: there is no step row to render
		// into, and the run is still producing frames.
		"a step frame with no address to render at": {
			"workflow": map[string]any{"workflowId": "wf_1", "type": "step"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := newBaseDeps()
			deps := &hookStatusDeps{baseDeps: base, enabled: false}
			tr := New(rolesOf(deps))

			tr.HandleRunStepFrame(t.Context(), "wf_1", mustJSON(t, map[string]any{
				"sessionId": "sess_step",
				"update": map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    "t1",
					"title":         "Run hook",
					"kind":          "other",
					"status":        "pending",
					"_meta":         map[string]any{"kiro": meta},
				},
			}))

			if want := []string{"wf_1"}; !slices.Equal(base.runProgress, want) {
				t.Errorf("progress reported = %v, want %v: a frame a run's card cannot render is "+
					"still evidence the run is producing frames, and it is the only evidence a "+
					"parentless run has besides a completed node", base.runProgress, want)
			}
		})
	}
}

// TestHandleRunStepFrame_DropsAnOrphanUpdate mirrors the chat path's `buf.ToolCall`
// miss: without the create there is nothing to fold into, so a partial would be a
// card that says nothing about what ran.
func TestHandleRunStepFrame_DropsAnOrphanUpdate(t *testing.T) {
	t.Parallel()
	var events []vibekit.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("tool_call_update", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"toolCallId": "never_created",
			"status":     "completed",
		}))

	if got := runSteps(t, events); len(got) != 0 {
		t.Errorf("an orphan update produced %d events, want 0: %+v", len(got), got)
	}
}

// TestApplyRunToolUpdate_FailedTakesReason pins the reason fold on the run path.
// run-step-blocks.ts force-opens a failed step's card exactly as the transcript
// does, onto the same region, so a step whose reason rides rawOutput is blank
// there for the same reason and is fixed by the same fold.
func TestApplyRunToolUpdate_FailedTakesReason(t *testing.T) {
	t.Parallel()
	var events []vibekit.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("tool_call", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"toolCallId": "t1",
			"title":      "Write config",
			"kind":       "edit",
			"status":     "in_progress",
		}))
	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("tool_call_update", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"toolCallId": "t1",
			"status":     "failed",
			"rawOutput":  "lock is held by another process",
			"content": []any{map[string]any{
				"type": "diff", "path": "", "newText": "{}\n",
			}},
		}))

	got := runSteps(t, events)
	if len(got) != 2 {
		t.Fatalf("got %d run_step events, want 2 (a create and a folded update)", len(got))
	}
	final := got[1].ToolCall
	if final == nil {
		t.Fatal("the update carried no tool call")
	}
	if final.Status != vibekit.ToolFailed {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	if final.Output != "lock is held by another process" {
		t.Errorf("output = %q, want the failure reason off rawOutput", final.Output)
	}
}

// TestHandleRunStepFrame_ForgetsAFinishedRunsTools pins the accumulator's BOUND.
// It lives in the step registry so `run_complete` — the frame KAS's own
// notification bridge unsubscribes on — drops a run's tool calls along with its
// session map, rather than holding them for the life of the process.
func TestHandleRunStepFrame_ForgetsAFinishedRunsTools(t *testing.T) {
	t.Parallel()
	var events []vibekit.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	tr.HandleRunStepFrame(t.Context(), "wf_1", stepFrame("tool_call", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"toolCallId": "t1", "title": "Run tests", "kind": "execute", "status": "in_progress",
		}))
	if _, _, ok := tr.steps.runTool("wf_1", "t1"); !ok {
		t.Fatal("Setup: the create was not recorded")
	}

	tr.steps.forgetRun("wf_1")

	if _, _, ok := tr.steps.runTool("wf_1", "t1"); ok {
		t.Error("a finished run's tool calls survived forgetRun")
	}
}

// TestHandleRunStepFrame_DropsUnaddressableAndReplayedFrames covers the two frames
// this door refuses, and they refuse for different reasons.
//
// An UNMARKED frame names no node, so there is no step row to render it in — it is
// the run session's own bookkeeping rather than a step's work. A REPLAYED frame is
// stored history: the chat path builds a transcript out of those through a load
// projection, and there is neither a transcript nor a load here, so replaying one
// would re-stream a finished step as though it were working now.
func TestHandleRunStepFrame_DropsUnaddressableAndReplayedFrames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params json.RawMessage
	}{
		{
			name: "no workflow marker",
			params: json.RawMessage(`{"sessionId":"s","update":{` +
				`"sessionUpdate":"agent_message_chunk",` +
				`"content":{"type":"text","text":"orphan"}}}`),
		},
		{
			// Hand-written rather than built by stepFrame: the replay flag sits on
			// the UPDATE object beside the workflow block, which that helper's own
			// `_meta` write would clobber. Note the nesting — the flag is NOT on
			// `params` (see ACPSessionUpdateBase, where reading it off params
			// yields false for every frame and looks exactly like a wire that
			// never sets it).
			name: "a replayed frame",
			params: json.RawMessage(`{"sessionId":"s","update":{` +
				`"sessionUpdate":"agent_message_chunk",` +
				`"content":{"type":"text","text":"history"},` +
				`"_meta":{"kiro":{"replay":true,"workflow":{"workflowId":"wf_1","nodeId":"coder",` +
				`"nodePath":["seq","coder"],"type":"step"}}}}}`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var events []vibekit.ServerEvent
			tr := New(rolesOf(capturing(&events)))
			tr.HandleRunStepFrame(t.Context(), "wf_1", tc.params)
			if got := runSteps(t, events); len(got) != 0 {
				t.Errorf("%s produced %d events, want 0: %+v", tc.name, len(got), got)
			}
		})
	}
}

// TestHandleRunStepFrame_RefusesAnUnidentifiedRun pins the precondition. The
// workflow id comes from the bridge's synthetic chat id rather than from the frame,
// so an empty one means the caller could not identify the run — and a frame with no
// run has no card to reach.
func TestHandleRunStepFrame_RefusesAnUnidentifiedRun(t *testing.T) {
	t.Parallel()
	var events []vibekit.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	tr.HandleRunStepFrame(t.Context(), "", stepFrame("agent_message_chunk", "wf_1",
		[]string{"seq", "coder"}, map[string]any{
			"content": map[string]any{"type": "text", "text": "nowhere to go"},
		}))

	if got := runSteps(t, events); len(got) != 0 {
		t.Errorf("an unidentified run produced %d events, want 0", len(got))
	}
}
