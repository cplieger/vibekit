package agent

// Serving ONE workflow step's transcript, read out of KAS on demand.
//
// The gap it closes: a step's transcript is on no other endpoint. `inspect`
// returns the node tree and each step's `capturedOutput` — what the step chose to
// declare, not how it got there — and the live `run_step` channel exists for
// PARENTLESS runs only and is persisted by nobody. So the answer had to come from
// the place that does hold it: KAS's own session log for the step's session, read
// back through `session/load` exactly as a resumed chat is.
//
// FOUR things this deliberately is not:
//
//   - It is not an accumulator. Nothing is stored; the projection is built per
//     request and dropped. A run has no chat, no message and no buffer.
//   - It is not addressed by node ID. A repeat's iterations share one, so an id
//     cannot name a single execution — the PATH does (workflow.StepSession.Path).
//   - It does not run on a chat bridge. A bridge Call has no client-side timeout,
//     so a wedged step read on a chat's bridge would be a wedged chat; this runs on
//     the shared UTILITY bridge under a budget of its own.
//   - It does not serve a LIVE step. A busy session cannot be `session/load`ed
//     (vibekit-acp.md), so the reader's own gate keeps it to a settled step and this
//     path answers `unavailable` if one slips through.

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

// stepTranscriptBudget bounds ONE step read end to end: the `session/load` RPC
// and the drain barrier after it.
//
// It exists because a bridge Call has no client-side timeout — the bridge's own
// contract, since an agent turn can legitimately run for hours — and this is a
// read a PERSON is waiting on behind an HTTP request. Without it a step whose
// session KAS cannot hydrate would hold the request until the client gave up and
// leave a replay open on the utility bridge behind it.
//
// 60s rather than the run surface's 45: a `session/load` replays a whole session's
// transcript, which is the same work `bridge.replayBudget` allows 300s for on the
// chat path. This is shorter on purpose — a step is one agent turn rather than a
// conversation, and the reader gets a refusal they can retry instead of a spinner.
//
// A `var` so a test can drive the expiry in milliseconds instead of waiting out a
// real minute, the same shape as healBaseDelay. Never reassigned in production.
var stepTranscriptBudget = 60 * time.Second

// errStepUnknown means the run's plan names no step at the requested path. A
// client error rather than a state of the world, so the handler answers 404 with
// it while every other outcome is a 200 carrying its own verdict.
var errStepUnknown = errors.New("this run has no step at that path")

// errRunStateUndecodable means the inspect reply could not be decoded, which is a
// state of the world rather than a client error — the requested path may name a
// real step in bytes this build cannot read. Kept apart from errStepUnknown for
// exactly that reason: one is a 404 and the other is an `unavailable` verdict, and
// collapsing them would blame the caller for a wire change.
var errRunStateUndecodable = errors.New("this run's state could not be decoded")

