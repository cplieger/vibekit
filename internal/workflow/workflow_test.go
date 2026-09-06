package workflow

// Tests for the two questions this package answers, both grounded in a REAL
// `_kiro/workflow/inspect` response rather than an invented shape.
//
// The tree below is trimmed from a measured probe result: a `parallel` with two
// branch steps, then a `repeat` whose two iterations each contain the SAME two
// step ids, then a pending step. That shape is what proves the three non-obvious
// facts the package rests on — every step node carries its own `sessionId` (so
// there is nothing to join), a repeat's iterations share a node id (so the id
// alone cannot address a step), and the iteration container's own node id
// (`loop#0`) is NOT what the wire calls it (`iter-0`).
//
// The measured live shape it mirrors, off this container's own volume:
// a step session's `_meta.kiro.workflow.nodePath` reads
// `["wf_a1b6513cf89ca106", "fu2-loop", "iter-1", "fu2-code"]` while its tree
// spells that third segment `fu2-loop#1`.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
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

	// Depth-first declaration order, and ONLY step nodes that actually ran: the
	// containers have no session and `replaced` is still pending.
	//
	// The PATHS are the load-bearing half. Note `iter-0`/`iter-1` where the tree
	// says `loop#0`/`loop#1`, and note that the step `iter` carries its own
	// `iteration` field and still contributes its NODE ID: the translation is
	// gated on the PARENT being the repeat, so only the iteration container is
	// rewritten.
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

// TestStepSessions_EveryPathIsDistinct is the property the whole feature rests on:
// a path names one EXECUTION. Asserted separately from the table above because the
// table would still pass if two rows happened to share a path — it compares each
// row against its own expectation and never the rows against each other.
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

// TestSteps_IncludesAStepThatHasNotRun pins the one thing Steps adds over
// StepSessions, and why the distinction is worth a second door: a path naming a
// PENDING step must be answerable, because a caller serving one step's transcript
// says something different for that than for a path naming nothing at all.
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
	// And it must stay out of the SESSION list, or RecordRunSteps would try to
	// address a session that does not exist.
	for _, s := range ran {
		if s.NodeID == "replaced" {
			t.Error("a step with no session reached StepSessions")
		}
	}
}

// TestSteps_EmptyInputs mirrors StepSessions' own empty cases: the unfiltered door
// must be as tolerant of a missing tree as the filtered one.
func TestSteps_EmptyInputs(t *testing.T) {
	t.Parallel()
	if got := Steps(nil); got != nil {
		t.Errorf("Steps(nil) = %+v, want nil", got)
	}
	if got := Steps(&State{}); got != nil {
		t.Errorf("Steps(no root) = %+v, want nil", got)
	}
}
