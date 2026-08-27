package translate

// A PARENTLESS run's step content: the half of the workflow contract that used to
// be thrown away.
//
// Two ways a run starts and they reach opposite ends of this file. An AGENT
// calling `run_workflow` gets its run parented on the calling chat's session, so
// every step frame arrives on that chat's bridge, goes through the ordinary
// content handlers, and lands in the transcript's run card keyed by
// `ACPWorkflowMeta.SubtaskID` — nothing here is involved. A USER clicking Run, or
// the scheduler, gets a run on its own bridge under the synthetic chat id
// `run:<workflowId>`, and that bridge's dispatcher DROPPED every `session/update`
// (run_host.go). So exactly the runs whose only surface is the run tab were the
// ones whose steps could not be watched: the tab had `capturedOutput` after a step
// finished and nothing at all while it worked.
//
// The reason recorded for the drop was that there is no transcript to put the
// content in, and that part is still true — a run has no chat, no message and no
// buffer, and opening one under the synthetic id would create the phantom chat
// invariant 3 exists to prevent. What was wrong was the conclusion. A transcript
// is not the only destination: the content can go straight to the clients watching
// that run, as a run-scoped event, rendered into the run card's own step rows.
//
// So this file is a SECOND, buffer-free projection of the same frames:
//
//   - text and reasoning deltas are forwarded as they arrive, with no
//     accumulation, because the client appends them into a live bubble exactly as
//     it does for a chat.
//   - a tool call IS accumulated, because an update has to fold into its create
//     and a delta-only stream cannot be rendered from the last frame received.
//     The fold lives in the step registry, bounded by the same `run_complete`
//     that bounds the session map beside it, so a finished run's calls are not
//     held for the life of the process.
//
// What is deliberately NOT here, and each for a stated reason. `plan` frames: a
// step's plan is a plan for the step, and the run card has no plan region to put
// one in — the steps ARE the plan. `session_info_update` and `usage_update`: a
// step's metering belongs to whoever pays for it, which for a parentless run is
// nobody, and the chat-side gate exists precisely to keep a step's numbers out of
// a conversation's accounting. Terminal-output adoption: the bytes reach the
// client on the terminal surface already, keyed by the same synthetic chat id.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// HandleRunStepFrame projects one `session/update` from a run bridge to the
// clients watching that run.
//
// `workflowID` comes from the synthetic chat id rather than from the frame, and
// that is the one fact this path can rely on: the run bridge hosts exactly one
// run, so its identity is known before anything is decoded. The frame's own
// `_meta.kiro.workflow` supplies the NODE, and a frame carrying no workflow block
// is dropped — it is either the run session's own bookkeeping or something this
// projection has no row to put content in.
func (t *Translator) HandleRunStepFrame(ctx context.Context, workflowID string, params json.RawMessage) {
	if workflowID == "" {
		return
	}
	// `params` is the notification's own params object — `{sessionId, update}` —
	// not a frame wrapping one. The chat dispatcher decodes through a wrapper
	// because it holds the whole RPCResponse; this is handed the params directly.
	var env ACPSessionUpdateEnvelope
	if json.Unmarshal(params, &env) != nil || env.Update == nil {
		return
	}
	var base ACPSessionUpdateBase
	if json.Unmarshal(env.Update, &base) != nil {
		return
	}
	// A replayed frame is stored history. The chat path builds a transcript out of
	// those through a load projection; there is no transcript here and no load
	// either, so replaying one would re-stream a finished step as though it were
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
// accumulates here, so a pathological step costs one event per delta rather than
// unbounded server memory. The client's own bubble is what holds the text, and it
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
	// The same hook-status suppression the chat path applies. A hook ask is a
	// kind:"other" call tagged `_meta.kiro.hookAsk`, and a reader who turned hook
	// status off did not mean "except inside a run".
	if len(tc.Meta.Kiro.HookAsk) > 0 && !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	path := runNodePath(tc.Meta.Kiro.Workflow)
	if path == "" {
		return
	}
	content := t.parseToolUpdateContent(tc.ToolCallID, tc.Content)
	// Subtask and sub-session are both empty on purpose. They are the CHAT's
	// grouping keys — which box inside a transcript a block belongs to — and this
	// payload carries its own address in NodePath, so filling them in would hand
	// the client a second, redundant answer that could disagree with the first.
	call := toolCallFromWire(&tc, "", "", content)
	t.steps.recordRunTool(workflowID, path, &call)
	t.broadcastRunTool(ctx, workflowID, path, &call)
}

// forwardRunToolUpdate folds an update into the call it names and sends the
// folded value.
//
// An update for a call this projection never saw is DROPPED rather than sent as a
// partial, mirroring the chat path's `buf.ToolCall` miss: without the create
// there is no title, no kind and no input, so a client would render a card that
// says nothing about what ran.
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
	// A COPY into the payload, because the registry holds the value this pointer
	// addresses and an event travels to a fan-out goroutine.
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
// Deliberately NOT `applyToolCallUpdate`. That function needs a `*buffer.Buffer`
// for the tool's duration and a chat id for the working-label broadcast and the
// line tracker, and all three of those are a CHAT's concerns: a run has no turn
// footer to summarise, no composer label to drive and no per-turn file ledger.
// Passing a fake buffer to reach the shared code would be the wrong trade — it
// would tie a run's correctness to a chat abstraction it does not have.
//
// The folds that DO carry over are the ones describing the tool itself, and they
// follow the chat path's order and its rules: a nullish title or kind must not
// wipe the create's value, and the terminal link is adopted before the status so a
// frame carrying both does not look up an id the call does not have yet.
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
	if len(tu.Locations) > 0 {
		tc.Locations = tu.Locations
	}
	if len(content.diffs) > 0 {
		tc.Diffs = append(tc.Diffs, content.diffs...)
	}
	mergeCheckpoint(tc, tu.Meta.Kiro.Checkpoint)
	mergeToolMeta(tc, tu)
}

// runNodePath is the step's address within its run, and the ONE join for it.
//
// The NODE PATH rather than the node id, for the reason ACPWorkflowMeta.SubtaskID
// records: a repeat's iterations share a node id, so an id addresses a node in the
// plan and not one execution of it, and two passes of a loop body would stream
// into each other's rows. Falls back to the id when KAS sends no path, because a
// row in the wrong place still beats content that vanishes.
//
// `SubtaskID` calls this rather than joining the array itself, and that is what
// keeps the two channels a step's content travels on addressing the same row. A
// CHAT-parented run's steps are keyed by `wf:<id>:<path>` in the transcript; a
// PARENTLESS run's are keyed by `<path>` in a `run_step` payload. Both resolve
// through the client's `nodePathOf`, so two joins here would be two chances for
// one of the two surfaces to grow phantom rows while the other stayed correct —
// and the failure would be invisible on whichever surface was not being tested.
func runNodePath(w *ACPWorkflowMeta) string {
	if w == nil {
		return ""
	}
	if len(w.NodePath) > 0 {
		return strings.Join(w.NodePath, "/")
	}
	return w.NodeID
}
