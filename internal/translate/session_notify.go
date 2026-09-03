package translate

// `_kiro/session/notify`: KAS's `send_message` builtin, and the ONLY frame that
// carries a workflow step's question.
//
// The tool splits one `send_message(severity:"warning")` into three independent
// signals, and vibekit used to consume the two that carry no question text:
//
//   - the run PAUSE (`node_paused`, then a run-level `paused` whose `pauseReason`
//     is a fixed literal and whose `pauseDetail` is empty) — consumed, which is
//     why a parked run's card says a step wants input;
//   - this notification, carrying `message`, `severity`, `workflowId`, `nodeId`,
//     `agentName` and `callerSessionId` — DROPPED at the dispatcher's Debug tail,
//     which lost the question, its run and its step together;
//   - a copy appended to the launching chat's steering buffer, which reaches a
//     client only if that chat's model happens to READ it before the turn
//     boundary clears the buffer.
//
// So this file is the decode half of closing that: a PURE derivation from the
// frame to the ask, with no broadcast and no registry write. The host owns both
// (`internal/agent/run_ask.go`), because the ANSWER path and the settle event are
// the host's — it holds the bridges and arbitrates the take-once claim — and
// splitting one ask's lifecycle across two packages would put its two halves
// where neither could be read against the other.

import (
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// kasSessionNotify mirrors `_kiro/session/notify`'s params.
//
// `sessionId` is the TARGET (the launching chat's session, since the tool
// defaults its target to "parent") and is deliberately not decoded: the frame
// arrives on that session's own connection, so the dispatcher already knows the
// chat and re-deriving it from the payload would be a second, weaker answer.
//
// `callerSessionId` is the PAUSED STEP's session, which is what makes this frame
// self-addressing: a `session/prompt` sent there is rerouted into the run by
// KAS's own `tryResumeStepWithMessage`, so the answer needs no registry lookup on
// the happy path.
type kasSessionNotify struct {
	CallerSessionID string `json:"callerSessionId"`
	Message         string `json:"message"`
	Severity        string `json:"severity"`
	WorkflowID      string `json:"workflowId"`
	NodeID          string `json:"nodeId"`
	AgentName       string `json:"agentName"`
	NotifyID        string `json:"notifyId"`
}

// severityWarning is the ONE severity that parks a run.
//
// KAS's `writeCompletionSignal` maps `warning` to `need_input` and the run loop
// parks on that signal; `success` advances the step, `error` fails the node and
// `info` does nothing to the lifecycle. So the other three leave nobody waiting,
// and forwarding them would put a card on screen for a step that has already
// moved on.
const severityWarning = "warning"

// SessionNotifyAsk derives the run ask a notify frame carries, or reports false
// when the frame is not one.
//
// Three gates, each dropping a frame that has no answerable ask behind it: a
// severity other than `warning` (nothing is waiting), a missing `workflowId` (a
// plain cross-session note between two chats is not a run ask), and an empty
// `message` (an ask with no question and no run state to reconcile against is a
// card a reader cannot act on).
//
// The step's node id is taken from the frame when it carries one and resolved
// from the step-session registry otherwise, keyed on `callerSessionId` — the
// registry's other consumer, and the same reason it exists: a frame that is not a
// `session/update` carries no workflow marker, so the session id is the only
// handle.
//
// Returns the payload rather than broadcasting it, so this derivation can be
// tested against a frame with no host at all.
func (t *Translator) SessionNotifyAsk(msg *vibekit.RPCResponse) (vibekit.RunInputNeededPayload, bool) {
	p, ok := unmarshalParams[kasSessionNotify](msg, "_kiro/session/notify")
	if !ok || p.Severity != severityWarning || p.WorkflowID == "" || p.Message == "" {
		return vibekit.RunInputNeededPayload{}, false
	}
	node := p.NodeID
	if node == "" {
		node = t.steps.refFor(p.CallerSessionID).NodeID
	}
	return vibekit.RunInputNeededPayload{
		WorkflowID: p.WorkflowID,
		// The notification's own id when KAS sent one, so a repeat pass of a loop
		// body cannot collide with the previous pass's ask; the caller session plus
		// the message otherwise, which is stable for a redelivered frame and
		// distinct for a second question from the same step.
		AskID:         askIDOf(&p),
		NodeID:        node,
		StepSessionID: p.CallerSessionID,
		AgentName:     p.AgentName,
		Question:      p.Message,
		AskedAt:       time.Now().UTC().Format(time.RFC3339),
	}, true
}

// askIDOf composes a live ask's id.
//
// keyenc rather than a template join: every part is arbitrary text off the wire
// (a message is the step agent's own prose), and a separator inside one of them
// would let two different asks produce one id.
//
// It is DETERMINISTIC in both spellings, which is what makes a redelivered frame
// idempotent: KAS's notification bridge can replay one, and an id derived from
// the clock would stack a second card for the same question.
func askIDOf(p *kasSessionNotify) string {
	if p.NotifyID != "" {
		return keyenc.Join("notify", p.NotifyID)
	}
	return keyenc.Join("ask", p.CallerSessionID, p.Message)
}
