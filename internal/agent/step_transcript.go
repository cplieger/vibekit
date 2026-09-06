package agent

// Serving ONE workflow step's transcript: KAS's own session log for the step's session,
// read back through `session/load`. Nothing is stored, the step is addressed by PATH (a
// repeat's iterations share a node id), and it runs on the UTILITY bridge under its own
// budget — a wedged read on a chat's bridge would be a wedged chat.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/rpcerr"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workflow"
)

// stepTranscriptBudget bounds ONE step read end to end: the `session/load` RPC and the
// drain barrier after it. A bridge Call has no client-side timeout, so without it a step
// whose session KAS cannot hydrate holds the HTTP request until the client gives up and
// leaves a replay open behind it. A `var` only so a test can drive the expiry in
// milliseconds; never reassigned in production.
var stepTranscriptBudget = 60 * time.Second

// errStepUnknown means the run's plan names no step at the requested path. The handler
// answers 404 with it; every other outcome is a 200 carrying its own verdict.
var errStepUnknown = errors.New("this run has no step at that path")

// errRunStateUndecodable means the inspect reply could not be decoded. Kept apart from
// errStepUnknown because that one is a 404 and this is an `unavailable` verdict, and
// collapsing them would blame the caller for a wire change.
var errRunStateUndecodable = errors.New("this run's state could not be decoded")

// StepTranscript reads one step's transcript out of KAS. Three-valued: `ready` with the
// messages, `gone` when KAS no longer holds the session (or the step never started), and
// `unavailable` when the read could not be completed. Only the last is worth retrying.
func (rs *Runs) StepTranscript(ctx context.Context, workflowID, nodePath string) (vibekit.RunStepTranscript, error) {
	out := vibekit.RunStepTranscript{
		Messages:   []vibekit.Message{},
		WorkflowID: workflowID,
		NodePath:   nodePath,
		State:      vibekit.RunStepTranscriptUnavailable,
	}
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		// The RUN endpoint is where a missing workflow engine is reported.
		slog.Warn("step transcript: run state unreadable", "workflow_id", workflowID,
			"node_path", nodePath, "error", err, "detail", rpcerr.Details(err))
		return out, nil
	}
	// No empty-reply check: an EMPTY reply is how a KAS refusal arrives (rawCall drops
	// `resp.Error`), and the undecodable branch below already answers for it. The LOAD
	// path does need its own, because there the two arms differ by a whole budget.
	//
	// The step→session registry attributes a resumed run's frames after a restart emptied it.
	rs.translate.RecordRunSteps(raw)

	sessionID, err := stepSessionAt(raw, nodePath)
	if errors.Is(err, errRunStateUndecodable) {
		// Not the caller's fault, so not a 404: the path may name a real step in bytes
		// this build cannot read.
		slog.Warn("step transcript: run state undecodable", "workflow_id", workflowID,
			"node_path", nodePath, "error", err)
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if sessionID == "" {
		// A step that never ran has no session, so there is nothing to load and never was.
		out.State = vibekit.RunStepTranscriptGone
		return out, nil
	}

	msgs, state := rs.replayStepSession(ctx, sessionID)
	out.State = state
	if len(msgs) > 0 {
		out.Messages = msgs
	}
	return out, nil
}

// stepSessionAt resolves the ACP session a step path names, off one inspect reply. Returns
// errStepUnknown when the plan names no such path, and an EMPTY session id when it names a
// step that has not run — which is why it reads workflow.Steps rather than StepSessions.
func stepSessionAt(raw json.RawMessage, nodePath string) (string, error) {
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil {
		return "", errRunStateUndecodable
	}
	for _, st := range workflow.Steps(res.State) {
		if strings.Join(st.Path, "/") == nodePath {
			return st.SessionID, nil
		}
	}
	return "", errStepUnknown
}

// replayStepSession loads one session on the utility bridge and returns what its replay
// projected. A RAW `session/load` Call rather than bridge.Start: Start's load path calls
// adoptLoadedSession, which REBINDS the bridge's sessionID, so a step's id would be
// reported as the utility session's own and take it out of the orphan reaper's keep-list.
func (rs *Runs) replayStepSession(ctx context.Context, sessionID string) ([]vibekit.Message, vibekit.RunStepTranscriptState) {
	if !rs.stepReplays.open(sessionID) {
		// Refused rather than joined: two readers of one barrier is a lifecycle this
		// registry does not carry, and the retry meets a settled registry a moment later.
		slog.Debug("step transcript: a read of this session is already in flight", "session_id", sessionID)
		return nil, vibekit.RunStepTranscriptUnavailable
	}
	// Taken on EVERY path, expiry included, so an abandoned replay leaks neither a map
	// entry nor a waiter.
	defer func() { _ = rs.stepReplays.take(sessionID) }()

	cctx, cancel := context.WithTimeout(ctx, stepTranscriptBudget)
	defer cancel()

	u := rs.utility()
	if u == nil {
		return nil, vibekit.RunStepTranscriptUnavailable
	}
	// An EMPTY result is the LOAD's most likely failure: rawCall drops `resp.Error`, so
	// KAS refusing to hydrate a reaped session arrives as (nil, nil), and without the
	// check the read waits out the whole budget on a replay that is never coming.
	raw, at, err := u.session.rawCallAt(cctx, "step transcript load", vibekit.MethodSessionLoad,
		callerParams(map[string]any{vibekit.KeySessionID: sessionID}))
	if err != nil || len(raw) == 0 {
		// KAS's reason is machine prose, so it is logged and NOT forwarded.
		slog.Warn("step transcript: session load failed", "session_id", sessionID,
			"error", err, "detail", rpcerr.Details(err))
		// UNAVAILABLE, never `gone`: KAS answers an id it does not hold and a transient
		// fault with the same -32603 shape, and only the latter is worth retrying, so
		// `gone` is reserved for what this side can PROVE — a step with no session id.
		return nil, vibekit.RunStepTranscriptUnavailable
	}
	// rawCallAt for the POSITION: the replay is complete once the consumer has folded
	// everything preceding this response, and this is the only place that number is known.
	// Recording it also ATTEMPTS one settle, which answers a replay already drained by the
	// time the RPC returned, whose barrier nothing else would close.
	rs.stepReplays.markLoadedAt(sessionID, at)

	// The barrier, not the RPC's return: the replay frames precede the result on the wire
	// and the notification channel is buffered. Already closed above in the drained-early
	// case. See step_replay.go.
	select {
	case <-rs.stepReplays.barrier(sessionID):
	case <-cctx.Done():
		slog.Warn("step transcript: the replay did not settle inside the budget",
			"session_id", sessionID, "budget", stepTranscriptBudget)
		return nil, vibekit.RunStepTranscriptUnavailable
	}

	return assistantRows(rs.stepReplays.take(sessionID)), vibekit.RunStepTranscriptReady
}

// assistantRows keeps the ASSISTANT rows of a projected transcript. A step's own prompt is
// already on screen as the pane's Instruction row, and the stream this feeds renders ONE
// assistant transcript; event rows are dropped because a step has no turn card to badge.
func assistantRows(msgs []vibekit.Message) []vibekit.Message {
	out := make([]vibekit.Message, 0, len(msgs))
	for i := range msgs {
		if msgs[i].Role == vibekit.RoleAssistant {
			out = append(out, msgs[i])
		}
	}
	return out
}
