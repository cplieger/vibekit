package translate

// The per-step turn cap (D56b). The counter lives here because a step's tool
// frames pass through this package and `_meta.kiro.workflow` is what identifies
// them; the enforcement is the host's, so these tests assert what the host is
// TOLD rather than what it does about it.

import (
	"strconv"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// stepToolFrame builds one workflow-step tool_call frame. nodePath is what makes
// a step instance unique, so it is a parameter rather than derived from nodeID: a
// repeat node's second iteration shares the node id and must not share a count.
func stepToolFrame(id, workflowID, nodeID string, nodePath []string) map[string]any {
	return map[string]any{
		"toolCallId": id,
		"title":      "Read a file",
		"kind":       "read",
		"status":     "pending",
		"_meta": map[string]any{"kiro": map[string]any{
			"workflow": map[string]any{
				"workflowId": workflowID,
				"nodeId":     nodeID,
				"nodePath":   nodePath,
				"type":       "step",
			},
		}},
	}
}

// TestStepTurnCap_ReportsOnceAtTheCap is the arithmetic the whole cap rests on.
//
// Once matters more than the threshold. A cancel is decided at a node boundary,
// so a runaway step keeps emitting tool frames while the cancel travels — with a
// `>=` comparison every one of those frames would report a fresh breach, and the
// host would issue a cancel per frame for a run it is already cancelling.
func TestStepTurnCap_ReportsOnceAtTheCap(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
	chatID := vibekit.ChatID("c1")
	path := []string{"wf", "step-a"}

	for i := range StepTurnCap + 5 {
		tr.HandleToolCall(t.Context(), chatID,
			mustJSON(t, stepToolFrame("tc-"+strconv.Itoa(i), "wf_1", "step-a", path)), FrameAttribution{})
	}

	if len(deps.stepCapBreaches) != 1 {
		t.Fatalf("breaches reported = %d, want exactly 1", len(deps.stepCapBreaches))
	}
	got := deps.stepCapBreaches[0]
	if got.workflowID != "wf_1" || got.nodeID != "step-a" {
		t.Errorf("breach named run %q node %q, want wf_1 / step-a", got.workflowID, got.nodeID)
	}
	if got.turns != StepTurnCap {
		t.Errorf("breach reported %d turns, want the cap %d", got.turns, StepTurnCap)
	}
}

// TestStepTurnCap_StaysSilentBelowTheCap: the cap must not fire for ordinary
// work, and 199 tool calls in one step is ordinary work.
func TestStepTurnCap_StaysSilentBelowTheCap(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))

	for i := range StepTurnCap - 1 {
		tr.HandleToolCall(t.Context(), vibekit.ChatID("c1"),
			mustJSON(t, stepToolFrame("tc-"+strconv.Itoa(i), "wf_1", "step-a", []string{"wf", "step-a"})), FrameAttribution{})
	}
	if len(deps.stepCapBreaches) != 0 {
		t.Errorf("a step one call short of the cap reported %d breaches", len(deps.stepCapBreaches))
	}
}

// TestStepTurnCap_CountsPerStepInstance pins the key.
//
// Two iterations of one repeat node share a NodeID and are separate work; a
// counter keyed on the node id would add them together and cap a two-iteration
// loop at half its allowance. Two different steps of one run must not pool
// either.
func TestStepTurnCap_CountsPerStepInstance(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
	chatID := vibekit.ChatID("c1")

	half := StepTurnCap / 2
	for i := range half {
		// Same run, same node id, DIFFERENT iteration.
		tr.HandleToolCall(t.Context(), chatID,
			mustJSON(t, stepToolFrame("a-"+strconv.Itoa(i), "wf_1", "loop-step", []string{"wf", "loop", "iter-0", "loop-step"})), FrameAttribution{})
		tr.HandleToolCall(t.Context(), chatID,
			mustJSON(t, stepToolFrame("b-"+strconv.Itoa(i), "wf_1", "loop-step", []string{"wf", "loop", "iter-1", "loop-step"})), FrameAttribution{})
		// Same run, a different step entirely.
		tr.HandleToolCall(t.Context(), chatID,
			mustJSON(t, stepToolFrame("c-"+strconv.Itoa(i), "wf_1", "other", []string{"wf", "other"})), FrameAttribution{})
	}

	if len(deps.stepCapBreaches) != 0 {
		t.Errorf("three separate step instances at %d calls each tripped the cap %d times",
			half, len(deps.stepCapBreaches))
	}
}

