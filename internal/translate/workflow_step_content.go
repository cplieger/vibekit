package translate

// A parentless run's step content. A chat-parented run's step frames go through the ordinary
// content handlers into that chat's transcript, so nothing here is involved. A parentless run
// runs under the synthetic chat id `run:<workflowId>`, whose dispatcher drops every
// `session/update`, because opening a buffer there would create the phantom chat invariant 3
// exists to prevent. So this is a second, buffer-free projection of the same frames: deltas
// forward unaccumulated, a tool call accumulates in the step registry.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// HandleRunStepFrame projects one `session/update` from a run bridge to the clients
// watching that run. `workflowID` comes from the synthetic chat id rather than the frame,
// since a run bridge hosts exactly one run; the frame's `_meta.kiro.workflow` supplies the
// node, and a frame carrying no workflow block has no row to land in and is dropped.
func (t *Translator) HandleRunStepFrame(ctx context.Context, workflowID string, params json.RawMessage) {
	if workflowID == "" {
		return
	}
	// `params` is the notification's own `{sessionId, update}` object, not a frame
	// wrapping one: this is handed the params directly.
	var env ACPSessionUpdateEnvelope
	if json.Unmarshal(params, &env) != nil || env.Update == nil {
		return
	}
	var base ACPSessionUpdateBase
	if json.Unmarshal(env.Update, &base) != nil {
		return
	}
	// A replayed frame is stored history, and with no transcript here it would re-stream
	// a finished step as though it were working now.
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
		// Silently, the chat dispatcher's own posture for a sub-kind with no handler.
	}
}

// forwardRunChunk sends one text or reasoning delta. No truncation ceiling, unlike the chat
// path's buffer cap: nothing accumulates here, so a pathological step costs one event per
// delta rather than server memory.
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
	// The idle window's only tool-call signal for the PARENTLESS population (countStepTurn's
	// is chat-parented), so without it node_complete is the sole signal and one long step
	// reads as a stall. ABOVE EVERY GUARD BELOW: those are RENDERING decisions and this is
	// ENFORCEMENT, so under them hooks.showStatus off would decide a cancellation.
	t.reportRunProgress(tc.Meta.Kiro.Workflow)
	// The chat path's hook-status suppression: turning hook status off is not meant to
	// exempt runs.
	if len(tc.Meta.Kiro.HookAsk) > 0 && !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	path := runNodePath(tc.Meta.Kiro.Workflow)
	if path == "" {
		return
	}
	content := t.parseToolUpdateContent(tc.ToolCallID, tc.Content)
	// Subtask and sub-session empty on purpose: they are the chat's grouping keys, and this
	// payload carries its own address in NodePath — a second answer could disagree.
	call := toolCallFromWire(&tc, "", "", content)
	t.steps.recordRunTool(workflowID, path, &call)
	t.broadcastRunTool(ctx, workflowID, path, &call)
}

// forwardRunToolUpdate folds an update into the call it names and sends the folded value. An
// update for a call this projection never saw is dropped rather than sent as a partial:
// without the create there is no title, no kind and no input.
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
	// A copy: the registry holds the value this pointer addresses, and the event travels to
	// a fan-out goroutine.
	sent := *call
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunStep, "", vibekit.RunStepPayload{
		ToolCall:   &sent,
		WorkflowID: workflowID,
		NodePath:   path,
		Kind:       vibekit.RunStepTool,
	}))
}

// applyRunToolUpdate is the run path's field fold, deliberately NOT `applyToolCallUpdate`:
// that one needs a buffer for the duration and a chat id for the working label and line
// tracker, and a run has none of those. Order matters in what does carry over — a nullish
// title or kind must not wipe the create's value, and the terminal link is adopted before
// the status so a frame carrying both does not look up an id the call lacks.
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

// runNodePath is the step's address within its run, and the ONE join for it — SubtaskID
// calls this rather than joining the array itself, so transcript keying and `run_step` keying
// cannot diverge. The node PATH rather than the id, because a repeat's iterations share an id
// and two passes of a loop body would stream into each other's rows. Falls back to the id
// when KAS sends no path: a row in the wrong place beats content that vanishes.
func runNodePath(w *ACPWorkflowMeta) string {
	if w == nil {
		return ""
	}
	if len(w.NodePath) > 0 {
		return strings.Join(w.NodePath, "/")
	}
	return w.NodeID
}
