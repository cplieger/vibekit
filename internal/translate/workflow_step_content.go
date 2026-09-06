package translate

// A parentless run's step content.
//
// A run parented on a calling chat's session (an agent's `run_workflow`) has its
// step frames arrive on that chat's bridge, go through the ordinary content
// handlers, and land in the transcript's run card keyed by
// `ACPWorkflowMeta.SubtaskID` — nothing here is involved. A parentless run (a
// user Run click, or the scheduler) runs on its own bridge under the synthetic
// chat id `run:<workflowId>`, whose dispatcher drops every `session/update`
// (run_host.go), because a run has no chat, no message and no buffer, and
// opening one under the synthetic id would create the phantom chat invariant 3
// exists to prevent. This file is a second, buffer-free projection of the same
// frames straight to the clients watching that run, rendered into the run
// card's own step rows:
//
//   - text and reasoning deltas forward as they arrive, unaccumulated, because
//     the client appends them into a live bubble exactly as it does for a chat.
//   - a tool call IS accumulated (an update must fold into its create), in the
//     step registry, bounded by the same `run_complete` that bounds the
//     session map beside it.
//
// Deliberately not projected here: `plan` frames (a step's plan is for the
// step; the run card has no plan region — the steps ARE the plan) and
// `session_info_update`/`usage_update` (a step's metering belongs to whoever
// pays for it, which for a parentless run is nobody; terminal output already
// reaches the client on the terminal surface, keyed by the same synthetic id).

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// HandleRunStepFrame projects one `session/update` from a run bridge to the
// clients watching that run.
//
// `workflowID` comes from the synthetic chat id rather than from the frame,
// because the run bridge hosts exactly one run, so its identity is known
// before anything is decoded. The frame's own `_meta.kiro.workflow` supplies
// the node, and a frame carrying no workflow block is dropped — it is either
// the run session's own bookkeeping or something this projection has no row to
// put content in.
func (t *Translator) HandleRunStepFrame(ctx context.Context, workflowID string, params json.RawMessage) {
	if workflowID == "" {
		return
	}
	// `params` is the notification's own `{sessionId, update}` object, not a
	// frame wrapping one — the chat dispatcher decodes through a wrapper because
	// it holds the whole RPCResponse, this is handed the params directly.
	var env ACPSessionUpdateEnvelope
	if json.Unmarshal(params, &env) != nil || env.Update == nil {
		return
	}
	var base ACPSessionUpdateBase
	if json.Unmarshal(env.Update, &base) != nil {
		return
	}
	// A replayed frame is stored history. There is no transcript or load here,
	// so replaying one would re-stream a finished step as though it were
	// working now.
	if base.Meta.Kiro.Replay {
		return
	}
	switch base.Kind {
	case vibekit.ACPUpdateAgentChunk:
		t.forwardRunChunk(ctx, workflowID, env.Update, vibekit.RunStepText)
	case vibekit.ACPUpdateThoughtChunk:
		t.forwardRunChunk(ctx, workflowID, env.Update, vibekit.RunStepThinking)
	case vibekit.ACPUpdateToolCall:
		t.forwardRunToolCall(ctx, workflowID, env.Update)
	case vibekit.ACPUpdateToolUpdate:
		t.forwardRunToolUpdate(ctx, workflowID, env.Update)
	default:
		// Every other kind falls through silently, which is the same posture the
		// chat dispatcher takes for a sub-kind with no handler. See the file
		// header for the four this path declines on purpose.
	}
}

// forwardRunChunk sends one text or reasoning delta.
//
// No truncation ceiling, unlike the chat path's 32 MiB buffer cap: nothing
// accumulates here, so a pathological step costs one event per delta rather
// than unbounded server memory. The client's own bubble holds the text, and it
// is discarded with the tab.
func (t *Translator) forwardRunChunk(
	ctx context.Context, workflowID string, raw json.RawMessage, kind vibekit.RunStepKind,
) {
	var chunk ACPChunkWire
	if json.Unmarshal(raw, &chunk) != nil ||
		chunk.Content.Type != vibekit.ContentTypeText || chunk.Content.Text == "" {
		return
	}
	path := runNodePath(chunk.Meta.Kiro.Workflow)
	if path == "" {
		return
	}
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunStep, "", vibekit.RunStepPayload{
		WorkflowID: workflowID,
		NodePath:   path,
		Kind:       kind,
		Delta:      chunk.Content.Text,
	}))
}

