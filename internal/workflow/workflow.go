// Package workflow decodes KAS's workflow-run wire shapes.
//
// It holds no run state: a run's state lives at
// ~/.kiro/sessions/<hash>/workflows/<workflowId>/ and `_kiro/workflow/inspect`
// returns it whole, which vibekit's read endpoints pass through verbatim. What is
// left here is the one question that passthrough cannot answer — which ACP session
// executed a given step — plus the error unwrapping KAS's `-32603` shape requires.
//
// `inspect` carries no stepSessions array: `state.root` already holds `sessionId`,
// `agentName`, `iteration`, `branchId` and the rest ON each step node, so the tree
// IS the join, and `nodePlan` adds only a repeat's ceiling fields.
package workflow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/rpcerr"
)

// Node is one node of a run's state tree: a sequence, parallel or repeat node
// carries children, a step node is a leaf and is the only kind with a session.
// Every other field stays on the wire, because the read endpoint passes the tree
// through and a second definition here could only drift.
type Node struct {
	// Iteration is which pass of an enclosing `repeat` this node belongs to. A
	// POINTER because iteration 0 is the FIRST pass, which a plain int cannot tell
	// from absent, and that pass is the one every non-looping run has.
	Iteration *int   `json:"iteration"`
	NodeID    string `json:"nodeId"`
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Children  []Node `json:"children"`
}

// State is the run state `inspect` returns, decoded only as far as step
// identification needs. Status, pauseReason and timings stay on the wire, for
// Node's reason.
type State struct {
	Root       *Node  `json:"root"`
	WorkflowID string `json:"workflowId"`
}

// InspectResult is `_kiro/workflow/inspect`'s reply, decoded to the one part
// vibekit reads. `nodePlan` rides through undecoded — see the package comment.
type InspectResult struct {
	State *State `json:"state"`
}

// StepSession names one step node, the ACP session that executed it, and its PATH
// within the run. The path addresses one EXECUTION where the node id addresses a
// node: a repeat's iterations share a node id, so every caller that has to name a
// single step keys on the path.
type StepSession struct {
	NodeID    string
	SessionID string
	// Path is the node path from the root down to this step, in the spelling KAS
	// puts on the WIRE. See pathSegment for the one segment that disagrees.
	Path []string
}

// StepSessions walks a run's state tree depth-first and returns every step that
// has a session, in the tree's own order — which is declaration order, so a
// `parallel` node's branches are ordered by declaration rather than by time.
func StepSessions(s *State) []StepSession {
	var out []StepSession
	for _, st := range Steps(s) {
		if st.SessionID != "" {
			out = append(out, st)
		}
	}
	return out
}

// Steps walks a run's state tree depth-first and returns EVERY step node, in the
// same order, whether or not it has run.
//
// The unfiltered door: StepSessions cannot tell "this run has no such step" from
// "that step has not started", and a caller serving one step's transcript has to
// answer those two differently.
func Steps(s *State) []StepSession {
	if s == nil || s.Root == nil {
		return nil
	}
	var out []StepSession
	walk(s.Root, nil, nil, &out)
	return out
}

// walk visits the tree depth-first, carrying the parent (which decides this node's
// path segment) and the trail above it. Each level allocates its path at EXACT
// capacity, or a child's append writes into a sibling's backing array.
func walk(n, parent *Node, trail []string, out *[]StepSession) {
	path := make([]string, 0, len(trail)+1)
	path = append(path, trail...)
	path = append(path, pathSegment(n, parent))
	if n.Type == "step" {
		*out = append(*out, StepSession{NodeID: n.NodeID, SessionID: n.SessionID, Path: path})
	}
	for i := range n.Children {
		walk(&n.Children[i], n, path, out)
	}
}

// pathSegment is what KAS calls this node in a node PATH, which is not always what
// it calls it in the state tree.
//
// ONE node kind diverges: a repeat's per-iteration container is `<repeatId>#<n>` in
// the tree and `iter-<n>` on the wire. Derived from the container's own `iteration`
// rather than the `#` suffix, so nothing depends on how KAS spells a generated id;
// a child carrying no iteration falls back to its node id, so it stays addressable.
// Two siblings hold the same translation: translate.runNodePath and run-store.ts.
func pathSegment(n, parent *Node) string {
	if parent != nil && parent.Type == "repeat" && n.Iteration != nil {
		return "iter-" + strconv.Itoa(*n.Iteration)
	}
	return n.NodeID
}

// unknownMethodMarker is how KAS reports a `_kiro/workflow/*` name it does not
// register: a -32603 whose `error.data.details` contains the classifier's own text.
// A registered method never produces that string, which is what makes it usable.
const unknownMethodMarker = "has no persistence classification"

// ErrUnknownMethod means the verb does not exist on this KAS build, as opposed to
// existing and failing: an unimplemented verb is a permanent capability answer,
// while a failure is transient and worth retrying. Callers ask errors.Is.
var ErrUnknownMethod = errors.New("workflow verb not registered on this kiro-cli build")

// Classify types a `_kiro/workflow/*` RPC failure AT THE BOUNDARY: an unregistered
// verb comes back wrapping ErrUnknownMethod with the original still unwrappable.
//
// It reads the boundary error's own `error.data` and never the rendered message
// chain: RPCError.Error() is the bare `error.message`, which for this shape is the
// literal "Internal error", so a text search matched only when some intermediate
// layer had already rendered the details into its own message.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(rpcerr.Details(err), unknownMethodMarker) {
		return fmt.Errorf("%w: %w", ErrUnknownMethod, err)
	}
	return err
}
