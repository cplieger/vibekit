package translate

// v3 (KAS) workflow-run lifecycle handlers. KAS's nine notifications become three SSE
// events: seven are pure invalidations sharing one handler, and the two ends of the run
// mean something other than "refetch" to a client. They arrive on the LAUNCHING CHAT's
// bridge, because KAS parents a run on the calling chat's session.

import (
	"cmp"
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// runOriginAgent labels a run KAS parented on a chat session, the population with no
// supervisor in this tier — see logAgentRun.
const runOriginAgent = "agent"

// kasRunStart mirrors _kiro/workflow/run_start. `nodeTree` and `inputs` are not decoded: the
// client refetches `inspect`, whose `state.root` carries the same tree with execution facts,
// so the launch-time copy would only ever be staler. `parentSessionId` is NOT decoded — see
// logAgentRun for what replaced it as the origin signal.
type kasRunStart struct {
	WorkflowID   string `json:"workflowId"`
	WorkflowName string `json:"workflowName"`
}

// kasRunNode is the shape shared by the seven progress frames. Every field is optional across
// the set: `paused` carries only the workflow id, and `loop_iteration` names its node in
// `loopId` rather than `nodeId`.
type kasRunNode struct {
	WorkflowID string   `json:"workflowId"`
	NodeID     string   `json:"nodeId"`
	LoopID     string   `json:"loopId"`
	SessionID  string   `json:"sessionId"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason"`
	NodePath   []string `json:"nodePath"`
}

// kasRunComplete mirrors _kiro/workflow/run_complete. `finalState` is not adopted as client
// state — the client refetches rather than rendering a snapshot from an event — but
// `workflowName` is read out of it for the log line, because this is the one lifecycle
// notification with no top-level name. Neither copy of `parentSessionId` is decoded; see
// logAgentRun.
type kasRunComplete struct {
	WorkflowID string `json:"workflowId"`
	Status     string `json:"status"`
	FinalState struct {
		WorkflowName string `json:"workflowName"`
	} `json:"finalState"`
}

// logAgentRun writes the one durable trace an agent-launched run gets in this tier: two
// append-only lines correlated by workflow_id, because such a run has no record, no supervisor
// and no host-lost detection, so otherwise the only evidence it existed is a chat transcript
// somebody has to open. It holds no state, so it cannot see a run whose host died between the
// two lines. Silent for a run VIBEKIT launched, which a person or a schedule already holds a
// run id and a lease for, so logging it would dilute the class this line exists to make
// greppable.
//
// The discriminator is the frame's DELIVERY ADDRESS, not `parentSessionId`. It used to be that
// payload field, on the premise that vibekit's own launch path sent none — a premise
// `_kiro/workflow/new` retired in 0.63.3 by REQUIRING one, so every manual and scheduled run
// would now log as origin=agent and the class would be worthless. The address is a fact
// vibekit owns rather than one upstream owns: (*Runtime).dispatch hands a run-bridge lifecycle
// frame an EMPTY chat id, while a chat bridge's Forward stamps that chat's real id. So a
// non-empty chat id IS "launched from inside a chat session", and HandleRunStart already rests
// on the same property one line below for the Scheduled flag.
//
// Not the lease, and the ordering is why: observeComplete calls forgetBounds — which releases
// the lease — BEFORE HandleRunComplete, so a lease-existence predicate reads false for every
// terminal run vibekit launched, mis-classifying exactly the frame it has to get right.
func logAgentRun(msg string, chatID vibekit.ChatID, workflowID, recipe string, extra ...any) {
	if chatID == "" {
		return
	}
	slog.Info(msg,
		append([]any{
			"workflow_id", workflowID,
			"origin", runOriginAgent,
			"recipe", recipe,
		}, extra...)...)
}

// HandleRunStart translates _kiro/workflow/run_start → the run_started SSE. It fires again on
// every resume, which is why the client treats it as "this run exists and something changed"
// rather than as a create; an insert keyed on workflow id is idempotent.
func (t *Translator) HandleRunStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[kasRunStart](msg, "workflow/run_start")
	if !ok || p.WorkflowID == "" {
		return
	}
	logAgentRun("agent-launched workflow run started", chatID, p.WorkflowID, p.WorkflowName)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunStarted, chatID, vibekit.RunStartedPayload{
		WorkflowID: p.WorkflowID,
		Name:       p.WorkflowName,
		// Keyed on the workflow id, not chatID: this frame's chat id is empty for exactly
		// the runs the flag is about.
		Scheduled: t.runOrigin.IsScheduled(p.WorkflowID),
	}))
}

// HandleRunComplete translates _kiro/workflow/run_complete → the run_finished SSE. Terminal
// covers more than success — a cancel, a failure and an `onMaxIterations: "pause"` policy stop
// all arrive here — which is why the status travels rather than being inferred from the event.
//
// It does NOT forget the run's step sessions: `paused` reaches this frame on a run that is
// still going, so the bound has to test the status, and the caller already does for the run's
// own bounds. The hook is agent.observeComplete's terminal branch, through ForgetRunSteps.
func (t *Translator) HandleRunComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[kasRunComplete](msg, "workflow/run_complete")
	if !ok || p.WorkflowID == "" {
		return
	}
	logAgentRun("agent-launched workflow run finished", chatID, p.WorkflowID,
		p.FinalState.WorkflowName, "status", p.Status)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunFinished, chatID, vibekit.RunFinishedPayload{
		WorkflowID: p.WorkflowID,
		Status:     p.Status,
		// This frame's one name for the run, inside the state rather than at the top level
		// as on run_start.
		Name: p.FinalState.WorkflowName,
	}))
}

// RunProgressHandler returns the handler for one of the seven progress kinds. One function
// rather than seven: they share a payload shape, and the kind stamped on the event tells the
// client how eagerly to refetch, never how to reconstruct state.
func (t *Translator) RunProgressHandler(kind vibekit.RunProgressKind) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		p, ok := unmarshalParams[kasRunNode](msg, "workflow/"+string(kind))
		if !ok || p.WorkflowID == "" {
			return
		}
		node := cmp.Or(p.NodeID, p.LoopID)
		// The ONE frame that announces a step's session id. Recorded before the broadcast so
		// a permission ask racing the event still classifies.
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