// TestStepTurnCap_IgnoresANonStepToolCall: an ordinary chat tool call and a
// SUBAGENT's carry no workflow block, so neither may consume a step's allowance
// or trip a cap for a run that does not exist.
func TestStepTurnCap_IgnoresANonStepToolCall(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
	chatID := vibekit.ChatID("c1")

	for i := range StepTurnCap + 5 {
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-" + strconv.Itoa(i),
			"title":      "Read a file",
			"kind":       "read",
			"status":     "pending",
			"_meta":      map[string]any{"kiro": map[string]any{"agentSubtaskId": "sub-1"}},
		}), FrameAttribution{})
	}
	if len(deps.stepCapBreaches) != 0 {
		t.Errorf("a subagent's tool calls reported %d step breaches", len(deps.stepCapBreaches))
	}
}

// TestStepTurnCap_CountsPerRunNotPerNodePath is the collision the turn key's
// workflow-id half exists to prevent.
//
// A node path is only unique WITHIN a run — `wf:<nodePath>` names no run at all —
// so two concurrent workflows executing the same path pooled their counts. Their
// combined calls reached the cap and cancelled a run whose step had made only half
// of them, and the other run could then step over the cap without ever landing on
// it, which left it unbounded. Interleaved rather than sequential, because that is
// the shape two live runs actually produce.
func TestStepTurnCap_CountsPerRunNotPerNodePath(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
	chatID := vibekit.ChatID("c1")
	// One shared path. Two DIFFERENT runs — an agent-launched run reaches KAS
	// directly, so the single-run-per-recipe rule does not keep these apart.
	path := []string{"wf", "step-a"}

	for i := range StepTurnCap {
		tr.HandleToolCall(t.Context(), chatID,
			mustJSON(t, stepToolFrame("a-"+strconv.Itoa(i), "wf_1", "step-a", path)), FrameAttribution{})
		tr.HandleToolCall(t.Context(), chatID,
			mustJSON(t, stepToolFrame("b-"+strconv.Itoa(i), "wf_2", "step-a", path)), FrameAttribution{})
	}

	// Each run reached the cap on its own, so each reports exactly once — and
	// crucially neither reported before its own 200th call.
	if len(deps.stepCapBreaches) != 2 {
		t.Fatalf("breaches = %d, want one per run", len(deps.stepCapBreaches))
	}
	seen := map[string]int{}
	for _, b := range deps.stepCapBreaches {
		seen[b.workflowID]++
		if b.turns != StepTurnCap {
			t.Errorf("run %s reported %d turns, want the cap %d", b.workflowID, b.turns, StepTurnCap)
		}
	}
	if seen["wf_1"] != 1 || seen["wf_2"] != 1 {
		t.Errorf("breaches per run = %v, want one each", seen)
	}
}

// TestStepTurnCap_HalfTheCapEachDoesNotTripEitherRun is the same collision read
// from the other side, and it is the one that fails LOUDLY on a shared counter: two
// runs at 100 calls apiece sum to the cap, so a key that omits the run cancels a
// run whose step is only half way through its allowance.
func TestStepTurnCap_HalfTheCapEachDoesNotTripEitherRun(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
	chatID := vibekit.ChatID("c1")
	path := []string{"wf", "step-a"}

	for i := range StepTurnCap / 2 {
		tr.HandleToolCall(t.Context(), chatID,
			mustJSON(t, stepToolFrame("a-"+strconv.Itoa(i), "wf_1", "step-a", path)), FrameAttribution{})
		tr.HandleToolCall(t.Context(), chatID,
			mustJSON(t, stepToolFrame("b-"+strconv.Itoa(i), "wf_2", "step-a", path)), FrameAttribution{})
	}
	if len(deps.stepCapBreaches) != 0 {
		t.Errorf("two runs at half the cap each reported %d breaches", len(deps.stepCapBreaches))
	}
}