// StepTranscript reads one step's transcript out of KAS.
//
// The three-valued answer is the point: `ready` with the messages, `gone` when KAS
// no longer holds the session (or the step never started, so there is none), and
// `unavailable` when the read could not be completed. Only the last is worth
// retrying, and only the client can tell a reader which of the three happened.
//
// TWO RESIDUALS, recorded rather than fixed because each needs a measurement this
// change does not have:
//
//   - The loaded step session stays RESIDENT in the utility connection afterwards.
//     Bounded by that session's own recycle (20 prompts) and its 30-minute idle
//     cull, so it is not unbounded — but a `session/close` after the read is
//     unmeasured, and it is the follow-up if memory shows up.
//   - Reading N steps of one run hydrates N sessions into that process. Bounded by
//     the reader arming ONE read per selection rather than per repaint, which is the
//     client's gate, not this one's.
func (rs *Runs) StepTranscript(ctx context.Context, workflowID, nodePath string) (vibekit.RunStepTranscript, error) {
	out := vibekit.RunStepTranscript{
		Messages:   []vibekit.Message{},
		WorkflowID: workflowID,
		NodePath:   nodePath,
		State:      vibekit.RunStepTranscriptUnavailable,
	}
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		// Not distinguished from any other read failure: this endpoint's own
		// existence already implies a workflow engine, and a run whose state cannot
		// be read is a run whose step transcript cannot be read either. The RUN
		// endpoint is where a missing engine is reported.
		slog.Warn("step transcript: run state unreadable", "workflow_id", workflowID,
			"node_path", nodePath, "error", err, "detail", rpcerr.Details(err))
		return out, nil
	}
	// NO empty-reply check here, deliberately, and it is not an omission: an EMPTY
	// reply is the shape a KAS refusal arrives in, because utility_rpc.go's rawCall
	// returns `resp.Result, nil` and drops `resp.Error` — and the undecodable branch
	// below already answers for it, since json.Unmarshal of nothing is an error. A
	// length guard here would shadow that branch and be unfalsifiable: both arms
	// produce `unavailable`, so no test could tell them apart. The LOAD path below
	// does need its own, because there the two arms differ by a whole budget.
	// The same seed handleRun does, for the same reason: this is a SECOND door onto
	// one inspect reply, and the step→session registry is what attributes a
	// resumed run's frames after a restart emptied it.
	rs.translate.RecordRunSteps(raw)

	sessionID, err := stepSessionAt(raw, nodePath)
	if errors.Is(err, errRunStateUndecodable) {
		// Undecodable is not the caller's fault, so it must not answer 404: the path
		// may well name a real step in a reply this build cannot read.
		slog.Warn("step transcript: run state undecodable", "workflow_id", workflowID,
			"node_path", nodePath, "error", err)
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if sessionID == "" {
		// KAS records no session for a step that never ran, so there is nothing to
		// load and never was. The client's own `neverRan` arm normally answers
		// first; this is the belt-and-braces answer for a hand-made request.
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

// stepSessionAt resolves the ACP session a step path names, off one inspect reply.
//
// Returns errStepUnknown when the plan names no such path, and an EMPTY session id
// when it names a step that has not run — two different answers, which is why it
// reads workflow.Steps rather than StepSessions.
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

// replayStepSession loads one session on the utility bridge and returns what its
// replay projected.
//
// A RAW `session/load` Call rather than bridge.Start, and that is load-bearing:
// Start's own load path calls adoptLoadedSession, which REBINDS the bridge's
// sessionID — so starting a step's session here would make the utility bridge
// report a step's id as its own, which takes the real utility session out of the
// orphan reaper's keep-list. Verified in internal/bridge: Call is a plain
// JSON-RPC round trip and touches no session state.
//
// Params are `{sessionId}` alone. KAS answers a load by replaying the session as
// ordinary `session/update` notifications carrying THAT session's id, which the
// utility session's forward goroutine offers to the open replay before it warns
// about a foreign frame.
func (rs *Runs) replayStepSession(ctx context.Context, sessionID string) ([]vibekit.Message, vibekit.RunStepTranscriptState) {
	if !rs.stepReplays.open(sessionID) {
		// Another read of this same step is already in flight. Refused rather than
		// joined: two readers of one barrier is a lifecycle this registry does not
		// carry, and the retry meets a settled registry a moment later.
		slog.Debug("step transcript: a read of this session is already in flight", "session_id", sessionID)
		return nil, vibekit.RunStepTranscriptUnavailable
	}
	// Taken on EVERY path, including the budget expiry: take is the reader's own
	// cleanup, so an abandoned replay leaks neither a map entry nor a waiter.
	defer func() { _ = rs.stepReplays.take(sessionID) }()

	cctx, cancel := context.WithTimeout(ctx, stepTranscriptBudget)
	defer cancel()

	u := rs.utility()
	if u == nil {
		return nil, vibekit.RunStepTranscriptUnavailable
	}
	// An EMPTY result counts as a failure, and it is the LOAD's most likely one:
	// rawCall drops `resp.Error`, so KAS refusing to hydrate a session it no longer
	// holds — the reaped-session case this endpoint exists to report — arrives as
	// (nil, nil). Without this check the read would go on to wait out the whole
	// budget on a replay that is never coming, and answer `unavailable` a minute
	// later instead of immediately.
	raw, at, err := u.session.rawCallAt(cctx, "step transcript load", vibekit.MethodSessionLoad,
		callerParams(map[string]any{vibekit.KeySessionID: sessionID}))
	if err != nil || len(raw) == 0 {
		// KAS's own reason is machine prose (an id it does not hold reads as an
		// internal error with the detail buried in error.data), so it is logged and
		// NOT forwarded. The client's copy says what a reader can act on.
		slog.Warn("step transcript: session load failed", "session_id", sessionID,
			"error", err, "detail", rpcerr.Details(err))
		// UNAVAILABLE, never `gone`, and the distinction is not a technicality: KAS
		// answers an id it does not hold and a transient fault with the same -32603
		// shape, so grading this `gone` would be a guess — and the two want opposite
		// client behaviour, since only a transient fault is worth retrying. `gone` is
		// reserved for what this side can PROVE, which is a step with no session id.
		return nil, vibekit.RunStepTranscriptUnavailable
	}
	// rawCallAt rather than rawCall, for the position: the replay is complete when
	// the consumer has folded everything that preceded this response, and this is
	// the only place that number is known. Recording it also ATTEMPTS one settle,
	// which is what answers the common case — a replay already drained by the time
	// the RPC returned, whose barrier nothing else would close.
	rs.stepReplays.markLoadedAt(sessionID, at)

	// The barrier, not the RPC's return: the replay frames precede the result on
	// the wire and the notification channel is buffered, so the load returning says
	// nothing about whether the consumer has folded them. Already closed above in
	// the drained-early case. See step_replay.go.
	select {
	case <-rs.stepReplays.barrier(sessionID):
	case <-cctx.Done():
		slog.Warn("step transcript: the replay did not settle inside the budget",
			"session_id", sessionID, "budget", stepTranscriptBudget)
		return nil, vibekit.RunStepTranscriptUnavailable
	}

	return assistantRows(rs.stepReplays.take(sessionID)), vibekit.RunStepTranscriptReady
}

// assistantRows keeps the ASSISTANT rows of a projected transcript.
//
// A step's own prompt is already on screen as the pane's Instruction row
// (ExecRun.inputs) and its node plan, so replaying it here would render the same
// text twice; and the stream this feeds renders ONE assistant transcript. Event
// rows are dropped for the same reason — a step has no turn card to badge.
func assistantRows(msgs []vibekit.Message) []vibekit.Message {
	out := make([]vibekit.Message, 0, len(msgs))
	for i := range msgs {
		if msgs[i].Role == vibekit.RoleAssistant {
			out = append(out, msgs[i])
		}
	}
	return out
}
