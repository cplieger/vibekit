package translate

// v3 (KAS) workflow-run lifecycle handlers.
//
// KAS's workflow engine emits nine notifications (`KIND_TO_METHOD` in
// `src/workflow/workflow-notification-bridge.ts`): run_start, node_start,
// node_complete, node_paused, loop_iteration, watch_poll, paused, run_complete,
// steps_queued. The bridge merges `parentSessionId` into every payload when the
// run has a parent, and unsubscribes itself once `run_complete` carries a
// terminal status. They arrive on the LAUNCHING CHAT's bridge, because KAS
// parents a run on the calling chat's session.
//
// Nine notifications become three SSE events (see api/domain_workflow.go).
// Seven of the nine are pure invalidations and share one handler; the two
// exceptions are the ends of the run, which mean something different to a
// client than "refetch".
//
// ONE side effect beyond broadcasting: `node_start` is the only frame that
// announces a step's session id, so it is recorded here — see StepSessions.

import (
	"cmp"
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// runOriginAgent labels a run KAS parented on a chat session, which is the
// population that has no supervisor in this tier — see logAgentRun.
const runOriginAgent = "agent"

// kasRunStart mirrors _kiro/workflow/run_start. `nodeTree` and `inputs` are
// deliberately not decoded: the client refetches `inspect`, whose `state.root`
// carries the same tree with execution facts on it, so decoding the launch-time
// copy would only ever be staler.
//
// `parentSessionId` IS decoded, as the only origin signal on this wire. KAS
// merges it into every lifecycle payload when the run has a parent, and
// vibekit's own launch path (workflowNew) sends none, so a frame carrying one
// is a run started from inside a session rather than from the Workflows tab.
type kasRunStart struct {
	WorkflowID      string `json:"workflowId"`
	WorkflowName    string `json:"workflowName"`
	ParentSessionID string `json:"parentSessionId"`
}

// kasRunNode is the shape shared by the seven progress frames. Every field is
// optional across the set: `paused` carries only the workflow id, and
// `loop_iteration` names its node in `loopId` rather than `nodeId`.
//
// NodePath is decoded on all seven even though only the four node frames send
// it, because the absence is what tells the emit side a frame is run-level.
// Status is node_complete's own terminal word, and FailureReason its reason for
// the failed case. `node_paused` spells its explanation `reason`, not
// `pauseReason` — the two frames disagree upstream, so both are decoded and
// neither is renamed.
type kasRunNode struct {
	WorkflowID string   `json:"workflowId"`
	NodeID     string   `json:"nodeId"`
	LoopID     string   `json:"loopId"`
	SessionID  string   `json:"sessionId"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason"`
	NodePath   []string `json:"nodePath"`
}

// kasRunComplete mirrors _kiro/workflow/run_complete. `finalState` is the whole
// run state and is not adopted as client state — the client refetches rather
// than rendering a snapshot from an event.
//
// `workflowName` IS read out of the state, for the log line only, because
// this is the one lifecycle notification carrying no top-level
// `workflowName`. `parentSessionId` is read from BOTH places, top level
// first: the notification bridge merges it top-level into every lifecycle
// payload when the run has a parent, and upstream treats that as the primary
// source with the copy inside the state as a back-compat fallback.
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
// An agent-launched run has no run record, no supervisor and no host-lost
// detection, so without this the only evidence such a run existed is a chat
// transcript somebody has to open. Two append-only lines (launch and terminal),
// correlated by workflow_id, are enough to answer "what did the agent start
// and how did it end" from the log alone.
//
// Deliberately not a store and not the supervisor: it holds no state, so it
// cannot detect a run whose host died between the two lines — that gap stays
// open until the run record lands.
//
// Silent for a parentless run: a manual or scheduled run was launched by a
// person already holding its run id, so logging it would dilute the class
// this line exists to make greppable.
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
// It fires again on every resume (probe 6 measured three for one run), which
// is why the client treats it as "this run exists and something changed"
// rather than as a create. An insert keyed on workflow id is idempotent.
func (t *Translator) HandleRunStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[kasRunStart](msg, "workflow/run_start")
	if !ok || p.WorkflowID == "" {
		return
	}
	logAgentRun("agent-launched workflow run started", p.WorkflowID, p.WorkflowName, p.ParentSessionID)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunStarted, chatID, vibekit.RunStartedPayload{
		WorkflowID: p.WorkflowID,
		Name:       p.WorkflowName,
		// Keyed on the workflow id, not on chatID: this frame's chat id is
		// empty for exactly the runs the flag is about (see RunOriginAccess).
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
	logAgentRun("agent-launched workflow run finished", p.WorkflowID, p.FinalState.WorkflowName,
		cmp.Or(p.ParentSessionID, p.FinalState.ParentSessionID), "status", p.Status)
	t.steps.forgetRun(p.WorkflowID)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunFinished, chatID, vibekit.RunFinishedPayload{
		WorkflowID: p.WorkflowID,
		Status:     p.Status,
		// This frame's one name for the run: inside the state rather than at
		// the top level, unlike run_start's.
		Name: p.FinalState.WorkflowName,
	}))
}

// RunProgressHandler returns the handler for one of the seven progress kinds.
//
// One function rather than seven near-identical ones: they share a payload
// shape, and the kind decides which of the fields below the frame can fill.
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
		t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunProgress, chatID,
			runProgress(kind, node, &p, time.Now())))
	}
}

// runProgress builds the frame for one progress kind: the node's state where the
// kind describes a node, and an empty node path where it does not.
//
// The empty path is the signal, not a gap. A client applies a frame that names a
// node and refetches one that does not, so the three kinds that cannot be
// expressed as a node patch — `loop_iteration` and `steps_queued` change the
// tree's shape, `paused` is run-level — keep the invalidation contract they
// always had, and only they pay for it.
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
		// KAS's own word, forwarded rather than mapped: `completed`, `failed` and
		// `skipped` are already the NodeState vocabulary the client's tree holds,
		// and translating them here would be a second enumeration of it.
		out.Status = p.Status
		out.EndedAt = stamp
		out.FailureReason = p.Reason
	case vibekit.RunProgressNodePaused:
		out.NodePath = runNodePathOf(p, node)
		out.Status = runNodeStatusPaused
	case vibekit.RunProgressWatchPoll:
		// A poll leaves the watch node running and says only that it looked, so it
		// re-states `running` and nothing else. The status is carried rather than
		// left off: a frame stating nothing is a frame the client cannot apply,
		// and it used to spend a tree rebuild and a full re-render arriving at the
		// value the node already held.
		out.NodePath = runNodePathOf(p, node)
		out.Status = runNodeStatusRunning
	case vibekit.RunProgressLoopIteration, vibekit.RunProgressPaused, vibekit.RunProgressStepsQueued:
	}
	return out
}

// KAS NodeState status words this translator asserts on its own, as opposed to
// forwarding. Only the two a lifecycle frame implies without stating.
const (
	runNodeStatusRunning = "running"
	runNodeStatusPaused  = "paused"
)

// runNodePathOf joins the frame's node path, falling back to the node id.
//
// The fallback is runNodePath's: a row in the wrong place beats content that
// vanishes. It matters more here, because an empty path means "refetch" — so a
// node frame that arrived without one would silently join the run-level kinds.
func runNodePathOf(p *kasRunNode, node string) string {
	if len(p.NodePath) > 0 {
		return strings.Join(p.NodePath, "/")
	}
	return node
}
