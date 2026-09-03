package translate

// Tests for the ONE frame that carries a workflow step's question.
//
// Every case here pins a gate rather than a rendering, because the gates are what
// the defect was: this frame used to fall through the dispatcher's Debug tail, and
// the three severities that park nothing must not now start producing cards for
// steps that have already moved on.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// notifyParams is the shape KAS puts on `_kiro/session/notify`, with the values a
// test wants to vary.
type notifyParams struct {
	severity   string
	workflowID string
	nodeID     string
	agentName  string
	message    string
	caller     string
	notifyID   string
}

func notifyFrame(p notifyParams) *vibekit.RPCResponse {
	return notif("_kiro/session/notify", map[string]any{
		"sessionId":       testParent,
		"callerSessionId": p.caller,
		"message":         p.message,
		"severity":        p.severity,
		"workflowId":      p.workflowID,
		"nodeId":          p.nodeID,
		"agentName":       p.agentName,
		"notifyId":        p.notifyID,
	})
}

func TestSessionNotifyAsk_Gates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params notifyParams
		wantOK bool
	}{
		{
			// The one severity that parks a run: KAS maps `warning` to the
			// `need_input` completion signal and the run loop stops on it.
			name:   "a warning with a run and a message is an ask",
			params: notifyParams{severity: "warning", workflowID: "wf_1", message: "which branch?"},
			wantOK: true,
		},
		{
			// `info` does nothing to the lifecycle, so nobody is waiting. A card
			// for it would ask the reader to answer a step that has moved on.
			name:   "info parks nothing, so it is not an ask",
			params: notifyParams{severity: "info", workflowID: "wf_1", message: "halfway"},
		},
		{
			// `success` ADVANCES the step. Same reasoning, opposite direction.
			name:   "success advances the step",
			params: notifyParams{severity: "success", workflowID: "wf_1", message: "done"},
		},
		{
			// `error` FAILS the node with a reason. The failure reaches the card
			// through the run's own state, not through an answerable ask.
			name:   "error fails the node",
			params: notifyParams{severity: "error", workflowID: "wf_1", message: "broke"},
		},
		{
			// `send_message` is also a plain cross-session note between two chats.
			// Without a run there is nothing to park and nothing to answer into.
			name:   "no workflow id is a cross-session note, not a run ask",
			params: notifyParams{severity: "warning", message: "hello"},
		},
		{
			// An ask with no question and no run state behind it is a card a
			// reader cannot act on. The restart-reconcile path mints the
			// empty-question ask deliberately; this frame is not that path.
			name:   "an empty message carries no question",
			params: notifyParams{severity: "warning", workflowID: "wf_1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := New(rolesOf(newBaseDeps()))
			got, ok := tr.SessionNotifyAsk(notifyFrame(tc.params))
			if ok != tc.wantOK {
				t.Fatalf("SessionNotifyAsk(%+v) ok = %v, want %v", tc.params, ok, tc.wantOK)
			}
			if !ok && got.WorkflowID != "" {
				t.Errorf("SessionNotifyAsk(%+v) returned a payload on a dropped frame: %+v",
					tc.params, got)
			}
		})
	}
}

func TestSessionNotifyAsk_CarriesTheAnswerAddress(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	got, ok := tr.SessionNotifyAsk(notifyFrame(notifyParams{
		severity:   "warning",
		workflowID: "wf_1",
		nodeID:     "review",
		agentName:  "reviewer",
		message:    "which branch should I target?",
		caller:     testStep,
		notifyID:   "n1",
	}))
	if !ok {
		t.Fatal("SessionNotifyAsk(a warning) ok = false, want true")
	}
	// callerSessionId is the PAUSED STEP's session, and a session/prompt sent
	// there is what KAS reroutes into the run. Losing it would leave the answer
	// path with nothing to address but a fresh inspect round trip.
	if got.StepSessionID != testStep {
		t.Errorf("StepSessionID = %q, want %q", got.StepSessionID, testStep)
	}
	if got.WorkflowID != "wf_1" || got.NodeID != "review" || got.AgentName != "reviewer" {
		t.Errorf("SessionNotifyAsk = %+v, want wf_1/review/reviewer", got)
	}
	if got.Question != "which branch should I target?" {
		t.Errorf("Question = %q, want the message verbatim", got.Question)
	}
	if got.AskID == "" {
		t.Error("AskID = \"\", want an id an answer can name")
	}
	if got.AskedAt == "" {
		t.Error("AskedAt = \"\", want a timestamp")
	}
}

func TestSessionNotifyAsk_ResolvesTheNodeFromTheStepRegistry(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	// node_start is the only frame that announces a step's session, and the
	// registry is what a frame with no workflow marker is resolved through.
	tr.RecordStepSession(testStep, "wf_1", "review")

	got, ok := tr.SessionNotifyAsk(notifyFrame(notifyParams{
		severity:   "warning",
		workflowID: "wf_1",
		message:    "which branch?",
		caller:     testStep,
	}))
	if !ok {
		t.Fatal("SessionNotifyAsk(a warning) ok = false, want true")
	}
	if got.NodeID != "review" {
		t.Errorf("NodeID = %q, want %q resolved from the step registry", got.NodeID, "review")
	}
}

func TestSessionNotifyAsk_NoNodeIsStillAnAsk(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	// A cold registry after a restart, and a frame KAS sent without a nodeId. The
	// run is blocked either way, so the ask must survive with an empty node — the
	// count is what the card renders, and the ROW is what it cannot mark.
	got, ok := tr.SessionNotifyAsk(notifyFrame(notifyParams{
		severity:   "warning",
		workflowID: "wf_1",
		message:    "which branch?",
		caller:     "sess_unknown",
	}))
	if !ok {
		t.Fatal("SessionNotifyAsk(a warning with no resolvable node) ok = false, want true")
	}
	if got.NodeID != "" {
		t.Errorf("NodeID = %q, want \"\" when nothing can resolve it", got.NodeID)
	}
}

func TestSessionNotifyAsk_IDIsStableForOneFrame(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	p := notifyParams{
		severity: "warning", workflowID: "wf_1", message: "which branch?",
		caller: testStep, notifyID: "n1",
	}
	first, ok1 := tr.SessionNotifyAsk(notifyFrame(p))
	second, ok2 := tr.SessionNotifyAsk(notifyFrame(p))
	if !ok1 || !ok2 {
		t.Fatal("SessionNotifyAsk twice: want both ok")
	}
	// Deterministic, so a redelivered frame de-duplicates against the entry the
	// first delivery recorded. An id derived from the clock would stack a second
	// card for one question.
	if first.AskID != second.AskID {
		t.Errorf("AskID = %q then %q, want one stable id per frame", first.AskID, second.AskID)
	}
	// And a second, different question from the same step gets its own id, or one
	// answer would retire both.
	p.message = "and which reviewer?"
	p.notifyID = "n2"
	third, _ := tr.SessionNotifyAsk(notifyFrame(p))
	if third.AskID == first.AskID {
		t.Errorf("two questions share AskID %q, want distinct ids", third.AskID)
	}
}

func TestSessionNotifyAsk_IDDistinguishesTwoQuestionsWithoutANotifyID(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	base := notifyParams{severity: "warning", workflowID: "wf_1", caller: testStep}
	base.message = "which branch?"
	first, _ := tr.SessionNotifyAsk(notifyFrame(base))
	base.message = "which reviewer?"
	second, _ := tr.SessionNotifyAsk(notifyFrame(base))
	if first.AskID == second.AskID {
		t.Errorf("two questions share AskID %q with no notifyId, want distinct ids", first.AskID)
	}
}
