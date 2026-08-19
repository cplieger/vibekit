package workflow

// Tests for the two questions this package answers, both grounded in a REAL
// `_kiro/workflow/inspect` response rather than an invented shape.
//
// The tree below is trimmed from a measured probe result: a `parallel` with two
// branch steps, then a `repeat` whose two iterations each contain one step under
// the SAME node id, then a pending step. That shape is what proves the two
// non-obvious facts the package rests on — every step node carries its own
// `sessionId` (so there is nothing to join), and a repeat's iterations share a
// node id (so the id alone cannot address a step).

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// realInspect is trimmed from a measured inspect result. Fields this package
// does not decode are kept so the test also proves they are tolerated.
const realInspect = `{
  "workflowId": "wf_4bdc8cd5",
  "state": {
    "workflowId": "wf_4bdc8cd5",
    "workflowName": "probe-parallel-repeat",
    "status": "paused",
    "pauseReason": "Repeat 'loop' reached maxIterations.",
    "parentSessionId": "sess_parent",
    "root": {
      "nodeId": "wf_4bdc8cd5", "type": "sequence", "status": "paused",
      "children": [
        {"nodeId": "par", "type": "parallel", "status": "completed", "children": [
          {"nodeId": "pa", "type": "step", "status": "completed", "agentName": "probe-tool",
           "branchId": "pa", "sessionId": "sess_pa", "capturedOutput": ""},
          {"nodeId": "pb", "type": "step", "status": "completed", "agentName": "probe-tool",
           "branchId": "pb", "sessionId": "sess_pb"}
        ]},
        {"nodeId": "loop", "type": "repeat", "status": "paused", "children": [
          {"nodeId": "loop#0", "type": "sequence", "status": "completed", "iteration": 0, "children": [
            {"nodeId": "iter", "type": "step", "status": "completed", "iteration": 0, "sessionId": "sess_i0"}
          ]},
          {"nodeId": "loop#1", "type": "sequence", "status": "completed", "iteration": 1, "children": [
            {"nodeId": "iter", "type": "step", "status": "completed", "iteration": 1, "sessionId": "sess_i1"}
          ]}
        ]},
        {"nodeId": "replaced", "type": "step", "status": "pending", "agentName": "probe-tool"}
      ]
    }
  },
  "nodePlan": [{"nodeId": "par", "type": "parallel"}]
}`

func TestStepSessions_WalksARealTree(t *testing.T) {
	t.Parallel()
	var res InspectResult
	if err := json.Unmarshal([]byte(realInspect), &res); err != nil {
		t.Fatalf("decode inspect: %v", err)
	}
	got := StepSessions(res.State)

	// Depth-first declaration order, and ONLY step nodes that actually ran: the
	// containers have no session and `replaced` is still pending.
	want := []StepSession{
		{NodeID: "pa", SessionID: "sess_pa"},
		{NodeID: "pb", SessionID: "sess_pb"},
		{NodeID: "iter", SessionID: "sess_i0"},
		{NodeID: "iter", SessionID: "sess_i1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d step sessions, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestStepSessions_TheTreeIsTheJoin pins the fact that removed a whole planned
// component: an earlier design joined a separate `stepSessions[]` array to the
// node plan by (nodeId, iteration, branchId). No such array exists — the session
// id is ON the step node.
func TestStepSessions_TheTreeIsTheJoin(t *testing.T) {
	t.Parallel()
	var res InspectResult
	if err := json.Unmarshal([]byte(realInspect), &res); err != nil {
		t.Fatal(err)
	}
	// Two iterations of ONE node id, each with its own session. Nothing outside
	// the tree was consulted to learn that.
	seen := map[string]int{}
	for _, s := range StepSessions(res.State) {
		seen[s.NodeID]++
	}
	if seen["iter"] != 2 {
		t.Errorf("node `iter` ran %d times, want 2 (a repeat's iterations share a node id)", seen["iter"])
	}
}

func TestStepSessions_EmptyInputs(t *testing.T) {
	t.Parallel()
	if got := StepSessions(nil); got != nil {
		t.Errorf("StepSessions(nil) = %+v, want nil", got)
	}
	if got := StepSessions(&State{}); got != nil {
		t.Errorf("StepSessions(no root) = %+v, want nil", got)
	}
	// A step that has not started has no session and must not be reported: a
	// caller would try to address a session that does not exist.
	only := &State{Root: &Node{NodeID: "r", Type: "sequence", Children: []Node{
		{NodeID: "a", Type: "step"},
	}}}
	if got := StepSessions(only); got != nil {
		t.Errorf("StepSessions(pending step) = %+v, want nil", got)
	}
}

// rpcErr builds the error shape a KAS failure arrives as.
func rpcErr(message, data string) error {
	e := &vibekit.RPCError{Code: -32603, Message: message}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

func TestClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			// The measured shape: message is the literal "Internal error" and the
			// classifier's text is in data. Reading message alone cannot tell an
			// unregistered verb from a genuine failure.
			"an unregistered verb",
			rpcErr("Internal error", `{"details":"[PersistenceClassification] Ext method _kiro/workflow/nope has no persistence classification"}`),
			true,
		},
		{"a real failure with details", rpcErr("Internal error", `{"details":"workflow not found"}`), false},
		{"a param error with no data at all", rpcErr("workspacePaths is not iterable", ""), false},
		{"a plain error", errors.New("boom"), false},
		{
			// The narrowing: an error that merely QUOTES the marker in its own text
			// is a failure, not an unregistered verb. The rendered-chain sniff this
			// replaced called it one.
			"a failure quoting the marker in its message",
			errors.New("workflow inspect call: _kiro/workflow/inspect has no persistence classification"),
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := Classify(c.err)
			if isUnknown := errors.Is(got, ErrUnknownMethod); isUnknown != c.want {
				t.Errorf("errors.Is(Classify(%v), ErrUnknownMethod) = %v, want %v", c.err, isUnknown, c.want)
			}
			if c.err == nil {
				if got != nil {
					t.Errorf("Classify(nil) = %v, want nil", got)
				}
				return
			}
			// The original error stays reachable whichever way it was classified,
			// so a caller can still report what KAS actually said.
			if !errors.Is(got, c.err) {
				t.Errorf("Classify(%v) = %v, want the original error still unwrappable", c.err, got)
			}
		})
	}
}