// forwardRunToolCall records a new tool call for the run and sends it whole.
func (t *Translator) forwardRunToolCall(ctx context.Context, workflowID string, raw json.RawMessage) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil || tc.ToolCallID == "" {
		return
	}
	// The idle window's tool-call signal for the PARENTLESS population, and the only
	// site that reaches it: countStepTurn's is chat-parented, so a manual or scheduled
	// run's step frames pass through here and nowhere else — without it node_complete
	// is the sole signal and one legitimately long step reads as a stall. (Its turn CAP
	// stays unenforced here; that is pre-existing and needs a step key for a
	// path-addressed frame.) ABOVE EVERY GUARD BELOW: those are RENDERING decisions and
	// this is ENFORCEMENT, so sat under them a display preference decides a cancellation
	// — hooks.showStatus off would stop a step's hook asks refilling the window.
	t.reportRunProgress(tc.Meta.Kiro.Workflow)
	// The same hook-status suppression the chat path applies: a hook ask is a
	// kind:"other" call tagged `_meta.kiro.hookAsk`, and turning hook status off
	// is not meant to exempt runs.
	if len(tc.Meta.Kiro.HookAsk) > 0 && !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	path := runNodePath(tc.Meta.Kiro.Workflow)
	if path == "" {
		return
	}
	content := t.parseToolUpdateContent(tc.ToolCallID, tc.Content)
	// Subtask and sub-session are empty on purpose: those are the chat's
	// grouping keys, and this payload carries its own address in NodePath, so
	// filling them in would give the client a second answer that could
	// disagree with the first.
	call := toolCallFromWire(&tc, "", "", content)
	t.steps.recordRunTool(workflowID, path, &call)
	t.broadcastRunTool(ctx, workflowID, path, &call)
}

// forwardRunToolUpdate folds an update into the call it names and sends the
// folded value.
//
// An update for a call this projection never saw is dropped rather than sent
// as a partial, mirroring the chat path's `buf.ToolCall` miss: without the
// create there is no title, no kind and no input.
func (t *Translator) forwardRunToolUpdate(ctx context.Context, workflowID string, raw json.RawMessage) {
	var tu ACPToolCallUpdateWire
	if json.Unmarshal(raw, &tu) != nil || tu.ToolCallID == "" {
		return
	}
	content := t.parseToolUpdateContent(tu.ToolCallID, tu.Content)
	call, path, ok := t.steps.runTool(workflowID, tu.ToolCallID)
	if !ok {
		return
	}
	applyRunToolUpdate(&call, &tu, content)
	t.steps.recordRunTool(workflowID, path, &call)
	t.broadcastRunTool(ctx, workflowID, path, &call)
}

func (t *Translator) broadcastRunTool(
	ctx context.Context, workflowID, path string, call *vibekit.ToolCall,
) {
	// A copy into the payload: the registry holds the value this pointer
	// addresses, and an event travels to a fan-out goroutine.
	sent := *call
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunStep, "", vibekit.RunStepPayload{
		ToolCall:   &sent,
		WorkflowID: workflowID,
		NodePath:   path,
		Kind:       vibekit.RunStepTool,
	}))
}

// applyRunToolUpdate is the run path's field fold.
//
// Deliberately NOT `applyToolCallUpdate`: that function needs a `*buffer.Buffer`
// for the tool's duration and a chat id for the working-label broadcast and
// line tracker, all of which are a chat's concerns — a run has no turn footer,
// no composer label and no per-turn file ledger. The folds that DO carry over
// describe the tool itself and follow the chat path's order: a nullish title
// or kind must not wipe the create's value, the terminal link is adopted
// before the status so a frame carrying both does not look up an id the call
// does not have yet, and a failed tool's reason is taken off rawOutput because
// run-step-blocks.ts opens a failed step's card onto the same region a chat's
// failed card opens onto.
func applyRunToolUpdate(tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire, content toolUpdateContent) {
	if tu.Title != "" {
		tc.Title = tu.Title
	}
	if tu.Kind != "" {
		tc.Kind = tu.Kind
	}
	if tc.TerminalID == "" && content.terminalID != "" {
		tc.TerminalID = content.terminalID
	}
	if tu.Status != "" {
		tc.Status = tu.Status
	}
	if content.output != "" {
		tc.Output += content.output
	}
	if tc.Status == vibekit.ToolFailed && tc.Output == "" {
		if reason := rawOutputFailureText(tu.RawOutput); reason != "" {
			tc.Output = sanitize.Output(reason)
		}
	}
	if len(tu.Locations) > 0 {
		tc.Locations = tu.Locations
	}
	if len(content.diffs) > 0 {
		tc.Diffs = append(tc.Diffs, content.diffs...)
	}
	mergeCheckpoint(tc, tu.Meta.Kiro.Checkpoint)
	mergeToolMeta(tc, tu)
}

// runNodePath is the step's address within its run, and the one join for it.
//
// The node PATH rather than the node id: a repeat's iterations share a node id
// (see ACPWorkflowMeta.SubtaskID), so two passes of a loop body would stream
// into each other's rows without it. Falls back to the id when KAS sends no
// path, because a row in the wrong place still beats content that vanishes.
// `SubtaskID` calls this rather than joining the array itself, which is what
// keeps a chat-parented run's transcript keying (`wf:<id>:<path>`) and a
// parentless run's `run_step` keying resolving through one join.
func runNodePath(w *ACPWorkflowMeta) string {
	if w == nil {
		return ""
	}
	if len(w.NodePath) > 0 {
		return strings.Join(w.NodePath, "/")
	}
	return w.NodeID
}