// TestStepTurnCap_ForgettingOneRunLeavesTheSiblings: `run_complete` for one run
// drops its counts, and a bare node-path key made that reach into a concurrent
// run's — resetting a sibling step's allowance mid-flight.
func TestStepTurnCap_ForgettingOneRunLeavesTheSiblings(t *testing.T) {
	t.Parallel()
	reg := newStepRegistry()

	reg.countTurn("wf_1", "wf:step-a")
	reg.countTurn("wf_2", "wf:step-a")
	reg.countTurn("wf_2", "wf:step-a")

	reg.forgetRun("wf_1")

	if got := reg.countTurn("wf_2", "wf:step-a"); got != 3 {
		t.Errorf("the sibling run's count resumed at %d, want 3; forgetting one run reset another", got)
	}
	if got := reg.countTurn("wf_1", "wf:step-a"); got != 1 {
		t.Errorf("the forgotten run's count resumed at %d, want 1", got)
	}
}

// TestStepTurnCap_ForgetsATerminatedRunsCounts pins the bound on the counter's
// own growth: run_complete is the hook, the same frame that drops the run's
// session mappings, because no later frame for that run can arrive.
func TestStepTurnCap_ForgetsATerminatedRunsCounts(t *testing.T) {
	t.Parallel()
	reg := newStepRegistry()

	if got := reg.countTurn("wf_1", "wf:a"); got != 1 {
		t.Errorf("first turn counted as %d, want 1", got)
	}
	if got := reg.countTurn("wf_1", "wf:a"); got != 2 {
		t.Errorf("second turn counted as %d, want 2", got)
	}
	reg.forgetRun("wf_1")
	if got := reg.countTurn("wf_1", "wf:a"); got != 1 {
		t.Errorf("after forgetRun the count resumed at %d; the counts leaked past the run", got)
	}
	// A missing identity is not the first turn of a real step: answering 1 here
	// would let an unidentifiable frame accumulate toward somebody's cap.
	if got := reg.countTurn("", "wf:a"); got != 0 {
		t.Errorf("countTurn with no run id = %d, want 0", got)
	}
	if got := reg.countTurn("wf_1", ""); got != 0 {
		t.Errorf("countTurn with no step key = %d, want 0", got)
	}
}

// A step's ask is attributed to its run, and the attribution is the registry
// lookup: the run id is what lets a run tab render an ask that arrived on
// another surface, and the node id is what makes the card say who is asking. A
// frame with no session id is not a step and stamps two empty strings, which is
// why the miss is not an error. Observed through the permission card, the
// surface the ref actually reaches.
func TestStepRef_AttributesAnAskToItsRun(t *testing.T) {
	tests := []struct {
		name       string
		sessionID  string
		wantRunID  string
		wantNodeID string
	}{
		{name: "a_registered_step_session", sessionID: "sess_step", wantRunID: "wf_1", wantNodeID: "build"},
		{name: "no_session_id_at_all", sessionID: "", wantRunID: "", wantNodeID: ""},
		{name: "a_session_nothing_announced", sessionID: "sess_stranger", wantRunID: "", wantNodeID: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			tr.RecordStepSession("sess_step", "wf_1", "build")

			id := int64(7)
			tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
				ID: &id,
				Params: mustJSON(t, map[string]any{
					"sessionId": tc.sessionID,
					"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "Read a file", "kind": "read"},
					"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
				}),
			})

			got, ok := findPermissionNeeded(t, events)
			if !ok {
				t.Fatal("no permission_needed event broadcast")
			}
			if got.RunID != tc.wantRunID {
				t.Errorf("permission_needed RunID for sessionId %q = %q, want %q", tc.sessionID, got.RunID, tc.wantRunID)
			}
			if got.NodeID != tc.wantNodeID {
				t.Errorf("permission_needed NodeID for sessionId %q = %q, want %q", tc.sessionID, got.NodeID, tc.wantNodeID)
			}
		})
	}
}
