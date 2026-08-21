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
	"cmp"
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// runOriginAgent labels a run KAS parented on a chat session, which is the
// population that has no supervisor in this tier — see logAgentRun.
const runOriginAgent = "agent"

// kasRunStart mirrors _kiro/workflow/run_start. `nodeTree` and `inputs` are
// deliberately not decoded: the client refetches `inspect`, whose `state.root`
// carries the same tree WITH execution facts on it, so decoding the launch-time
// copy would be a second representation that can only ever be staler.
//
// `parentSessionId` IS decoded, and it is the only origin signal on this wire.
// KAS merges it into every lifecycle payload when the run has a parent, and
// vibekit's own launch path (workflowNew) sends none, so a frame that carries one
// is a run started from inside a session rather than from the Workflows tab.
type kasRunStart struct {
	WorkflowID      string `json:"workflowId"`
	WorkflowName    string `json:"workflowName"`
	ParentSessionID string `json:"parentSessionId"`
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
// run state and is not adopted as client state — the client refetches rather than
// rendering a snapshot from an event.
//
// `workflowName` IS read out of the state, for the LOG line only, because this is
// the one lifecycle notification that carries no top-level `workflowName`.
// Reading it is not the thing the comment above forbids: nothing here reaches the
// client, and the alternative was remembering every launched run's name in a map,
// which is the store this record exists to avoid.
//
// `parentSessionId` is read from BOTH places, top level first. The notification
// bridge merges it top-level into EVERY lifecycle payload when the run has a
// parent (createWorkflowNotificationBridge spreads `{...event.payload,
// parentSessionId}` for all nine kinds, run_complete included), and upstream
// treats that as the primary source with the copy inside the state as a
// back-compat fallback. Decoding only the fallback is what this frame used to do,
// and it is value-identical today — but the day the state copy goes away, the
// terminal log line would silently stop while the launch line kept printing,
// which is the exact asymmetry the two-line record exists to prevent.
type kasRunComplete struct {
	WorkflowID      string `json:"workflowId"`
	Status          string `json:"status"`
	ParentSessionID string `json:"parentSessionId"`
	FinalState      struct {
		WorkflowName    string `json:"workflowName"`
		ParentSessionID string `json:"parentSessionId"`
	} `json:"finalState"`
}

// logAgentRun writes the one durable trace an agent-launched run gets in this
// tier, and does nothing for a run launched from the UI.
//
// T5 put run_workflow in the chat agent's hands, which creates runs nobody
// clicked. There is no run record, no supervisor and no host-lost detection here
// yet, so without this the only evidence such a run ever existed is a chat
// transcript somebody has to open. Two append-only lines (launch and terminal),
// correlated by workflow_id, are enough to answer "what did the agent start and
// how did it end" from the log alone.
//
// Deliberately NOT a store, and deliberately not the supervisor: it holds no
// state, so it cannot detect a run whose host died between the two lines. That
// gap is real and stays open until the run record lands.
//
// Silent for a parentless run: a manual or scheduled run was launched by a person
// looking at its page and already has a run id in hand, so logging it would only
// dilute the class this line exists to make greppable.
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

// HandleRunStart translates _kiro/workflow/run_start → the run_started SSE.
//
// It fires again on every resume (probe 6 measured three for one run), which is
// why the client treats it as "this run exists and something changed" rather
// than as a create. An insert keyed on workflow id is idempotent.
func (t *Translator) HandleRunStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[kasRunStart](msg, "workflow/run_start")
	if !ok || p.WorkflowID == "" {
		return
	}
	// Re-fires on every resume, so this line can repeat for one run. Kept that
	// way on purpose: each occurrence is a real launch of the run's remaining
	// work, and de-duplicating it would need the state this record refuses.
	logAgentRun("agent-launched workflow run started", p.WorkflowID, p.WorkflowName, p.ParentSessionID)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunStarted, chatID, vibekit.RunStartedPayload{
		WorkflowID: p.WorkflowID,
		Name:       p.WorkflowName,
		// Keyed on the workflow id, NOT on chatID: this frame's chat id is empty
		// for exactly the runs the flag is about (see RunOriginAccess).
		Scheduled: t.runOrigin.IsScheduled(p.WorkflowID),
	}))
}

// HandleRunComplete translates _kiro/workflow/run_complete → the run_finished
// SSE, and forgets the run's step sessions.
//
// Terminal covers more than success: a cancel, a failure and an
// `onMaxIterations: "pause"` policy stop all arrive here, which is why the
// status travels rather than being inferred from the event's existence.
func (t *Translator) HandleRunComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[kasRunComplete](msg, "workflow/run_complete")
	if !ok || p.WorkflowID == "" {
		return
	}
	// Terminal covers success, failure, cancel and a policy stop, which is why
	// the status travels on the line rather than being implied by its existence.
	logAgentRun("agent-launched workflow run finished", p.WorkflowID, p.FinalState.WorkflowName,
		cmp.Or(p.ParentSessionID, p.FinalState.ParentSessionID), "status", p.Status)
	t.steps.forgetRun(p.WorkflowID)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunFinished, chatID, vibekit.RunFinishedPayload{
		WorkflowID: p.WorkflowID,
		Status:     p.Status,
		// The one name this frame carries. It is inside the state rather than at
		// the top level, unlike run_start's, which is why the log line above
		// already reaches for it.
		Name: p.FinalState.WorkflowName,
	}))
}

// RunProgressHandler returns the handler for one of the seven progress kinds.
//
// One function rather than seven near-identical ones: they share a payload
// shape, and the only thing that differs is the kind stamped on the event —
// which the client uses to decide how eagerly to refetch, not to reconstruct
// state. Seven copies of the same four lines would be seven places for the
// invalidation contract to drift.
func (t *Translator) RunProgressHandler(kind vibekit.RunProgressKind) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		p, ok := unmarshalParams[kasRunNode](msg, "workflow/"+string(kind))
		if !ok || p.WorkflowID == "" {
			return
		}
		node := cmp.Or(p.NodeID, p.LoopID)
		// The ONE frame that announces a step's session id. Recorded before the
		// broadcast so a permission ask racing the event still classifies.
		if kind == vibekit.RunProgressNodeStart && p.SessionID != "" {
			t.steps.record(p.SessionID, p.WorkflowID, node)
		}
		t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunProgress, chatID, vibekit.RunProgressPayload{
			WorkflowID: p.WorkflowID,
			NodeID:     node,
			Kind:       kind,
		}))
	}
}
