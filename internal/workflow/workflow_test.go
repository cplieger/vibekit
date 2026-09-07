package workflow

// The fixture below is trimmed from a real `_kiro/workflow/inspect` response
// because its shape carries the three facts the package rests on: every step node
// has its own `sessionId`, a repeat's iterations share one node id, and the wire
// names the iteration container `iter-0` where the tree says `loop#0`.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// realInspect keeps fields this package does not decode, so the test also proves
// they are tolerated.
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
            {"nodeId": "iter", "type": "step", "status": "completed", "iteration": 0, "sessionId": "sess_i0"},
            {"nodeId": "review", "type": "step", "status": "completed", "iteration": 0, "sessionId": "sess_r0"}
          ]},
          {"nodeId": "loop#1", "type": "sequence", "status": "completed", "iteration": 1, "children": [
            {"nodeId": "iter", "type": "step", "status": "completed", "iteration": 1, "sessionId": "sess_i1"},
            {"nodeId": "review", "type": "step", "status": "completed", "iteration": 1, "sessionId": "sess_r1"}
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

	// The PATHS are the load-bearing half: `iter-0`/`iter-1` where the tree says
	// `loop#0`/`loop#1`, while the step named `iter` keeps its own NODE ID because
	// the translation is gated on the PARENT being the repeat.
	want := []StepSession{
		{NodeID: "pa", SessionID: "sess_pa", Path: []string{"wf_4bdc8cd5", "par", "pa"}},
		{NodeID: "pb", SessionID: "sess_pb", Path: []string{"wf_4bdc8cd5", "par", "pb"}},
		{NodeID: "iter", SessionID: "sess_i0", Path: []string{"wf_4bdc8cd5", "loop", "iter-0", "iter"}},
		{NodeID: "review", SessionID: "sess_r0", Path: []string{"wf_4bdc8cd5", "loop", "iter-0", "review"}},
		{NodeID: "iter", SessionID: "sess_i1", Path: []string{"wf_4bdc8cd5", "loop", "iter-1", "iter"}},
		{NodeID: "review", SessionID: "sess_r1", Path: []string{"wf_4bdc8cd5", "loop", "iter-1", "review"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d step sessions, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].NodeID != want[i].NodeID || got[i].SessionID != want[i].SessionID {
			t.Errorf("step %d = %s/%s, want %s/%s",
				i, got[i].NodeID, got[i].SessionID, want[i].NodeID, want[i].SessionID)
		}
		if !slices.Equal(got[i].Path, want[i].Path) {
			t.Errorf("step %d path = %v, want %v", i, got[i].Path, want[i].Path)
		}
	}
}

// TestStepSessions_EveryPathIsDistinct pins that a path names one EXECUTION. The
// table above compares each row to its own expectation, so it would still pass if
// two rows shared a path.
func TestStepSessions_EveryPathIsDistinct(t *testing.T) {
	t.Parallel()
	var res InspectResult
	if err := json.Unmarshal([]byte(realInspect), &res); err != nil {
		t.Fatal(err)
	}
	got := StepSessions(res.State)
	seen := map[string]string{}
	for _, s := range got {
		key := strings.Join(s.Path, "/")
		if prev, dup := seen[key]; dup {
			t.Errorf("path %q addresses two sessions (%s and %s)", key, prev, s.SessionID)
		}
		seen[key] = s.SessionID
	}
	if len(seen) != len(got) {
		t.Errorf("%d distinct paths for %d step sessions", len(seen), len(got))
	}
}

// TestStepSessions_TheTreeIsTheJoin pins that the session id is ON the step node:
// there is no separate array to join by (nodeId, iteration, branchId).
func TestStepSessions_TheTreeIsTheJoin(t *testing.T) {
	t.Parallel()
	var res InspectResult
	if err := json.Unmarshal([]byte(realInspect), &res); err != nil {
		t.Fatal(err)
	}
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
	// An unstarted step has no session: reporting one makes a caller address nothing.
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
			// Measured: message is the literal "Internal error" and the classifier's
			// text is in data, so message alone cannot tell this from a real failure.
			"an unregistered verb",
			rpcErr("Internal error", `{"details":"[PersistenceClassification] Ext method _kiro/workflow/nope has no persistence classification"}`),
			true,
		},
		{"a real failure with details", rpcErr("Internal error", `{"details":"workflow not found"}`), false},
		{"a param error with no data at all", rpcErr("workspacePaths is not iterable", ""), false},
		{"a plain error", errors.New("boom"), false},
		{
			// An error that merely QUOTES the marker in its own text is a failure,
			// not an unregistered verb.
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
			// The original stays reachable either way, so a caller can still report
			// what KAS said.
			if !errors.Is(got, c.err) {
				t.Errorf("Classify(%v) = %v, want the original error still unwrappable", c.err, got)
			}
		})
	}
}

// TestSteps_IncludesAStepThatHasNotRun pins the one thing Steps adds over
// StepSessions: a path naming a PENDING step must be answerable, because that is a
// different answer from a path naming nothing at all.
func TestSteps_IncludesAStepThatHasNotRun(t *testing.T) {
	t.Parallel()
	var res InspectResult
	if err := json.Unmarshal([]byte(realInspect), &res); err != nil {
		t.Fatal(err)
	}
	all := Steps(res.State)
	ran := StepSessions(res.State)
	if len(all) != len(ran)+1 {
		t.Fatalf("Steps returned %d and StepSessions %d, want exactly one more (the pending step)",
			len(all), len(ran))
	}
	var pending *StepSession
	for i := range all {
		if all[i].NodeID == "replaced" {
			pending = &all[i]
		}
	}
	if pending == nil {
		t.Fatal("the pending step is absent from Steps")
	}
	if pending.SessionID != "" {
		t.Errorf("pending step SessionID = %q, want empty", pending.SessionID)
	}
	if !slices.Equal(pending.Path, []string{"wf_4bdc8cd5", "replaced"}) {
		t.Errorf("pending step path = %v, want [wf_4bdc8cd5 replaced]", pending.Path)
	}
	// It must stay out of the SESSION list, or RecordRunSteps addresses nothing.
	for _, s := range ran {
		if s.NodeID == "replaced" {
			t.Error("a step with no session reached StepSessions")
		}
	}
}

// TestSteps_EmptyInputs pins that the unfiltered door tolerates a missing tree.
func TestSteps_EmptyInputs(t *testing.T) {
	t.Parallel()
	if got := Steps(nil); got != nil {
		t.Errorf("Steps(nil) = %+v, want nil", got)
	}
	if got := Steps(&State{}); got != nil {
		t.Errorf("Steps(no root) = %+v, want nil", got)
	}
}
