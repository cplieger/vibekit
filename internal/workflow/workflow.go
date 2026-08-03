// Package workflow decodes KAS's workflow-run wire shapes.
//
// It holds no run state and builds no model of a run: a run's state lives at
// ~/.kiro/sessions/<hash>/workflows/<workflowId>/ and `_kiro/workflow/inspect`
// returns it whole. vibekit's read endpoints pass that through verbatim, so what
// is left for this package is the one question the passthrough cannot answer for
// itself — which ACP session executed a given step — plus the error unwrapping
// KAS's `-32603` shape requires.
//
// WHAT THIS PACKAGE DELIBERATELY DOES NOT CONTAIN, because each was designed and
// then measured away:
//
//   - A `stepSessions[]` join. An earlier design joined a separate stepSessions
//     array to the node plan by (nodeId, iteration, branchId). `inspect` returns
//     no such array: `state.root` carries `sessionId`, `agentName`, `modelId`,
//     `iteration`, `branchId`, `startedAt`, `endedAt`, `status`,
//     `completionSignal` and `capturedOutput` ON each step node already. The tree
//     IS the join.
//   - A `nodePlan` model. `inspect` returns it beside `state`, and against a real
//     response its only content the state tree lacks is a repeat's
//     `maxIterations`/`onMaxIterations`/`stopCondition`. The state tree includes
//     pending nodes, so the plan is otherwise a second copy — and `pauseReason`
//     already says in words when a repeat hit its ceiling.
//   - `ActionsFor(status, …)`. There are no run controls at all (user decision):
//     no Retry, no Continue, no Pause, no Resume. A verb-precedence table with no
//     verbs is not a small version of itself.
//   - An authoring validator. `_kiro/workflow/new` rejects an over-long step
//     list, a duplicate id, an unregistered agent and a dangling template ref
//     with precise messages and writes nothing, so launch IS validation. A local
//     copy would buy a saved round trip and cost a second source of truth.
package workflow

import (
	"encoding/json"
	"errors"
	"strings"
)

// Node is one node of a run's state tree, decoded only as far as step
// identification needs. Recursive: a sequence, parallel or repeat node carries
// children; a step node is a leaf and is the only kind with a session.
//
// Every other field KAS puts here is left on the wire on purpose — the read
// endpoint passes the whole tree through, so decoding a field here would create
// a second definition of it that can only drift.
type Node struct {
	NodeID    string `json:"nodeId"`
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Children  []Node `json:"children"`
}

// State is the run state `inspect` returns, decoded to the same depth.
type State struct {
	Root            *Node  `json:"root"`
	WorkflowID      string `json:"workflowId"`
	Status          string `json:"status"`
	ParentSessionID string `json:"parentSessionId"`
}

// InspectResult is `_kiro/workflow/inspect`'s reply.
type InspectResult struct {
	State      *State `json:"state"`
	WorkflowID string `json:"workflowId"`
	// NodePlan is KAS's declared plan. Kept as raw JSON and never decoded: see
	// the package comment for why it carries nothing the state tree lacks.
	NodePlan json.RawMessage `json:"nodePlan"`
}

// StepSession names one step node and the ACP session that executed it.
type StepSession struct {
	NodeID    string
	SessionID string
}

// StepSessions walks a run's state tree depth-first and returns every step that
// has a session, in execution order.
//
// Order is the tree's own, which is the order the steps were declared and
// therefore the order a reader expects to see them. A `parallel` node's branches
// ran concurrently, so their relative order is declaration order rather than a
// claim about time.
func StepSessions(s *State) []StepSession {
	if s == nil || s.Root == nil {
		return nil
	}
	var out []StepSession
	walk(s.Root, &out)
	return out
}

func walk(n *Node, out *[]StepSession) {
	if n.Type == "step" && n.SessionID != "" {
		*out = append(*out, StepSession{NodeID: n.NodeID, SessionID: n.SessionID})
	}
	for i := range n.Children {
		walk(&n.Children[i], out)
	}
}

// unknownMethodMarker is how KAS reports a `_kiro/workflow/*` name it does not
// register: a -32603 whose `error.data.details` contains the classifier's own
// text. A registered method never produces that string, which is what makes it a
// usable feature-detection signal in the absence of a capability flag.
const unknownMethodMarker = "has no persistence classification"

// IsUnknownMethod reports whether an RPC error means the method does not exist
// on this KAS build, as opposed to failing.
//
// The distinction matters because the two demand opposite responses: an
// unimplemented verb is a permanent capability answer to cache, while a failure
// is transient and worth retrying or surfacing.
func IsUnknownMethod(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(Details(err), unknownMethodMarker) ||
		strings.Contains(err.Error(), unknownMethodMarker)
}

// rpcDetailer is satisfied by an error carrying KAS's `error.data`.
type rpcDetailer interface {
	ErrorData() json.RawMessage
}

// Details extracts KAS's real error text.
//
// BOTH fields have to be read, and that is a measured requirement rather than
// defensiveness. Counted over every engine-emitted frame in the probe logs: 127
// `-32603` errors put the text in `error.data.details` or in `error.data` as a
// Zod issue array and set `error.message` to the literal "Internal error", while
// the 6 `-32602` and 4 `-32000` errors put it in `error.message` and carry no
// `error.data` at all. Reading only `message` loses every internal error's cause;
// reading only `data` loses every parameter-validation message.
func Details(err error) string {
	var d rpcDetailer
	if !errors.As(err, &d) {
		return ""
	}
	raw := d.ErrorData()
	if len(raw) == 0 {
		return ""
	}
	// The common shape: {"details": "…"}.
	var obj struct {
		Details string `json:"details"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Details != "" {
		return obj.Details
	}
	// The other shape: a Zod issue array. Its messages are what a caller wants.
	var issues []struct {
		Message string `json:"message"`
		Path    []any  `json:"path"`
	}
	if json.Unmarshal(raw, &issues) == nil && len(issues) > 0 {
		msgs := make([]string, 0, len(issues))
		for _, is := range issues {
			if is.Message != "" {
				msgs = append(msgs, is.Message)
			}
		}
		if len(msgs) > 0 {
			return strings.Join(msgs, "; ")
		}
	}
	// Neither shape parsed: the raw JSON still beats an empty string, because it
	// is what KAS actually said.
	return string(raw)
}
