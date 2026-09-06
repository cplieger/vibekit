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
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/rpcerr"
)

// Node is one node of a run's state tree, decoded only as far as step
// identification needs. Recursive: a sequence, parallel or repeat node carries
// children; a step node is a leaf and is the only kind with a session.
//
// Every other field KAS puts here is left on the wire on purpose — the read
// endpoint passes the whole tree through, so decoding a field here would create
// a second definition of it that can only drift.
type Node struct {
	// Iteration is which pass of an enclosing `repeat` this node belongs to,
	// present on a repeat's per-iteration container and on the steps beneath it.
	//
	// A POINTER, because iteration 0 is the FIRST pass and a plain int cannot tell
	// it from absent — and the first pass is the one every non-looping run has, so
	// a value type would silently mis-address the common case rather than an edge.
	Iteration *int   `json:"iteration"`
	NodeID    string `json:"nodeId"`
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Children  []Node `json:"children"`
}

// State is the run state `inspect` returns, decoded only as far as step
// identification needs. Status, pauseReason, timings and the rest stay on the
// wire: the read endpoint passes the whole thing through, so decoding a field
// here would create a second definition of it that can only drift.
type State struct {
	Root       *Node  `json:"root"`
	WorkflowID string `json:"workflowId"`
}

// InspectResult is `_kiro/workflow/inspect`'s reply, decoded to the one part
// vibekit reads. `nodePlan` rides through undecoded — see the package comment.
type InspectResult struct {
	State *State `json:"state"`
}

// StepSession names one step node, the ACP session that executed it, and its
// PATH within the run.
//
// The path is what addresses one EXECUTION, where the node id addresses a node: a
// repeat's iterations share a node id, so two passes of one loop body are
// indistinguishable by id alone. Every caller that has to name a single step —
// the step-transcript endpoint, the client's own step rows — keys on the path for
// that reason.
type StepSession struct {
	NodeID    string
	SessionID string
	// Path is the node path from the root down to this step, in the spelling KAS
	// puts on the WIRE. See pathSegment for the one segment where the wire and the
	// state tree disagree.
	Path []string
}

// StepSessions walks a run's state tree depth-first and returns every step that
// has a session, in execution order.
//
// Order is the tree's own, which is the order the steps were declared and
// therefore the order a reader expects to see them. A `parallel` node's branches
// ran concurrently, so their relative order is declaration order rather than a
// claim about time.
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
// The unfiltered door, and the distinction it draws is the one a reader needs:
// StepSessions cannot tell "this run has no such step" from "that step has not
// started", because both answer absent. A caller serving one step's transcript
// has to say something different for each — a path naming nothing is a client
// error, while a path naming a pending step is a real step with nothing to show —
// so it asks this and reads SessionID itself.
func Steps(s *State) []StepSession {
	if s == nil || s.Root == nil {
		return nil
	}
	var out []StepSession
	walk(s.Root, nil, nil, &out)
	return out
}

// walk visits the tree depth-first, carrying the parent (which decides this
// node's path segment) and the trail above it.
//
// Each level allocates its path at EXACT capacity, so a child's own append
// reallocates rather than writing into a sibling's backing array. A shared array
// would make the last branch visited overwrite the paths already reported.
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
// ONE node kind diverges, and it is the one that matters: a repeat's
// per-iteration container is `<repeatId>#<n>` in the state tree and `iter-<n>` on
// the wire. Every other node contributes its own `nodeId` and the two spellings
// agree.
//
// THREE implementations of this one spelling exist, and that is deliberate rather
// than drift — the tree and the frame describe the path differently, so each side
// of the join needs its own translation:
//
//   - this one, translating the STATE TREE into the wire spelling;
//   - `internal/translate/workflow_step_content.go` runNodePath, which JOINS the
//     path KAS already put on a frame;
//   - `static-src/run-store.ts` nodePathSegment, the client's copy of this same
//     translation, because the client also holds the tree and not the frame.
//
// Derived from the container's own `iteration` rather than by rewriting the `#`
// suffix off the id, so nothing here depends on how KAS spells a generated id. A
// repeat child carrying no iteration falls back to its node id, which is the same
// call the two siblings above make when the fact they need is absent: a path in
// the wrong place beats a step that cannot be addressed at all.
func pathSegment(n, parent *Node) string {
	if parent != nil && parent.Type == "repeat" && n.Iteration != nil {
		return "iter-" + strconv.Itoa(*n.Iteration)
	}
	return n.NodeID
}

// unknownMethodMarker is how KAS reports a `_kiro/workflow/*` name it does not
// register: a -32603 whose `error.data.details` contains the classifier's own
// text. A registered method never produces that string, which is what makes it a
// usable feature-detection signal in the absence of a capability flag.
const unknownMethodMarker = "has no persistence classification"

// ErrUnknownMethod means the verb does not exist on this KAS build, as opposed
// to existing and failing.
//
// The distinction matters because the two demand opposite responses: an
// unimplemented verb is a permanent capability answer, while a failure is
// transient and worth retrying or surfacing. Callers ask errors.Is.
var ErrUnknownMethod = errors.New("workflow verb not registered on this kiro-cli build")

// Classify types a `_kiro/workflow/*` RPC failure AT THE BOUNDARY: call it on
// what the RPC returned, and an unregistered verb comes back wrapping
// ErrUnknownMethod with the original error still unwrappable beneath it.
//
// It reads the boundary error's own `error.data` (RPCDetails walks to the
// *vibekit.RPCError with errors.As) and nothing else. It deliberately does NOT
// search the rendered message chain: RPCError.Error() is the bare
// `error.message`, which for this shape is the literal "Internal error", so the
// text search matched only when some intermediate layer had already rendered the
// details into its own message — and any layer that quotes KAS then reads as an
// unregistered verb. Typing it here is what keeps the answer a property of the
// response rather than of how many wrappers it collected on the way out.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(rpcerr.Details(err), unknownMethodMarker) {
		return fmt.Errorf("%w: %w", ErrUnknownMethod, err)
	}
	return err
}
