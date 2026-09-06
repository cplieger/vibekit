package translate

// v3 (KAS) workflow-run lifecycle handlers: nine upstream notifications
// (`KIND_TO_METHOD` in `src/workflow/workflow-notification-bridge.ts`) become
// three SSE events, all arriving on the LAUNCHING chat's bridge. Seven are pure
// invalidations and share one handler.

import (
	"cmp"
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// runOriginAgent labels a run KAS parented on a chat session.
const runOriginAgent = "agent"

// kasRunStart mirrors _kiro/workflow/run_start. `nodeTree` and `inputs` are not
// decoded: the client refetches `inspect`, whose `state.root` carries the same
// tree with execution facts on it. `parentSessionId` is the only origin signal on
// this wire — vibekit's own launch path sends none, so a frame carrying one was
// started from inside a session.
type kasRunStart struct {
	WorkflowID      string `json:"workflowId"`
	WorkflowName    string `json:"workflowName"`
	ParentSessionID string `json:"parentSessionId"`
}

// kasRunNode is the shape shared by the seven progress frames; every field is
// optional across the set (`paused` carries only the workflow id, and
// `loop_iteration` names its node in `loopId`). An absent NodePath is what tells
// the emit side a frame is run-level.
type kasRunNode struct {
	WorkflowID string   `json:"workflowId"`
	NodeID     string   `json:"nodeId"`
	LoopID     string   `json:"loopId"`
	SessionID  string   `json:"sessionId"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason"`
	NodePath   []string `json:"nodePath"`
}

// kasRunComplete mirrors _kiro/workflow/run_complete. `finalState` is not adopted
// as client state; `workflowName` is read out of it because this is the one
// lifecycle frame carrying none at the top level. `parentSessionId` is read from
// both places, top level first, which is upstream's primary source.
type kasRunComplete struct {
	WorkflowID      string `json:"workflowId"`
	Status          string `json:"status"`
	ParentSessionID string `json:"parentSessionId"`
	FinalState      struct {
		WorkflowName    string `json:"workflowName"`
		ParentSessionID string `json:"parentSessionId"`
	} `json:"finalState"`
}

// logAgentRun writes the one durable trace an agent-launched run gets: two
// append-only lines correlated by workflow_id, since such a run has no run record
// and no supervisor. Silent for a parentless run, to keep the class greppable.
func logAgentRun(msg, workflowID, recipe, parentSessionID string, extra ...any) {
	if parentSessionID == "" {
		return
	}
	slog.Info(msg,
		append([]any{
			"workflow_id", workflowID,
			"origin", runOriginAgent,
			"recipe", recipe,
		}, extra...)...)
}

// HandleRunStart translates _kiro/workflow/run_start → the run_started SSE. It
// fires again on every resume, so the client upserts on the workflow id rather
// than treating it as a create.
func (t *Translator) HandleRunStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[kasRunStart](msg, "workflow/run_start")
	if !ok || p.WorkflowID == "" {
		return
	}
	logAgentRun("agent-launched workflow run started", p.WorkflowID, p.WorkflowName, p.ParentSessionID)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunStarted, chatID, vibekit.RunStartedPayload{
		WorkflowID: p.WorkflowID,
		Name:       p.WorkflowName,
		// Keyed on the workflow id: this frame's chat id is empty for exactly the
		// runs the flag is about.
		Scheduled: t.runOrigin.IsScheduled(p.WorkflowID),
	}))
}

// HandleRunComplete translates _kiro/workflow/run_complete → the run_finished SSE
// and forgets the run's step sessions. A cancel, a failure and an
// `onMaxIterations: "pause"` stop all arrive here, so the status travels.
func (t *Translator) HandleRunComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[kasRunComplete](msg, "workflow/run_complete")
	if !ok || p.WorkflowID == "" {
		return
	}
	logAgentRun("agent-launched workflow run finished", p.WorkflowID, p.FinalState.WorkflowName,
		cmp.Or(p.ParentSessionID, p.FinalState.ParentSessionID), "status", p.Status)
	t.steps.forgetRun(p.WorkflowID)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunFinished, chatID, vibekit.RunFinishedPayload{
		WorkflowID: p.WorkflowID,
		Status:     p.Status,
		// This frame's one name for the run: inside the state, unlike run_start's.
		Name: p.FinalState.WorkflowName,
	}))
}

// RunProgressHandler returns the handler for one of the seven progress kinds. One
// function rather than seven: they share a payload shape, and the kind decides
// which fields the frame can fill.
func (t *Translator) RunProgressHandler(kind vibekit.RunProgressKind) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		p, ok := unmarshalParams[kasRunNode](msg, "workflow/"+string(kind))
		if !ok || p.WorkflowID == "" {
			return
		}
		node := cmp.Or(p.NodeID, p.LoopID)
		// Recorded before the broadcast so a permission ask racing the event still
		// classifies. node_start is the only frame naming a step's session id.
		if kind == vibekit.RunProgressNodeStart && p.SessionID != "" {
			t.steps.record(p.SessionID, p.WorkflowID, node)
		}
		t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunProgress, chatID,
			runProgress(kind, node, &p, time.Now())))
	}
}

// runProgress builds the frame for one progress kind: the node's state where the
// kind describes a node, an empty node path where it does not. The empty path is
// the signal, not a gap — a client applies a named node and refetches otherwise,
// which is the contract the three tree-shape kinds keep.
func runProgress(
	kind vibekit.RunProgressKind, node string, p *kasRunNode, at time.Time,
) vibekit.RunProgressPayload {
	out := vibekit.RunProgressPayload{WorkflowID: p.WorkflowID, NodeID: node, Kind: kind}
	stamp := at.UTC().Format(time.RFC3339Nano)
	switch kind {
	case vibekit.RunProgressNodeStart:
		out.NodePath = runNodePathOf(p, node)
		out.Status = runNodeStatusRunning
		out.StartedAt = stamp
	case vibekit.RunProgressNodeComplete:
		out.NodePath = runNodePathOf(p, node)
		// KAS's own word, forwarded: it is already the client tree's NodeState
		// vocabulary, so mapping it here would be a second enumeration.
		out.Status = p.Status
		out.EndedAt = stamp
		out.FailureReason = p.Reason
	case vibekit.RunProgressNodePaused:
		out.NodePath = runNodePathOf(p, node)
		out.Status = runNodeStatusPaused
	case vibekit.RunProgressWatchPoll:
		// A poll only says it looked, so it re-states `running`: a frame stating
		// nothing is a frame the client cannot apply.
		out.NodePath = runNodePathOf(p, node)
		out.Status = runNodeStatusRunning
	case vibekit.RunProgressLoopIteration, vibekit.RunProgressPaused, vibekit.RunProgressStepsQueued:
	}
	return out
}

// The two KAS NodeState words this translator asserts rather than forwards.
const (
	runNodeStatusRunning = "running"
	runNodeStatusPaused  = "paused"
)

// runNodePathOf joins the frame's node path, falling back to the node id: an
// empty path would silently mean "refetch" (see runProgress).
func runNodePathOf(p *kasRunNode, node string) string {
	if len(p.NodePath) > 0 {
		return strings.Join(p.NodePath, "/")
	}
	return node
}
