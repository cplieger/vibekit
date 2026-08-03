package translate

// v3 (KAS) workflow-run lifecycle handlers.
//
// KAS's workflow engine emits nine notifications, listed in its own
// `KIND_TO_METHOD` table (`src/workflow/workflow-notification-bridge.ts`):
// run_start, node_start, node_complete, node_paused, loop_iteration,
// watch_poll, paused, run_complete, steps_queued. The bridge merges
// `parentSessionId` into every payload when the run has a parent, and
// unsubscribes itself once run_complete carries a terminal status.
//
// They arrive on the LAUNCHING CHAT's bridge, because KAS parents a run on the
// calling chat's session, so `chatID` is already the right topic and no
// session→chat resolution is needed here.
//
// Nine notifications become three SSE events (see api/domain_workflow.go). Seven
// of the nine are pure invalidations and share one handler; the exceptions are
// the two ends of the run, which mean something different to a client than
// "refetch": one inserts a row, the other is terminal.
//
// ONE side effect beyond broadcasting, and it is what makes step attribution
// possible: `node_start` is the only frame that announces a step's session id,
// so it is recorded here. See StepSessions below.

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// kasRunStart mirrors _kiro/workflow/run_start. `nodeTree` and `inputs` are
// deliberately not decoded: the client refetches `inspect`, whose `state.root`
// carries the same tree WITH execution facts on it, so decoding the launch-time
// copy would be a second representation that can only ever be staler.
type kasRunStart struct {
	WorkflowID   string `json:"workflowId"`
	WorkflowName string `json:"workflowName"`
}

// kasRunNode is the shape shared by the seven progress frames. Every field is
// optional across the set: `paused` carries only the workflow id, and
// `loop_iteration` names its node in `loopId` rather than `nodeId`.
type kasRunNode struct {
	WorkflowID string `json:"workflowId"`
	NodeID     string `json:"nodeId"`
	LoopID     string `json:"loopId"`
	SessionID  string `json:"sessionId"`
}

// kasRunComplete mirrors _kiro/workflow/run_complete. `finalState` is the whole
// run state and is not decoded — the client refetches rather than adopting a
// snapshot from an event.
type kasRunComplete struct {
	WorkflowID string `json:"workflowId"`
	Status     string `json:"status"`
}

// HandleRunStart translates _kiro/workflow/run_start → the run_started SSE.
//
// It fires again on every resume (probe 6 measured three for one run), which is
// why the client treats it as "this run exists and something changed" rather
// than as a create. An insert keyed on workflow id is idempotent.
func (t *Translator) HandleRunStart(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[kasRunStart](msg, "workflow/run_start")
	if !ok || p.WorkflowID == "" {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventRunStarted, chatID, api.RunStartedPayload{
		WorkflowID: p.WorkflowID,
		Name:       p.WorkflowName,
	}))
}

// HandleRunComplete translates _kiro/workflow/run_complete → the run_finished
// SSE, and forgets the run's step sessions.
//
// Terminal covers more than success: a cancel, a failure and an
// `onMaxIterations: "pause"` policy stop all arrive here, which is why the
// status travels rather than being inferred from the event's existence.
func (t *Translator) HandleRunComplete(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[kasRunComplete](msg, "workflow/run_complete")
	if !ok || p.WorkflowID == "" {
		return
	}
	t.steps.forgetRun(p.WorkflowID)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventRunFinished, chatID, api.RunFinishedPayload{
		WorkflowID: p.WorkflowID,
		Status:     p.Status,
	}))
}

// RunProgressHandler returns the handler for one of the seven progress kinds.
//
// One function rather than seven near-identical ones: they share a payload
// shape, and the only thing that differs is the kind stamped on the event —
// which the client uses to decide how eagerly to refetch, not to reconstruct
// state. Seven copies of the same four lines would be seven places for the
// invalidation contract to drift.
func (t *Translator) RunProgressHandler(kind api.RunProgressKind) func(context.Context, api.ChatID, *api.RPCResponse) {
	return func(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
		p, ok := unmarshalParams[kasRunNode](msg, "workflow/"+string(kind))
		if !ok || p.WorkflowID == "" {
			return
		}
		node := p.NodeID
		if node == "" {
			node = p.LoopID
		}
		// The ONE frame that announces a step's session id. Recorded before the
		// broadcast so a permission ask racing the event still classifies.
		if kind == api.RunProgressNodeStart && p.SessionID != "" {
			t.steps.record(p.SessionID, p.WorkflowID, node)
		}
		t.deps.Broadcast(ctx, api.NewEvent(api.EventRunProgress, chatID, api.RunProgressPayload{
			WorkflowID: p.WorkflowID,
			NodeID:     node,
			Kind:       kind,
		}))
	}
}
