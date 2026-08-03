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

	"github.com/cplieger/vibekit/internal/api"
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
	e := &api.RPCError{Code: -32603, Message: message}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

func TestIsUnknownMethod(t *testing.T) {
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := IsUnknownMethod(c.err); got != c.want {
				t.Errorf("IsUnknownMethod(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestDetails pins that BOTH error fields are read, which is the whole reason
// api.RPCError grew a Data member: 127 measured -32603 errors put their text in
// data and set message to "Internal error", so the old struct discarded the cause
// of every internal error KAS reported.
func TestDetails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"an error carrying no data", rpcErr("boom", ""), ""},
		{"a non-RPC error", errors.New("boom"), ""},
		{"the details shape", rpcErr("Internal error", `{"details":"the real cause"}`), "the real cause"},
		{
			// The other measured shape: a Zod issue array. Its messages are what a
			// caller wants; the paths are noise.
			"a Zod issue array",
			rpcErr("Internal error", `[{"message":"workspacePaths is required","path":["workspacePaths"]},{"message":"inputs must be an object","path":["inputs"]}]`),
			"workspacePaths is required; inputs must be an object",
		},
		{
			// Neither shape: the raw JSON still beats "" because it is what KAS said.
			"an unrecognised shape falls back to the raw JSON",
			rpcErr("Internal error", `{"weird":true}`),
			`{"weird":true}`,
		},
		{"an empty details string falls through rather than winning", rpcErr("Internal error", `{"details":""}`), `{"details":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Details(c.err); got != c.want {
				t.Errorf("Details() = %q, want %q", got, c.want)
			}
		})
	}
}

// FuzzDetails pins that unwrapping an arbitrary `error.data` cannot panic and
// never invents content. Data is untrusted input from another process, and this
// is the only function that interprets it.
func FuzzDetails(f *testing.F) {
	f.Add(`{"details":"x"}`)
	f.Add(`[{"message":"a"}]`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(`"a string"`)
	f.Add(`{`)
	f.Add(``)
	f.Add(`{"details":123}`)
	f.Fuzz(func(t *testing.T, data string) {
		got := Details(rpcErr("Internal error", data))
		if data == "" && got != "" {
			t.Fatalf("Details() = %q for absent data, want \"\"", got)
		}
		// The fallback returns the raw bytes, so the result is either derived from
		// the JSON or exactly it — never longer than the input plus the joiner
		// budget an issue array can add.
		if len(got) > len(data)+2*len(data) {
			t.Fatalf("Details() grew %d bytes of data into %d", len(data), len(got))
		}
	})
}
